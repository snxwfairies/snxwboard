package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postHook(h http.Handler, token, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/hooks/claude", strings.NewReader(payload))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestClaudeHookFoldsIntoSession(t *testing.T) {
	s, st := newTestServer(t, 0)
	h := s.Handler()

	payloads := []string{
		`{"session_id":"c1","hook_event_name":"SessionStart","cwd":"/nowhere/app","source":"startup"}`,
		`{"session_id":"c1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"API_KEY=abc123 make deploy"},"tool_use_id":"t1"}`,
		`{"session_id":"c1","hook_event_name":"PostToolUseFailure","tool_name":"Bash","tool_use_id":"t1","error":"exit 2"}`,
		`{"session_id":"c1","hook_event_name":"Notification","notification_type":"idle_prompt","message":"waiting"}`,
		`{"session_id":"c1","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build"}}`,
		`not json`,
		`{"session_id":"c1","hook_event_name":"SomeFutureEvent"}`,
	}
	for _, p := range payloads {
		rec := postHook(h, testToken, p)
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("hook %s: status %d body %q, want 204 and empty", p, rec.Code, rec.Body.String())
		}
	}

	// idle_prompt, bad JSON and unknown events are dropped: 4 stored.
	evs := waitForEvents(t, st, 4)
	if len(evs) != 4 {
		t.Fatalf("stored %d events, want 4", len(evs))
	}
	if got := string(evs[1].Body); !strings.Contains(got, `API_KEY=[redacted] make deploy`) {
		t.Fatalf("tool.start body not redacted: %s", got)
	}

	var snap sessionsSnapshot
	getJSON(t, h, "/v1/sessions", &snap)
	if len(snap.Sessions) != 1 {
		t.Fatalf("sessions = %+v", snap.Sessions)
	}
	got := snap.Sessions[0]
	if got.ID != "c1" || got.Status != "waiting_for_you" || got.ToolCalls != 1 || got.Errors != 1 || got.Tool != "" {
		t.Fatalf("session = %+v", got)
	}
}

func TestClaudeHookNeedsToken(t *testing.T) {
	s, st := newTestServer(t, 0)
	rec := postHook(s.Handler(), "wrong", `{"session_id":"c1","hook_event_name":"UserPromptSubmit","prompt":"hi"}`)
	// Still the empty 204 Claude Code expects, but nothing is stored.
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status %d body %q, want 204 and empty", rec.Code, rec.Body.String())
	}
	postHook(s.Handler(), testToken, `{"session_id":"c1","hook_event_name":"Stop"}`)
	evs := waitForEvents(t, st, 1)
	if len(evs) != 1 || evs[0].Kind != "turn.end" {
		t.Fatalf("stored %+v, want only the authorized event", evs)
	}
}

// A Write of a large file must not cost the event: the payload is parsed
// whole and clipped by the adapter.
func TestClaudeHookLargePayload(t *testing.T) {
	s, st := newTestServer(t, 0)
	big := strings.Repeat("x", 3<<20)
	postHook(s.Handler(), testToken, `{"session_id":"c1","hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/a.txt","content":"`+big+`"}}`)
	evs := waitForEvents(t, st, 1)
	if len(evs[0].Body) > 1000 {
		t.Fatalf("stored body is %d bytes, want the clipped event", len(evs[0].Body))
	}
}

// Claude Code in -p mode never sends SessionStart: the first event carries the
// session context instead.
func TestClaudeHookContextWithoutSessionStart(t *testing.T) {
	s, st := newTestServer(t, 0)
	dir := t.TempDir()
	postHook(s.Handler(), testToken, `{"session_id":"p1","hook_event_name":"UserPromptSubmit","cwd":"`+dir+`","prompt":"hi"}`)
	waitForEvents(t, st, 1)

	var snap struct {
		Sessions []struct {
			Cwd string `json:"cwd"`
		} `json:"sessions"`
	}
	getJSON(t, s.Handler(), "/v1/sessions", &snap)
	if len(snap.Sessions) != 1 || snap.Sessions[0].Cwd != dir {
		t.Fatalf("sessions = %+v, want cwd %s", snap.Sessions, dir)
	}
}
