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

	"github.com/snxwfairies-ai/snxwboard-oss/internal/fold"
	"github.com/snxwfairies-ai/snxwboard-oss/internal/redact"
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

	if !s.enqueue(evs) {
		writeError(w, http.StatusServiceUnavailable, "ingest buffer full")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(evs)})
}

// enqueue appends evs to the ingest buffer, all or nothing, so a client
// retrying a 503 never duplicates events that were half-accepted. ingestMu keeps
// the room check and the sends atomic against other handlers.
func (s *Server) enqueue(evs []store.Event) bool {
	s.ingestMu.Lock()
	defer s.ingestMu.Unlock()
	if cap(s.ingest)-len(s.ingest) < len(evs) {
		s.log.Warn("ingest buffer full", "rejected", len(evs))
		return false
	}
	for _, ev := range evs {
		s.ingest <- ev // cannot block: room was checked above and only enqueue sends
	}
	return true
}

// sessionKey is the id an event folds under: its session, else its source.
func sessionKey(ev store.Event) string {
	if ev.Session != "" {
		return ev.Session
	}
	return ev.Source
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
		body := redact.Redact(it.Body)
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

// flush persists a batch in one transaction, folds it into session state, then
// publishes the raw events and the per-session patches that changed.
func (s *Server) flush(batch []store.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()

	type change struct {
		id    string
		patch map[string]any
		seq   int64
	}
	changes := make([]change, 0, 4)
	index := make(map[string]int, 4)
	// prev holds each touched session as it was before this batch (nil if
	// new), to undo the fold if the transaction fails.
	prev := make(map[string]*fold.Session, 4)

	err := s.st.WriteBatch(ctx, batch, func(evs []store.Event) []store.SessionRow {
		for _, ev := range evs {
			if key := sessionKey(ev); key != "" {
				if _, seen := prev[key]; !seen {
					if before, ok := s.folder.Get(key); ok {
						prev[key] = &before
					} else {
						prev[key] = nil
					}
				}
			}
			patch, changed := s.folder.Apply(ev)
			if !changed {
				continue
			}
			id := sessionKey(ev)
			if i, seen := index[id]; seen {
				for k, v := range patch {
					changes[i].patch[k] = v
				}
				changes[i].seq = ev.Seq
				continue
			}
			index[id] = len(changes)
			changes = append(changes, change{id: id, patch: patch, seq: ev.Seq})
		}

		rows := make([]store.SessionRow, 0, len(changes))
		for _, c := range changes {
			sess, ok := s.folder.Get(c.id)
			if !ok {
				continue
			}
			data, err := json.Marshal(sess)
			if err != nil {
				s.log.Error("marshal session", "id", c.id, "err", err)
				continue
			}
			rows = append(rows, store.SessionRow{ID: c.id, Data: data, UpdatedSeq: c.seq})
		}
		return rows
	})
	if err != nil {
		// The batch is lost (its events were already acknowledged); at least
		// keep the folded state consistent with what is on disk.
		s.folder.Restore(prev)
		s.log.Error("persist events: batch dropped", "count", len(batch), "err", err)
		return
	}
	for _, ev := range batch {
		s.publish(ev)
	}
	for _, c := range changes {
		s.publishSession(c.id, c.patch, c.seq)
	}
}
