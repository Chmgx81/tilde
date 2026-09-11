# Changelog

Release notes are grouped by version and describe shipped behavior. For
planned work and implementation status, see [docs/Plan.md](docs/Plan.md).

## v0.10.0 (unreleased)

- Website redesigned to the product's own design language: the TUI spec
  token palette (amber/cyan/semantic glyphs on the blue-gray `#0D1117`
  surface), self-hosted Newsreader/Inter/JetBrains Mono, spec-glyph
  workflow demos, a deny/ask/allow permission-ladder section, and a
  WCAG-AA-verified dark docs theme. The transparent banner set is now the
  site mark: nav banner in the header (~140px desktop, ~120px mobile),
  hero banner above the landing headline (~480px desktop, ~416px tablet,
  up to 320px mobile), and the 32px PNG as favicon (48px for
  apple-touch-icon, hero banner for og/twitter images).
- The site deploys to Vercel from the repo root (`vercel.json`: build
  inside `website/`, clean URLs, immutable `/_astro/` caching; Node 24
  comes from the project's Node.js Version setting); `/docs` redirects to
  the overview page. The build reads the repo-root `docs/`, so don't
  deploy from inside `website/`. Pushes to `main` touching `website/`,
  `docs/`, or `vercel.json` deploy
  automatically via GitHub Actions.
- CI's `build-test` job checks out full history so version tests see the
  release tags: a shallow checkout made `BuildVersion()` report a bare
  SHA instead of `v0.9.1+…` and fail `internal/update` tests.

- `--version` and the TUI now include the short source revision for VCS
  builds (for example, `v0.9.0+g5f94724`), while release comparisons still
  use the stable release tag.
- `tilde update`: repairs a stale or missing installed binary even when the
  source checkout is already current; CI now smoke-tests both version flag forms.
- `tilde run-due`: scheduled headless runs from `.tilde/schedule.yaml`
  (interval or daily HH:MM, state file, failed jobs retry next tick).
- `tilde schedule [--json]`: list scheduled jobs with due state + next run.
- `tilde audit`: read the governance trail with since/tool/decision filters.
- `tilde plugin install|upgrade|enable|disable|remove|rollback|verify|list`: hash-pinned local plugins (v1 manifest); `install --dry-run` previews.
- `diagnose`: gofmt/parse-error/TODO diagnostics over Go code, read-only.
- `remember`: vector memory over the repo (offline TF-IDF default,
  Ollama embeddings via `TILDE_EMBED_MODEL`); index is ask + Plan-blocked.
- `web_shot`: headless-Firefox viewport screenshots (ask, SSRF-gated on
  every redirect hop, viewport clamped 640–3840 × 480–2160).
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
- Contrast-verified accent-select; exit 3 for provider errors (any backend, including ollama).
- Secret scrubbing is one pattern set (`internal/scrub`; tools/hooks/session/audit
  consume it — hooks previously redacted a narrower subset).
- `web_fetch` allow_net redirects refuse unless the target host is listed;
  `web_search` backend redirects re-validate scheme/userinfo/SSRF.

P1+P2 user-visible changes (binary still reports `v0.9.1` until release):

- `web_search`: keyless web search (ask-tier, Plan-OK). Needs
  `TILDE_ALLOW_NET=1` or a listed host, like `web_fetch`.
- `save_plan`: Plan mode persists the numbered plan to
  `.tilde/plans/<slug>.md` (re-saving revises); approval stays the
  Tab-to-Build handoff, Build reads it back with `read_file`.
- `/critic`: score-gated self-review of the working-tree diff
  (deterministic offline rubric, threshold 80).
- `/apply [path]` / `/discard [path]`: apply (keep worktree) or discard
  (remove worktree) the pending `spawn_work` session via the registry
  `apply_work`/`discard_work` tools; no session fails loud.
- `allow_net` in `policies.yaml`: per-host fetch/search approval without
  the session-wide opt-in. SSRF guards still apply.
- `web_fetch` returns readable text or markdown (`format:` arg).
- `spawn_work` / `apply_work` / `discard_work`: one worktree session at a
  time; review the diff, then apply or discard it.
- Credentials are sealed at rest (AES-GCM envelope, transparent on use).
- `tilde update` verifies the release tag signature before pulling.
- Hooks gained a minimal env (secrets never pass through);
  `session_start` / `session_end` are parsed and shown but reserved —
  not executed.
- MCP gained remote servers, per-tool approval, and user-authoritative
  merge (project configs cannot rewire your servers). MCP servers run
  unsandboxed with your user privileges — only install sources you trust.
- Background tasks cap at 16 running; wide read-only turns flush at 8.
- `symbol_search`: definition index (go/py/ts/js/rs) with reference fallback.
- `memory`: project-local facts at `.tilde/memory.md` (recall reads free, save/forget ask).
- Streaming model output (ollama/openai) with automatic non-stream fallback.
- `TILDE_BACKEND=podman` runs shell calls in a digest-pinned container.
- Governance trail at `~/.tilde/audit/audit.jsonl` (hashes, never raw args).
- Releases ship tarballs + checksums + SBOM; `install.sh --from-release TAG`
  installs one sha256-verified. `get.sh` now refuses unverified installs
  (missing checksums, entry, or sha256 tool all fail closed).
- `tilde --help` lists management subcommands; `tilde plugin install
  <dir> --dry-run` previews a plugin install without writing.
- Marketplace tab seeds bundled starter plugins (`go-dev`, `git-hygiene`)
  installable offline with one keypress (`embedded://` sources run the full
  validated install); empty tabs explain what belongs there and the next step.
- TUI safety: resume-list delete, `/clear`, and named `/logout` all need a
  second confirming press; `Esc` cancels a running shell escape and shows
  armed in the status bar; `q` quits on an empty idle composer.
- Bundled skills load from the marketplace (embedded sources resolve via
  `ParseFile`; load failures name the reason instead of "not found").
- Cancelled turns read as a calm receipt, not `✗ context canceled`;
  error classification gains a `cancelled` class.
- Agent loop prompt requires plan-first, build-through-tools discipline:
  code is delivered via `write_file`/`edit_file` and summarized, never
  pasted in full into chat.
- The pre-response `thinking` row animates in place (spinner + elapsed)
  instead of sitting frozen during slow model waits.
- Plan mode withholds mutating tools from the model's tool list (not just
  gate-blocked), demands a numbered plan in the prompt, and injects a plan
  reminder every 2nd gate denial — Plan turns plan instead of collecting
  denials until the user cancels.
- Build and Auto modes get their own prompt blocks (approval-aware batching
  and momentum; bounded auto-approved automation with destructive shapes
  still denied), and repeated policy denials inject a redirect reminder.
- docs website (Astro + Starlight) with the doc set as `/docs/*` pages.
- Secret scrubbing covers password/secret-style assignments, GitHub
  OAuth/server tokens, all `xox*` Slack prefixes, and `OLLAMA_*`/`GEMINI_*`
  key names; URL query forms keep the `query` marker (no double redact).

## v0.9.0

P0 fix round (matches `Version` in `internal/update/update.go`):

- `/export` brief output (`internal/export/`) with tests.
- Provider error classification (`internal/provider/classify.go`) with tests.
- Policy tool-call validation (`internal/policy/`) with tests.
- Resume offer in slash commands (`internal/tui/slash.go`).
- Versioned self-update: release tags replace SHAs in notices, `--version` flag.
