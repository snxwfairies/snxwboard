package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/snxwfairies/snxwboard/internal/config"
	"github.com/snxwfairies/snxwboard/internal/features"
	"github.com/snxwfairies/snxwboard/internal/mcpboard"
)

// runMCP serves the board to an MCP client over stdio. The client is the parent
// process (Claude Code, Codex, any MCP host), so stdout carries protocol
// messages only: every human-readable line goes to stderr.
func runMCP() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	set, err := features.Resolve(cfg.Features)
	if err != nil {
		return err
	}
	backend := mcpboard.NewHTTPBackend(cfg.Addr, cfg.Token)
	fmt.Fprintf(os.Stderr, "snxwboard MCP: serving %s over stdio\n", cfg.Addr)
	if n := len(set.Names()); n > 0 {
		fmt.Fprintf(os.Stderr, "features on: %s\n", strings.Join(set.Names(), " "))
	}
	return mcpboard.New(backend, set).Serve(ctx, os.Stdin, os.Stdout)
}
