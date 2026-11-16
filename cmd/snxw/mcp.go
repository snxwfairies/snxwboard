package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/snxwfairies/snxwboard/internal/config"
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

	backend := mcpboard.NewHTTPBackend(cfg.Addr, cfg.Token)
	fmt.Fprintf(os.Stderr, "snxwboard MCP: serving %s over stdio\n", cfg.Addr)
	return mcpboard.New(backend).Serve(ctx, os.Stdin, os.Stdout)
}
