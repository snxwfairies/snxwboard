package trajectory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeFeed struct {
	events []Event
	err    error
	got    string
	limit  int
}

func (f *fakeFeed) History(_ context.Context, session string, limit int) ([]Event, error) {
	f.got, f.limit = session, limit
	return f.events, f.err
}

func TestNumbersStepsInOrder(t *testing.T) {
	feed := &fakeFeed{events: []Event{
		{Seq: 10, TS: 1000, Source: "cli", Kind: "session.start", Body: json.RawMessage(`{"repo":"x"}`)},
		{Seq: 11, TS: 1001, Source: "cli", Kind: "tool.start", Body: json.RawMessage(`{"tool":"Bash"}`)},
	}}
	steps, err := Collect(context.Background(), feed, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	if steps[0].Step != 1 || steps[1].Step != 2 {
		t.Errorf("steps must be numbered from 1 in order, got %d,%d", steps[0].Step, steps[1].Step)
	}
	if steps[0].Seq != 10 || steps[1].Seq != 11 {
		t.Errorf("seq must be preserved so a consumer can resume, got %d,%d", steps[0].Seq, steps[1].Seq)
	}
	if feed.limit != 1000 {
		t.Errorf("default limit = %d, want 1000", feed.limit)
	}
}

func TestOutOfOrderEventsAreSortedOldestFirst(t *testing.T) {
	// GET /v1/events answers newest-first without a cursor, so this is the shape
	// the daemon actually returns.
	feed := &fakeFeed{events: []Event{
		{Seq: 30, Kind: "turn.end"},
		{Seq: 10, Kind: "session.start"},
		{Seq: 20, Kind: "tool.start"},
	}}
	steps, err := Collect(context.Background(), feed, "s1", 10)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{10, 20, 30} {
		if steps[i].Seq != want {
			t.Fatalf("step %d seq = %d, want %d (oldest first)", i+1, steps[i].Seq, want)
		}
		if steps[i].Step != i+1 {
			t.Errorf("step numbering must follow the sorted order, got %d", steps[i].Step)
		}
	}
}

func TestSessionIsRequired(t *testing.T) {
	if _, err := Collect(context.Background(), &fakeFeed{}, "  ", 10); err == nil {
		t.Fatal("an empty session must be an error: exporting everything is a different question")
	}
}

func TestLimitIsClampedBothWays(t *testing.T) {
	feed := &fakeFeed{}
	if _, err := Collect(context.Background(), feed, "s1", -5); err != nil {
		t.Fatal(err)
	}
	if feed.limit != 1000 {
		t.Errorf("negative limit = %d, want the default", feed.limit)
	}
	if _, err := Collect(context.Background(), feed, "s1", 100000); err != nil {
		t.Fatal(err)
	}
	if feed.limit != 5000 {
		t.Errorf("oversized limit = %d, want 5000", feed.limit)
	}
}

func TestBackendErrorSurfaces(t *testing.T) {
	feed := &fakeFeed{err: errors.New("db closed")}
	if _, err := Collect(context.Background(), feed, "s1", 10); err == nil {
		t.Fatal("a read failure must surface, not produce an empty trajectory")
	}
}

func TestWriteIsOneJSONObjectPerLine(t *testing.T) {
	steps := []Step{
		{Step: 1, Seq: 5, TS: 1000, Source: "cli", Kind: "session.start", Body: json.RawMessage(`{"a":1}`)},
		{Step: 2, Seq: 6, TS: 1001, Source: "mcp", Kind: "attention"},
	}
	var out bytes.Buffer
	if err := Write(&out, steps); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	for i, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		if decoded["step"].(float64) != float64(i+1) {
			t.Errorf("line %d step = %v", i, decoded["step"])
		}
	}
}

func TestEmptySessionWritesNothing(t *testing.T) {
	var out bytes.Buffer
	if err := Write(&out, nil); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("an empty trajectory must write no bytes, got %q", out.String())
	}
}
