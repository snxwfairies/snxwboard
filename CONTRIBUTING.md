# Contributing to snxwboard

Thanks for helping. This file is short on purpose.

## Before you start

- **Bugs:** open an issue with the bug template. Include `snxw version`, your OS, the agent and its version, and
  the smallest steps that reproduce it.
- **Features:** open an issue or a discussion first, so we can agree on the shape before you write code. Check
  [ROADMAP.md](ROADMAP.md); a new agent adapter is the most useful thing you can add.
- **Security issues:** do not open a public issue. See [SECURITY.md](SECURITY.md).

## Develop

```sh
git clone https://github.com/snxwfairies/snxwboard && cd snxwboard
go test -race ./...
go run ./cmd/snxw up
```

The web UI is plain HTML, CSS and JavaScript in `internal/web/static`, embedded in the binary. There is no build
step and no npm. Reload the page after `go run` picks up your change.

## Pull requests

- One change per PR. Keep it small enough to review in one sitting.
- Add a test for every behaviour change. A bug fix should come with a test that fails without it.
- Run `gofmt -l .`, `go vet ./...` and `go test -race ./...` before you push. CI runs them on Linux, macOS and
  Windows.
- Follow the design rules in [ARCHITECTURE.md](ARCHITECTURE.md): never block or slow an agent, no telemetry,
  redact before storing, and add no dependency without a reason in the PR.
- By opening a PR you agree that your contribution is licensed under the [MIT License](LICENSE).

## Adding an agent adapter

An adapter turns an agent's native events into snxwboard events (`session.start`, `prompt`, `tool.start`,
`tool.end`, `attention`, `turn.end`, `session.end`). `internal/adapter/claude` is the reference: a pure
`Translate` function with table tests, plus a `snxw connect <agent>` command that edits the agent's config
idempotently and keeps a backup.
