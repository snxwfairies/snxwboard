// Package connect wires coding agents to the local snxw daemon by editing their
// own config files. Every edit is idempotent and reversible.
package connect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/adapter/claude"
)

// hookPath marks a hook as ours: any http hook whose URL ends with it.
const hookPath = "/v1/hooks/claude"

// ClaudeSettingsPath returns ~/.claude/settings.json, or
// <dir>/.claude/settings.local.json for a single project.
func ClaudeSettingsPath(projectDir string) (string, error) {
	if projectDir != "" {
		return filepath.Join(projectDir, ".claude", "settings.local.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// ConnectClaude adds one http hook per event to the settings file at path,
// replacing any earlier snxw hook so a re-run refreshes the address and token.
// It returns true when the file changed.
func ConnectClaude(path, addr, token string) (bool, error) {
	return editClaude(path, func(hooks map[string]any) {
		for _, event := range claude.Events {
			groups := withoutOurs(hooks[event])
			groups = append(groups, map[string]any{
				"hooks": []any{map[string]any{
					"type":    "http",
					"url":     "http://" + addr + hookPath,
					"timeout": 2,
					"headers": map[string]any{"Authorization": "Bearer " + token},
				}},
			})
			hooks[event] = groups
		}
	})
}

// DisconnectClaude removes every snxw hook from the settings file at path.
func DisconnectClaude(path string) (bool, error) {
	return editClaude(path, func(hooks map[string]any) {
		for event, v := range hooks {
			groups := withoutOurs(v)
			if len(groups) == 0 {
				delete(hooks, event)
			} else {
				hooks[event] = groups
			}
		}
	})
}

func editClaude(path string, edit func(hooks map[string]any)) (bool, error) {
	// Settings are often a symlink into a dotfiles repo: edit the target.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	before, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	settings := map[string]any{}
	if len(bytes.TrimSpace(before)) > 0 {
		if err := json.Unmarshal(before, &settings); err != nil {
			return false, fmt.Errorf("%s is not valid JSON, not touching it: %w", path, err)
		}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	edit(hooks)
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return false, fmt.Errorf("encode settings: %w", err)
	}
	after := buf.Bytes()
	if bytes.Equal(before, after) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if len(before) > 0 {
		if err := os.WriteFile(path+".snxw.bak", before, 0o600); err != nil {
			return false, fmt.Errorf("write backup: %w", err)
		}
	}
	// The file now holds the daemon token: keep it private, and replace it
	// atomically so Claude Code never reads a half-written file.
	tmp := path + ".snxw.tmp"
	if err := os.WriteFile(tmp, after, 0o600); err != nil {
		return false, fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("replace %s: %w", path, err)
	}
	return true, nil
}

// withoutOurs returns the matcher groups of one event with snxw hooks removed;
// groups left with no hooks are dropped.
func withoutOurs(v any) []any {
	groups, _ := v.([]any)
	out := make([]any, 0, len(groups)+1)
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			out = append(out, g)
			continue
		}
		hs, _ := gm["hooks"].([]any)
		kept := make([]any, 0, len(hs))
		for _, h := range hs {
			if !isOurs(h) {
				kept = append(kept, h)
			}
		}
		if len(kept) == 0 && len(hs) > 0 {
			continue
		}
		if len(kept) != len(hs) {
			gm["hooks"] = kept
		}
		out = append(out, gm)
	}
	return out
}

func isOurs(h any) bool {
	hm, ok := h.(map[string]any)
	if !ok {
		return false
	}
	url, _ := hm["url"].(string)
	return hm["type"] == "http" && strings.HasSuffix(url, hookPath)
}
