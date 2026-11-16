package autopilot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeBoard struct {
	sessions []Session
	ingested []record
	err      error
}

type record struct {
	kind    string
	session string
	body    any
}

func (f *fakeBoard) Sessions(context.Context) ([]Session, error) { return f.sessions, f.err }

func (f *fakeBoard) Ingest(_ context.Context, kind, session string, body any) error {
	if f.err != nil {
		return f.err
	}
	f.ingested = append(f.ingested, record{kind: kind, session: session, body: body})
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestNothingIsConfiguredNothingHappens(t *testing.T) {
	board := &fakeBoard{sessions: []Session{{ID: "s1", Status: "waiting_for_you", UpdatedAt: 1}}}
	if err := Run(context.Background(), board, quiet(), Options{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(board.ingested) != 0 {
		t.Fatalf("a zero Options must do nothing, escalated %d", len(board.ingested))
	}
}

func TestEscalatesASessionBlockedPastTheDeadline(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	board := &fakeBoard{sessions: []Session{{ID: "stuck", Repo: "cctverp.com", Status: "waiting_for_you", UpdatedAt: old}}}
	if err := Run(context.Background(), board, quiet(), Options{Deadline: time.Minute, Once: true}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(board.ingested) != 1 {
		t.Fatalf("want one escalation, got %d", len(board.ingested))
	}
	got := board.ingested[0]
	if got.kind != KindAttention || got.session != "stuck" {
		t.Errorf("escalated %+v", got)
	}
	raw, err := json.Marshal(got.body)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Errorf("body is not JSON: %s", raw)
	}
}

func TestLeavesAFreshSessionAlone(t *testing.T) {
	now := time.Now().UnixMilli()
	board := &fakeBoard{sessions: []Session{{ID: "busy", Status: "waiting_for_you", UpdatedAt: now}}}
	if err := Run(context.Background(), board, quiet(), Options{Deadline: time.Hour, Once: true}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(board.ingested) != 0 {
		t.Fatalf("a session inside the deadline must be left alone, got %+v", board.ingested)
	}
}

func TestIgnoresSessionsThatAreNotWaiting(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	board := &fakeBoard{sessions: []Session{
		{ID: "run", Status: "running", UpdatedAt: old},
		{ID: "done", Status: "done", UpdatedAt: old},
		{ID: "failed", Status: "failed", UpdatedAt: old},
	}}
	if err := Run(context.Background(), board, quiet(), Options{Deadline: time.Minute, Once: true}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(board.ingested) != 0 {
		t.Fatalf("only waiting_for_you is escalated, got %+v", board.ingested)
	}
}

func TestSessionsWithoutATimestampAreSkipped(t *testing.T) {
	board := &fakeBoard{sessions: []Session{{ID: "noClock", Status: "waiting_for_you"}}}
	if err := Run(context.Background(), board, quiet(), Options{Deadline: time.Minute, Once: true}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(board.ingested) != 0 {
		t.Fatalf("an unknown age must not be guessed into an escalation, got %+v", board.ingested)
	}
}

func TestOneEscalationPerStall(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	board := &fakeBoard{sessions: []Session{{ID: "stuck", Status: "waiting_for_you", UpdatedAt: old}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Run(ctx, board, quiet(), Options{Deadline: time.Minute, Interval: time.Millisecond}) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	if len(board.ingested) != 1 {
		t.Fatalf("the same stall must be escalated once, got %d", len(board.ingested))
	}
}

func TestBackendErrorsDoNotStopTheLoop(t *testing.T) {
	board := &fakeBoard{err: errors.New("db closed")}
	if err := Run(context.Background(), board, quiet(), Options{Deadline: time.Minute, Once: true}); err != nil {
		t.Fatalf("a read error must be logged, not fatal: %v", err)
	}
}
