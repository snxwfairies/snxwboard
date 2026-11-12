package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/config"
	"github.com/snxwfairies-ai/snxwboard-oss/internal/connect"
)

func runConnect(args []string, on bool) error {
	if len(args) == 0 {
		return errors.New("which agent? supported: claude")
	}
	agent := args[0]
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	project := fs.String("project", "", "edit DIR/.claude/settings.local.json instead of the user settings")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if agent != "claude" {
		return fmt.Errorf("unknown agent %q; supported: claude", agent)
	}

	path, err := connect.ClaudeSettingsPath(*project)
	if err != nil {
		return err
	}
	var changed bool
	if on {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		changed, err = connect.ConnectClaude(path, cfg.Addr, cfg.Token)
		if err != nil {
			return err
		}
	} else {
		changed, err = connect.DisconnectClaude(path)
		if err != nil {
			return err
		}
	}
	switch {
	case !changed:
		fmt.Println("already up to date:", path)
	case on:
		fmt.Println("connected Claude Code:", path)
		fmt.Println("new sessions report to snxw; if the daemon is down, Claude Code carries on unaffected.")
	default:
		fmt.Println("disconnected Claude Code:", path)
	}
	return nil
}
