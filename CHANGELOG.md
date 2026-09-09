# Changelog

Release notes are grouped by version and describe shipped behavior. For
planned work and implementation status, see [docs/Plan.md](docs/Plan.md).

## v0.11.0 (unreleased)

- `--version` and the TUI now include the short source revision for VCS
  builds (for example, `v0.9.0+g5f94724`), while release comparisons still
  use the stable release tag.
- `tilde update`: repairs a stale or missing installed binary even when the
  source checkout is already current; CI now smoke-tests both version flag forms.
- `tilde run-due`: scheduled headless runs from `.tilde/schedule.yaml`
  (interval or daily HH:MM, state file, failed jobs retry next tick).
- `tilde audit`: read the governance trail with since/tool/decision filters.
- `tilde plugin install|verify|list`: hash-pinned local plugins (v1 manifest).
- `diagnose`: gofmt/parse-error/TODO diagnostics over Go code, read-only.
- `remember`: vector memory over the repo (offline TF-IDF default,
  Ollama embeddings via `TILDE_EMBED_MODEL`); index is ask + Plan-blocked.
- `web_shot`: headless-Firefox viewport screenshots (ask, SSRF-gated on
  the initial URL; Firefox follows redirects internally, so prefer
  `web_fetch` when only text is needed).
- `tilde ide-bridge`: stdio JSON bridge for IDE hosts (chat approvals deny).
- `tilde models [provider]`: catalog windows + prices without network.
- `sandbox.Containerfile` + image doc: digest-pinned podman backend image.
- Eval: trajectory tasks for symbol/diagnose/memory/search-unavailable.
- Project rules files auto-load under the project trust gate.
- `remember` recall accepts a recency half-life; `memory` saves are dated.
- Eval reports a MED_COST column (median over passing trials).
- `tilde prune`: retention windows for sessions and the audit trail.
- `deny_paths`: path-scoped policy deny (beats --yes, names its pattern).
- `tilde fork`: branch a session at an earlier point or tip.
- Status bar shows session $ cost; audit records denials too.
- `tilde --export`: headless brief export (cwd-contained).
- Plan banner + Update Todos block; subagent ⋮/│ timeline rows.
- Contrast-verified accent-select; exit 3 for all cloud providers.
- Secret scrubbing is one pattern set (`internal/scrub`; tools/hooks/session/audit
  consume it — hooks previously redacted a narrower subset).
- `web_fetch` allow_net redirects refuse unless the target host is listed;
  `web_search` backend redirects re-validate scheme/userinfo/SSRF.

P1+P2 user-visible changes (binary still reports `v0.9.0` until release):

- `web_search`: keyless web search (ask-tier, Plan-OK). Needs
  `TILDE_ALLOW_NET=1` or a listed host, like `web_fetch`.
- `allow_net` in `policies.yaml`: per-host fetch/search approval without
  the session-wide opt-in. SSRF guards still apply.
- `web_fetch` returns readable text or markdown (`format:` arg).
- `spawn_work` / `apply_work` / `discard_work`: one worktree session at a
  time; review the diff, then apply or discard it.
- Credentials are sealed at rest (AES-GCM envelope, transparent on use).
- `tilde update` verifies the release tag signature before pulling.
- Hooks gained `session_start` / `session_end` and a minimal env (secrets
  never pass through).
- MCP gained remote servers, per-tool approval, and user-authoritative
  merge (project configs cannot rewire your servers).
- Background tasks cap at 16 running; wide read-only turns flush at 8.
- `symbol_search`: definition index (go/py/ts/js/rs) with reference fallback.
- `memory`: project-local facts at `.tilde/memory.md` (recall reads free, save/forget ask).
- Streaming model output (ollama/openai) with automatic non-stream fallback.
- `TILDE_BACKEND=podman` runs shell calls in a digest-pinned container.
- Governance trail at `~/.tilde/audit/audit.jsonl` (hashes, never raw args).
- Releases ship tarballs + checksums + SBOM; `install.sh --from-release TAG`
  installs one sha256-verified.

## v0.9.0

P0 fix round (matches `Version` in `internal/update/update.go`):

- `/export` brief output (`internal/export/`) with tests.
- Provider error classification (`internal/provider/classify.go`) with tests.
- Policy tool-call validation (`internal/policy/`) with tests.
- Resume offer in slash commands (`internal/tui/slash.go`).
- Versioned self-update: release tags replace SHAs in notices, `--version` flag.
