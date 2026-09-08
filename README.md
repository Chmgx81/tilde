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

**A terminal-native coding agent.** Describe a goal in plain language — tilde
reads your codebase, edits files, runs commands in a sandbox, and reports back
with the diff.

No telemetry, no tracking, no accounts. The only network connection tilde ever
opens is the model endpoint you configured.

| Setup | Model runs | Sandbox network | Your code leaves the machine? |
|---|---|---|---|
| Default (Ollama) | localhost | Denied | **Never** |
| `--provider openai` | Your endpoint / API | Denied | Only to the endpoint you configured |

## Why tilde

- **Security-first.** A policy engine gates every tool call; shell commands run
  inside a bubblewrap jail with a disposable filesystem and no network. Deny
  tiers beat `--yes`, beat Auto mode, beat everything.
- **Local-first.** The default provider is Ollama on localhost — your code and
  your context never leave the machine unless you point it somewhere else.
- **Measured, not demoed.** Repeat-run reliability per task instead of
  single-demo applause: same tasks, repeated trials, fresh directories,
  trajectory scoring. Qwen3.8-4b scores **22/24 ≈ 92%** on the built-in suite.

## Quickstart

Prerequisites: Go 1.25+, Linux with
[bubblewrap](https://github.com/containers/bubblewrap) — shell calls refuse to
run without it.

**1. Build and install** (to `~/.local/bin`; override with `PREFIX=...`):

```sh
./install.sh
```

Or build without installing:

```sh
go build -o tilde .
```

**2. Get a model** — Ollama on localhost, nothing to sign up for:

```sh
ollama serve & ollama pull qwen3.8-4b:16k
```

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

## Interface

### Keys & commands

| Input | Action |
|---|---|
| `Tab` | Cycle Plan → Build → Auto (read-only enforcement is in code, not prose) |
| `/` | Command palette: `/mode /compact /clear /copy /sandbox /diff /undo /sessions /model /skills /help /quit` |
| `@file` | Fuzzy file reference (respects `.gitignore`) |
| `!cmd` | Shell escape — same sandbox and confirm tier as the agent |
| `Ctrl+Y` | Copy the latest assistant response (raw markdown) |
| `Ctrl+C` | Cancel the turn (again to quit) |
| `Esc Esc` | Cancel the running turn |
| `Ctrl+J` | Newline inside the composer |
| `↑ ↓` | Prompt history; picks up the transcript when nothing to recall |
| `PgUp/PgDn` · `Ctrl+U/D` | Scroll the transcript by half a screen |
| `Home/End` | Jump to top of history / back to live |

### Mouse & clipboard

| Gesture | Action |
|---|---|
| Wheel / two-finger scroll | Scroll the transcript — even mid-drag or with a picker open |
| Left-drag on the transcript | Select rows and release to copy: inverse-video highlight, transcript-absolute anchors (hold at a screen edge to autoscroll), plain-text copy with a toast. A bare click copies nothing |
| `Shift` + drag | Bypass to the terminal's own selection |
| `Alt+M` | Pause mouse tracking entirely for native terminal selection (rectangles, terminal copy chords); the hint bar shows the paused state until re-armed |

Clipboard writes try `xclip`/`xsel` first and fall back to **OSC 52** — the
terminal sets its own clipboard, so copying works on bare Wayland (kitty,
WezTerm, alacritty), over SSH, and in tmux with `set -g set-clipboard on`,
with nothing to install. Collapsed pastes copy expanded: a
`[Pasted text #N …]` token in the transcript ships the full original body to
the clipboard, while the screen keeps the compact token. `/copy [n]` copies
the whole transcript (or one line) the same way.

### Headless

```sh
tilde --prompt "Fix the failing test" --mode build --yes
tilde --prompt "..." --output json   # JSONL events + result object with token usage
```

### Evaluation

```sh
tilde --eval [--eval-task write-two-files,rename-chain] [--trials 3]
```

Same tasks, repeated trials, fresh directories, trajectory scoring. Exits 1
if any task scores zero.

## Safety model

- **Three tiers** — `deny / ask / allow`. Deny always wins. `policies.yaml`
  loads at startup from the project root and fails closed: a broken file
  refuses to start the session.
- **Structural parsing.** Destructive shell shapes are recognized per pipeline
  segment, never substring-matched.
- **Real isolation.** Every shell call runs under bwrap with a disposable root
  and no network by default.
- **Explicit opt-in.** Project MCP servers and hooks do nothing until you ask
  for them (`--mcp-project`, `--hooks-project`).
- **Reversible.** Every mutating step snapshots for `/undo`.
- **Untrusted by default.** All tool output is fenced as data, never
  instructions. Package installs (`pip`, `npm`, `go get`, …) stay Ask-tier
  with an explicit unverified-package-name warning — approve only names you
  have checked (models hallucinate plausible package names that attackers
  pre-register; with default-deny egress the fetch fails closed anyway).

**Honest scope:** Linux only (sandboxing is OS-level work and bwrap is the
mechanism); no clipboard capture, no cloud runs, no multi-session tabs — the
terminal already does those jobs.

## Configuration

| File | Scope | Notes |
|---|---|---|
| `policies.yaml` | project root | Live tiers; deny beats `--yes` and Auto |
| `.tilde/skills/*.md` | project | Progressive disclosure (one-liners in prompt, bodies on load) |
| `~/.tilde/skills/*.md` | user | Same, personal |
| `.tilde/hooks.yaml` | project | Needs `--hooks-project`; `before` blocks, `after` observes |
| `~/.tilde/hooks.yaml` | user | Always on |
| `.tilde/mcp.json` | project | Needs `--mcp-project`; only tool *names* enter prompts |
| `~/.tilde/mcp.json` | user | Auto-starts |

| Env var | Meaning |
|---|---|
| `TILDE_MODEL` / `OLLAMA_HOST` | Model + Ollama endpoint |
| `TILDE_BUDGET` | Token budget before auto-compaction (default 32000) |
| `TILDE_NO_SANDBOX=1` | Disable bwrap (loud warning, not recommended) |
| `TILDE_ALLOW_NET=1` | Lift the sandbox network ban (policy still judges commands) |
| `TILDE_MCP_PROJECT=1` / `TILDE_HOOKS_PROJECT=1` | Opt into project MCP / hooks |
| `TILDE_PASTE_LINES` | Large-paste collapse threshold in lines (default 4, `0` disables) |
| `TILDE_ARROWS=scroll` | Arrows scroll the transcript instead of recalling history |
| `OPENAI_API_KEY` / `OPENAI_BASE_URL` | `--provider openai` credentials |
| `ANTHROPIC_API_KEY` | `--provider anthropic` credentials (native Messages API; needs live-key verification) |

## Providers

- `--provider ollama` (default) — localhost, private by construction.
- `--provider openai` — OpenAI-compatible, bring your own key and base URL;
  wire format verified against Ollama's `/v1`.
- `--provider anthropic` — native Messages API (`ANTHROPIC_API_KEY`); wire
  format httptest-verified, awaiting a live-key run.

## Architecture

```text
main.go                  flags, wiring, headless + eval runners
internal/agent/          ReAct loop, modes gate, subagents, compaction hooks
internal/tools/          read/edit/write/shell/grep/glob/git + registry,
                         repair layer, undo, sandbox tasks, containment
internal/sandbox/        bwrap isolation (fs + net, PID namespace)
internal/policy/         deny/ask/allow + destructive-command parser
internal/mode/           Plan/Build/Auto gate
internal/provider/       ollama + openai + anthropic backends, usage accounting
internal/compact/        80% auto-compaction with model summaries
internal/session/        append-only JSONL transcripts, resume
internal/skills/         progressive-disclosure loader + frontmatter lint
internal/mcp/            stdio client, lazy gateway (names in prompt)
internal/hooks/          before/after tool scripts
internal/repair/         tool-call mistake repair (nulls, shapes, coercions)
internal/eval/           trajectory suite: tasks × trials + cost columns
internal/tui/            Bubble Tea UI (spec: docs/tui-design-spec.md)
docs/                    Plan.md · tui-design-spec.md · agents-survey-2026.md
examples/                sample skill, hooks.yaml, mcp.json
```

Design rules for changing anything: every tool result names its own recovery
(never silence, never bare errors); fail loud, cheap, and closed; repair the
model's mistakes instead of punishing them; every token must justify itself.
Full checklist in `docs/Plan.md §1`.

## Development

```sh
go build ./... && go vet ./... && gofmt -l .   # must all be silent
go test -count=1 ./...                          # full suite, fresh
go test -race ./internal/agent/ ./internal/tools/
./tilde --eval --trials 3                        # consistency number
```
