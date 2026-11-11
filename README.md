# snxwboard

**One live screen for every coding agent you run.**

You have Claude Code in one terminal, Codex in another, OpenCode on a remote box, and a background agent
opening PRs. snxwboard shows all of them in one place: what each agent is doing right now, what it changed,
which PR it opened, whether CI passed, how long it took, and a ping the moment one is waiting for you.

- **Local-first.** One small binary and one SQLite file on your machine. Nothing leaves it unless you turn on sync.
- **No telemetry.** None, not even opt-out. Secrets in tool inputs are redacted before anything is stored.
- **Zero-overhead hooks.** Claude Code talks to it over native HTTP hooks and Codex over MCP tool hooks, so no
  process starts on each tool call. If snxwboard is down, your agents carry on.
- **MCP both ways.** Agents can ask snxwboard what other agents are doing. snxwboard can pull feeds from the MCP
  servers you already use.
- **Keyboard-first.** `⌘K` for everything, `j`/`k` to move. A web UI, a TUI and a CLI share one API.
- **MIT licensed**, self-hostable end to end.

> Status: pre-alpha, under active development. See [ROADMAP.md](ROADMAP.md) and [ARCHITECTURE.md](ARCHITECTURE.md).

## Quick start (target for v0.1)

```sh
go install github.com/snxwfairies-ai/snxwboard-oss/cmd/snxw@latest   # or a release binary
snxw up                                             # daemon on 127.0.0.1:7766, opens the board
snxw connect claude                                 # adds the HTTP hooks to ~/.claude/settings.json
snxw connect codex                                  # adds MCP-tool hooks to ~/.codex/config.toml
snxw connect opencode                               # subscribes to `opencode serve` events
```

Start any agent. It appears on the board.

## License

MIT, see [LICENSE](LICENSE).
