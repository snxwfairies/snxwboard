// Package fold turns the append-only event log into per-session state.
package fold

import (
	"encoding/json"
	"sync"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/store"
)

// Status values a session can be in.
const (
	StatusRunning = "running"
	StatusWaiting = "waiting_for_you"
	StatusIdle    = "idle"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// Generic event kinds. Adapters map vendor hooks onto these.
const (
	KindSessionStart = "session.start"
	KindToolStart    = "tool.start"
	KindToolEnd      = "tool.end"
	KindPromptSubmit = "prompt.submit"
	KindAttention    = "attention"
	KindTurnEnd      = "turn.end"
	KindSessionEnd   = "session.end"
	KindError        = "error"
)

// Session is the folded state of one agent session.
type Session struct {
	ID         string `json:"id"`
	Source     string `json:"source,omitempty"`
	Repo       string `json:"repo,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Status     string `json:"status"`
	Tool       string `json:"tool"`
	Title      string `json:"title,omitempty"`
	Model      string `json:"model,omitempty"`
	ToolCalls  int    `json:"toolCalls"`
	Errors     int    `json:"errors"`
	StartedAt  int64  `json:"startedAt"`
	UpdatedAt  int64  `json:"updatedAt"`
	UpdatedSeq int64  `json:"updatedSeq"`
}

// Folder holds the folded state of every session seen so far.
type Folder struct {
	mu   sync.Mutex
	sess map[string]*Session
}

// New returns an empty Folder.
func New() *Folder {
	return &Folder{sess: make(map[string]*Session)}
}

// Load seeds the folder, for example from the persisted sessions table.
func (f *Folder) Load(list []Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range list {
		if s.ID == "" {
			continue
		}
		c := s
		f.sess[c.ID] = &c
	}
}

// Get returns a copy of one session.
func (f *Folder) Get(id string) (Session, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sess[id]
	if !ok {
		return Session{}, false
	}
	return *s, true
}

// Apply folds one event into state and returns only the fields that changed.
// Events without a session id fold onto their source.
func (f *Folder) Apply(ev store.Event) (map[string]any, bool) {
	id := ev.Session
	if id == "" {
		id = ev.Source
	}
	if id == "" {
		return nil, false
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	s, exists := f.sess[id]
	if !exists {
		if !known(ev.Kind) {
			return nil, false
		}
		s = &Session{ID: id, Source: ev.Source, Status: StatusRunning, StartedAt: ev.TS}
		f.sess[id] = s
	}

	p := make(map[string]any, 8)
	if !exists {
		// First event for this session: hand the client its identity.
		p["source"] = s.Source
		p["status"] = s.Status
		if s.StartedAt > 0 {
			p["startedAt"] = s.StartedAt
		}
	}

	b := parseBody(ev.Body)
	// Context fields may ride on any event: adapters attach them to the first
	// event they see when the agent never sent a session start.
	setField(p, "repo", &s.Repo, field(b, "repo"))
	setField(p, "cwd", &s.Cwd, field(b, "cwd"))
	setField(p, "branch", &s.Branch, field(b, "branch"))
	setField(p, "model", &s.Model, field(b, "model"))
	if ev.Kind == KindSessionStart {
		setField(p, "title", &s.Title, field(b, "title"))
	}
	switch ev.Kind {
	case KindSessionStart:
		setStatus(p, s, StatusRunning)
	case KindToolStart:
		setField(p, "tool", &s.Tool, field(b, "tool"))
		s.ToolCalls++
		p["toolCalls"] = s.ToolCalls
		setStatus(p, s, StatusRunning)
	case KindToolEnd:
		if s.Tool != "" {
			s.Tool = ""
			p["tool"] = ""
		}
		if ok, present := boolField(b, "ok"); present && !ok {
			s.Errors++
			p["errors"] = s.Errors
		}
	case KindPromptSubmit:
		if s.Tool != "" {
			s.Tool = ""
			p["tool"] = ""
		}
		setStatus(p, s, StatusRunning)
	case KindAttention:
		setStatus(p, s, StatusWaiting)
	case KindTurnEnd:
		setStatus(p, s, StatusIdle)
	case KindSessionEnd:
		if ok, present := boolField(b, "ok"); present && !ok {
			setStatus(p, s, StatusFailed)
		} else {
			setStatus(p, s, StatusDone)
		}
	case KindError:
		s.Errors++
		p["errors"] = s.Errors
	}

	if ev.TS > 0 {
		s.UpdatedAt = ev.TS
		p["updatedAt"] = ev.TS
	}
	if ev.Seq > 0 {
		s.UpdatedSeq = ev.Seq
		p["updatedSeq"] = ev.Seq
	}
	return p, len(p) > 0
}

func known(kind string) bool {
	switch kind {
	case KindSessionStart, KindToolStart, KindToolEnd, KindPromptSubmit,
		KindAttention, KindTurnEnd, KindSessionEnd, KindError:
		return true
	}
	return false
}

func setStatus(p map[string]any, s *Session, status string) {
	if s.Status != status {
		s.Status = status
		p["status"] = status
	}
}

func setField(p map[string]any, key string, dst *string, v string) {
	if v != "" && *dst != v {
		*dst = v
		p[key] = v
	}
}

func parseBody(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func field(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func boolField(m map[string]any, key string) (bool, bool) {
	b, ok := m[key].(bool)
	return b, ok
}
