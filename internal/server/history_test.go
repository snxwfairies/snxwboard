package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/store"
)

type history struct {
	Events []store.Event `json:"events"`
}

func TestHistoryPaging(t *testing.T) {
	s, st := newTestServer(t, 0)
	var b strings.Builder
	b.WriteString("[")
	for i := 1; i <= 5; i++ {
		if i > 1 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"source":"x","session":"a","kind":"tool.start","body":{"n":%d}},{"source":"x","session":"b","kind":"tool.start"}`, i)
	}
	b.WriteString("]")
	post(s.Handler(), testToken, b.String())
	waitForEvents(t, st, 10)

	var newest history
	if code := getJSON(t, s.Handler(), "/v1/events?session=a&limit=2", &newest); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(newest.Events) != 2 || newest.Events[0].Seq != 9 || newest.Events[1].Seq != 7 {
		t.Fatalf("newest = %+v, want seqs 9,7 (session a only, newest first)", newest.Events)
	}

	var older history
	getJSON(t, s.Handler(), "/v1/events?session=a&before=7&limit=10", &older)
	if len(older.Events) != 3 || older.Events[0].Seq != 5 || older.Events[2].Seq != 1 {
		t.Fatalf("older = %+v, want 5,3,1", older.Events)
	}

	var after history
	getJSON(t, s.Handler(), "/v1/events?session=a&after=5", &after)
	if len(after.Events) != 2 || after.Events[0].Seq != 7 || after.Events[1].Seq != 9 {
		t.Fatalf("after = %+v, want 7,9 (oldest first)", after.Events)
	}

	var none history
	getJSON(t, s.Handler(), "/v1/events?session=zzz", &none)
	if none.Events == nil || len(none.Events) != 0 {
		t.Fatalf("unknown session = %+v, want []", none.Events)
	}
}

func TestHistoryValidation(t *testing.T) {
	s, _ := newTestServer(t, 0)
	for _, q := range []string{"", "?session=a&after=x", "?session=a&limit=-1"} {
		var out map[string]any
		if code := getJSON(t, s.Handler(), "/v1/events"+q, &out); code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", q, code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/events?session=a", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
}

func TestUIServedWithCSP(t *testing.T) {
	s, _ := newTestServer(t, 0)
	for path, ctype := range map[string]string{"/": "text/html", "/app.js": "javascript", "/app.css": "text/css"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), ctype) {
			t.Errorf("%s: content-type %q", path, rec.Header().Get("Content-Type"))
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
			t.Errorf("%s: missing CSP", path)
		}
	}
}

// `snxw emit` posts without a session: the fold files it under the source, and
// the history endpoint must find it under the same key.
func TestHistoryForSessionlessEvents(t *testing.T) {
	s, st := newTestServer(t, 0)
	post(s.Handler(), testToken, `{"source":"cli","kind":"tool.start","body":{"tool":"build"}}`)
	waitForEvents(t, st, 1)

	var snap sessionsSnapshot
	getJSON(t, s.Handler(), "/v1/sessions", &snap)
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "cli" {
		t.Fatalf("sessions = %+v", snap.Sessions)
	}
	var h history
	getJSON(t, s.Handler(), "/v1/events?session=cli", &h)
	if len(h.Events) != 1 {
		t.Fatalf("history for cli = %+v, want the event", h.Events)
	}
}
