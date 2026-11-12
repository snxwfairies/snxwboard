// Package claude turns Claude Code hook payloads into snxw events.
//
// Claude Code POSTs the hook's JSON input to an HTTP hook URL. Only the fields
// the board shows are kept, and long text is clipped, so an event stays a few
// hundred bytes even when a tool call carries a whole file.
package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Source is the event source for Claude Code sessions.
const Source = "claude"

const clip = 280

// Events lists the hook events snxw subscribes to, in settings order.
// SessionStart is absent: it accepts only command and mcp_tool hooks, so the
// first event of a session carries its context instead (see Context).
var Events = []string{
	"UserPromptSubmit", "PreToolUse", "PostToolUse",
	"PostToolUseFailure", "Notification", "PermissionRequest", "Stop",
	"StopFailure", "SubagentStart", "SubagentStop", "SessionEnd",
}

// Context adds cwd, repo and branch to ev.Body, for the first event of a
// session the daemon has not seen start.
func Context(ev *Event, cwd string) {
	if cwd == "" {
		return
	}
	if _, ok := ev.Body["cwd"]; ok {
		return
	}
	ev.Body["cwd"] = clipText(cwd)
	if repo, branch := gitInfo(cwd); repo != "" {
		ev.Body["repo"] = repo
		ev.Body["branch"] = branch
	}
}

// Event is the adapter's output: the same shape as one POST /v1/events item.
type Event struct {
	Session string
	Cwd     string // the payload's cwd, for Context
	Kind    string
	Body    map[string]any
}

type hookInput struct {
	SessionID      string          `json:"session_id"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	Source         string          `json:"source"`
	Model          json.RawMessage `json:"model"`
	Prompt         string          `json:"prompt"`
	ToolName       string          `json:"tool_name"`
	ToolInput      map[string]any  `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
	Error          string          `json:"error"`
	Message        string          `json:"message"`
	Title          string          `json:"title"`
	NotifType      string          `json:"notification_type"`
	LastAssistant  string          `json:"last_assistant_message"`
	Reason         string          `json:"reason"`
	AgentID        string          `json:"agent_id"`
	AgentType      string          `json:"agent_type"`
	PermissionMode string          `json:"permission_mode"`
}

// ErrSkip means the payload is valid but carries nothing worth storing.
var ErrSkip = errors.New("skip")

// Translate maps one hook payload to an event.
func Translate(raw []byte) (Event, error) {
	var in hookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return Event{}, err
	}
	if in.SessionID == "" || in.HookEventName == "" {
		return Event{}, errors.New("session_id and hook_event_name required")
	}
	ev := Event{Session: in.SessionID, Cwd: in.Cwd, Body: map[string]any{}}
	put := func(k, v string) {
		if v != "" {
			ev.Body[k] = clipText(v)
		}
	}

	switch in.HookEventName {
	case "SessionStart":
		ev.Kind = "session.start"
		put("cwd", in.Cwd)
		put("start", in.Source)
		put("model", modelName(in.Model))
		repo, branch := gitInfo(in.Cwd)
		put("repo", repo)
		put("branch", branch)
		if repo == "" && in.Cwd != "" {
			put("title", filepath.Base(in.Cwd))
		}
	case "UserPromptSubmit":
		ev.Kind = "prompt.submit"
		put("prompt", in.Prompt)
	case "PreToolUse":
		ev.Kind = "tool.start"
		put("tool", in.ToolName)
		put("id", in.ToolUseID)
		put("arg", toolArg(in.ToolInput))
	case "PostToolUse":
		ev.Kind = "tool.end"
		put("tool", in.ToolName)
		put("id", in.ToolUseID)
		ev.Body["ok"] = true
	case "PostToolUseFailure":
		ev.Kind = "tool.end"
		put("tool", in.ToolName)
		put("id", in.ToolUseID)
		put("error", in.Error)
		ev.Body["ok"] = false
	case "Notification", "PermissionRequest":
		ev.Kind = "attention"
		put("reason", in.NotifType)
		put("title", in.Title)
		put("message", in.Message)
		if in.HookEventName == "PermissionRequest" {
			ev.Body["reason"] = "permission"
			put("tool", in.ToolName)
			put("arg", toolArg(in.ToolInput))
		}
		if in.NotifType == "idle_prompt" {
			// Claude is simply waiting for the next prompt; Stop already
			// marked the session idle.
			return Event{}, ErrSkip
		}
	case "Stop":
		ev.Kind = "turn.end"
		put("last", in.LastAssistant)
	case "StopFailure":
		// The turn ended on an API error (rate limit, overload, auth): the
		// session is idle again, with one more error.
		ev.Kind = "turn.end"
		put("error", in.Error)
		ev.Body["ok"] = false
	case "SubagentStart":
		ev.Kind = "subagent.start"
		put("agent", in.AgentType)
		put("id", in.AgentID)
	case "SubagentStop":
		ev.Kind = "subagent.end"
		put("agent", in.AgentType)
		put("id", in.AgentID)
	case "SessionEnd":
		ev.Kind = "session.end"
		put("reason", in.Reason)
	default:
		return Event{}, ErrSkip
	}
	return ev, nil
}

// toolArg picks the one argument that says what a tool call is doing.
func toolArg(in map[string]any) string {
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description", "prompt"} {
		if s, ok := in[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// modelName accepts the model as a plain string or as {"id": ..., "display_name": ...}.
func modelName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var m struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}
	if json.Unmarshal(raw, &m) == nil {
		if m.ID != "" {
			return m.ID
		}
		return m.DisplayName
	}
	return ""
}

func clipText(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= clip {
		return s
	}
	r := []rune(s)
	return string(r[:clip-1]) + "…"
}

// gitInfo finds the enclosing repository and its branch by reading .git/HEAD,
// without running git. Worktrees (a .git file) are followed to their gitdir.
func gitInfo(cwd string) (repo, branch string) {
	if cwd == "" {
		return "", ""
	}
	dir := filepath.Clean(cwd)
	for i := 0; i < 64; i++ {
		gitPath := filepath.Join(dir, ".git")
		if fi, err := os.Stat(gitPath); err == nil {
			gitDir := gitPath
			if !fi.IsDir() {
				gitDir = worktreeGitDir(gitPath, dir)
			}
			return filepath.Base(dir), headBranch(gitDir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", ""
}

func worktreeGitDir(gitFile, dir string) string {
	b, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
	if p != "" && !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return p
}

func headBranch(gitDir string) string {
	if gitDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(b))
	if ref, ok := strings.CutPrefix(head, "ref: refs/heads/"); ok {
		return ref
	}
	if len(head) >= 7 {
		return head[:7] // detached HEAD: short sha
	}
	return ""
}
