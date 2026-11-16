// Package autopilot is the one behaviour behind the `autopilot` feature: it
// watches sessions that are blocked on the human and, when the deadline passes,
// records an escalation once instead of leaving the board to show the same
// stalled session forever.
//
// It never starts an agent and never answers a permission prompt. Acting on an
// agent is `dispatcher` and needs spawning an agent, which is a bigger trust
// ask; this package only writes what it already knows into the feed.
package autopilot

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// Session is the folded state the loop needs. Kept local so the package does
// not depend on the store or the server.
type Session struct {
	ID        string
	Repo      string
	Status    string
	UpdatedAt int64
}

// Backend reads the board and records an event.
type Backend interface {
	Sessions(ctx context.Context) ([]Session, error)
	Ingest(ctx context.Context, kind, session string, body any) error
}

// Options tune the loop. The zero value is a loop that does nothing until a
// session has been blocked for Deadline, which is the safe direction.
type Options struct {
	// Deadline is how long a session may sit in waiting_for_you before it is
	// escalated. Zero disables escalation.
	Deadline time.Duration
	// Interval is how often the board is read. Zero means 30s.
	Interval time.Duration
	// Once stops after the first escalation, for tests and one-shot runs.
	Once bool
}

const (
	// KindAttention is the generic event kind the fold rules already map onto
	// waiting_for_you, so an escalation needs no new kind.
	KindAttention = "attention"
	statusWaiting = "waiting_for_you"
)

// Run loops until ctx is cancelled. It is only ever called when the autopilot
// feature is enabled; a caller that is unsure must not call it.
func Run(ctx context.Context, b Backend, log *slog.Logger, opts Options) error {
	if opts.Deadline <= 0 {
		return nil // nothing configured to do
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	escalated := map[string]int64{}

	tick := func() bool {
		sessions, err := b.Sessions(ctx)
		if err != nil {
			log.Warn("autopilot: read sessions", "err", err)
			return false
		}
		now := time.Now().UnixMilli()
		for _, sess := range sessions {
			if sess.Status != statusWaiting {
				continue
			}
			if sess.UpdatedAt == 0 || now-sess.UpdatedAt < opts.Deadline.Milliseconds() {
				continue
			}
			if escalated[sess.ID] == sess.UpdatedAt {
				continue // already escalated for this stall
			}
			body := map[string]any{
				"reason":   "autopilot: blocked past the deadline",
				"repo":     sess.Repo,
				"waitedMs": now - sess.UpdatedAt,
			}
			if err := b.Ingest(ctx, KindAttention, sess.ID, body); err != nil {
				log.Warn("autopilot: escalate", "session", sess.ID, "err", err)
				continue
			}
			escalated[sess.ID] = sess.UpdatedAt
			log.Info("autopilot: escalated a stalled session", "session", sess.ID, "repo", sess.Repo)
			if opts.Once {
				return true
			}
		}
		return false
	}

	// Once means exactly that: one pass, whatever it found or failed to find.
	// Falling through to the ticker here would make a one-shot run wait forever
	// when the board could not be read.
	if opts.Once {
		tick()
		return nil
	}
	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if tick() && opts.Once {
				return nil
			}
		}
	}
}

// bodyJSON is here so the escalation payload is marshalled the same way every
// time; the server accepts any JSON, but a stable shape keeps the feed readable.
func bodyJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
