package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/store"
)

func getJSON(t *testing.T, h http.Handler, path string, out any) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK && out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %s: %v (%s)", path, err, rec.Body)
		}
	}
	return rec.Code
}

// drain collects every SSE message that arrives within d.
func (r *sseReader) drain(d time.Duration) []sseMsg {
	var out []sseMsg
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case m := <-r.msgs:
			out = append(out, m)
		case <-timer.C:
			return out
		}
	}
}

func TestRedactBeforeStorage(t *testing.T) {
	s, st := newTestServer(t, 0)
	payload := `{"source":"claude","session":"s1","kind":"tool.start",
		"body":{"tool":"Bash","api_key":"sk-abcdefghijklmnopqrstuvwxyz","nested":{"Authorization":"Bearer abcdefghijkl"}}}`
	if rec := post(s.Handler(), testToken, payload); rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}

	evs := waitForEvents(t, st, 1)
	body := string(evs[0].Body)
	for _, secret := range []string{"sk-abcdefghijklmnopqrstuvwxyz", "abcdefghijkl"} {
		if strings.Contains(body, secret) {
			t.Fatalf("stored body leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, `"tool":"Bash"`) || strings.Count(body, "[redacted]") != 2 {
		t.Fatalf("stored body = %s", body)
	}
}

func TestSessionsSnapshotEndpoint(t *testing.T) {
	s, st := newTestServer(t, 0)

	var empty sessionsSnapshot
	if code := getJSON(t, s.Handler(), "/v1/sessions", &empty); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if empty.Seq != 0 || len(empty.Sessions) != 0 {
		t.Fatalf("empty snapshot = %+v", empty)
	}

	post(s.Handler(), testToken, `[
		{"source":"claude","session":"s1","kind":"session.start","body":{"repo":"r","branch":"main"}},
		{"source":"claude","session":"s1","kind":"tool.start","body":{"tool":"Bash"}}
	]`)
	waitForEvents(t, st, 2)

	var snap sessionsSnapshot
	if code := getJSON(t, s.Handler(), "/v1/sessions", &snap); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if snap.Seq != 2 {
		t.Fatalf("seq = %d, want 2", snap.Seq)
	}
	if len(snap.Sessions) != 1 {
		t.Fatalf("sessions = %+v", snap.Sessions)
	}
	got := snap.Sessions[0]
	if got.ID != "s1" || got.Status != "running" || got.Tool != "Bash" || got.ToolCalls != 1 ||
		got.Repo != "r" || got.Branch != "main" || got.UpdatedSeq != 2 {
		t.Fatalf("session = %+v", got)
	}
}

// sessionsSnapshot mirrors GET /v1/sessions.
type sessionsSnapshot struct {
	Seq      int64           `json:"seq"`
	Sessions []foldedSession `json:"sessions"`
}

type foldedSession struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Tool       string `json:"tool"`
	ToolCalls  int    `json:"toolCalls"`
	Errors     int    `json:"errors"`
	Repo       string `json:"repo"`
	Branch     string `json:"branch"`
	UpdatedSeq int64  `json:"updatedSeq"`
}

func TestFoldPersistedAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	s := New(Options{Store: st, Token: testToken, Version: "test", Logger: discardLogger()})
	s.Start()

	post(s.Handler(), testToken, `[
		{"source":"claude","session":"s1","kind":"session.start","body":{"repo":"r"}},
		{"source":"claude","session":"s1","kind":"tool.start","body":{"tool":"Bash"}},
		{"source":"claude","session":"s1","kind":"tool.start","body":{"tool":"Edit"}}
	]`)
	waitForEvents(t, st, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	st2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st2.Close()
	s2 := New(Options{Store: st2, Token: testToken, Version: "test", Logger: discardLogger()})
	s2.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s2.Shutdown(ctx)
	})

	var snap sessionsSnapshot
	if code := getJSON(t, s2.Handler(), "/v1/sessions", &snap); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if snap.Seq != 3 || len(snap.Sessions) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if got := snap.Sessions[0]; got.ToolCalls != 2 || got.Tool != "Edit" || got.Repo != "r" || got.UpdatedSeq != 3 {
		t.Fatalf("reloaded session = %+v", got)
	}

	// Counters continue from the persisted state, not from zero.
	post(s2.Handler(), testToken, `{"source":"claude","session":"s1","kind":"tool.start","body":{"tool":"Bash"}}`)
	waitForEvents(t, st2, 4)
	if code := getJSON(t, s2.Handler(), "/v1/sessions", &snap); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if got := snap.Sessions[0]; got.ToolCalls != 3 || got.UpdatedSeq != 4 {
		t.Fatalf("session after reload = %+v", got)
	}
}

func TestSessionStreamSnapshotThenLive(t *testing.T) {
	s, st := newTestServer(t, 0)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	resp := openStream(t, ts.URL+"/v1/stream?view=sessions", testToken, nil)
	reader := newSSEReader(resp.Body)

	snap := reader.next(t)
	if snap.event != "snap" {
		t.Fatalf("first event = %q, want snap", snap.event)
	}
	var first struct {
		T        string          `json:"t"`
		Seq      int64           `json:"seq"`
		Sessions []foldedSession `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(snap.data), &first); err != nil {
		t.Fatalf("decode snap: %v", err)
	}
	if first.T != "snap" || first.Seq != 0 || snap.id != "0" || len(first.Sessions) != 0 {
		t.Fatalf("snap = %s", snap.data)
	}

	post(s.Handler(), testToken, `[
		{"source":"claude","session":"s1","kind":"session.start","body":{"repo":"r"}},
		{"source":"claude","session":"s1","kind":"tool.start","body":{"tool":"Bash"}}
	]`)
	waitForEvents(t, st, 2)

	msg := reader.next(t)
	if msg.event != "s" || msg.id != "2" {
		t.Fatalf("message = %+v", msg)
	}
	var patch struct {
		T  string         `json:"t"`
		ID string         `json:"id"`
		P  map[string]any `json:"p"`
	}
	if err := json.Unmarshal([]byte(msg.data), &patch); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patch.T != "s" || patch.ID != "s1" {
		t.Fatalf("patch = %s", msg.data)
	}
	if patch.P["toolCalls"] != float64(1) || patch.P["tool"] != "Bash" || patch.P["repo"] != "r" {
		t.Fatalf("patch fields = %v", patch.P)
	}
}

func TestSessionStreamResumesWithSnapshot(t *testing.T) {
	s, st := newTestServer(t, 0)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	post(s.Handler(), testToken, `[
		{"source":"claude","session":"s1","kind":"session.start","body":{}},
		{"source":"claude","session":"s1","kind":"tool.start","body":{"tool":"Bash"}}
	]`)
	waitForEvents(t, st, 2)

	header := http.Header{"Last-Event-Id": []string{"2"}}
	resp := openStream(t, ts.URL+"/v1/stream?view=sessions", testToken, header)
	reader := newSSEReader(resp.Body)

	// A resume never replays raw events: the snapshot is the state as of the
	// client's cursor, then only newer patches follow.
	msg := reader.next(t)
	if msg.event != "snap" || msg.id != "2" {
		t.Fatalf("message = %+v", msg)
	}
	var snap struct {
		Seq      int64           `json:"seq"`
		Sessions []foldedSession `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(msg.data), &snap); err != nil {
		t.Fatalf("decode snap: %v", err)
	}
	if snap.Seq != 2 || len(snap.Sessions) != 1 || snap.Sessions[0].Tool != "Bash" {
		t.Fatalf("snap = %s", msg.data)
	}

	if msgs := reader.drain(50 * time.Millisecond); len(msgs) != 0 {
		t.Fatalf("replayed %d messages after snapshot", len(msgs))
	}
}

func TestSessionStreamCoalesces(t *testing.T) {
	s, st := newTestServer(t, 0)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	resp := openStream(t, ts.URL+"/v1/stream?view=sessions", testToken, nil)
	reader := newSSEReader(resp.Body)
	if msg := reader.next(t); msg.event != "snap" {
		t.Fatalf("first event = %q, want snap", msg.event)
	}

	items := make([]string, 0, 51)
	items = append(items, `{"source":"claude","session":"s1","kind":"session.start","body":{}}`)
	for i := 0; i < 50; i++ {
		items = append(items, `{"source":"claude","session":"s1","kind":"tool.start","body":{"tool":"Bash"}}`)
	}
	if rec := post(s.Handler(), testToken, "["+strings.Join(items, ",")+"]"); rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}
	waitForEvents(t, st, 51)

	msgs := reader.drain(300 * time.Millisecond)
	if len(msgs) == 0 || len(msgs) > 2 {
		t.Fatalf("burst produced %d messages, want 1-2", len(msgs))
	}

	last := map[string]any{}
	for _, m := range msgs {
		if m.event != "s" {
			t.Fatalf("unexpected event %q", m.event)
		}
		var patch struct {
			ID string         `json:"id"`
			P  map[string]any `json:"p"`
		}
		if err := json.Unmarshal([]byte(m.data), &patch); err != nil {
			t.Fatalf("decode patch: %v", err)
		}
		if patch.ID != "s1" {
			t.Fatalf("patch id = %q", patch.ID)
		}
		for k, v := range patch.P {
			last[k] = v
		}
	}
	if last["toolCalls"] != float64(50) {
		t.Fatalf("merged toolCalls = %v, want 50", last["toolCalls"])
	}
}

func TestStreamUnknownView(t *testing.T) {
	s, _ := newTestServer(t, 0)
	req := httptest.NewRequest(http.MethodGet, "/v1/stream?view=nope", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestSessionsSnapshotFromManySources(t *testing.T) {
	s, st := newTestServer(t, 0)
	items := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		items = append(items, fmt.Sprintf(`{"source":"a%d","session":"s%d","kind":"session.start","body":{}}`, i, i))
	}
	post(s.Handler(), testToken, "["+strings.Join(items, ",")+"]")
	waitForEvents(t, st, 4)

	var snap sessionsSnapshot
	if code := getJSON(t, s.Handler(), "/v1/sessions", &snap); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(snap.Sessions) != 4 {
		t.Fatalf("sessions = %d, want 4", len(snap.Sessions))
	}
	for i, sess := range snap.Sessions {
		if want := fmt.Sprintf("s%d", i); sess.ID != want {
			t.Fatalf("session %d = %q, want %q", i, sess.ID, want)
		}
	}
}
