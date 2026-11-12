package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/store"
)

const (
	maxBodyBytes = 1 << 20
	maxBatch     = 500
	maxFieldLen  = 64
)

type wireEvent struct {
	Source  string          `json:"source"`
	Session string          `json:"session"`
	Kind    string          `json:"kind"`
	Body    json.RawMessage `json:"body"`
}

// handleEvents accepts one event object or an array of them. It never touches
// the disk: validation and an in-memory append, then 202.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	evs, err := parseEvents(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Reject the whole batch up front when it can't fit, so a client retrying a
	// 503 never duplicates events that were half-accepted. The ingestMu keeps
	// the room check and the sends atomic against other handlers.
	s.ingestMu.Lock()
	if cap(s.ingest)-len(s.ingest) < len(evs) {
		s.ingestMu.Unlock()
		s.log.Warn("ingest buffer full", "rejected", len(evs))
		writeError(w, http.StatusServiceUnavailable, "ingest buffer full")
		return
	}
	for _, ev := range evs {
		s.ingest <- ev // cannot block: room was reserved above and only handlers send
	}
	s.ingestMu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(evs)})
}

// parseEvents decodes and validates one event or a batch of at most maxBatch.
func parseEvents(data []byte) ([]store.Event, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("empty body")
	}

	var items []wireEvent
	if data[0] == '[' {
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, fmt.Errorf("invalid json: %v", err)
		}
	} else {
		var one wireEvent
		if err := json.Unmarshal(data, &one); err != nil {
			return nil, fmt.Errorf("invalid json: %v", err)
		}
		items = []wireEvent{one}
	}
	if len(items) == 0 {
		return nil, errors.New("no events")
	}
	if len(items) > maxBatch {
		return nil, fmt.Errorf("too many events: %d (max %d)", len(items), maxBatch)
	}

	now := time.Now().UnixMilli()
	evs := make([]store.Event, len(items))
	for i, it := range items {
		if err := validateEvent(it); err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}
		body := it.Body
		if len(body) == 0 {
			body = json.RawMessage("null")
		}
		evs[i] = store.Event{
			TS:      now,
			Source:  it.Source,
			Session: it.Session,
			Kind:    it.Kind,
			Body:    body,
		}
	}
	return evs, nil
}

func validateEvent(it wireEvent) error {
	if err := checkField("source", it.Source); err != nil {
		return err
	}
	return checkField("kind", it.Kind)
}

func checkField(name, v string) error {
	if v == "" {
		return fmt.Errorf("%s required", name)
	}
	if !utf8.ValidString(v) {
		return fmt.Errorf("%s must be valid utf-8", name)
	}
	if utf8.RuneCountInString(v) > maxFieldLen {
		return fmt.Errorf("%s too long: max %d chars", name, maxFieldLen)
	}
	return nil
}

// writeLoop is the only goroutine that writes to SQLite. It batches for at most
// flushInterval or flushSize events and drains everything on shutdown.
func (s *Server) writeLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	batch := make([]store.Event, 0, flushSize)
	for {
		select {
		case ev := <-s.ingest:
			batch = append(batch, ev)
			if len(batch) >= flushSize {
				s.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				s.flush(batch)
				batch = batch[:0]
			}
		case <-s.done:
			for {
				select {
				case ev := <-s.ingest:
					batch = append(batch, ev)
					if len(batch) >= flushSize {
						s.flush(batch)
						batch = batch[:0]
					}
				default:
					if len(batch) > 0 {
						s.flush(batch)
					}
					return
				}
			}
		}
	}
}

// flush persists a batch in one transaction, then publishes the events that got
// their seq assigned.
func (s *Server) flush(batch []store.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := s.st.InsertBatch(ctx, batch); err != nil {
		s.log.Error("persist events", "count", len(batch), "err", err)
		return
	}
	for _, ev := range batch {
		s.publish(ev)
	}
}
