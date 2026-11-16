// Package trajectory turns a session's event log into the shape a batch run
// wants: one JSON object per step, newline-delimited, oldest first.
//
// This is the one thing SWE-agent and Devika both treat as a first-class
// artefact rather than a debug view: a finished session is training data, and
// it has to be replayable in order, with the same fields every time. The events
// here are already redacted at ingest, so a trajectory cannot leak a secret that
// the feed does not already contain.
package trajectory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Event is one recorded step, as the feed returns it.
type Event struct {
	Seq     int64           `json:"seq"`
	TS      int64           `json:"ts"`
	Source  string          `json:"source,omitempty"`
	Session string          `json:"session,omitempty"`
	Kind    string          `json:"kind"`
	Body    json.RawMessage `json:"body,omitempty"`
}

// Step is one line of the export. It keeps the seq so a consumer can resume and
// the ts so a consumer can align steps with anything else it logged.
type Step struct {
	Step      int             `json:"step"`
	Seq       int64           `json:"seq"`
	TS        int64           `json:"ts"`
	Source    string          `json:"source,omitempty"`
	Kind      string          `json:"kind"`
	Body      json.RawMessage `json:"body,omitempty"`
	Synthetic bool            `json:"synthetic,omitempty"`
}

// Backend reads one session's events.
type Backend interface {
	History(ctx context.Context, session string, limit int) ([]Event, error)
}

// Collect reads the session and numbers its steps. An empty session is an
// error: exporting "everything" is a different question with a different answer.
func Collect(ctx context.Context, b Backend, session string, limit int) ([]Step, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return nil, fmt.Errorf("session is required")
	}
	if limit <= 0 {
		limit = 1000
	}
	if limit > 5000 {
		limit = 5000
	}
	events, err := b.History(ctx, session, limit)
	if err != nil {
		return nil, err
	}
	// The feed answers newest-first when no cursor is given; a trajectory is read
	// forwards, so order by seq before numbering rather than trusting the caller.
	sort.SliceStable(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	steps := make([]Step, 0, len(events))
	for i, ev := range events {
		steps = append(steps, Step{
			Step:   i + 1,
			Seq:    ev.Seq,
			TS:     ev.TS,
			Source: ev.Source,
			Kind:   ev.Kind,
			Body:   ev.Body,
		})
	}
	return steps, nil
}

// Write emits the steps as JSONL, one object per line, in order.
func Write(w io.Writer, steps []Step) error {
	enc := json.NewEncoder(w)
	for _, s := range steps {
		if err := enc.Encode(s); err != nil {
			return err
		}
	}
	return nil
}
