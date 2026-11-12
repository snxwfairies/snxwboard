package fold

import (
	"encoding/json"
	"testing"

	"github.com/snxwfairies/snxwboard/internal/store"
)

// apply folds one event into a fresh-seq session and fails when nothing changed.
func apply(t *testing.T, f *Folder, seq int64, kind, body string) map[string]any {
	t.Helper()
	p, changed := f.Apply(store.Event{
		Seq: seq, TS: 1000 + seq, Source: "claude", Session: "s1", Kind: kind, Body: json.RawMessage(body),
	})
	if !changed {
		t.Fatalf("%s: changed = false, want true", kind)
	}
	if p["updatedSeq"] != seq {
		t.Fatalf("%s: updatedSeq = %v, want %d", kind, p["updatedSeq"], seq)
	}
	if p["updatedAt"] != int64(1000)+seq {
		t.Fatalf("%s: updatedAt = %v, want %d", kind, p["updatedAt"], 1000+seq)
	}
	return p
}

func want(t *testing.T, p map[string]any, key string, val any) {
	t.Helper()
	if p[key] != val {
		t.Fatalf("%s = %v, want %v (patch %v)", key, p[key], val, p)
	}
}

func absent(t *testing.T, p map[string]any, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, ok := p[key]; ok {
			t.Fatalf("%s present in patch %v, want absent", key, p)
		}
	}
}

func TestSessionStart(t *testing.T) {
	f := New()
	p := apply(t, f, 1, KindSessionStart,
		`{"cwd":"/w","repo":"r","branch":"main","model":"m","title":"fix it"}`)

	want(t, p, "status", StatusRunning)
	want(t, p, "cwd", "/w")
	want(t, p, "repo", "r")
	want(t, p, "branch", "main")
	want(t, p, "model", "m")
	want(t, p, "title", "fix it")
	want(t, p, "source", "claude")
	want(t, p, "startedAt", int64(1001))

	// Same values again: only the bookkeeping fields move.
	p = apply(t, f, 2, KindSessionStart,
		`{"cwd":"/w","repo":"r","branch":"main","model":"m","title":"fix it"}`)
	absent(t, p, "status", "cwd", "repo", "branch", "model", "title", "source", "startedAt")
}

func TestToolTransitions(t *testing.T) {
	f := New()
	apply(t, f, 1, KindSessionStart, `{}`)

	p := apply(t, f, 2, KindToolStart, `{"tool":"Bash"}`)
	want(t, p, "tool", "Bash")
	want(t, p, "toolCalls", 1)
	absent(t, p, "status", "errors")

	// A second tool start while running changes only the tool and count.
	p = apply(t, f, 3, KindToolStart, `{"tool":"Edit"}`)
	want(t, p, "tool", "Edit")
	want(t, p, "toolCalls", 2)
	absent(t, p, "status")

	p = apply(t, f, 4, KindToolEnd, `{"tool":"Edit","ok":true}`)
	want(t, p, "tool", "")
	absent(t, p, "errors", "status")

	p = apply(t, f, 5, KindToolStart, `{"tool":"Bash"}`)
	p = apply(t, f, 6, KindToolEnd, `{"tool":"Bash","ok":false}`)
	want(t, p, "errors", 1)

	s, _ := f.Get("s1")
	if s.Tool != "" || s.ToolCalls != 3 || s.Errors != 1 {
		t.Fatalf("session = %+v", s)
	}
}

func TestAttentionAndTurn(t *testing.T) {
	f := New()
	apply(t, f, 1, KindSessionStart, `{}`)
	apply(t, f, 2, KindToolStart, `{"tool":"Bash"}`)

	p := apply(t, f, 3, KindAttention, `{"reason":"permission"}`)
	want(t, p, "status", StatusWaiting)
	absent(t, p, "tool")

	p = apply(t, f, 4, KindAttention, `{"reason":"permission"}`)
	absent(t, p, "status")

	p = apply(t, f, 5, KindPromptSubmit, `{}`)
	want(t, p, "status", StatusRunning)
	want(t, p, "tool", "")

	p = apply(t, f, 6, KindTurnEnd, `{}`)
	want(t, p, "status", StatusIdle)
	p = apply(t, f, 7, KindTurnEnd, `{}`)
	absent(t, p, "status")
}

func TestSessionEnd(t *testing.T) {
	f := New()
	apply(t, f, 1, KindSessionStart, `{}`)

	p := apply(t, f, 2, KindSessionEnd, `{"ok":true}`)
	want(t, p, "status", StatusDone)

	f2 := New()
	apply(t, f2, 1, KindSessionStart, `{}`)
	p = apply(t, f2, 2, KindSessionEnd, `{"ok":false}`)
	want(t, p, "status", StatusFailed)
}

func TestErrorAndUnknownKind(t *testing.T) {
	f := New()
	apply(t, f, 1, KindSessionStart, `{}`)

	p := apply(t, f, 2, KindError, `{"message":"boom"}`)
	want(t, p, "errors", 1)

	// Unknown kinds only move the bookkeeping fields.
	p = apply(t, f, 3, "vendor.whatever", `{"anything":true}`)
	if len(p) != 2 {
		t.Fatalf("patch = %v, want only updatedAt and updatedSeq", p)
	}
	absent(t, p, "status", "tool", "errors", "toolCalls")

	// An unknown kind for a session we never saw creates nothing.
	if _, changed := f.Apply(store.Event{Seq: 4, TS: 1004, Source: "claude", Session: "other", Kind: "vendor.whatever"}); changed {
		t.Fatal("unknown kind created a session")
	}
}

func TestSessionIDFallsBackToSource(t *testing.T) {
	f := New()
	p, changed := f.Apply(store.Event{Seq: 1, TS: 10, Source: "cli", Kind: KindPromptSubmit})
	if !changed {
		t.Fatal("changed = false")
	}
	want(t, p, "status", StatusRunning)
	if _, ok := f.Get("cli"); !ok {
		t.Fatal("session not stored under source")
	}
}

func TestLoadSeedsState(t *testing.T) {
	f := New()
	f.Load([]Session{{ID: "s1", Source: "claude", Status: StatusIdle, ToolCalls: 41, UpdatedSeq: 9}})

	p, changed := f.Apply(store.Event{Seq: 10, TS: 100, Source: "claude", Session: "s1", Kind: KindToolStart, Body: json.RawMessage(`{"tool":"Bash"}`)})
	if !changed {
		t.Fatal("changed = false")
	}
	want(t, p, "toolCalls", 42)
	want(t, p, "status", StatusRunning)

	s, _ := f.Get("s1")
	if s.Source != "claude" || s.Status != StatusRunning || s.UpdatedSeq != 10 {
		t.Fatalf("session = %+v", s)
	}
}

func TestToolEndAfterPermissionResumesRunning(t *testing.T) {
	f := New()
	f.Apply(store.Event{Seq: 1, Source: "claude", Session: "s", Kind: KindToolStart, Body: []byte(`{"tool":"Bash"}`)})
	f.Apply(store.Event{Seq: 2, Source: "claude", Session: "s", Kind: KindAttention})
	if s, _ := f.Get("s"); s.Status != StatusWaiting {
		t.Fatalf("after attention: %s", s.Status)
	}
	p, _ := f.Apply(store.Event{Seq: 3, Source: "claude", Session: "s", Kind: KindToolEnd, Body: []byte(`{"ok":true}`)})
	if s, _ := f.Get("s"); s.Status != StatusRunning || p["status"] != StatusRunning {
		t.Fatalf("after approved tool finished: status %s, patch %v", s.Status, p)
	}
}

func TestFailedTurnCountsAnError(t *testing.T) {
	f := New()
	f.Apply(store.Event{Seq: 1, Source: "claude", Session: "s", Kind: KindPromptSubmit})
	f.Apply(store.Event{Seq: 2, Source: "claude", Session: "s", Kind: KindTurnEnd, Body: []byte(`{"ok":false}`)})
	if s, _ := f.Get("s"); s.Status != StatusIdle || s.Errors != 1 {
		t.Fatalf("got %+v, want idle with 1 error", s)
	}
}

func TestRestoreUndoesAFold(t *testing.T) {
	f := New()
	f.Apply(store.Event{Seq: 1, Source: "x", Session: "old", Kind: KindToolStart})
	before, _ := f.Get("old")
	prev := map[string]*Session{"old": &before, "new": nil}
	f.Apply(store.Event{Seq: 2, Source: "x", Session: "old", Kind: KindToolStart})
	f.Apply(store.Event{Seq: 3, Source: "x", Session: "new", Kind: KindToolStart})
	f.Restore(prev)
	if s, _ := f.Get("old"); s.ToolCalls != 1 {
		t.Fatalf("old not restored: %+v", s)
	}
	if _, ok := f.Get("new"); ok {
		t.Fatal("new session survived the undo")
	}
}
