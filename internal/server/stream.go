package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/snxwfairies/snxwboard/internal/fold"
	"github.com/snxwfairies/snxwboard/internal/store"
)

const (
	replayPage = 1000

	viewEvents   = "events"
	viewSessions = "sessions"

	// sessionFlush is how often a session-view client gets its merged patches,
	// which caps a tool-call burst at 4 messages a second per session.
	sessionFlush = 250 * time.Millisecond
)

// notification is one live push to a subscriber: either a raw event or a folded
// session patch.
type notification struct {
	ev    store.Event
	id    string
	patch map[string]any
	seq   int64
}

type subscriber struct {
	view   string
	ch     chan notification
	closed bool
}

// close must be called with subMu held.
func (s *subscriber) close() {
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}

// handleStream serves the raw event log (the default) or the coalesced session
// view. Every message carries the seq it reflects.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	view := r.URL.Query().Get("view")
	switch view {
	case "", viewEvents:
		view = viewEvents
	case viewSessions:
	default:
		writeError(w, http.StatusBadRequest, "unknown view "+strconv.Quote(view))
		return
	}

	since, err := cursor(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	sub, err := s.subscribe(view)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer s.unsubscribe(sub)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if view == viewSessions {
		s.streamSessions(w, r, sub, flusher)
		return
	}
	s.streamEvents(w, r, sub, flusher, since)
}

// streamEvents replays everything after the client's cursor, then forwards live
// events. Subscribing happens before the replay query so nothing falls in the
// gap; live events already covered by the replay are dropped by seq.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, sub *subscriber, flusher http.Flusher, since int64) {
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
		case n, ok := <-sub.ch:
			if !ok {
				// Slow client: the stream is closed so it can resume from its
				// last seen seq.
				return
			}
			if n.ev.Seq <= since {
				continue
			}
			if err := writeSSE(w, flusher, n.ev); err != nil {
				return
			}
			since = n.ev.Seq
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ":\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// streamSessions sends a snapshot first, then one merged patch per session at
// most every sessionFlush. Patches the snapshot already covers are dropped.
func (s *Server) streamSessions(w http.ResponseWriter, r *http.Request, sub *subscriber, flusher http.Flusher) {
	snapSeq, sessions, err := s.snapshotSessions(r.Context())
	if err != nil {
		s.log.Error("session snapshot", "err", err)
		return
	}
	if err := writeSnap(w, flusher, snapSeq, sessions); err != nil {
		return
	}

	type pending struct {
		patch map[string]any
		seq   int64
	}
	merged := make(map[string]*pending, 8)
	lastSent := make(map[string]time.Time, 8)

	// flush writes every session whose last message is at least sessionFlush
	// old, and keeps the rest for a later tick.
	flush := func() error {
		now := time.Now()
		for id, p := range merged {
			if t, ok := lastSent[id]; ok && now.Sub(t) < sessionFlush {
				continue
			}
			if err := writeSession(w, flusher, id, p.seq, p.patch); err != nil {
				return err
			}
			lastSent[id] = now
			delete(merged, id)
		}
		return nil
	}

	ticker := time.NewTicker(sessionFlush)
	defer ticker.Stop()
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.closing:
			return
		case n, ok := <-sub.ch:
			if !ok {
				return
			}
			if n.seq <= snapSeq {
				continue
			}
			p := merged[n.id]
			if p == nil {
				p = &pending{patch: make(map[string]any, len(n.patch))}
				merged[n.id] = p
			}
			for k, v := range n.patch {
				p.patch[k] = v
			}
			p.seq = n.seq
		case <-ticker.C:
			if err := flush(); err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ":\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleSessions returns the folded snapshot: every session plus the seq it is
// current as of.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	seq, sessions, err := s.snapshotSessions(r.Context())
	if err != nil {
		s.log.Error("read sessions", "err", err)
		writeError(w, http.StatusInternalServerError, "read sessions")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Seq      int64          `json:"seq"`
		Sessions []fold.Session `json:"sessions"`
	}{Seq: seq, Sessions: sessions})
}

func (s *Server) snapshotSessions(ctx context.Context) (int64, []fold.Session, error) {
	seq, rows, err := s.st.SnapshotSessions(ctx)
	if err != nil {
		return 0, nil, err
	}
	sessions := make([]fold.Session, 0, len(rows))
	for _, row := range rows {
		var sess fold.Session
		if err := json.Unmarshal(row.Data, &sess); err != nil || sess.ID == "" {
			s.log.Warn("skip unreadable session", "id", row.ID, "err", err)
			continue
		}
		sessions = append(sessions, sess)
	}
	return seq, sessions, nil
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

func writeSession(w io.Writer, f http.Flusher, id string, seq int64, patch map[string]any) error {
	data, err := json.Marshal(map[string]any{"t": "s", "id": id, "p": patch})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: s\ndata: %s\n\n", seq, data); err != nil {
		return err
	}
	f.Flush()
	return nil
}

func writeSnap(w io.Writer, f http.Flusher, seq int64, sessions []fold.Session) error {
	data, err := json.Marshal(map[string]any{"t": "snap", "seq": seq, "sessions": sessions})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: snap\ndata: %s\n\n", seq, data); err != nil {
		return err
	}
	f.Flush()
	return nil
}

func (s *Server) subscribe(view string) (*subscriber, error) {
	sub := &subscriber{view: view, ch: make(chan notification, subscriberBuffer)}
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
	s.broadcast(viewEvents, notification{ev: ev})
}

func (s *Server) publishSession(id string, patch map[string]any, seq int64) {
	s.broadcast(viewSessions, notification{id: id, patch: patch, seq: seq})
}

func (s *Server) broadcast(view string, n notification) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for sub := range s.subs {
		if sub.view != view {
			continue
		}
		select {
		case sub.ch <- n:
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
