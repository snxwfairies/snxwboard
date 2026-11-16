# snxwboard

**One live screen for every coding agent you run.**

[![ci](https://github.com/snxwfairies/snxwboard/actions/workflows/ci.yml/badge.svg)](https://github.com/snxwfairies/snxwboard/actions/workflows/ci.yml)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

You have Claude Code in three terminals and lose track of which one is waiting for you. snxwboard shows every
session on one board: what each agent is doing right now, which tool it is running, which ones failed, and which
ones are blocked on you, sorted to the top.

- **Local-first.** One small Go binary and one SQLite file. It binds `127.0.0.1` and needs a per-install token.
- **No telemetry.** None, not even opt-out.
- **Secrets stay out.** API keys, tokens, passwords and `.env` values in tool input are redacted before anything
  is stored.
- **Never slows your agent.** Claude Code talks to it over native HTTP hooks with a 2 s timeout, so no process
  starts per tool call. If snxwboard is down, your agents carry on.
- **Keyboard-first.** `j`/`k` to move, `w` for the next session waiting for you, `/` to filter.

> **Status: pre-alpha.** Claude Code works today. Codex, OpenCode, an MCP server, a GitHub/CI inbox and MCP feeds
> are next; see [ROADMAP.md](ROADMAP.md). Expect breaking changes before v0.1.

## Quick start

Needs Go 1.26.11 or newer (release binaries come with v0.1).

```sh
go install github.com/snxwfairies/snxwboard/cmd/snxw@latest
snxw up                  # daemon on 127.0.0.1:7766; leave it running
snxw connect claude      # adds HTTP hooks to ~/.claude/settings.json (backup kept)
snxw open                # the board in your browser, signed in
```

Start a Claude Code session. It appears on the board. `snxw disconnect claude` removes the hooks again, and
`snxw connect claude --project DIR` limits them to one project.

Anything else can post events too:

```sh
snxw emit note '{"text":"shipping v2"}'   # or POST /v1/events with the token
```

### Let an agent act on the board

`snxw mcp` serves the same board to any MCP client over stdio, so an agent can read the state instead of only
reporting into it:

```sh
claude mcp add snxwboard -- snxw mcp
```

Tools: `whats_running` (what is in flight, what is blocked on you), `list_sessions`, `get_session`,
`search_feed`, `notify_me` (ask the human), and `snxw_ingest` (record your own events). An agent that calls
`whats_running` before it starts knows which repos another session already has open.

### Autonomy is opt-in, and off

Every autonomous behaviour is in the binary and off. `snxw features` lists them; nothing runs until you name it:

```sh
snxw features                          # the catalogue, every entry "off"
snxw features enable session_search     # cross-session search, for MCP search_feed
snxw features enable autopilot session_search
snxw features disable autopilot
```

The shape is borrowed from [Hermes Agent](https://github.com/NousResearch/hermes-agent), whose toolsets are
opt-in groups behind `enabled: false`: the capability is present and documented, and does nothing until the
operator asks for it. Two rules make the gate worth having:

- **Unmet needs fail loudly.** `autopilot` needs `session_search`; enabling one alone is an error, never an
  implicit enable. An unknown name fails too, so a typo cannot quietly leave you with less than you meant.
- **An off feature is unreachable, not just quiet.** The MCP server hides a gated tool from `tools/list` and
  refuses it in `tools/call` — with the command that would turn it on.

Features are `read` (observe only), `act` (change folded state and the feed) or `spawn` (start processes or drive
other software). Entries marked *planned* are declared but have no behaviour wired yet, and cannot be enabled.

## How it works

Agents push events to the daemon over HTTP. The daemon redacts them, appends them to a SQLite log and folds them
into per-session state. The board holds one SSE stream and receives a snapshot, then small patches at most four
times a second. Details and performance budgets are in [ARCHITECTURE.md](ARCHITECTURE.md).

## Contributing

Issues and pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) first. Report security issues
privately, as described in [SECURITY.md](SECURITY.md).

## License

MIT, see [LICENSE](LICENSE).
