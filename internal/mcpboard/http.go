package mcpboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// HTTPBackend reads the board from a running daemon over loopback, using the
// same endpoints the CLI and the web UI use. Keeping the MCP server on the
// public surface means it cannot drift from what the board shows, and it adds
// no second path into SQLite.
type HTTPBackend struct {
	addr   string
	token  string
	client *http.Client
}

// NewHTTPBackend points a backend at a daemon address ("127.0.0.1:7766") and
// the per-install token.
func NewHTTPBackend(addr, token string) *HTTPBackend {
	return &HTTPBackend{
		addr:   addr,
		token:  token,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Sessions returns every folded session.
func (b *HTTPBackend) Sessions(ctx context.Context) ([]Session, error) {
	var out struct {
		Sessions []Session `json:"sessions"`
	}
	if err := b.get(ctx, "/v1/sessions", nil, &out); err != nil {
		return nil, err
	}
	if out.Sessions == nil {
		out.Sessions = []Session{}
	}
	return out.Sessions, nil
}

// History returns up to limit recent events, for one session when session is
// set and across every session when it is empty.
func (b *HTTPBackend) History(ctx context.Context, session string, limit int) ([]Event, error) {
	q := url.Values{}
	if session != "" {
		q.Set("session", session)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Events []Event `json:"events"`
	}
	if err := b.get(ctx, "/v1/events", q, &out); err != nil {
		return nil, err
	}
	if out.Events == nil {
		out.Events = []Event{}
	}
	return out.Events, nil
}

// Ingest records one event. The daemon answers 202 after an in-memory append,
// so this never waits on disk.
func (b *HTTPBackend) Ingest(ctx context.Context, ev IngestEvent) error {
	source := ev.Source
	if source == "" {
		source = "mcp"
	}
	payload, err := json.Marshal(map[string]any{
		"source":  source,
		"session": ev.Session,
		"kind":    ev.Kind,
		"body":    ev.Body,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url("/v1/events", nil), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	b.authorise(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("daemon not reachable at %s: %w", b.addr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return fmt.Errorf("ingest failed: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	return nil
}

func (b *HTTPBackend) get(ctx context.Context, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.url(path, q), nil)
	if err != nil {
		return err
	}
	b.authorise(req)
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("daemon not reachable at %s: %w", b.addr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return fmt.Errorf("%s: %s: %s", path, resp.Status, bytes.TrimSpace(msg))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode: %w", path, err)
	}
	return nil
}

func (b *HTTPBackend) authorise(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+b.token)
}

func (b *HTTPBackend) url(path string, q url.Values) string {
	u := "http://" + b.addr + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}
