// Package server exposes the daemon HTTP API: token-authed ingest and one SSE
// stream per client.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/store"
)

const (
	defaultIngestCapacity = 4096
	subscriberBuffer      = 256
	flushInterval         = 50 * time.Millisecond
	flushSize             = 512
	heartbeatInterval     = 25 * time.Second
	writeTimeout          = 10 * time.Second
)

// Options configures a Server.
type Options struct {
	Store   *store.Store
	Token   string
	Version string
	Logger  *slog.Logger
	// IngestCapacity is how many events may sit in memory before ingest
	// answers 503. Defaults to 4096.
	IngestCapacity int
}

// Server is the ingest + stream daemon.
type Server struct {
	st      *store.Store
	token   string
	version string
	log     *slog.Logger
	ingest  chan store.Event

	startOnce       sync.Once
	stopOnce        sync.Once
	stopStreamsOnce sync.Once
	closing         chan struct{} // closed to end SSE handlers
	done            chan struct{} // closed to drain the writer and stop it
	wg              sync.WaitGroup

	subMu    sync.Mutex
	ingestMu sync.Mutex
	subs     map[*subscriber]struct{}
}

// New builds a Server. The writer goroutine starts on Start or Serve.
func New(opts Options) *Server {
	if opts.IngestCapacity <= 0 {
		opts.IngestCapacity = defaultIngestCapacity
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Server{
		st:      opts.Store,
		token:   opts.Token,
		version: opts.Version,
		log:     opts.Logger,
		ingest:  make(chan store.Event, opts.IngestCapacity),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
		subs:    make(map[*subscriber]struct{}),
	}
}

// Handler returns the HTTP routes. /healthz is open; every /v1 route needs the
// bearer token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("POST /v1/events", s.auth(http.HandlerFunc(s.handleEvents)))
	mux.Handle("GET /v1/stream", s.auth(http.HandlerFunc(s.handleStream)))
	return mux
}

// Start begins the single writer goroutine.
func (s *Server) Start() {
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go s.writeLoop()
	})
}

// Shutdown stops the SSE handlers, drains queued events to disk and exits the
// writer. It is safe to call more than once.
func (s *Server) Shutdown(ctx context.Context) error {
	s.stopStreams()
	s.stopOnce.Do(func() { close(s.done) })
	err := waitOrTimeout(ctx, &s.wg)
	s.closeSubs()
	return err
}

// stopStreams signals every SSE handler to return. Serve calls it before
// http.Server.Shutdown so that shutdown does not wait on open streams.
func (s *Server) stopStreams() {
	s.stopStreamsOnce.Do(func() { close(s.closing) })
}

// Serve validates addr, listens, and blocks until ctx is cancelled or the
// listener fails. On ctx cancel it stops accepting, ends streams, drains the
// ingest buffer to disk and returns.
func (s *Server) Serve(ctx context.Context, addr string) error {
	if err := checkBindAddr(addr); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	s.Start()

	hs := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- hs.Serve(ln) }()
	s.log.Info("snxw listening", "addr", ln.Addr().String())

	select {
	case err := <-serveErr:
		s.Shutdown(context.Background())
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	s.stopStreams()
	if err := hs.Shutdown(shutCtx); err != nil {
		hs.Close()
	}
	return s.Shutdown(shutCtx)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.version})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || s.token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkBindAddr refuses to expose the daemon beyond loopback unless the user
// opted in with SNXW_ALLOW_REMOTE=1.
func checkBindAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid addr %q: %w", addr, err)
	}
	if isLoopbackHost(host) || os.Getenv("SNXW_ALLOW_REMOTE") == "1" {
		return nil
	}
	return fmt.Errorf("refusing to bind non-loopback address %q (set SNXW_ALLOW_REMOTE=1 to override)", addr)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func waitOrTimeout(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
