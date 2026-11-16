package mcpboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// newHTTPTestBackend points an HTTPBackend at a test daemon. The host:port is
// what the production backend builds its URLs from.
func newHTTPTestBackend(t *testing.T, h http.HandlerFunc) *HTTPBackend {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewHTTPBackend(strings.TrimPrefix(srv.URL, "http://"), "test-token")
}

func TestHTTPBackendSessions(t *testing.T) {
	b := newHTTPTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/sessions" {
			t.Errorf("path = %s, want /v1/sessions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q, want the daemon token", got)
		}
		io.WriteString(w, `{"sessions":[{"id":"calm","status":"running","toolCalls":2}]}`)
	})

	got, err := b.Sessions(context.Background())
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(got) != 1 || got[0].ID != "calm" || got[0].ToolCalls != 2 {
		t.Errorf("sessions = %+v", got)
	}
}

func TestHTTPBackendSessionsEmpty(t *testing.T) {
	b := newHTTPTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{}`)
	})
	got, err := b.Sessions(context.Background())
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("empty daemon response = %#v, want a non-nil empty slice", got)
	}
}

func TestHTTPBackendHistoryBuildsQuery(t *testing.T) {
	queries := make(chan url.Values, 2)
	b := newHTTPTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events" {
			t.Errorf("path = %s, want /v1/events", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q, want the daemon token", got)
		}
		queries <- r.URL.Query()
		io.WriteString(w, `{"events":[{"seq":4,"kind":"tool.start","session":"a b"}]}`)
	})

	got, err := b.History(context.Background(), "a b", 7)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(got) != 1 || got[0].Seq != 4 || got[0].Session != "a b" {
		t.Errorf("history = %+v", got)
	}
	q := <-queries
	if q.Get("session") != "a b" || q.Get("limit") != "7" {
		t.Errorf("query = %v, want session=a b and limit=7", q)
	}

	// An empty session and a zero limit mean "whole feed, daemon default":
	// neither key should be sent.
	if _, err := b.History(context.Background(), "", 0); err != nil {
		t.Fatalf("history without filters: %v", err)
	}
	q = <-queries
	if q.Has("session") || q.Has("limit") {
		t.Errorf("query = %v, want no session or limit", q)
	}
}

func TestHTTPBackendIngest(t *testing.T) {
	type request struct {
		method  string
		path    string
		auth    string
		ctype   string
		payload map[string]any
	}
	seen := make(chan request, 2)
	b := newHTTPTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		seen <- request{
			method:  r.Method,
			path:    r.URL.Path,
			auth:    r.Header.Get("Authorization"),
			ctype:   r.Header.Get("Content-Type"),
			payload: payload,
		}
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"accepted":1}`)
	})

	body := json.RawMessage(`{"message":"need a decision"}`)
	if err := b.Ingest(context.Background(), IngestEvent{Kind: "attention", Session: "calm", Body: body}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	got := <-seen
	if got.method != http.MethodPost || got.path != "/v1/events" {
		t.Errorf("request = %s %s, want POST /v1/events", got.method, got.path)
	}
	if got.auth != "Bearer test-token" {
		t.Errorf("authorization = %q, want the daemon token", got.auth)
	}
	if got.ctype != "application/json" {
		t.Errorf("content-type = %q, want application/json", got.ctype)
	}
	if got.payload["source"] != "mcp" {
		t.Errorf("source = %v, want the mcp default", got.payload["source"])
	}
	if got.payload["session"] != "calm" || got.payload["kind"] != "attention" {
		t.Errorf("payload = %v", got.payload)
	}
	payloadBody, ok := got.payload["body"].(map[string]any)
	if !ok || payloadBody["message"] != "need a decision" {
		t.Errorf("body = %v, want the message", got.payload["body"])
	}

	// An explicit source wins over the mcp default.
	if err := b.Ingest(context.Background(), IngestEvent{Kind: "tool.start", Session: "calm", Source: "agent"}); err != nil {
		t.Fatalf("ingest with source: %v", err)
	}
	if got := <-seen; got.payload["source"] != "agent" {
		t.Errorf("source = %v, want agent", got.payload["source"])
	}
}

func TestHTTPBackendIngestOnlyAccepts202(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusCreated, http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			b := newHTTPTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				io.WriteString(w, "rejected")
			})
			err := b.Ingest(context.Background(), IngestEvent{Kind: "attention", Session: "calm"})
			if err == nil {
				t.Fatalf("status %d was accepted, want an error", code)
			}
			if !strings.Contains(err.Error(), "ingest failed") || !strings.Contains(err.Error(), "rejected") {
				t.Errorf("error = %q, want the status and daemon message", err)
			}
		})
	}
}

func TestHTTPBackendGetErrors(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		b := newHTTPTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no token", http.StatusUnauthorized)
		})
		_, err := b.Sessions(context.Background())
		if err == nil {
			t.Fatal("401 was accepted")
		}
		for _, want := range []string{"/v1/sessions", "401", "no token"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to contain %q", err, want)
			}
		}
	})

	t.Run("decode", func(t *testing.T) {
		b := newHTTPTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{not json`)
		})
		_, err := b.Sessions(context.Background())
		if err == nil || !strings.Contains(err.Error(), "decode") {
			t.Errorf("error = %v, want a decode error", err)
		}
	})

	t.Run("transport", func(t *testing.T) {
		b := NewHTTPBackend("127.0.0.1:1", "test-token")
		b.client = &http.Client{Transport: errorTransport{errors.New("boom")}}
		_, err := b.Sessions(context.Background())
		if err == nil || !strings.Contains(err.Error(), "daemon not reachable at 127.0.0.1:1") {
			t.Errorf("error = %v, want the daemon address", err)
		}
	})
}

type errorTransport struct{ err error }

func (e errorTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }
