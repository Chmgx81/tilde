# tilde (~)

```
     ▄▄▄▄▄▄▄
  ▄▄█▀▀▀▀▀▀▀█▄
  ▀▀         ▀███▄         ▄
                 ▀█▄▄▄▄▄▄▄█▀
                   ▀▀▀▀▀▀▀
```

![Go 1.25](https://img.shields.io/badge/go-1.25-00ADD8?style=flat-square)
![Linux](https://img.shields.io/badge/platform-linux-E6E6E6?style=flat-square)
![Sandboxed](https://img.shields.io/badge/sandbox-bubblewrap-4CAF50?style=flat-square)

**A terminal coding agent.** Describe a goal in plain language — tilde reads
your code, edits files, runs commands in a sandbox, and shows you the diff.

No telemetry, no accounts, no tracking. The only network connection tilde
opens is the model endpoint you chose.

| Setup | Model runs | Sandbox network | Your code leaves the machine? |
|---|---|---|---|
| Default (Ollama) | localhost | Denied | **Never** |
| Any cloud provider | The endpoint you configured | Denied | Only to that endpoint |

## Why tilde

- **Security-first.** Every tool call passes a policy engine. Shell runs in a
  bubblewrap jail: disposable filesystem, no network. Deny beats everything.
- **Local-first.** The default is Ollama on localhost. Nothing leaves your
  machine unless you point tilde at a cloud endpoint.
- **Measured.** Same tasks, repeated trials, fresh directories, trajectory
  scoring — not demo applause. Qwen3.8-4b scores **22/24 ≈ 92%** built in.

## Quickstart

Needs: Go 1.25+, Linux with
[bubblewrap](https://github.com/containers/bubblewrap). Shell calls refuse to
run without it.

**1. Install** (to `~/.local/bin`; override with `PREFIX=...`):

```sh
./install.sh
```

Or just build it:

```sh
go build -o tilde .
```

**2. Get a model** — Ollama on localhost, nothing to sign up for:

```sh
ollama serve & ollama pull qwen3.8-4b:16k
```

No GPU? Skip to [Providers](#providers) — cloud keys and free tiers work too.

**3. Run it in your repo** — starts in read-only Plan mode:

```sh
cd ~/my-project && tilde
```

```text
→ Add a test for the empty-config case
● Listed             . 20 files, 12 directories
● Read               internal/config/config.go
● Edit               internal/config/config.go
  ⎿ +12 -3
● Run                go test ./internal/config/
  ⎿ ok    tilde/internal/config   0.412s
✓ Done
```

## Updating

tilde tells you when it goes stale. Once a day, interactive starts
compare your build against `main` on GitHub (one anonymous API read —
no identity or telemetry leaves the machine) and cache the answer. When
an update exists, the welcome panel gains a dim line:

```text
update available (v0.8.0 → v0.9.0) — run tilde update
```

```sh
tilde update   # pull --ff-only, rebuild, smoke-test, reinstall, restart
```

Fail-closed like everything else: a dirty source tree refuses (commit or
stash first — nothing is stashed or reset for you), and a failed build
or smoke test never touches your installed binary (atomic rename, never
a partial overwrite). Headless runs never check; `TILDE_NO_UPDATE_CHECK=1`
opts out entirely. No install record (`~/.tilde/install.json`, written by
`install.sh`) means `tilde update` can't find its source — reinstall from
a fresh clone instead.

## Interface

### Keys & commands

| Input | Action |
|---|---|
| `Tab` | Cycle Plan → Build → Auto |
| `/` | Palette: `/mode /compact /clear /copy /sandbox /diff /undo /sessions /export /model /login /logout /skills /help /quit` |
| `@file` | Fuzzy file reference (respects `.gitignore`) |
| `!cmd` | Shell escape — same sandbox and confirm tier as the agent |
| `Ctrl+Y` | Copy the latest assistant response (raw markdown) |
| `Ctrl+C` | Quit when idle (points to `Esc Esc` during running turns) |
| `Esc Esc` | Cancel the running turn |
| `Ctrl+J` | Newline inside the composer |
| `↑ ↓` | Prompt history |
| `PgUp/PgDn` · `Ctrl+U/D` | Scroll half a screen |
| `Home/End` | Top of history / back to live |

### Mouse & clipboard

| Gesture | Action |
|---|---|
| Wheel / two-finger scroll | Scroll the transcript — even mid-drag or with a picker open |
| Left-drag on the transcript | Select rows, release to copy. Edge autoscroll for long selections. A bare click copies nothing |
| `Shift` + drag | The terminal's own selection |
| `Alt+M` | Pause mouse tracking for native selection; hint bar shows the state |

Copying works with nothing to install: `xclip`/`xsel` first, then **OSC 52**
(bare Wayland, SSH, tmux with `set -g set-clipboard on`). Collapsed pastes
copy expanded — a `[Pasted text #N …]` token ships the full body to the
clipboard while the screen stays compact. `/copy [n]` copies the transcript
the same way.

### Headless

```sh
tilde --prompt "Fix the failing test" --mode build --yes
tilde --prompt "..." --output json   # JSONL events + result object with token usage
```

### Scheduled runs, audit, plugins

```sh
tilde run-due [--yes] [dir]   # run due .tilde/schedule.yaml entries headlessly (cron/systemd wakes it)
tilde audit [--since 24h] [--tool shell_command] [--json]   # read the governance trail
tilde plugin install <dir> [--upgrade] | verify <name> | list   # hash-pinned local plugins
tilde models [provider]   # print the model catalog (windows + prices), no network
tilde ide-bridge          # serve line-delimited JSON over stdio for IDE hosts
tilde prune --sessions 30d --audit 90d --yes   # enforce retention (dry run without --yes)
tilde fork <id> [--at RFC3339]   # branch a session at an earlier point (or tip)
tilde --export <id> [--out path.md]   # write the portable brief headlessly (cwd-contained)
```

No daemon lives in tilde: the OS owns waking, tilde owns deciding
what is due. Failed schedule jobs are not marked and retry next tick.

### Evaluation

```sh
tilde --eval [--eval-task write-two-files,rename-chain] [--trials 3]
```

Same tasks, repeated trials, fresh directories. Exits 1 if any task scores zero.

## Safety model

- **Three tiers** — `deny / ask / allow`. Deny always wins. A broken
  `policies.yaml` refuses to start the session (fail closed).
- **Structural parsing.** Destructive shell shapes are matched per pipeline
  segment, never substring-matched.
- **Real isolation.** Every shell call runs under bwrap: disposable root,
  no network by default.
- **Explicit opt-in.** Project MCP servers, hooks, and skills stay off until you pass
  `--mcp-project` / `--hooks-project` / `--skills-project` (or `tilde trust` the folder).
- **Scrubbed.** Shell/file/search output is secret-scrubbed before the model sees it; sensitive paths warn.
- **Reversible.** Every mutating step snapshots for `/undo`.
- **Untrusted by default.** Tool output is data, never instructions. Package
  installs stay Ask-tier with an unverified-name warning — approve only names
  you checked (models hallucinate packages attackers pre-register; with
  default-deny egress the fetch fails closed anyway).
- **SSRF-guarded.** `web_fetch` and remote MCP hosts refuse private,
  loopback, and link-local targets; redirects are re-checked, userinfo URLs refused.
- **User-authoritative MCP.** A project `mcp.json` can only add new servers
  or tighten approval — never rewire or auto-approve your user servers.
- **Minimal hook env.** Hooks get `PATH/HOME/USER/SHELL/LANG/PWD` plus tool
  args only; `*_KEY/*_TOKEN/*_SECRET/*_PASSWORD` never pass through, output capped at 32KB and scrubbed.
- **Signed updates.** `tilde update` verifies the release tag signature
  before pulling; unsigned fails closed, nothing is pulled or built.
- **Scrubbed at rest.** Session logs and the audit trail are secret-scrubbed
  on write (mode `0600`); the audit stores arg hashes, never raw args.

| Tier + Plan gate | Tools |
|---|---|
| Ask, Plan-blocked (mutating) | `write_file` `edit_file` `shell_command` `git_worktree_add/remove` `mcp_call` `spawn_work` `discard_work` `memory` (save/forget) `remember` (index) |
| Ask, Plan-OK (read-only) | `todo_write` `ask_user` `web_fetch` `web_search` `web_shot` `apply_work` `memory` (recall only) `remember` (recall/status) `shell_poll` status/log |
| Allow, Plan-OK | `read_file` `grep` `glob` `git_status` `git_diff` `git_worktree_list` `load_skill` `mcp_list` `spawn_explore` `symbol_search` `diagnose` |

Plan blocks mutating tools at the registry layer, not the prompt.
`spawn_work` opens one worktree session; writer children may
`write_file`/`edit_file` inside their worktree only; `apply_work` /
`discard_work` close it. `web_fetch` / `web_search` need
`TILDE_ALLOW_NET=1` or a `policies.yaml` `allow_net` host. `memory`
recall reads free; `save` / `forget` are Plan-blocked like other writes.
`remember` recall/status read free; `index` rebuilds the vector store.
`web_shot` captures viewport PNGs for human review (the model cannot
see images yet — no vision pipeline).

**Honest scope:** Linux only (sandboxing is OS-level; bwrap is the mechanism).
No clipboard capture, no cloud runs, no multi-session tabs — the terminal
already does those jobs.

## Configuration

| File | Scope | Notes |
|---|---|---|
| `policies.yaml` | project root | Live tiers; deny beats `--yes` and Auto; `allow_net` lists hosts `web_fetch` may reach without `TILDE_ALLOW_NET=1`; `deny_paths` denies file-tool paths by glob before tiers |
| `.tilde/skills/*.md` | project | Needs `--skills-project`; progressive disclosure (one-liners in prompt, bodies on load) |
| `AGENTS.md` / `CLAUDE.md` / `.tilde/RULES.md` | project | First existing wins; auto-loaded into the prompt under the project-skills trust gate; 8KB cap |
| `~/.tilde/skills/*.md` | user | Same, personal |
| `.tilde/hooks.yaml` | project | Needs `--hooks-project`; `before` blocks, `after` observes |
| `~/.tilde/hooks.yaml` | user | Always on |
| `.tilde/mcp.json` | project | Needs `--mcp-project`; only tool *names* enter prompts |
| `~/.tilde/mcp.json` | user | Auto-starts |

| Env var | Meaning |
|---|---|
| `TILDE_MODEL` / `OLLAMA_HOST` | Model + Ollama endpoint |
| `TILDE_BUDGET` | Token budget before auto-compaction (default 32000, or model window when known) |
| `TILDE_NO_SANDBOX=1` | Disable bwrap (loud warning, not recommended) |
| `TILDE_BACKEND=podman` | Run shell calls in an ephemeral digest-pinned podman container instead of bwrap (needs `TILDE_SANDBOX_IMAGE` with `@sha256:` digest) |
| `TILDE_SANDBOX_IMAGE` | Container image for the podman backend (digest-pin required, e.g. `img@sha256:<64 hex>`) |
| `TILDE_ALLOW_NET=1` | Lift the sandbox network ban (policy still judges commands; required for `web_fetch`) |
| `TILDE_MCP_PROJECT=1` / `TILDE_HOOKS_PROJECT=1` / `TILDE_SKILLS_PROJECT=1` | Opt into project MCP / hooks / skills |
| `TILDE_PASTE_LINES` | Large-paste collapse threshold in lines (default 4, `0` disables) |
| `TILDE_ARROWS=scroll` | Arrows scroll the transcript instead of recalling history |
| `TILDE_NO_UPDATE_CHECK=1` | Disable the daily update check and splash notice |
| `OPENAI_API_KEY` / `OPENAI_BASE_URL` | `--provider openai` credentials |
| `ANTHROPIC_API_KEY` | `--provider anthropic` (native API; needs live-key verification) |
| `OPENROUTER_API_KEY` | `--provider openrouter` (free `:free` models included) |
| `GEMINI_API_KEY` / `GOOGLE_API_KEY` | `--provider gemini` (either name works; free tier via AI Studio) |
| `OPENCODE_API_KEY` | `--provider opencode` (Zen dashboard key; curated coding models) |

Trust a folder once — `tilde trust [dir]` (`tilde untrust [dir]` to revoke) — instead of per-run project flags.

## Providers

Six backends, one shape. No GPU is fine — cloud keys are first-class, and three
providers cost nothing to start.

- `--provider ollama` (default) — localhost, private by construction.
- `--provider openai` — OpenAI-compatible; bring a key and base URL.
- `--provider anthropic` — native Messages API; httptest-verified, awaiting a
  live-key run.
- `--provider openrouter` — one key, many vendors, plus a **free shelf**
  (`:free` models, $0 per token — account and key still required).
- `--provider gemini` — Google via its OpenAI-compatible endpoint; AI Studio
  serves these on a **free tier**, so a Google account is enough to start.
- `--provider opencode` — OpenCode Zen: models the OpenCode team tested and
  benchmarked for coding agents, behind one key (`OPENCODE_API_KEY`).
  Ships coding-first picks plus a logged free-trial row.

Keys resolve down a ladder — no silent fallbacks, a stored key owns its
provider:

1. `--api-key` flag (this process only, wins outright)
2. Stored credential — `/login <provider>` asks (masked, never echoed or
   logged), validates with a one-token request, saves to
   `~/.tilde/credentials.json` (mode `0600`); `/logout` removes it
3. Env var from the table above

Bare `/login` shows every provider, its source, and its key tail. Switching
mid-session keeps transcript and session: `/model <provider/model>` (e.g.
`/model gemini/gemini-2.5-flash`); bare `/model` lists the catalog with
context windows and prices. A catalog pick adopts the model's window as the
budget — unless `--budget` / `TILDE_BUDGET` was set, which is never
second-guessed.

## Architecture

```text
main.go                  flags, wiring, headless + eval runners
internal/agent/          ReAct loop, modes gate, explore + work subagents, compaction hooks
internal/tools/          read/edit/write/shell/grep/glob/git + registry,
                         todo/ask/web_fetch/web_search/web_shot/symbol_search/memory/remember/diagnose, scrubbed output, undo, containment
internal/trust/          explicit project trust, no auto-trust
internal/sandbox/        bwrap isolation (fs + net, PID namespace); podman backend via TILDE_BACKEND=podman
internal/policy/         deny/ask/allow + destructive-command parser, allow_net hosts
internal/mode/           Plan/Build/Auto gate
internal/provider/       6 backends + registry, credential ladder, model catalog, streaming (ollama/openai) with fallback
internal/compact/        80% auto-compaction with model summaries
internal/session/        append-only JSONL transcripts, scrubbed at rest, resume
internal/creds/          credential store, sealed AES-GCM envelope
internal/export/         distilled /export briefs
internal/audit/          append-only tool decision trail (hashes, never raw args)
internal/skills/         progressive-disclosure loader + frontmatter lint
internal/plugin/         hash-pinned local plugin installs (manifest v1 + lockfile)
internal/schedule/       due-checker for .tilde/schedule.yaml (no daemon; run-due reexecs headless)
internal/rules/          project rules auto-load (AGENTS.md/CLAUDE.md/.tilde/RULES.md, trust-gated)
internal/vec/            vector memory engine (TF-IDF default, Ollama embeddings optional)
internal/mcp/            stdio client, lazy gateway (names in prompt)
internal/hooks/          before/after tool scripts, session start/end
internal/ide/            stdio JSON bridge for IDE hosts (initialize/health/session.chat/history)
internal/repair/         tool-call mistake repair (nulls, shapes, coercions)
internal/eval/           trajectory suite: tasks × trials + cost columns
internal/update/         versioned self-update, signed-tag verification
internal/tui/            Bubble Tea UI (spec: docs/tui-design-spec.md)
docs/                    Plan.md · tui-design-spec.md · agents-survey-2026.md
examples/                sample skill, hooks.yaml, mcp.json
```

Module map: `docs/ARCHITECTURE.md` (layout only; behavior lives in
`docs/Plan.md`).

Changing anything? Every tool result names its recovery (never silence, never
bare errors); fail loud, cheap, and closed; repair the model's mistakes
instead of punishing them; every token must justify itself. Full checklist in
`docs/Plan.md §1`.

## Development

```sh
go build ./... && go vet ./... && gofmt -l .   # must all be silent
go test -count=1 ./...                          # full suite, fresh
go test -race ./internal/agent/ ./internal/tools/
./tilde --eval --trials 3                        # consistency number
```

CI (`.github/workflows/ci.yml`) runs vet + tests + build on push/PR.
