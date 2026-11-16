package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/snxwfairies/snxwboard/internal/config"
	"github.com/snxwfairies/snxwboard/internal/features"
)

// runFeatures prints the catalogue and every entry's state, or changes which
// names are enabled. Enabling is the only way anything autonomous turns on.
func runFeatures(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	enabled, err := config.Features(cfg.Dir)
	if err != nil {
		return err
	}
	set, resolveErr := features.Resolve(enabled)

	if len(args) == 0 {
		return printFeatures(enabled, resolveErr)
	}
	switch args[0] {
	case "enable", "disable":
		if len(args) < 2 {
			return fmt.Errorf("usage: snxw features %s NAME [NAME...]", args[0])
		}
		next := map[string]bool{}
		for _, name := range set.Names() {
			next[name] = true
		}
		for _, name := range args[1:] {
			name = strings.TrimSpace(name)
			if args[0] == "enable" {
				next[name] = true
			} else {
				delete(next, name)
			}
		}
		names := make([]string, 0, len(next))
		for name := range next {
			names = append(names, name)
		}
		// Resolve again: enabling something with an unmet requirement, an
		// unknown name or a planned one must fail here, not at daemon start.
		if _, err := features.Resolve(names); err != nil {
			return err
		}
		if err := config.WriteFeatures(cfg.Dir, names); err != nil {
			return err
		}
		fmt.Printf("%sd %s\n", args[0], strings.Join(args[1:], " "))
		fmt.Printf("restart the daemon to apply: snxw up\n")
		return nil
	case "status":
		if resolveErr != nil {
			return resolveErr
		}
		if set.Empty() {
			fmt.Println("every feature is off")
			return nil
		}
		fmt.Printf("on: %s\n", strings.Join(set.Names(), " "))
		return nil
	default:
		return fmt.Errorf("usage: snxw features [enable|disable NAME...|status]")
	}
}

func printFeatures(enabled []string, resolveErr error) error {
	on := map[string]bool{}
	for _, name := range enabled {
		on[name] = true
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FEATURE\tSTATE\tTIER\tNEEDS\tWHAT IT DOES")
	for _, f := range features.Catalog {
		state := "off"
		switch {
		case on[f.Name] && resolveErr != nil:
			state = "invalid"
		case on[f.Name]:
			state = "ON"
		case f.Planned:
			state = "off (planned)"
		}
		needs := "-"
		if len(f.Requires) > 0 {
			needs = strings.Join(f.Requires, "+")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", f.Name, state, f.Tier, needs, f.Description)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if resolveErr != nil {
		return fmt.Errorf("config is not valid: %w", resolveErr)
	}
	fmt.Println("\nevery feature ships off. `snxw features enable NAME` turns one on.")
	return nil
}
