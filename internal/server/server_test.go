package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/store"
)

const testToken = "0123456789abcdef"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestServer starts a server with its writer running.
func newTestServer(t *testing.T, capacity int) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	s := New(Options{Store: st, Token: testToken, Version: "test", Logger: discardLogger(), IngestCapacity: capacity})
	s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return s, st
}

func post(h http.Handler, token, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(payload))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func waitForEvents(t *testing.T, st *store.Store, want int) []store.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs, err := st.QueryAfter(context.Background(), 0, 1000)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(evs) >= want {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d persisted events", want)
	return nil
}

func TestAuth(t *testing.T) {
	s, _ := newTestServer(t, 0)
	h := s.Handler()

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"wrong scheme", "Basic " + testToken, http.StatusUnauthorized},
		{"valid", "Bearer " + testToken, http.StatusAccepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(`{"source":"cli","kind":"x"}`))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body)
			}
		})
	}

	t.Run("stream requires token", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/stream", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
}

func TestHealthzOpen(t *testing.T) {
	s, _ := newTestServer(t, 0)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK || body.Version != "test" {
		t.Fatalf("health = %+v", body)
	}
}

func TestIngestSingle(t *testing.T) {
	s, st := newTestServer(t, 0)
	rec := post(s.Handler(), testToken, `{"source":"claude","session":"s1","kind":"tool","body":{"name":"Bash"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body)
	}

	evs := waitForEvents(t, st, 1)
	got := evs[0]
	if got.Seq != 1 {
		t.Errorf("seq = %d, want 1", got.Seq)
	}
	if got.Source != "claude" || got.Session != "s1" || got.Kind != "tool" {
		t.Errorf("event = %+v", got)
	}
	if string(got.Body) != `{"name":"Bash"}` {
		t.Errorf("body = %s", got.Body)
	}
	if got.TS == 0 {
		t.Error("ts is zero")
	}
}

func TestIngestArray(t *testing.T) {
	s, st := newTestServer(t, 0)
	payload := `[
		{"source":"cli","kind":"a"},
		{"source":"cli","kind":"b","body":[1,2,3]},
		{"source":"cli","kind":"c","body":"text"}
	]`
	rec := post(s.Handler(), testToken, payload)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body)
	}

	evs := waitForEvents(t, st, 3)
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d, want %d", i, ev.Seq, i+1)
		}
	}
	if string(evs[0].Body) != "null" {
		t.Errorf("missing body = %s, want null", evs[0].Body)
	}
	if string(evs[1].Body) != "[1,2,3]" {
		t.Errorf("body = %s", evs[1].Body)
	}
}

func TestIngestValidation(t *testing.T) {
	s, _ := newTestServer(t, 0)
	h := s.Handler()

	long := strings.Repeat("x", 65)
	many := make([]string, 501)
	for i := range many {
		many[i] = `{"source":"cli","kind":"x"}`
	}

	cases := []struct {
		name    string
		payload string
	}{
		{"empty body", ""},
		{"not json", "nope"},
		{"no source", `{"kind":"x"}`},
		{"no kind", `{"source":"cli"}`},
		{"source too long", fmt.Sprintf(`{"source":%q,"kind":"x"}`, long)},
		{"kind too long", fmt.Sprintf(`{"source":"cli","kind":%q}`, long)},
		{"empty array", `[]`},
		{"too many events", "[" + strings.Join(many, ",") + "]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(h, testToken, tc.payload)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
		})
	}

	t.Run("oversized body", func(t *testing.T) {
		big := `{"source":"cli","kind":"x","body":"` + strings.Repeat("a", maxBodyBytes) + `"}`
		rec := post(h, testToken, big)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
	})
}

func TestIngestBufferFull(t *testing.T) {
	// No writer is started, so with a capacity of 1 the second event cannot be
	// accepted and the handler must answer 503 instead of blocking.
	st, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	s := New(Options{Store: st, Token: testToken, Version: "test", Logger: discardLogger(), IngestCapacity: 1})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Shutdown(ctx)
	}()

	rec := post(s.Handler(), testToken, `[{"source":"cli","kind":"a"},{"source":"cli","kind":"b"}]`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	// All-or-nothing: a rejected batch must leave nothing queued, so a retry
	// cannot duplicate events.
	if n := len(s.ingest); n != 0 {
		t.Fatalf("queued after 503 = %d, want 0", n)
	}
}

func TestShutdownDrainsQueuedEvents(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	s := New(Options{Store: st, Token: testToken, Version: "test", Logger: discardLogger(), IngestCapacity: 1 << 10})
	s.Start()

	items := make([]string, 20)
	for i := range items {
		items[i] = fmt.Sprintf(`{"source":"cli","kind":"k%d"}`, i)
	}
	rec := post(s.Handler(), testToken, "["+strings.Join(items, ",")+"]")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("post status = %d", rec.Code)
	}

	// Shutdown must persist everything already accepted, not just what the
	// writer happened to batch.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	evs, err := st.QueryAfter(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(evs) != len(items) {
		t.Fatalf("persisted %d events, want %d", len(evs), len(items))
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d seq = %d", i, ev.Seq)
		}
	}
}

func TestShutdownEndsStream(t *testing.T) {
	s, _ := newTestServer(t, 0)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	resp := openStream(t, ts.URL+"/v1/stream", testToken, nil)
	reader := newSSEReader(resp.Body)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	select {
	case err := <-reader.errs:
		if err != nil {
			t.Fatalf("stream ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream still open after shutdown")
	}
}

// sseMsg is one parsed SSE message.
type sseMsg struct {
	id    string
	event string
	data  string
}

type sseReader struct {
	msgs chan sseMsg
	errs chan error
}

func newSSEReader(r io.Reader) *sseReader {
	sr := &sseReader{msgs: make(chan sseMsg, 64), errs: make(chan error, 1)}
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		var m sseMsg
		flush := func() {
			if m.id != "" || m.data != "" {
				sr.msgs <- m
				m = sseMsg{}
			}
		}
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				flush()
			case strings.HasPrefix(line, "id: "):
				m.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				m.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				m.data = strings.TrimPrefix(line, "data: ")
			}
		}
		sr.errs <- sc.Err()
	}()
	return sr
}

func (r *sseReader) next(t *testing.T) sseMsg {
	t.Helper()
	select {
	case m := <-r.msgs:
		return m
	case err := <-r.errs:
		t.Fatalf("stream ended: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an SSE message")
	}
	return sseMsg{}
}

func (m sseMsg) asEvent(t *testing.T) store.Event {
	t.Helper()
	var ev store.Event
	if err := json.Unmarshal([]byte(m.data), &ev); err != nil {
		t.Fatalf("decode event %q: %v", m.data, err)
	}
	return ev
}

// openStream connects to /v1/stream and returns the response; the caller must
// close the body and cancel the request.
func openStream(t *testing.T, url, token string, header http.Header) *http.Response {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		t.Fatalf("open stream: status %d: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("open stream: content-type = %q", got)
	}
	return resp
}

func TestStreamLive(t *testing.T) {
	s, _ := newTestServer(t, 0)
	ts := httptest.NewServer(s.Handler())
	// Cleanups run LIFO: openStream closes the body before ts.Close waits.
	t.Cleanup(ts.Close)

	resp := openStream(t, ts.URL+"/v1/stream", testToken, nil)
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("x-accel-buffering = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache-control = %q", got)
	}

	rec := post(s.Handler(), testToken, `{"source":"cli","session":"s1","kind":"live","body":{"n":1}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("post status = %d", rec.Code)
	}

	msg := newSSEReader(resp.Body).next(t)
	if msg.id != "1" {
		t.Fatalf("id = %q, want 1", msg.id)
	}
	ev := msg.asEvent(t)
	if ev.Kind != "live" || ev.Session != "s1" || ev.Seq != 1 {
		t.Fatalf("event = %+v", ev)
	}
}

func TestStreamResume(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header http.Header
		url    string
	}{
		{name: "last-event-id header", header: http.Header{"Last-Event-Id": []string{"2"}}},
		{name: "since query", url: "?since=2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, st := newTestServer(t, 0)
			ts := httptest.NewServer(s.Handler())
			t.Cleanup(ts.Close)

			rec := post(s.Handler(), testToken, `[
				{"source":"cli","kind":"e1"},
				{"source":"cli","kind":"e2"},
				{"source":"cli","kind":"e3"},
				{"source":"cli","kind":"e4"},
				{"source":"cli","kind":"e5"}
			]`)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("post status = %d", rec.Code)
			}
			waitForEvents(t, st, 5)

			resp := openStream(t, ts.URL+"/v1/stream"+tc.url, testToken, tc.header)
			reader := newSSEReader(resp.Body)

			for _, want := range []string{"3", "4", "5"} {
				msg := reader.next(t)
				if msg.id != want {
					t.Fatalf("replayed id = %q, want %q", msg.id, want)
				}
			}

			// Across the replay/live boundary the client must see each seq
			// once, in order.
			post(s.Handler(), testToken, `{"source":"cli","kind":"e6"}`)
			msg := reader.next(t)
			if msg.id != "6" {
				t.Fatalf("live id = %q, want 6", msg.id)
			}
			if ev := msg.asEvent(t); ev.Kind != "e6" {
				t.Fatalf("live event = %+v", ev)
			}
		})
	}
}

func TestStreamRejectsBadCursor(t *testing.T) {
	s, _ := newTestServer(t, 0)
	req := httptest.NewRequest(http.MethodGet, "/v1/stream", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Last-Event-ID", "abc")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestBindAddr(t *testing.T) {
	t.Setenv("SNXW_ALLOW_REMOTE", "")

	allowed := []string{"127.0.0.1:7766", "[::1]:7766", "localhost:7766"}
	for _, addr := range allowed {
		if err := checkBindAddr(addr); err != nil {
			t.Errorf("checkBindAddr(%q) = %v, want nil", addr, err)
		}
	}

	refused := []string{"0.0.0.0:7766", ":7766", "192.168.1.5:7766"}
	for _, addr := range refused {
		if err := checkBindAddr(addr); err == nil {
			t.Errorf("checkBindAddr(%q) = nil, want error", addr)
		}
	}

	t.Setenv("SNXW_ALLOW_REMOTE", "1")
	if err := checkBindAddr("0.0.0.0:7766"); err != nil {
		t.Errorf("with SNXW_ALLOW_REMOTE=1: %v", err)
	}
}

func TestNonLoopbackBindRefused(t *testing.T) {
	t.Setenv("SNXW_ALLOW_REMOTE", "")
	s, _ := newTestServer(t, 0)
	if err := s.Serve(context.Background(), "0.0.0.0:0"); err == nil {
		t.Fatal("Serve on 0.0.0.0 succeeded, want refusal")
	}
}

func BenchmarkIngest(b *testing.B) {
	st, err := store.Open(filepath.Join(b.TempDir(), "events.db"))
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	defer st.Close()

	s := New(Options{Store: st, Token: testToken, Version: "bench", Logger: discardLogger(), IngestCapacity: 1 << 16})
	s.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.Shutdown(ctx)
	}()

	h := s.Handler()
	payload := []byte(`{"source":"claude","session":"bench","kind":"tool","body":{"tool":"Bash"}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			b.Fatalf("status = %d at iteration %d", rec.Code, i)
		}
	}
}
