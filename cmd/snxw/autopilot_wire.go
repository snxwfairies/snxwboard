package main

import (
	"context"
	"encoding/json"

	"github.com/snxwfairies/snxwboard/internal/autopilot"
	"github.com/snxwfairies/snxwboard/internal/mcpboard"
)

// boardForAutopilot adapts the autopilot loop to the daemon's own HTTP surface,
// the same way the MCP server does: one write path, one place that auth lives.
type boardForAutopilot struct{ b *mcpboard.HTTPBackend }

func (a boardForAutopilot) Sessions(ctx context.Context) ([]autopilot.Session, error) {
	sessions, err := a.b.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]autopilot.Session, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, autopilot.Session{ID: s.ID, Repo: s.Repo, Status: s.Status, UpdatedAt: s.UpdatedAt})
	}
	return out, nil
}

func (a boardForAutopilot) Ingest(ctx context.Context, kind, session string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return a.b.Ingest(ctx, mcpboard.IngestEvent{Kind: kind, Session: session, Source: "autopilot", Body: raw})
}
