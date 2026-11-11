package connect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/adapter/claude"
)

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestConnectClaudeMergesAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{
  "model": "x",
  "hooks": {
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "guard.sh"}]}]
  }
}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := ConnectClaude(path, "127.0.0.1:7766", "tok1")
	if err != nil || !changed {
		t.Fatalf("connect: changed=%v err=%v", changed, err)
	}
	m := readSettings(t, path)
	if m["model"] != "x" {
		t.Fatal("unrelated setting lost")
	}
	hooks := m["hooks"].(map[string]any)
	for _, ev := range claude.Events {
		if _, ok := hooks[ev]; !ok {
			t.Fatalf("no hook for %s", ev)
		}
	}
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 2 {
		t.Fatalf("PreToolUse groups = %d, want user's guard + snxw", len(pre))
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600 (file holds the token)", fi.Mode().Perm())
	}
	if _, err := os.Stat(path + ".snxw.bak"); err != nil {
		t.Fatalf("no backup: %v", err)
	}

	if changed, err := ConnectClaude(path, "127.0.0.1:7766", "tok1"); err != nil || changed {
		t.Fatalf("second connect: changed=%v err=%v, want no change", changed, err)
	}

	// A new token replaces the old hook instead of adding a second one.
	if _, err := ConnectClaude(path, "127.0.0.1:7766", "tok2"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "tok1") || strings.Count(string(b), "/v1/hooks/claude") != len(claude.Events) {
		t.Fatalf("stale or duplicate hooks:\n%s", b)
	}

	if changed, err := DisconnectClaude(path); err != nil || !changed {
		t.Fatalf("disconnect: changed=%v err=%v", changed, err)
	}
	m = readSettings(t, path)
	hooks = m["hooks"].(map[string]any)
	if len(hooks) != 1 || len(hooks["PreToolUse"].([]any)) != 1 {
		t.Fatalf("after disconnect hooks = %v, want only the user's guard", hooks)
	}
}

func TestConnectClaudeNewFileAndBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.local.json")
	if _, err := ConnectClaude(path, "127.0.0.1:7766", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := DisconnectClaude(path); err != nil {
		t.Fatal(err)
	}
	if m := readSettings(t, path); len(m) != 0 {
		t.Fatalf("after disconnect = %v, want {}", m)
	}

	bad := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(bad, []byte("{ not json"), 0o644)
	if _, err := ConnectClaude(bad, "127.0.0.1:7766", "t"); err == nil {
		t.Fatal("want an error for invalid JSON")
	}
	if b, _ := os.ReadFile(bad); string(b) != "{ not json" {
		t.Fatal("invalid file was modified")
	}
}

func TestConnectFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles-settings.json")
	link := filepath.Join(dir, "settings.json")
	os.WriteFile(real, []byte(`{}`), 0o644)
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	if _, err := ConnectClaude(link, "127.0.0.1:7766", "t"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced by a file")
	}
	if !strings.Contains(string(must(os.ReadFile(real))), "/v1/hooks/claude") {
		t.Fatal("target not updated")
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// A file that never had snxw hooks must come out of disconnect byte-for-byte
// unchanged, with no backup and its mode kept.
func TestDisconnectLeavesUnrelatedFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	orig := "{\n    \"zeta\": 1,\n    \"alpha\": {\"n\": 12345678901234567890},\n    \"hooks\": {\"Stop\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"x\"}]}]}\n}\n"
	os.WriteFile(path, []byte(orig), 0o644)
	changed, err := DisconnectClaude(path)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v, want no change", changed, err)
	}
	if b, _ := os.ReadFile(path); string(b) != orig {
		t.Fatalf("file rewritten:\n%s", b)
	}
	if _, err := os.Stat(path + ".snxw.bak"); err == nil {
		t.Fatal("backup written for a no-op")
	}
}

func TestConnectKeepsBigNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"n": 12345678901234567890}`), 0o644)
	if _, err := ConnectClaude(path, "127.0.0.1:7766", "t"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "12345678901234567890") {
		t.Fatalf("number changed:\n%s", b)
	}
}
