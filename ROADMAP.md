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

## Phase 2: the board

The cockpit shows what agents are doing. The board adds what they should be doing: a Kanban view of work, with
agents as the workers on it.

**A card is a unit of work, not a session.** Sessions attach to a card as its runs, so one task can show three
attempts by two agents. A session that starts without a card gets one automatically, so nothing is off the board.

Columns, in order: Triage, Todo, Ready, Running, Blocked, Review, Done (Archived is a filter, not a column).
People can drag cards between Triage, Todo, Ready, Blocked and Done. Running and Review are set only by events: a
run starting, a PR opening for review.

1. **Column view over existing state.** Show today's sessions as auto-cards: `running` goes to Running,
   `waiting_for_you` to Blocked, `done` to Done, `failed` to Blocked with the error. Same stream, same budgets.
2. **Cards people create.** `POST /v1/cards`, a quick-add box per column, `snxw card add`, title and description,
   labels, assignee (an agent such as `claude` or `codex`, or a person). Drag moves follow the allow-list above.
3. **Comments and attention.** Comments on a card go to its agent: shown in its next session through the MCP server
   (`get_card`, `add_comment`) and the Claude Code hook. An attention bar counts cards that are blocked or
   waiting for you, first in the list.
4. **Dependencies, run history and logs.** Parent and child cards; a card with an open parent cannot go to Ready.
   The card detail shows every run with its agent, duration, cost and log.
5. **Several boards.** One per project or team, with a switcher, search and filters (assignee, label, archived), and
   bulk complete or archive.
6. **Dispatch (decided separately).** Optionally start an agent on a Ready card. This changes snxwboard from a
   viewer into an orchestrator, so it gets its own design PR before any code.

## Phase 3: GitHub and CI inbox

- Link sessions and cards to branches, PRs and check runs automatically; a PR in review moves its card to Review,
  a merge moves it to Done.
- Inbox of PRs, reviews, failing checks and deploys across all your repos and orgs, keyboard-first.
- GraphQL batching, ETag polling within `X-Poll-Interval`, webhook mode when self-hosted.
- TUI (`snxw tui`) on the same stream.

## Phase 4: one feed through MCP

- snxwboard as an MCP client: add any MCP server (Sentry, Linear, Vercel, Cloudflare, Slack, …) and map its
  tools to feed items on a schedule.
- Unified timeline and search across agents, GitHub and MCP feeds.
- Optional end-to-end encrypted sync relay for multiple machines.
