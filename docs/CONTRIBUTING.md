# Contributing

tilde is a single Go binary: a terminal-native coding agent built security-first.
This page covers the conventions a change is expected to follow. Read
[AGENTS.md](../AGENTS.md) first — it is the condensed rule set — then the docs
for whichever area you are touching.

## Before you start

```sh
go build ./...
go vet ./...
gofmt -l $(git ls-files '*.go')   # must print nothing
go test ./...
```

All four must be clean before a commit. Anything that needs explanation is in
[TESTING.md](TESTING.md).

## Where things live

| Path | Owns |
|---|---|
| `main.go`, `auth.go`, `deploy.go`, `doctor.go`, `harness.go` | CLI dispatch, credential/deploy/doctor commands, tool-registry wiring |
| `internal/agent/` | The ReAct loop, modes, subagents |
| `internal/tools/` | read/write/edit/shell/search/git/web/memory tools |
| `internal/tui/` | Bubble Tea UI — [`tui-design-spec.md`](tui-design-spec.md) is the source of truth |
| `internal/policy/`, `internal/sandbox/`, `internal/trust/`, `internal/scrub/`, `internal/slopsquatting/` | The security gates |
| `internal/provider/` | Model backends |
| `docs/` | Behavior authority; `website/` renders it |

[ARCHITECTURE.md](ARCHITECTURE.md) states the dependency rule: `main` may
import `internal/*`; `internal/*` must not import `main`, and new cross-package
dependencies point inward toward the leaf gates, never forming a cycle.

## Hard invariants

These are code-level gates, not suggestions. A change that weakens one is a
bug, even if tests pass.

- **Plan mode is read-only.** Mutating tools are withheld from the model's
  tool list *and* blocked at the mode/registry gates. Never widen a gate for a
  feature — add a narrowly scoped `PlanAllow` entry instead.
- **The sandbox stays fail-closed.** A missing `bwrap` refuses startup;
  `TILDE_NO_SANDBOX` is an explicit user opt-out, never a code default. New
  subprocess execution goes through `sandbox.Config`, never raw `os/exec` with
  user input.
- **Secrets never reach logs or transcripts.** Extend `internal/scrub`
  patterns for new secret shapes; session and audit append paths scrub before
  writing.
- **Nothing overflows silently.** Context, transcript, caches, and log reads
  stay bounded with eviction and a user-visible receipt.
- **Deny beats everything**, in every mode (Plan/Build/Auto/`--yes`).
- **No flag day.** A breaking change to flags, tool schemas, exit codes, or
  output shapes needs a deprecation path.

## Writing the change

**Keep it small and single-purpose.** One concern per commit; a refactor and a
behavior change do not share one.

**Match the house style.**

- Errors are actionable: `fmt.Errorf("refusing %q: outside the project root — work inside %s", p, root)`. Say what happened *and* what to do.
- A tool never returns silence: empty results, past-EOF, and denials all carry
  a one-line reason plus the next step.
- Comments explain *why*, not *what*. The code says what it does.
- No new dependencies without a reason that survives review; the current
  dependency set is deliberate.
- Prefer stdlib. If a helper would exist for one caller, inline it.

**Fail closed on anything security-shaped.** An unparseable policy file, an
unknown tier, or an unresolvable path must refuse rather than fall back to a
weaker default.

**Add the test.** Behavior change → test. Security fix → regression test, and
confirm it fails without the fix.

## Docs are part of the change

Update the doc that describes the behavior in the *same* commit; docs that lag
by even one change start rotting.

- User-visible behavior → `CHANGELOG.md` (unreleased section) and the relevant
  page under `docs/`.
- TUI presentation → `docs/tui-design-spec.md`.
- Product scope/status → `docs/Plan.md` (update the status table in place).
- Security properties → `docs/SECURITY.md`; package-install risk →
  `docs/SLOPSQUATTING.md`.
- Marketplace/plugins → `docs/marketplace.md`.

## Commits

Small, single-purpose commits on `main`, lowercase one-line messages:

```
fix resume delete needing no confirm
```

Never commit secrets, session logs, or the contents of `~/.tilde/`.

## Reporting a security issue

Do not open a public issue. Report it privately so it can be fixed before
disclosure. See [SECURITY.md](SECURITY.md) for the threat model and the
defenses a report should be measured against.
