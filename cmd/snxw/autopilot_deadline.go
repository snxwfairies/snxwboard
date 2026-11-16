package main

import (
	"os"
	"time"
)

// autopilotDeadline is how long a session may sit blocked on the human before
// the loop escalates it. Ten minutes is long enough that a person reading their
// screen will beat it, and short enough that a forgotten session does not sit
// there all afternoon pretending to be work in progress.
func autopilotDeadline() time.Duration {
	if raw := os.Getenv("SNXW_AUTOPILOT_DEADLINE"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return 10 * time.Minute
}
