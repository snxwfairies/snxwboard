// Command snxw is the snxwboard daemon and CLI.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/config"
	"github.com/snxwfairies-ai/snxwboard-oss/internal/server"
	"github.com/snxwfairies-ai/snxwboard-oss/internal/store"
)

const version = "0.1.0-dev"

const usage = `snxw — local event daemon for coding agents

usage:
  snxw up                  run the daemon in the foreground
  snxw status              print daemon health
  snxw emit <kind> [json]  post an event to the daemon
  snxw connect claude [--project DIR]
                           add snxw hooks to Claude Code settings
  snxw disconnect claude [--project DIR]
                           remove them again
  snxw version             print the version
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "snxw:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "up":
		return runUp()
	case "status":
		return runStatus()
	case "emit":
		return runEmit(args[1:])
	case "connect":
		return runConnect(args[1:], true)
	case "disconnect":
		return runConnect(args[1:], false)
	case "version", "--version", "-v":
		fmt.Println(version)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

func runUp() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	st, err := store.Open(filepath.Join(cfg.Dir, "events.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	srv := server.New(server.Options{
		Store:   st,
		Token:   cfg.Token,
		Version: version,
		Logger:  logger,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("snxw starting", "version", version, "dir", cfg.Dir, "addr", cfg.Addr)
	return srv.Serve(ctx, cfg.Addr)
}

func runStatus() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	resp, err := httpClient().Get("http://" + cfg.Addr + "/healthz")
	if err != nil {
		return fmt.Errorf("daemon not reachable at %s: %w", cfg.Addr, err)
	}
	defer resp.Body.Close()

	var health struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&health); err != nil {
		return fmt.Errorf("read health: %w", err)
	}
	if !health.OK {
		return errors.New("daemon unhealthy")
	}
	fmt.Printf("ok snxw %s (%s)\n", health.Version, cfg.Addr)
	return nil
}

// runEmit posts one event; the optional argument is the event body.
func runEmit(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: snxw emit <kind> [json]")
	}
	var body json.RawMessage
	if len(args) > 1 {
		raw := strings.Join(args[1:], " ")
		if !json.Valid([]byte(raw)) {
			return errors.New("body must be valid JSON")
		}
		body = json.RawMessage(raw)
	}

	payload, err := json.Marshal(map[string]any{
		"source":  "cli",
		"session": "",
		"kind":    args[0],
		"body":    body,
	})
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+cfg.Addr+"/v1/events", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("daemon not reachable at %s: %w", cfg.Addr, err)
	}
	defer resp.Body.Close()

	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("ingest failed: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	fmt.Printf("accepted %s\n", bytes.TrimSpace(msg))
	return nil
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}
