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

**A terminal-native coding agent.** You type a goal — it reads your codebase,
edits files, runs commands, and reports back. Security-first, local-first,
and measured: repeat-run reliability per task instead of single-demo
applause (qwen3.8-4b: **22/24 ≈ 92%** on the built-in trajectory suite).

Where your code goes:

| Setup | Model runs | Network | Your code leaves the machine? |
|---|---|---|---|
| Default (Ollama) | localhost | Denied in sandbox | **Never** |
| `--provider openai` | Your endpoint / API | Provider API only | Only to the endpoint you configured |

No telemetry, no tracking, no accounts. The only network connection tilde
ever opens is to the model endpoint you configured.

## Quickstart

**1. Build** — prerequisites: Go 1.25+, Linux with
[bubblewrap](https://github.com/containers/bubblewrap)
(shell calls refuse to run without it).

```sh
./install.sh
```

This builds and installs to `~/.local/bin` (`PREFIX=... ./install.sh`
to override). Alternative (manual build, no install):

```sh
go build -o tilde .
```

**2. Get a model** — Ollama on localhost, nothing to sign up for:

```sh
ollama serve & ollama pull qwen3.8-4b:16k
```

**3. Run it in your repo** — starts in read-only Plan mode:

```sh
cd ~/my-project && ./tilde
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

## Daily use

| Input | Meaning |
|---|---|
| `Tab` | Cycle Plan → Build → Auto (Plan is read-only, enforced in code, not in prose) |
| `/command` | Palette: `/mode /compact /clear /sandbox /diff /undo /sessions /model /skills /help` |
| `@file` | Fuzzy file reference (respects `.gitignore`) |
| `!cmd` | Run a shell command (same sandbox + confirm tier as the agent) |
| `Ctrl+C` | Cancel the turn (again to quit) · `Ctrl+J` newline · `esc` dismiss |

Headless (CI, scripts):

```sh
tilde --prompt "Fix the failing test" --mode build --yes
tilde --prompt "..." --output json   # JSONL events + result object with token usage
```

Measure, don't demo:

```sh
tilde --eval [--eval-task write-two-files,rename-chain] [--trials 3]
```

Same tasks, repeated trials, fresh dirs, trajectory scoring. Exit 1 if
any task scores zero.

## Safety model

Three tiers — `deny / ask / allow`, deny always wins, `policies.yaml`
is loaded at startup from your project root (fail-closed: a broken
file refuses to start). Destructive shell shapes are parsed per pipeline
segment, never substring-matched. Every shell runs under bwrap with a
disposable root and no network by default. Project MCP servers and
hooks need explicit opt-in. Every mutating step snapshots for `/undo`.
All tool output is fenced as untrusted data. Package installs (`pip`,
`npm`, `go get`, …) stay Ask-tier but carry an explicit
unverified-package-name warning — approve only names you checked
(slopsquatting: models hallucinate plausible names attackers
pre-register). With default-deny egress the fetch fails closed anyway.

Honest scope: Linux only (sandboxing is OS-level work and bwrap is the
mechanism); no clipboard capture, no cloud runs, no multi-session tabs —
the terminal already does those jobs.

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
| `TILDE_ARROWS=scroll` | Arrows scroll the transcript (non-empty Up still recalls, navigating Down still walks); default recalls history |
| `OPENAI_API_KEY` / `OPENAI_BASE_URL` | `--provider openai` credentials |
| `ANTHROPIC_API_KEY` | `--provider anthropic` credentials (native Messages API; needs live-key verification) |

Mouse wheel and touchpad scroll (two fingers) scroll the transcript natively via cell-motion mouse tracking; keyboard bindings are unchanged. Drag with the touchpad (or left mouse button) to select transcript rows — the selection is transcript-absolute (wheel mid-drag keeps it glued to the content; holding the drag at a screen edge autoscrolls), and release copies the selection as plain text with a toast, no setup needed (OSC 52 fallback means it works on bare Wayland/SSH without xclip). Collapsed pastes copy expanded: a `[Pasted text #N …]` token in the transcript carries the full original body into the clipboard, even though the screen shows only the token. `Alt+M` still pauses tracking for the terminal's own selection (rectangles, terminal copy chords); `Ctrl+Y` copies the latest response and `/copy [n]` copies the transcript (or one line).

Providers: `--provider ollama` (default), `--provider openai`
(OpenAI-compatible, BYOK, verified against Ollama's `/v1`), or
`--provider anthropic` (native Messages API, `ANTHROPIC_API_KEY`; wire
format httptest-verified, awaiting a live-key run).

## Layout (for maintainers)

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

Rules for changing anything: every tool result names its own recovery
(never silence, never bare errors); fail loud, cheap, and closed; repair
the model's mistakes instead of punishing them; every token must justify
itself. Full checklist: `docs/Plan.md §1`.

## Development

```sh
go build ./... && go vet ./... && gofmt -l .   # must all be silent
go test -count=1 ./...                          # full suite, fresh
go test -race ./internal/agent/ ./internal/tools/
./tilde --eval --trials 3                       # consistency number
```
