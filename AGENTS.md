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

## Reference agents (local only: `~/Desktop/Materials/`)

Consult these when designing modes, permissions, prompts, or verification —
follow the cited pattern, don't copy code across languages.

- `alysis-code/` — gate-is-law + tool-exposure-by-mode (with tests), persona
  clamp rule (never raise above session mode), always-on destructive denylist.
  Also: SHA-fingerprinted session grants, verification-evidence hierarchy with
  blast-radius gate, Forge plan flow (worktrees + gates + repair loop).
- `codex/` — verification discipline: scoped test commands, integration over
  unit for agent logic, change-size caps, model-context budgets. Also:
  3-consecutive-denial circuit breaker (abort + guidance), sandbox-escalation
  requests with justification + prefix rules, snapshot tests for UI.
- `grok-build/` — `CapabilityMode` read-only filtering, explicit plan toolsets,
  destructive-command blocklists with bypass tests. Also: TodoGate turn
  re-opening while todos pending, best-of-N tournament evaluation, whole-
  process Landlock/Seatbelt sandbox profiles.
- `kimi-code/` — yolo/auto split, denial discipline ("adjust approach, don't
  retry unchanged"), verify-before-done turn closing. Also: ordered deny-first
  policy chain, resource-conflict parallelism (overlap non-conflicting calls),
  plan revisions with sha tracking, compaction honesty rules.
- `OpenHands-CLI/` — confirmation-mode design (always-ask / always-approve /
  llm-approve) for approval-timing decisions. Also: critic self-review loop
  (score-gated refinement with iteration cap), Textual snapshot-test
  discipline, env-vars-ignored-by-default config stance.
- `pi/` — `setActiveTools` plan-mode pattern, destructive vs safe command
  patterns, parallel subagent fan-out. Also: JSONL tree sessions with
  rebranch/fork, structured compaction schema, extension-only architecture
  (core stays tiny; workflows are extensions).
- `cline/` — plan/act/yolo presets, plan→act handoff wording, mistake-tracker
  anti-runaway, batch-independent-calls discipline. Also: loop soft-3/hard-5
  thresholds, yolo gated on `submit_and_exit`, checkpoint/undo recovery.
- `freebuff/` — parallel-tools-always discipline, plan-only orchestrator prompt,
  validate/test step in multi-step work. Also: reviewer+basher verify fan-out
  per task, direct/propose/patch tool triplets, buffbench eval harness with
  flake hunting.
- `kilocode/` — permission ceilings that user rules can't widen, guarded tool
  lists for read-only modes. Also: rule-source explanations in approval
  prompts, memory-as-context-not-instruction, 5-phase plan-file workflow with
  exit review, per-agent step caps.
- `opencode/` — permission rulesets + plan-file flow, doom-loop threshold,
  deny-still-enforced-under-auto. Also: reject-with-message feedback
  (`CorrectedError`), question tool for clarifications, terse turn-closing
  with mandatory lint/typecheck.

## Git

- Small, single-purpose commits on `main`; lowercase one-line messages
  (`fix resume delete needing no confirm`).
- Update `CHANGELOG.md` (unreleased section) and the relevant `docs/` file in
  the same change that changes behavior — docs that lag code start rotting
  immediately.
- Never commit secrets, session logs, or `~/.tilde/` contents.
