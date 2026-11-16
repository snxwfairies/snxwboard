// Package mcpboard serves the board to agent clients over the Model Context
// Protocol, so an agent can act on what the daemon folded instead of only
// looking at it: read the sessions, read the feed, and ask for the human.
//
// Only the subset a stdio client needs is implemented — initialize,
// tools/list, tools/call and ping — because every MCP client has to support
// that much, and the rest (resources, prompts, sampling) is deliberately out
// of scope for the first cut. Messages are newline-delimited JSON-RPC 2.0, one
// object per line, which is what the stdio transport specifies.
//
// The backend is an interface so the tools can be tested without a daemon and
// so the same tools can later be served over streamable HTTP in-process.
package mcpboard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/snxwfairies/snxwboard/internal/features"
	"github.com/snxwfairies/snxwboard/internal/trajectory"
)

// ProtocolVersion is the MCP revision this server speaks. A client asking for
// a different one is answered with this, and decides for itself whether it can
// proceed.
const ProtocolVersion = "2025-06-18"

// Session is the folded state of one agent session, as the board sees it.
type Session struct {
	ID        string `json:"id"`
	Source    string `json:"source,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status"`
	Tool      string `json:"tool,omitempty"`
	ToolCalls int    `json:"toolCalls"`
	Errors    int    `json:"errors"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
}

// Event is one entry of the append-only feed.
type Event struct {
	Seq     int64           `json:"seq"`
	TS      int64           `json:"ts,omitempty"`
	Source  string          `json:"source,omitempty"`
	Session string          `json:"session,omitempty"`
	Kind    string          `json:"kind"`
	Body    json.RawMessage `json:"body,omitempty"`
}

// IngestEvent is a new event an agent wants recorded.
type IngestEvent struct {
	Kind    string          `json:"kind"`
	Session string          `json:"session,omitempty"`
	Source  string          `json:"source,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
}

// Backend is the slice of the daemon the tools need. Every method must respect
// the caller's context: a tools/call from an agent must never outlive it.
type Backend interface {
	Sessions(ctx context.Context) ([]Session, error)
	History(ctx context.Context, session string, limit int) ([]Event, error)
	Ingest(ctx context.Context, ev IngestEvent) error
}

// Server answers MCP requests from one client at a time.
type Server struct {
	backend  Backend
	features features.Set
	// mu serialises writes to out; the loop itself is single-threaded, but a
	// caller may add notifications later without re-deriving that guarantee.
	mu sync.Mutex
}

// New returns a server reading the given backend. A feature set that is off hides
// its tools: an agent cannot call what the operator has not enabled, and a tool it
// cannot see is clearer than one that refuses.
func New(b Backend, set features.Set) *Server { return &Server{backend: b, features: set} }

// gatedTools maps a tool to the feature that must be on for it to exist.
var gatedTools = map[string]string{
	"search_feed":       "session_search",
	"export_trajectory": "trajectory",
}

// Serve reads newline-delimited JSON-RPC from in and writes answers to out
// until in is closed or ctx is cancelled.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	w := bufio.NewWriter(out)
	defer w.Flush()

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			// A line we cannot parse cannot be answered to any id; tell the
			// client with a null id, as JSON-RPC requires.
			if err := s.write(w, errorResponse(nil, codeParse, "invalid JSON: "+err.Error())); err != nil {
				return err
			}
			continue
		}
		resp, send := s.handle(ctx, req)
		if !send {
			continue // a notification: no response, ever
		}
		if err := s.write(w, resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) write(w *bufio.Writer, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParse    = -32700
	codeInvalid  = -32600
	codeMethod   = -32601
	codeParams   = -32602
	codeInternal = -32603
)

func errorResponse(id json.RawMessage, code int, msg string) response {
	if id == nil {
		id = json.RawMessage("null")
	}
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// handle answers one request. The bool is false for notifications, which take
// no reply.
func (s *Server) handle(ctx context.Context, req request) (response, bool) {
	if req.ID == nil {
		// notifications/initialized, notifications/cancelled, anything else:
		// nothing to say and nothing to do.
		return response{}, false
	}
	switch req.Method {
	case "initialize":
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "snxwboard", "version": "1"},
			"instructions": "Live state of every coding-agent session on this machine. " +
				"Call whats_running before starting work so you do not edit a repo another agent is in, " +
				"and notify_me when you need the human.",
		}}, true
	case "ping":
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}, true
	case "tools/list":
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": s.tools()}}, true
	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &call); err != nil {
			return errorResponse(req.ID, codeParams, "params must be {name, arguments}"), true
		}
		res, err := s.callTool(ctx, call.Name, call.Arguments)
		if err != nil {
			var unknown unknownTool
			if errors.As(err, &unknown) {
				return errorResponse(req.ID, codeParams, err.Error()), true
			}
			// A tool that failed is a result, not a protocol error: the model
			// needs to read the message and retry differently.
			return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
				"isError": true,
			}}, true
		}
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": res}},
		}}, true
	default:
		return errorResponse(req.ID, codeMethod, "unknown method "+req.Method), true
	}
}

type unknownTool struct{ name string }

func (u unknownTool) Error() string { return "unknown tool " + u.name }

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func object(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	} else {
		schema["required"] = []string{}
	}
	return schema
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// tools is the catalogue minus everything the operator has not enabled.
func (s *Server) tools() []tool {
	out := make([]tool, 0, len(catalogue()))
	for _, t := range catalogue() {
		if need, gated := gatedTools[t.Name]; gated && !s.features.Enabled(need) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func catalogue() []tool {
	return []tool{
		{
			Name: "whats_running",
			Description: "Summary of every session on this machine: counts by status, and which sessions are " +
				"waiting on the human. Call this first when deciding whether a repo is safe to touch.",
			InputSchema: object(nil),
		},
		{
			Name:        "list_sessions",
			Description: "Every session, sessions waiting for the human first. Filter by status when you only care about one.",
			InputSchema: object(map[string]any{
				"status": str("Keep only this status: running, waiting_for_you, idle, done or failed."),
				"repo":   str("Keep only sessions whose repo contains this text."),
			}),
		},
		{
			Name:        "get_session",
			Description: "One session by id, with its current tool, counts and status.",
			InputSchema: object(map[string]any{"id": str("Session id, as returned by list_sessions.")}, "id"),
		},
		{
			Name:        "search_feed",
			Description: "Search recent events for a text match. Use it to find what an agent was doing when something broke.",
			InputSchema: object(map[string]any{
				"query":   str("Case-insensitive text to look for in the event kind or body."),
				"session": str("Only events from this session id."),
				"limit":   map[string]any{"type": "integer", "description": "Maximum events to scan. Default 200, max 1000.", "minimum": 1, "maximum": 1000},
			}, "query"),
		},
		{
			Name: "notify_me",
			Description: "Ask the human for attention. Records an attention event, which puts the session at the top " +
				"of the board and fires a desktop notification, then returns immediately.",
			InputSchema: object(map[string]any{
				"message": str("What you need from the human, in one line."),
				"session": str("Session to attach it to. Defaults to a session named after this client."),
			}, "message"),
		},
		{
			Name: "export_trajectory",
			Description: "Export one session's event log as a JSONL trajectory: one numbered step per event, oldest first, " +
				"already redacted, ready for a batch run or a replay. Needs the trajectory feature.",
			InputSchema: object(map[string]any{
				"session": str("Session id to export."),
				"limit":   map[string]any{"type": "integer", "description": "Maximum events. Default 1000, max 5000.", "minimum": 1, "maximum": 5000},
			}, "session"),
		},
		{
			Name:        "snxw_ingest",
			Description: "Record one event from this agent, so the board shows what you are doing. Adapters use this; an agent without one can too.",
			InputSchema: object(map[string]any{
				"kind":    str("Event kind: session.start, tool.start, tool.end, prompt.submit, attention, turn.end, session.end or error."),
				"session": str("Session id to attribute the event to."),
				"body":    map[string]any{"type": "object", "description": "Optional event payload, stored as compact JSON."},
			}, "kind"),
		},
	}
}

func (s *Server) callTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	// A gated tool is refused even if a client remembers it from a run where the
	// feature was on: the gate belongs to the daemon, not to the client.
	if need, gated := gatedTools[name]; gated && !s.features.Enabled(need) {
		return "", fmt.Errorf("tool %q is off: enable the %q feature with `snxw features enable %s`", name, need, need)
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	switch name {
	case "whats_running":
		return s.whatsRunning(ctx)
	case "list_sessions":
		var in struct {
			Status string `json:"status"`
			Repo   string `json:"repo"`
		}
		_ = json.Unmarshal(args, &in)
		sessions, err := s.backend.Sessions(ctx)
		if err != nil {
			return "", err
		}
		kept := make([]Session, 0, len(sessions))
		for _, sess := range sessions {
			if in.Status != "" && sess.Status != in.Status {
				continue
			}
			if in.Repo != "" && !strings.Contains(strings.ToLower(sess.Repo), strings.ToLower(in.Repo)) {
				continue
			}
			kept = append(kept, sess)
		}
		sortByAttention(kept)
		return jsonText(map[string]any{"sessions": kept, "count": len(kept)})
	case "get_session":
		var in struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(args, &in); err != nil || in.ID == "" {
			return "", fmt.Errorf("id is required")
		}
		sessions, err := s.backend.Sessions(ctx)
		if err != nil {
			return "", err
		}
		for _, sess := range sessions {
			if sess.ID == in.ID {
				return jsonText(sess)
			}
		}
		return "", fmt.Errorf("no session with id %q", in.ID)
	case "search_feed":
		var in struct {
			Query   string `json:"query"`
			Session string `json:"session"`
			Limit   int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("query is required")
		}
		if strings.TrimSpace(in.Query) == "" {
			return "", fmt.Errorf("query is required")
		}
		if in.Limit <= 0 {
			in.Limit = 200
		}
		if in.Limit > 1000 {
			in.Limit = 1000
		}
		events, err := s.backend.History(ctx, in.Session, in.Limit)
		if err != nil {
			return "", err
		}
		needle := strings.ToLower(in.Query)
		matches := make([]Event, 0, 8)
		for _, ev := range events {
			if strings.Contains(strings.ToLower(ev.Kind), needle) || strings.Contains(strings.ToLower(string(ev.Body)), needle) {
				matches = append(matches, ev)
			}
		}
		return jsonText(map[string]any{"events": matches, "count": len(matches), "scanned": len(events)})
	case "notify_me":
		var in struct {
			Message string `json:"message"`
			Session string `json:"session"`
		}
		_ = json.Unmarshal(args, &in)
		msg := strings.TrimSpace(in.Message)
		if msg == "" {
			return "", fmt.Errorf("message is required")
		}
		body, _ := json.Marshal(map[string]any{"message": msg, "reason": "agent requested attention"})
		if err := s.backend.Ingest(ctx, IngestEvent{Kind: "attention", Session: in.Session, Source: "mcp", Body: body}); err != nil {
			return "", err
		}
		return jsonText(map[string]any{"notified": true, "message": msg})
	case "export_trajectory":
		var in struct {
			Session string `json:"session"`
			Limit   int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("session is required")
		}
		// Collect owns ordering, numbering and validation; numbering events here
		// as well is how two shapes of the same export drift apart.
		steps, err := trajectory.Collect(ctx, trajectoryReader{s.backend}, in.Session, in.Limit)
		if err != nil {
			return "", err
		}
		var buf bytes.Buffer
		if err := trajectory.Write(&buf, steps); err != nil {
			return "", err
		}
		return buf.String(), nil
	case "snxw_ingest":
		var in struct {
			Kind    string          `json:"kind"`
			Session string          `json:"session"`
			Body    json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("kind is required")
		}
		k := strings.TrimSpace(in.Kind)
		if k == "" {
			return "", fmt.Errorf("kind is required")
		}
		if !knownKind(k) {
			return "", fmt.Errorf("unknown kind %q: use session.start, tool.start, tool.end, prompt.submit, attention, turn.end, session.end or error", k)
		}
		if err := s.backend.Ingest(ctx, IngestEvent{Kind: k, Session: in.Session, Source: "mcp", Body: in.Body}); err != nil {
			return "", err
		}
		return jsonText(map[string]any{"accepted": true, "kind": k})
	default:
		return "", unknownTool{name: name}
	}
}

func knownKind(k string) bool {
	switch k {
	case "session.start", "tool.start", "tool.end", "prompt.submit", "attention", "turn.end", "session.end", "error":
		return true
	}
	return false
}

// whatsRunning answers the one question an agent asks before it writes code:
// is someone else in this repo, and what is blocked on the human.
func (s *Server) whatsRunning(ctx context.Context) (string, error) {
	sessions, err := s.backend.Sessions(ctx)
	if err != nil {
		return "", err
	}
	counts := map[string]int{}
	waiting := make([]Session, 0, 4)
	active := make([]Session, 0, 8)
	for _, sess := range sessions {
		counts[sess.Status]++
		switch sess.Status {
		case "waiting_for_you":
			waiting = append(waiting, sess)
		case "running":
			active = append(active, sess)
		}
	}
	sortByAttention(waiting)
	sortByAttention(active)
	return jsonText(map[string]any{
		"total":          len(sessions),
		"byStatus":       counts,
		"waitingOnHuman": waiting,
		"running":        active,
	})
}

// sortByAttention puts what the human (or a reading agent) can act on first,
// then the most recently updated.
func sortByAttention(sessions []Session) {
	rank := func(s string) int {
		switch s {
		case "waiting_for_you":
			return 0
		case "failed":
			return 1
		case "running":
			return 2
		case "idle":
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		ri, rj := rank(sessions[i].Status), rank(sessions[j].Status)
		if ri != rj {
			return ri < rj
		}
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})
}

func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// trajectoryReader adapts the board's feed to the trajectory package.
type trajectoryReader struct{ b Backend }

func (t trajectoryReader) History(ctx context.Context, session string, limit int) ([]trajectory.Event, error) {
	events, err := t.b.History(ctx, session, limit)
	if err != nil {
		return nil, err
	}
	out := make([]trajectory.Event, 0, len(events))
	for _, ev := range events {
		out = append(out, trajectory.Event{Seq: ev.Seq, TS: ev.TS, Source: ev.Source, Session: ev.Session, Kind: ev.Kind, Body: ev.Body})
	}
	return out, nil
}
