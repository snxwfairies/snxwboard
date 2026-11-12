package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/store"
)

const replayPage = 1000

type subscriber struct {
	ch     chan store.Event
	closed bool
}

// close must be called with subMu held.
func (s *subscriber) close() {
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}

// handleStream replays everything after the client's cursor, then forwards live
// events. Subscribing happens before the replay query so nothing falls in the
// gap; live events already covered by the replay are dropped by seq.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	since, err := cursor(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	sub, err := s.subscribe()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer s.unsubscribe(sub)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		evs, err := s.st.QueryAfter(r.Context(), since, replayPage)
		if err != nil {
			if r.Context().Err() == nil {
				s.log.Error("replay events", "after", since, "err", err)
			}
			return
		}
		for _, ev := range evs {
			if ev.Seq <= since {
				continue
			}
			if err := writeSSE(w, flusher, ev); err != nil {
				return
			}
			since = ev.Seq
		}
		if len(evs) < replayPage {
			break
		}
	}

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.closing:
			return
		case ev, ok := <-sub.ch:
			if !ok {
				// Slow client: the stream is closed so it can resume from its
				// last seen seq.
				return
			}
			if ev.Seq <= since {
				continue
			}
			if err := writeSSE(w, flusher, ev); err != nil {
				return
			}
			since = ev.Seq
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ":\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// cursor reads the resume point from Last-Event-ID, or ?since= as a fallback.
func cursor(r *http.Request) (int64, error) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("since")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid Last-Event-ID %q", raw)
	}
	return n, nil
}

func writeSSE(w io.Writer, f http.Flusher, ev store.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: e\ndata: %s\n\n", ev.Seq, data); err != nil {
		return err
	}
	f.Flush()
	return nil
}

func (s *Server) subscribe() (*subscriber, error) {
	sub := &subscriber{ch: make(chan store.Event, subscriberBuffer)}
	s.subMu.Lock()
	defer s.subMu.Unlock()
	select {
	case <-s.closing:
		return nil, errors.New("shutting down")
	default:
	}
	s.subs[sub] = struct{}{}
	return sub, nil
}

func (s *Server) unsubscribe(sub *subscriber) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if _, ok := s.subs[sub]; ok {
		delete(s.subs, sub)
		sub.close()
	}
}

func (s *Server) publish(ev store.Event) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for sub := range s.subs {
		select {
		case sub.ch <- ev:
		default:
			// Slow client: drop the stream; it resumes from Last-Event-ID.
			sub.close()
			delete(s.subs, sub)
		}
	}
}

func (s *Server) closeSubs() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for sub := range s.subs {
		sub.close()
		delete(s.subs, sub)
	}
}
