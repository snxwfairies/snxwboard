// Package features is the gate every autonomous capability goes through.
//
// The catalogue below ships complete and every entry is off. That is the same
// shape Nosu Research's Hermes uses for its toolsets (opt-in groups behind
// `enabled: false`): the capability exists in the binary, is visible in
// `snxw features`, and does nothing at all until the operator names it in
// config. Nothing here is enabled by implication — not by another feature, not
// by a default, not by the environment.
//
// The rule that makes the gate worth having: a feature that is not enabled must
// not be reachable. The MCP server hides its tools, the daemon logs what is on
// at start, and adding an entry to the catalogue without wiring it to a
// behaviour is a lie a reviewer can see.
package features

import (
	"fmt"
	"sort"
	"strings"
)

// Tier says how much trust a feature asks for. It exists so an operator can
// reason about a name they have not read the code of.
type Tier string

const (
	// TierRead observes: it reads state that is already there and writes nothing to an agent.
	TierRead Tier = "read"
	// TierAct acts inside the daemon: it changes folded state, sends notifications, writes events.
	TierAct Tier = "act"
	// TierSpawn starts processes or drives other software on the machine.
	TierSpawn Tier = "spawn"
)

// Feature is one capability and what it needs.
type Feature struct {
	Name        string
	Tier        Tier
	Description string
	// Requires names features that must be on for this one to be meaningful.
	// An unmet requirement is an error, never an implicit enable.
	Requires []string
	// Planned marks a declared capability with no behaviour wired yet. It is
	// rejected by Resolve rather than silently enabling nothing, and
	// `snxw features` prints it as planned.
	Planned bool
}

// Catalog is every feature in the binary, all off. Order is the order
// `snxw features` prints.
var Catalog = []Feature{
	{
		Name:        "session_search",
		Tier:        TierRead,
		Description: "Search events across every session, not just one. Backs the MCP search_feed tool.",
	},
	{
		Name:        "subagents",
		Tier:        TierRead,
		Description: "Read the tree of sessions a session spawned (parent/child), so a delegate is visible as work rather than noise.",
		Planned:     true,
	},
	{
		Name:        "heartbeat",
		Tier:        TierAct,
		Description: "Write a periodic liveness event for sessions that have gone quiet mid-turn, so a stalled agent is distinguishable from a finished one.",
	},
	{
		Name:        "cron",
		Tier:        TierAct,
		Description: "Run scheduled board jobs (daily report, nightly prune) described in the config, unattended.",
		Requires:    []string{"heartbeat"},
		Planned:     true,
	},
	{
		Name:        "autopilot",
		Tier:        TierAct,
		Description: "Act on a session that has been waiting on the human without starting a new agent: escalate it, repeat the notification, or close it as abandoned after a deadline.",
		Requires:    []string{"session_search"},
	},
	{
		Name:        "dispatcher",
		Tier:        TierSpawn,
		Description: "Start an agent on queued work when the board has a card nobody has picked up.",
		Requires:    []string{"autopilot"},
		Planned:     true,
	},
	{
		Name:        "skills",
		Tier:        TierAct,
		Description: "Record a repeatable procedure the board learned from a finished session, and offer it the next time the same shape appears.",
		Planned:     true,
	},
	{
		Name:        "memory",
		Tier:        TierAct,
		Description: "Keep per-repo notes across sessions (what breaks here, what the owner keeps asking for) and surface them to an agent that touches the repo.",
		Planned:     true,
	},
	{
		Name:        "browser",
		Tier:        TierSpawn,
		Description: "Drive a browser to check a deployed product end to end, the way the deployment sections of each README describe.",
		Planned:     true,
	},
	{
		Name:        "computer_use",
		Tier:        TierSpawn,
		Description: "Control the desktop: click, type and screenshot a native app when there is no API to verify against.",
		Requires:    []string{"browser"},
		Planned:     true,
	},
	{
		Name:        "trajectory",
		Tier:        TierRead,
		Description: "Export a session's event log as a JSONL trajectory, one numbered step per event, for batch runs and replay.",
	},
}

// Set is the resolved on/off state. The zero value is everything off.
type Set struct {
	on map[string]bool
}

// Enabled reports whether a feature is on. An unknown name is off: a typo in a
// config must never turn something on.
func (s Set) Enabled(name string) bool { return s.on[name] }

// Names returns the enabled names, sorted.
func (s Set) Names() []string {
	out := make([]string, 0, len(s.on))
	for name := range s.on {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Empty reports whether nothing is enabled.
func (s Set) Empty() bool { return len(s.on) == 0 }

// Find returns the catalogue entry for a name.
func Find(name string) (Feature, bool) {
	for _, f := range Catalog {
		if f.Name == name {
			return f, true
		}
	}
	return Feature{}, false
}

// Resolve turns the configured names into a Set, rejecting anything unknown or
// unmet. Every rejection is an error the operator can act on, and an empty list
// resolves to everything off rather than everything on.
func Resolve(names []string) (Set, error) {
	set := Set{on: map[string]bool{}}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		f, ok := Find(name)
		if !ok {
			return Set{}, fmt.Errorf("unknown feature %q: run `snxw features` for the list", name)
		}
		if f.Planned {
			return Set{}, fmt.Errorf("feature %q is declared but not implemented yet; it stays off", name)
		}
		set.on[name] = true
	}
	for name := range set.on {
		f, _ := Find(name)
		for _, req := range f.Requires {
			if !set.on[req] {
				return Set{}, fmt.Errorf("feature %q needs %q enabled too", name, req)
			}
		}
	}
	return set, nil
}

// ParseList splits a comma- or space-separated list, which is what both the
// config value and the CLI flag carry.
func ParseList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
}
