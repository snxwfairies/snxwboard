# Roadmap

Each step is one small PR with tests and, where it applies, a benchmark against a budget in
[ARCHITECTURE.md](ARCHITECTURE.md).

## Phase 1: agent fleet cockpit (v0.1, "try it in 60 seconds")

1. Repo skeleton: Go module, `cmd/snxw`, CI (build, test, `go vet`, race detector), release config.
2. Daemon core: token auth, `127.0.0.1` bind, SQLite WAL event log, `POST /v1/events`, `GET /v1/stream` with
   `Last-Event-ID` resume and snapshots.
3. Fold engine: session state machine, coalesced 4 Hz deltas, `waiting_for_you` detection, secret redaction.
4. Claude Code adapter + `snxw connect claude` (merges HTTP hooks into settings without clobbering existing ones,
   with a backup and `snxw disconnect claude`).
5. Web UI: sessions grouped by status ("needs you" first), detail timeline with history paging, `j`/`k`/`w`/`/`,
   desktop notifications. Done in PR #4; a `⌘K` command palette moves to Phase 2.
6. Codex adapter (MCP tool hooks) + `snxw connect codex`.
7. OpenCode adapter (`/event` SSE) + `snxw connect opencode`.
8. MCP server: `list_sessions`, `get_session`, `whats_running`, `notify_me`.
9. Benchmarks for every budget, wired into CI. Tag v0.1: the release workflow already builds binaries and
   checksums for Linux, macOS and Windows.

## Phase 2: GitHub and CI inbox

- Link sessions to branches, PRs and check runs automatically.
- Inbox of PRs, reviews, failing checks and deploys across all your repos and orgs, keyboard-first.
- GraphQL batching, ETag polling within `X-Poll-Interval`, webhook mode when self-hosted.
- TUI (`snxw tui`) on the same stream.

## Phase 3: one feed through MCP

- snxwboard as an MCP client: add any MCP server (Sentry, Linear, Vercel, Cloudflare, Slack, …) and map its
  tools to feed items on a schedule.
- Unified timeline and search across agents, GitHub and MCP feeds.
- Optional end-to-end encrypted sync relay for multiple machines.
