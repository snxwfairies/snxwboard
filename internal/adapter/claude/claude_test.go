package claude

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranslate(t *testing.T) {
	cases := []struct {
		name, in, kind string
		body           map[string]any
	}{
		{"prompt", `{"session_id":"s","hook_event_name":"UserPromptSubmit","prompt":"fix the bug"}`,
			"prompt.submit", map[string]any{"prompt": "fix the bug"}},
		{"pre bash", `{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test ./...","timeout":1}}`,
			"tool.start", map[string]any{"tool": "Bash", "arg": "go test ./..."}},
		{"pre edit", `{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"/a/b.go","old_string":"x"}}`,
			"tool.start", map[string]any{"tool": "Edit", "arg": "/a/b.go"}},
		{"post ok", `{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Read","tool_response":{"content":"huge"}}`,
			"tool.end", map[string]any{"tool": "Read", "ok": true}},
		{"post fail", `{"session_id":"s","hook_event_name":"PostToolUseFailure","tool_name":"Bash","error":"boom"}`,
			"tool.end", map[string]any{"tool": "Bash", "ok": false, "error": "boom"}},
		{"permission", `{"session_id":"s","hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs permission"}`,
			"attention", map[string]any{"reason": "permission_prompt", "message": "Claude needs permission"}},
		{"stop", `{"session_id":"s","hook_event_name":"Stop","last_assistant_message":"Done."}`,
			"turn.end", map[string]any{"last": "Done."}},
		{"end", `{"session_id":"s","hook_event_name":"SessionEnd","reason":"logout"}`,
			"session.end", map[string]any{"reason": "logout"}},
		{"model object", `{"session_id":"s","hook_event_name":"SessionStart","model":{"id":"m-1"}}`,
			"session.start", map[string]any{"model": "m-1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, err := Translate([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if ev.Session != "s" || ev.Kind != c.kind {
				t.Fatalf("got session %q kind %q", ev.Session, ev.Kind)
			}
			if len(ev.Body) != len(c.body) {
				t.Fatalf("body = %v, want %v", ev.Body, c.body)
			}
			for k, v := range c.body {
				if ev.Body[k] != v {
					t.Fatalf("body[%s] = %v, want %v (body %v)", k, ev.Body[k], v, ev.Body)
				}
			}
		})
	}
}

func TestTranslateSkipsAndRejects(t *testing.T) {
	for _, in := range []string{
		`{"session_id":"s","hook_event_name":"Notification","notification_type":"idle_prompt"}`,
		`{"session_id":"s","hook_event_name":"FutureThing"}`,
	} {
		if _, err := Translate([]byte(in)); !errors.Is(err, ErrSkip) {
			t.Errorf("%s: err = %v, want ErrSkip", in, err)
		}
	}
	for _, in := range []string{`{}`, `{"session_id":"s"}`, `[`} {
		if _, err := Translate([]byte(in)); err == nil || errors.Is(err, ErrSkip) {
			t.Errorf("%s: err = %v, want a rejection", in, err)
		}
	}
}

func TestClipKeepsEventsSmall(t *testing.T) {
	long := strings.Repeat("é", 5000)
	ev, err := Translate([]byte(`{"session_id":"s","hook_event_name":"UserPromptSubmit","prompt":"` + long + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(ev.Body["prompt"].(string))); n != clip {
		t.Fatalf("clipped to %d runes, want %d", n, clip)
	}
}

func TestGitInfo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "myrepo")
	sub := filepath.Join(root, "pkg", "x")
	must(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	must(t, os.MkdirAll(sub, 0o755))
	must(t, os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/feat/a\n"), 0o644))
	if repo, branch := gitInfo(sub); repo != "myrepo" || branch != "feat/a" {
		t.Fatalf("got %q %q", repo, branch)
	}

	// A worktree: .git is a file pointing at the real gitdir.
	wt := filepath.Join(t.TempDir(), "wt")
	gd := filepath.Join(root, ".git", "worktrees", "wt")
	must(t, os.MkdirAll(wt, 0o755))
	must(t, os.MkdirAll(gd, 0o755))
	must(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gd+"\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(gd, "HEAD"), []byte("0123456789abcdef\n"), 0o644))
	if repo, branch := gitInfo(wt); repo != "wt" || branch != "0123456" {
		t.Fatalf("worktree got %q %q", repo, branch)
	}

	if repo, branch := gitInfo(t.TempDir()); repo != "" || branch != "" {
		t.Fatalf("no repo got %q %q", repo, branch)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
