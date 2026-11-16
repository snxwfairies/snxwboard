package features

import (
	"strings"
	"testing"
)

func TestNothingIsOnByDefault(t *testing.T) {
	set, err := Resolve(nil)
	if err != nil {
		t.Fatalf("resolve nothing: %v", err)
	}
	if !set.Empty() {
		t.Fatalf("the default set must be empty, got %v", set.Names())
	}
	for _, f := range Catalog {
		if set.Enabled(f.Name) {
			t.Errorf("%q is on by default", f.Name)
		}
	}
}

func TestEveryCatalogueEntryIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Catalog {
		if f.Name == "" || f.Description == "" {
			t.Errorf("entry %+v needs a name and a description", f)
		}
		if seen[f.Name] {
			t.Errorf("duplicate feature %q", f.Name)
		}
		seen[f.Name] = true
		switch f.Tier {
		case TierRead, TierAct, TierSpawn:
		default:
			t.Errorf("%q has tier %q, want read, act or spawn", f.Name, f.Tier)
		}
		for _, req := range f.Requires {
			if _, ok := Find(req); !ok {
				t.Errorf("%q requires %q, which is not in the catalogue", f.Name, req)
			}
		}
	}
}

func TestUnknownNameIsRejectedNotIgnored(t *testing.T) {
	if _, err := Resolve([]string{"autopilot", "typo_feature"}); err == nil {
		t.Fatal("an unknown name must fail, or a typo silently runs with less than the operator meant")
	} else if !strings.Contains(err.Error(), "snxw features") {
		t.Errorf("the error should point at the list, got %q", err)
	}
}

func TestPlannedFeatureCannotBeEnabled(t *testing.T) {
	planned := ""
	for _, f := range Catalog {
		if f.Planned {
			planned = f.Name
			break
		}
	}
	if planned == "" {
		t.Skip("no planned entries")
	}
	_, err := Resolve([]string{planned})
	if err == nil {
		t.Fatalf("%q is declared but has no behaviour; enabling it must fail", planned)
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("error should say it is not implemented, got %q", err)
	}
}

func TestUnmetRequirementIsAnError(t *testing.T) {
	if _, err := Resolve([]string{"autopilot"}); err == nil {
		t.Fatal("autopilot needs session_search; enabling it alone must fail")
	}
	if _, err := Resolve([]string{"autopilot", "session_search"}); err != nil {
		t.Fatalf("with the requirement met it must resolve: %v", err)
	}
}

func TestRequirementOrderDoesNotMatter(t *testing.T) {
	for _, order := range [][]string{
		{"session_search", "autopilot"},
		{"autopilot", "session_search"},
	} {
		if _, err := Resolve(order); err != nil {
			t.Errorf("%v should resolve: %v", order, err)
		}
	}
}

func TestNamesAreSortedAndEmptyIsSafe(t *testing.T) {
	set, err := Resolve([]string{"session_search", "autopilot"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(set.Names(), ",")
	if got != "autopilot,session_search" {
		t.Errorf("Names() = %q, want sorted", got)
	}
	var zero Set
	if zero.Enabled("autopilot") || !zero.Empty() {
		t.Error("the zero Set must be everything off")
	}
	if len(zero.Names()) != 0 {
		t.Error("the zero Set has no names")
	}
}

func TestParseList(t *testing.T) {
	got := ParseList("autopilot, session_search\nheartbeat")
	want := []string{"autopilot", "session_search", "heartbeat"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ParseList = %v, want %v", got, want)
	}
}
