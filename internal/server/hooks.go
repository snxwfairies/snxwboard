package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/adapter/claude"
	"github.com/snxwfairies-ai/snxwboard-oss/internal/redact"
	"github.com/snxwfairies-ai/snxwboard-oss/internal/store"
)

const hookBodyBytes = 32 << 20

// handleClaudeHook takes a raw Claude Code HTTP-hook payload. It always answers
// 204 with an empty body, which Claude Code reads as "no decision", so the
// board can never block, slow or steer the agent. Problems are logged instead.
func (s *Server) handleClaudeHook(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusNoContent)

	if !s.authorized(r) {
		// Usually a token regenerated after `snxw connect`: say how to fix it.
		s.log.Warn("claude hook: bad or missing token; re-run `snxw connect claude`")
		return
	}
	// Tool inputs and responses can be large (a Write of a whole file); the
	// adapter keeps a few clipped fields, but it must parse the payload whole.
	r.Body = http.MaxBytesReader(w, r.Body, hookBodyBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		s.log.Warn("claude hook: read body", "err", err)
		return
	}
	ev, err := claude.Translate(data)
	if errors.Is(err, claude.ErrSkip) {
		return
	}
	if err != nil {
		s.log.Warn("claude hook: bad payload", "err", err)
		return
	}
	session := ev.Session
	if len(session) > maxFieldLen {
		session = session[:maxFieldLen]
	}
	if _, known := s.folder.Get(session); !known {
		claude.Context(&ev, ev.Cwd)
	}
	body, err := json.Marshal(ev.Body)
	if err != nil {
		return
	}
	s.enqueue([]store.Event{{
		TS:      time.Now().UnixMilli(),
		Source:  claude.Source,
		Session: session,
		Kind:    ev.Kind,
		Body:    redact.Redact(body),
	}})
}
