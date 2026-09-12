# Changelog

Release notes are grouped by version and describe shipped behavior. For
planned work and implementation status, see [docs/Plan.md](docs/Plan.md).

## v0.10.0 (unreleased)

- **Security hardening — slopsquatting defense**: a new
  `internal/slopsquatting/slopsquatting.go` package detects package-name
  hallucinations (e.g. `langchin` for `langchain`) in install commands.
  Flagged names trigger a prominent `SLOPSQUATTING WARNING` in the confirm
  prompt naming the probable real package. `TILDE_STRICT_INSTALL=1` makes
  all install commands require explicit approval even in Auto mode with
  `--yes`. See `docs/SLOPSQUATTING.md`.
- **Slopsquatting hardening**: detection and name extraction now share one
  manager table, closing the evasions that produced only a generic (or no)
  warning: `pip install x==1.2.3` / `~=` / `<` / `[extra]`, `python -m pip`
  and versioned interpreters (`python3.11`, `pip3.11`), transparent
  wrappers (`env`/`nice`/`timeout`/`command`), `npm ci`, `yarn install`,
  `composer`, system managers, combined `pacman -Syu`, backslash-newline
  continuations, scoped+versioned npm names, and flag values
  (`-r FILE`, `--index-url URL`) read as packages. Typo detection is
  separator/case-insensitive and length-aware (short names need distance
  1); a wholly known install reads as `known package(s)`.
- **Deny-tier bypass fixes**: command substitution inside a `VAR=value`
  prefix (`X=$(reboot) true`) is judged before the prefix is stripped;
  `xargs` no longer launders a denied verb or interpreter payload
  (`find . | xargs sh -c 'rm -rf /'`); fd-prefixed device redirects
  (`2>`, `&>`) are normalized before the `/dev`/`/proc`/`/sys` check.
- **Deny-tier second pass**: nested `xargs` recurses instead of failing
  open; process substitution (`<(…)`/`>(…)`) is treated as substitution;
  git's leading global options are skipped so `git --no-pager reset
  --hard` still denies; the `tee /absolute/path` sink only fires when
  `tee` is the command word (no more false Deny on `grep … tee /path`),
  and a bare `[` test is no longer read as obfuscation.
- `TOOL_TIMEOUT` is detected from the tool's wrapped context deadline, so
  a network tool that wraps its error with `%v` still reports the
  distinct, retryable timeout instead of a generic failure.
- `--export` writes via temp-file + rename, replacing a pre-planted
  final-component symlink instead of following it.
- The read spill is cached per file version (paging a large file spills
  it once), `internal/spill` refuses payloads over 16 MiB, and the
  `deny_paths` argument-vs-content limitation is documented.
- **Path-scoped deny fix**: `deny_paths` now matches the root-relative and
  symlink-resolved spelling of an absolute contained path, so `*.key` can
  no longer be dodged by writing `<root>/id.key`.
- **Network tools are builtin ask-tier**: `web_fetch`/`web_search`/
  `web_shot` prompt even when a project ships no `policies.yaml`.
- **Export containment fix**: `--export --out` resolves symlinks in the
  existing prefix, so a symlinked parent cannot redirect a brief outside
  the cwd.
- **Per-tool cooperative timeout**: dispatch probes an optional
  `Timeout()` and cancels a hung call with a distinct `TOOL_TIMEOUT`
  result instead of running to the iteration cap.
- **Repeat-tool guard**: the doom fingerprint canonicalizes args through
  JSON, so nested objects/arrays can no longer defeat repeat detection;
  the nudge/handoff messages carry a capped argument preview.
- **Pairing-safe compaction**: the kept-recent boundary walks past a
  leading tool-result run, so a retained result never loses its
  originating call.
- **Tool-output spill**: oversized results are written (scrubbed) to a 0600
  file under `~/.tilde/spill` and named inline — capped shell output, a
  read that hits the byte/line window (the full file is spilled), and a
  capped grep match list. `tilde prune --spill <age>` ages them out.
- **Verify gate**: a new `internal/verify` leaf resolves one authoritative
  verification command ($TILDE_VERIFY_CMD, then `.tilde/verify.yaml`
  `command:`, then project markers — go.mod, Cargo.toml, pytest,
  package.json test script, Makefile). A run that changed files and then
  tries to finish without running it gets a bounded reminder
  (`TILDE_VERIFY=warn`, the default; `strict` refuses to finish; `off`
  disables). The command is model-run through `shell_command`, so the
  sandbox and policy tiers still apply.
- **Secret scrubbing enhancement**: `internal/scrub/scrub.go` adds patterns
  for Vercel tokens (`vcp_` prefix), URL credentials (`user:pass@host`),
  and the GCP service-account `private_key` JSON field (redacted in place,
  so the rest of the file stays readable). All tool output, session logs,
  and audit trails benefit automatically.
- **SSRF hardening (DNS rebinding)**: `web_fetch` and `web_search` now
  connect through a transport whose dialer resolves the host once, refuses
  un-routable addresses, and dials the exact validated address. Previously
  the URL was checked and then the HTTP client resolved it again — a
  rebinding attacker could pass the check and connect to
  loopback/link-local/private space. The private-range guard is now one
  shared implementation (`internal/tools/netsafe.go`). These guarded
  fetchers deliberately do not use an HTTP proxy, which would move
  resolution outside the guard.
- `web_shot`'s redirect probe now uses the same hardened transport. It was
  still building its client from the default transport, so the third
  outbound-HTTP tool kept the unpinned-dialer gap the other two had fixed.
  A test pins the transport on every outbound client so a new one cannot
  reintroduce it silently. Firefox's own fetch remains the documented
  residual that keeps `web_shot` ask-tier.
- `tilde login [provider|service]` / `tilde logout [provider|service]`:
  headless credential management against the sealed store (the TUI
  `/login` is the interactive equivalent). The key is read from the
  target's env var or stdin — never argv, so it stays out of `ps` and
  shell history; a bare `tilde login` prints the masked
  credential-ladder status.
- **Ollama Cloud is first-class**: `ollama` is now an optional-key
  backend — the local daemon still runs keyless, and `tilde login ollama`
  / `/login ollama` store a Cloud key the credential ladder resolves
  (store, then `$OLLAMA_API_KEY`). `tilde doctor` reports Ollama
  readiness and warns when `$OLLAMA_HOST` is remote with no key. A
  generic service-token table is the seam for future non-model keys.
- Removed the unreleased `tilde deploy`/Vercel command (target, dispatch,
  tests): it had no role in an agent CLI. The `vcp_` scrub pattern stays.
- An unknown subcommand now fails loud with the command list (exit 2)
  instead of silently falling through to the interactive TUI.
- Clarified `--yes` semantics: in a headless run it auto-approves only the
  read-only allowlist, so mutating and network confirm-tier calls need a
  `y` on stdin. The flag help, `docs/SECURITY.md`, the TUI spec, and the
  Plan's Phase 0 note now say so (that note previously implied `--yes`
  alone wrote files, which it does not).
- `tilde doctor [--json]`: one read-only health report covering the sandbox
  backstop, the policy file, credential availability per provider, session
  and audit dir writability, git, provider construction, and the network
  opt-in. Exit 2 when a hard check fails, so a script can gate on it; the
  human form pairs a glyph with an explicit word so it reads in monochrome.
- Fixed a path-traversal hole in `tilde fork <id>`: the session id was
  joined into a path without validation, so `tilde fork ../../secret` read
  an arbitrary `*.jsonl` from outside the sessions directory and copied it
  into a new branch. Ids are now validated at the `session.Fork` entry
  point (`session.ValidID`), protecting every caller — and the guard is now
  one implementation shared by `fork`, `--export`, and the TUI `/export`
  (three copies before). Regression test included.
- Fixed a nil-pointer panic in `internal/hooks`: when the sandbox wrapper
  refused to build (missing bwrap, an invalid backend, or `/` as the project
  root), `runHook` dereferenced the nil `*exec.Cmd` before checking the
  error. Hooks now surface the refusal as a normal error/note. Regression
  test included (`TestSandboxBuildFailureDoesNotPanic`).
- Input bounds: a NUL byte in any file-tool path is refused with a clear
  message at the shared `contain()` choke point, and `shell_command` refuses a
  command over 64 KiB (far above any real command) naming the fix.
- Status bar shows running background work (`⚙ N bg`, `accent-auto`) while
  detached shell tasks are live, read in-memory; absent at zero so the
  default bar is unchanged. Documented in tui-design-spec §2.2, including its
  place in the narrow-terminal drop order.
- New docs: `docs/TESTING.md` (suite shape, conventions, security-testing
  rules) and `docs/CONTRIBUTING.md` (invariants, house style, change
  checklist), both added to the documentation index. `docs/Plan.md`'s status
  table now covers this session's work.
- Test coverage: a PTY smoke test (`pty_smoke_test.go`) runs the real binary
  under a pseudo-terminal and asserts alt-screen entry, live resize
  (TIOCSWINSZ → SIGWINCH re-render), a clean Ctrl+C exit, alt-screen exit,
  and cursor restore — the terminal-cleanup contract the rendered tests
  cannot observe. Linux-only, skipped under `-short`.
- Maintainability refactor (no behavior change): the `main` composition root
  is split (`main.go` flag parsing/dispatch + `harness.go` registry/gates
  wiring), `tools/undo.go` snapshot args go through the shared `optStr`
  helper, and `policy` arg normalization is centralized in `argOp`. CLI
  flags, tool schemas, exit codes, and output shapes are unchanged.
- Plan mode no longer dumps artifacts into chat: the Plan prompt requires a
  short numbered outline (full detail lives in the `save_plan` file), and
  `todo_write add` revises instead of duplicating identical open text.
- Plan hardening round 2: compact outline budget (3-5 short sections, at
  most 3 file paths), same-batch identical tool calls dispatch once and
  share the result with a dedup marker, and mode-gate denials state
  explicitly that nothing was changed.
- Website redesigned to the product's own design language: the TUI spec
  token palette (amber/cyan/semantic glyphs on the blue-gray `#0D1117`
  surface), self-hosted Newsreader/Inter/JetBrains Mono, spec-glyph
  workflow demos, a deny/ask/allow permission-ladder section, and a
  WCAG-AA-verified dark docs theme. The transparent banner set is now the
  site mark: nav banner in the header (~140px desktop, ~120px mobile),
  hero banner above the landing headline (~480px desktop, ~416px tablet,
  up to 320px mobile), and the 32px PNG as favicon (48px for
  apple-touch-icon,   hero banner for og/twitter images).
- Website UX pass: wider editorial container (~1216px), fluid section
  rhythm, landing header with Product/Workflow/Models/Docs anchors and a
  quiet Get started action (compact disclosure menu on mobile), a larger
  centered hero (~88vh, 80px serif headline) over a framed live product
  preview, an issue→merge stepper, a coordinator delegation tree, a
  copyable install terminal with a binary/platform/runtime spec strip,
  selectable provider cards (Ollama marked default), and restrained
  hover/reveal micro-interactions, all CSS-only, verified at
  375/768/1440px with no horizontal overflow.
- Website refinement pass: deliberate type system (42–52px section
  headings, 16–17px body, 12px metadata floor), narrow/wide content
  widths, larger hero (640px mark, taller CTAs, 70rem preview with active
  step highlight), bigger ladder rows with hover shift, clearer model
  active state, ringed issue→merge stepper with a mobile connector rail,
  coordinator org-chart (root pill, animated delegation bus, worker
  cards, vertical mobile rail), two-column install specs, and a redesigned
  open final CTA closing on a run terminal, verified in-browser at
  desktop, tablet, and mobile widths.
- Landing page now ends in a native footer (brand + tagline, Product and
  Resources link columns, © line, working back-to-top) in the site's
  terminal styling, no new dependencies. The Starlight footer-hide rule
  is scoped so docs pages keep their own footer.
- Product preview and ladder polish: ringed tier status badges, a
  `working` mode pill and `2 of 9` counter pill echoing the live badge,
  and staged row entrances (plan steps on load, ladder rows on scroll
  into view) gated to `prefers-reduced-motion: no-preference`.
- Provider cards now show real brand marks instead of letter
  placeholders: monochrome community glyphs (Simple Icons, CC0) for
  Ollama, Anthropic, Gemini, and OpenRouter, the OpenAI blossom from
  Simple Icons 13.0.0 (later releases dropped it), and SST's own
  opencode icon reduced to monochrome, all inlined as currentColor
  SVGs with amber hover/active states.
- Site copy drops em dashes across the landing page and the docs
  sources (commas/colons instead; literal TUI strings, ASCII diagrams,
  and code samples untouched), and the generic `live` and
  `Available now` badges are removed.
- Tablet mechanics grid: the third card now closes the row full-width
  instead of sitting as a half-width orphan.
- Hero preview speaks the product's own language: `~` session mark with
  a left-aligned title instead of macOS dots, session density (finished
  and pending steps recede around the active one), and a TUI status
  strip (model, sandbox, session cost). The ask tier is a real inset
  card with breathing room instead of rails borrowing its neighbors'
  dividers.
- Hero demo rebuilt as a faithful TUI transcript after comparing with
  the real terminal: `● Update Todos` with `☑`/`□` states (no invented
  `2 of 9` header or detail sub-lines), the verb-column tool timeline
  with `⎿` result lines and a closing `✓ Done` (no jest-style proof
  bar), and the real split status bar (`plan · ~/payments-api · main`
  with model, cost). Every string now traces to the spec or the code.
- Demo session chrome flattened: nested welcome box, mode banner with
  floating tab label, and bordered composer box replaced with plain
  monospace text flowing in the outer card frame — matching the visual
  density of real terminal CLIs (Cursor, Grok Build, Copilot CLI) and
  eliminating the "AI slop" card-inside-card nesting pattern.
- Website contrast and alignment fixes: hintbar text upgraded from
  `fg-dim` to `fg-faint` for WCAG AA compliance at 0.75rem, status bar
  separator visibility improved, tool result indentation aligned to the
  updated verb-column grid, and status bar gap tightened.
- README banner fix: ASCII art moved outside the `<div align="center">`
  wrapper so GitHub's markdown parser renders the code fence and centered
  heading/badges correctly.
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
