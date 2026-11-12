# Architecture

snxwboard is a local daemon (`snxw`) that receives events from coding agents and developer services, folds them
into per-session state, and streams small deltas to the UIs (web, TUI, CLI) and to MCP clients.

```
 Claude Code ──HTTP hook POST──┐
 Codex ───────MCP tool hook────┤                 ┌── web UI (SSE)
 OpenCode ────SSE /event───────┼──► snxw daemon ─┼── TUI (SSE)
 any script ──snxw emit / HTTP─┤   ingest → log  ├── CLI (HTTP, --json)
 GitHub ──GraphQL + ETag poll──┤   fold → state  └── MCP server (stdio + streamable HTTP)
 other MCP servers (client)────┘   SQLite (WAL)
```

## Design rules

1. **Never slow or break an agent.** Ingest endpoints answer `204` after an in-memory append and never block on
   disk, network or locks. Hooks are configured with short timeouts and the default `onFailure: continue`, so a
   stopped daemon is a non-blocking error, not a stuck agent.
2. **Local-first, private by default.** Binds `127.0.0.1` only. Every request needs the per-install token from
   `~/.config/snxw/token` (0600). No telemetry of any kind. Secrets in captured tool input (keys, tokens, `.env`
   values, `Authorization` headers, high-entropy strings) are redacted at ingest, before storage. Prompt text and
   file contents are off by default and kept only if the user opts in per source.
3. **Push, never poll, between our own processes.** UIs and MCP clients hold one SSE stream each.
4. **Measured, not claimed.** Every budget below has a benchmark in CI that fails the build when exceeded.

## Runtime

- **Go**, one static binary for macOS, Linux and Windows, with the web UI embedded (`embed.FS`). Pure-Go SQLite
  (`modernc.org/sqlite`), so no cgo is needed to cross-compile.
- **Why Go:** small resident memory for an always-on daemon, fast cold start for the CLI and for the
  command-hook fallback, and trivial cross-compilation. Claude Code (HTTP hooks) and Codex (MCP tool hooks)
  never spawn our process per event, so spawn time only matters for that fallback.
- **Web UI:** three static files (HTML, CSS, plain JS) embedded in the binary with `go:embed`: no framework, no
  build step, 9 KB gzipped. It renders the state the daemon already folded, so the client does no data joining. It
  reads the stream with `fetch` rather than `EventSource`, so the token travels in the `Authorization` header and
  never in a URL or cookie; `snxw open` passes it in the URL fragment, which never reaches the server. Agent output
  is inserted as text nodes only, under a `default-src 'none'; script-src 'self'` CSP.

## Data model

- `events`: append-only log. `(seq INTEGER PRIMARY KEY, ts, source, session, kind, body BLOB)`, where the body
  is compact JSON. `seq` is the single cursor everything resumes from.
- `sessions`: folded state per agent session: agent, repo, cwd, branch, status
  (`running | waiting_for_you | idle | done | failed`), current tool, counts, model, tokens and cost if reported,
  linked PR and CI state, `updated_seq`.
- `prs`, `checks`: GitHub state, keyed by `repo#number` and head SHA, with the ETag of the last fetch.
- Retention: raw high-frequency events (every tool call) are compacted after 7 days into per-session
  summaries. Folded state is kept until the user deletes it.

## Ingest adapters

| Source | Mechanism (verified against vendor docs, 2026-11-11) | Process spawn per event |
|---|---|---|
| Claude Code | `type: "http"` hooks POST the hook JSON to `http://127.0.0.1:7766/v1/hooks/claude`: SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, PostToolUseFailure, Notification, Stop, SubagentStop, SessionEnd | none |
| Codex | `type: "mcp_tool"` hooks call our MCP tool `snxw_ingest` on SessionStart, PreToolUse, PostToolUse, Stop, SubagentStop. `command` hook running `snxw hook codex` as a fallback | none (fallback: one) |
| OpenCode | the daemon subscribes to `opencode serve` at `GET /event` (SSE) and maps bus events | none |
| Anything else | `snxw emit` CLI, or `POST /v1/events` | caller's choice |
| GitHub | GraphQL batch per repo for PRs + check rollups, REST events with `If-None-Match`; honours `X-Poll-Interval`; webhooks when self-hosted with a public URL | n/a |
| MCP servers | snxwboard as an MCP **client**: user-listed servers (Sentry, Linear, Vercel, …) are polled through their tools on a schedule and mapped to feed items | n/a |

Fold rules turn adapter events into session state. Example: Claude `Notification` with a permission prompt, or
`Stop` with no follow-up, sets `waiting_for_you` and triggers a desktop notification.

## Wire protocol (daemon → clients)

- `GET /v1/stream` (SSE). Each message is `id: <seq>` plus one JSON patch of folded state:
  `{"t":"s","id":"<session>","p":{"status":"running","tool":"Bash"}}`. Only changed fields are sent.
- **Resume:** the client sends `Last-Event-ID`. The daemon replays deltas after that `seq`, or sends a snapshot
  if the gap is past compaction.
- **Coalescing:** tool-call bursts (an agent can fire dozens per second) are folded on the daemon and flushed to
  clients at most 4 times a second per session. Clients never see raw hook traffic.
- **Idle cost:** a comment-only heartbeat every 25 s (`:\n`, 2 bytes plus framing) keeps proxies from closing the
  stream.
- **Compression:** snapshots and static assets are compressed (brotli for assets, gzip for snapshots). Small
  deltas are sent uncompressed, because per-message compression on SSE costs more than it saves under
  ~200 bytes.
- **Same API for everyone:** web UI, TUI, `snxw --json` and the MCP server read the same endpoints.

## MCP server

`snxw mcp` (stdio) and `POST /mcp` (streamable HTTP, token-auth).

- Tools: `snxw_ingest` (used by Codex hooks), `list_sessions`, `get_session`, `search_feed`, `notify_me`
  (an agent can ask for the human's attention), `whats_running` (summary for an agent deciding whether to
  touch a repo another agent is editing).
- Resources: `snxw://sessions`, `snxw://sessions/{id}`, `snxw://feed`.

## Performance budgets (CI-enforced)

| Budget | Target |
|---|---|
| Ingest latency, p99, `POST /v1/hooks/claude` on loopback | < 2 ms |
| Ingest throughput | ≥ 20,000 events/s on one core |
| Daemon resident memory, idle with 50 sessions | < 30 MB |
| Idle stream bytes per client per hour | < 1 KB |
| Delta size, median | < 120 bytes |
| Web UI first load (gzip, all JS + CSS) | < 60 KB |
| GitHub requests per watched repo per hour with no changes | ≤ 4 that count against the rate limit (304s are free) |
| CLI cold start (`snxw status`) | < 15 ms |

## Sync (later, optional)

A self-hostable relay (single binary, or a Cloudflare Worker + D1) that forwards encrypted deltas between your
machines. End-to-end encrypted with a key that never leaves your devices; the relay only stores ciphertext and
`seq` cursors.
