# AGENTS.md — tilde (`~`), security-first terminal coding agent

Single Go binary. Read this before changing code; follow `docs/` for behavior.

## Repo map

- `main.go` — CLI, flags, headless, plugin/schedule/prune commands
- `internal/agent/` — ReAct loop, modes, subagents (`explore`, `work`)
- `internal/tools/` — read/write/edit/shell/search/git/web/memory tools
- `internal/tui/` — Bubble Tea UI (spec is law, see below)
- `internal/policy/` + `policies.yaml` — deny/ask/allow tiers
- `internal/sandbox/` — bubblewrap (default) / podman backends
- `internal/provider/` — ollama, openai, anthropic, openrouter, gemini, opencode
- `internal/{session,audit,scrub,creds,trust,hooks,mcp,plugin,skills}` — supporting systems
- `docs/` — behavior authority; `website/` — Astro/Starlight docs site

## Commands (Go 1.25+)

```sh
go build ./...          # must pass before any commit
go vet ./...            # must be clean
gofmt -l $(git ls-files '*.go')  # must print nothing
go test ./...           # full suite; internal/update needs git on PATH
```

Never commit with a red build, vet finding, unformatted file, or failing test.

## Hard invariants (code-level gates, not suggestions)

- **Plan mode is read-only**: mutating tools are withheld from the tool list
  *and* blocked at the mode/registry gates. Never weaken a gate to satisfy
  a feature — add a `PlanAllow` whitelist entry instead, scoped as narrowly
  as possible.
- **Sandbox stays fail-closed**: missing bwrap refuses startup; `TILDE_NO_SANDBOX`
  is an explicit user opt-out, never a code default. New subprocess execution
  goes through `sandbox.Config`, never raw `os/exec` with user input.
- **Secrets never touch logs/transcripts**: extend `internal/scrub` patterns for
  new secret shapes; session/audit append paths must scrub before writing.
- **No silent overflows**: context, transcript (`maxTranscriptLines`), caches,
  and log reads stay bounded with eviction + user-visible receipts.
- **Deny beats everything** in every mode (plan/build/auto/--yes). A breaking
  contract change (flags, tool schemas, exit codes, output shapes) needs a
  deprecation path, not a flag day.

## Behavior docs (read before touching the area)

- TUI/presentation: `docs/tui-design-spec.md` is source of truth (glyphs,
  keybindings, empty states, confirm flows). Open findings: `docs/tui-audit.md`.
- Product scope/status: `docs/Plan.md` (update the status table in place).
- Architecture boundaries: `docs/ARCHITECTURE.md`.
- Marketplace/plugins: `docs/marketplace.md`.

## Git

- Small, single-purpose commits on `main`; lowercase one-line messages
  (`fix resume delete needing no confirm`).
- Update `CHANGELOG.md` (unreleased section) and the relevant `docs/` file in
  the same change that changes behavior — docs that lag code start rotting
  immediately.
- Never commit secrets, session logs, or `~/.tilde/` contents.
