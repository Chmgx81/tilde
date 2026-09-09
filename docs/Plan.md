# tilde (~) — Master Build Plan

> Product scope, architectural decisions, and implementation status.

One file that answers: what are we building, why does it look the way it
does, and what order do we build it in. Everything else (TUI spec, policies,
detailed tool code) hangs off this document. If a decision here conflicts
with an older note, this file wins.

For a quick orientation, see the [documentation index](README.md). Treat `DONE` as a
claim that must remain backed by code and tests; use `TODO` and deferred notes
to distinguish planned work from shipped behavior.

---

## 0. What tilde is, in one paragraph

tilde is a terminal-native coding agent — you type a goal, it reads your
codebase, edits files, runs commands, and reports back — built solo, in Go,
security-first, for real engineering work rather than demos. It runs local
models (Ollama) as the default so it costs near-zero to use, with a Provider
interface so any API model can be swapped in. v0.1's whole job is to prove
one thing: a tight, safe, honest agent loop — not a feature checklist.

---

## 1. Guiding principles (why any of this looks the way it does)

Pulled from a survey of the whole 2026 coding-agent field (Claude Code, Codex
CLI, Command Code, Cursor, Kimi Code, Grok Build, and the rest) and from
Command Code's own public harness-engineering write-ups. The pattern that
shows up everywhere: **the differentiator is the harness, not the model.**
A cheap local model behind a careful harness beats an expensive model behind
a sloppy one. Six rules follow from that, and every phase below answers to
them:

1. **Consistency beats a single good run.** An agent that works 9 times out
   of 10 on retry is worth more than one that nails a demo once. Build for
   repeatability, not applause.
2. **Least privilege, always.** The agent gets the smallest access a task
   needs. Escalating is visible and explicit; nothing is silently trusted.
3. **Every dead end names its own recovery.** A tool result never returns
   silence or a bare error — it says what happened and what to send instead.
   Silence is the single most expensive thing a tool can return, because the
   model can't tell "empty" from "broken" and burns a turn finding out.
4. **Fail loud, fail cheap, fail closed.** A stuck agent should look stuck
   immediately (doom-loop guard → drop to Plan) rather than quietly loop
   until the budget's gone.
5. **Repair the model's mistakes before punishing them.** Open/local models
   make a small, predictable set of tool-call mistakes (null instead of
   omit, a string where an array belongs, a wrapped single arg). Fix these
   at the harness layer — this is the single highest-leverage thing a small
   team can build, because it turns "the model is bad at tool calls" into
   "the model works fine, actually," for free, forever.
6. **Every token spent should have to justify itself.** Context is the
   tightest resource an agent has. A read that returns 3,000 lines when 40
   were needed, or a shell log re-read five times unchanged, is waste — and
   waste compounds every turn it stays in the window. This is also why the
   TUI groups low-signal lookups instead of printing one line per action
   (tui-design-spec.md §2.20) — the same "don't make the reader re-process
   what didn't change" discipline applied to the transcript, not just the
   model's context.

These aren't aspirational. They're the checklist every tool and screen below
gets built against.

---

## 2. Tech stack (locked)

| Decision | Choice | Why (short version) |
|---|---|---|
| Language | **Go** | Bottleneck is I/O (API calls, subprocess, UI), not compute. Goroutines map cleanly onto the agent loop + streaming + concurrent tool calls. Simpler language = more solo hours spent on the harness, not the borrow checker. |
| TUI | **Bubble Tea + Lip Gloss + Glamour** | Best-in-class message-passing TUI stack in any language right now; matches the agent loop's event-driven shape directly. |
| Sandboxing | **bubblewrap (bwrap), OS-level, Linux** | Isolation is a kernel/OS job, not a language job — Go calls the same syscalls Rust would. bwrap + denied network egress by default is a cheap, mature primitive, not a big engineering lift. |
| Diffing | **`internal/tui/diff.go` hand-rolled hunk renderer (no diff dep)** | Inline diff rendering and per-edit revert with zero extra dependencies. |
| Sessions | **JSONL, append-only** | Matches the industry-standard pattern (every major agent does this): replayable, crash-safe, never rewritten in place. |
| Model access | **Provider interface** | Local Ollama as the zero-cost default; any HTTP-based model (Anthropic, OpenAI-compatible, OpenRouter) plugs into the same interface later. |
| Permissions | **`policies.yaml`: deny / ask / allow tiers** | Matches the field's converged pattern (Command Code, Claude Code, etc. all land on some version of this). Deny beats ask beats allow, always. Ask tier also covers `todo_write` / `ask_user` / `web_fetch`. |
| Secrets | **`internal/tools/scrub.go`: Scrub + IsHighRiskPath** | Dispatch redacts secret-shaped output before it re-enters context; `read_file` annotates high-risk paths. |
| Project trust | **`internal/trust` + `tilde trust` / `untrust`** | Project skills stay unloaded until `--skills-project` (or env) or a recorded trust; missing/corrupt store resolves to untrusted. |
| Compaction budget | **`provider.BudgetFor` model-aware auto-size** | Catalog context window when known, else 32000; explicit `--budget` / `TILDE_BUDGET` always wins. |

---

## 3. Architecture, in one picture

```
 user input
     │
     ▼
 ┌─────────────────────────────────────────────┐
 │  MODE GATE  (Plan / Build / Auto — Tab)      │  ← blocks mutating tools
 └───────────────────┬───────────────────────────┘     at the registry level
                      ▼
 ┌─────────────────────────────────────────────┐
 │  AGENT LOOP  (think → act → observe → repeat)│
 │   - context assembly + auto-compaction        │
 │   - tool call → REPAIR LAYER → dispatch       │
 └───────────────────┬───────────────────────────┘
                      ▼
 ┌─────────────────────────────────────────────┐
 │  TOOL REGISTRY: read · edit · write · shell   │
 │      grep · glob · git · todo · ask · fetch   │
 │  each gated by policies.yaml (deny/ask/allow) │
 │  each executes inside the bwrap sandbox       │
 └───────────────────┬───────────────────────────┘
                      ▼
 ┌─────────────────────────────────────────────┐
 │  SESSION LOG (JSONL, append-only)             │
 │  + git snapshot per mutating step (undo)      │
 └─────────────────────────────────────────────┘
                      │
                      ▼
                 TUI renders it
              (see tui-design-spec.md)
```

Everything below is either building one of these boxes, or hardening one
that already exists.

---

## 4. The tool contract (non-negotiable rules for every tool)

These are lessons pulled directly from published harness-engineering work on
read/shell/edit tools across the field, adapted for tilde's own tool set.
Every tool we ship — now or later — has to follow these, because this is
where most of an agent's real-world token cost and reliability actually
lives (not in the model).

**For every tool:**
- Never return silence. Empty result, past-EOF, denied action — always a
  one-line reason plus what to send next.
- Never return a bare error. Wrap it in enough context that the model can
  self-correct without a retry-and-guess cycle.
- Dispatch secret-scrubs every result (`internal/tools/scrub.go`);
  `read_file` annotates high-risk paths instead of silently serving them.
- Repair, don't reject, malformed input. See "the repair layer" below —
  this is the single highest-priority piece of harness work for a project
  running local models, because local/open models make the *same small set*
  of tool-call mistakes over and over.

**For `read_file` specifically (three ceilings, always together):**
- A line window (e.g. 2,000 lines) — catches an ordinary long file.
- A byte cap (e.g. 128 KB) — catches a file with a few enormous lines
  (lockfiles, logs).
- A per-line character clamp — catches one minified line that's small in
  line-count and byte-count individually but eats the whole read alone.
  Miss any one of these three and some shape of file silently burns your
  whole context budget on a single tool call.
- Every truncation names its **exact resume offset** — never make the model
  do pagination arithmetic in reasoning tokens.
- Re-reading an unchanged file (same mtime, same window) returns a short
  stub instead of the content again — but the stub **expires on first use**,
  so a stale pointer can never loop the model forever.

**For `shell_command` specifically:**
- Exit codes are honest: a signal-killed process reports `128+N`
  (SIGKILL → 137), never a fake success.
- Common non-error exits are annotated (`grep` exit 1 → "no matches found,
  not an error") — this alone prevents a huge share of retry loops on
  models trained to treat "nonzero = failure."
- Long-running commands run in the background by default with a task id and
  a log path returned immediately — the agent is never blocked babysitting
  a build.
- Output is fenced as **untrusted data** before it re-enters context — a
  malicious or noisy log line can never be mistaken for an instruction.

**The repair layer (build this early, not last):**
Local models make a small, finite, predictable set of tool-call mistakes:
`null` instead of omitting an optional field, a JSON-stringified array
instead of a real one, a bare value wrapped in `{}` where an array was
expected. Don't reject these calls — catch the validator's own error,
apply the matching repair, retry the parse once, and only fall back to a
model-readable error if nothing fixes it. This one piece of engineering is
reported to have taken an open model from clearly worse than a frontier
model to beating it on real tasks, purely by making its tool calls land.
Given tilde runs local models by default, this is not optional polish —
it's the thing standing between "local models feel unreliable" and
"local models feel fine."

---

## 5. The TUI

Full detail lives in **`tui-design-spec.md`** — this section is just the
map. Screens, in the order a user meets them:

1. **Splash** — safety notice (once), model, sandbox status, mode.
2. **Composer** — one input box, border color = active mode (amber = Plan,
   neutral = Build, cyan = Auto).
3. **Mode toast** — Tab cycles Plan → Build → Auto; auto-demotions
   (confused task → back to Plan) always explain themselves in one line.
4. **Tool-call timeline** — one glyph, one verb, one line per action;
   diffs render inline where they happen, not batched at the end; a run of
   same-kind, low-signal lookups collapses under one parent line instead of
   one line each (spec §2.20).
5. **Confirm / deny prompts** — the literal command, never a paraphrase;
   default answer is always the safe one.
6. **Compaction** — ambient (status bar turns amber past 80% context) plus
   a one-line dim marker in the transcript when it fires. Never a popup.
7. **Handoff panel** — when the doom-loop guard fires: what got stuck, what
   we did about it (back to Plan), reassurance nothing was lost.
8. **Session resume** — pick a past session, resume at its compacted state,
   never the raw pre-compaction log.

Design system underneath all of it: one glyph vocabulary, one color-per-
meaning table, fixed indentation/spacing rules — all spelled out in the spec
file so nothing gets a bespoke look later.

---

## 6. Permissions & safety model

`policies.yaml` has three tiers, checked in this order, deny always wins:

```
deny   → blocked, no exceptions, not even in Auto mode
ask    → interrupts the loop, waits for y/n/always-this-session
allow  → runs without asking
```

Plus two structural guarantees that sit **above** the policy file and can't
be bypassed by it:

- **Plan mode is enforced at the tool-registry layer.** Even a confused
  model calling `write_file` in Plan mode gets a hard deny — this isn't a
  prompt-level instruction, it's a code-level gate.
- **Sandboxing is OS-level, always on, on Linux.** bwrap wraps every shell
  call: filesystem access limited to the project dir, network egress denied
  by default. This holds even if `policies.yaml` is missing or misconfigured
  — the sandbox is the backstop, not the only line of defense.

Rule of thumb for writing new policy rules: match `deny`/`ask` aggressively
(catch env-var tricks, wrapper commands, compound commands), match `allow`
conservatively (an allow rule should only ever say yes to exactly what it
names). Failing toward a prompt is always the safe direction.
Ask tier also gates `todo_write`, `ask_user` (unwired/denied callback reads
as deny), and `web_fetch` — which additionally needs `TILDE_ALLOW_NET=1`,
http(s) only, 30s timeout, 5MB cap.

---

## 7. Roadmap

Each phase has a **Definition of Done** — don't move on until it's true.
This list favors "prove the loop works" before "prove it's safe at scale"
before "make it pleasant," which matches the MVP-scope decision already
made: loop + tools first, security skeleton alongside it (not after it, and
not before it either).

### Phase 0 — Loop + tools (done, verified 2026-09-05)
Build: ReAct loop, file read/edit/write, shell, grep, git status/diff, basic
TUI (composer, timeline). A minimal confirm tier gates destructive ops from
day one — this was never "no guardrails," just "no full policy engine yet."
**Done when:** you can give tilde a real multi-file task in your own repo
and it completes it, unassisted, using only these tools.
Verified: headless `--mode build --yes` wrote two files + shell-verified
them unassisted against local Ollama (qwen2.5-coder:7b). Note: that model
echoes tool calls as JSON-in-content, so the provider has a minimal
content-fallback parser; the full repair layer is still Phase 3 work.

### Phase 1 — Mode system + auto-compaction (done, verified 2026-09-05)
Tab-cycle Plan/Build/Auto, enforced at the registry layer. Auto-compaction
at 80% of a configurable token budget, JSONL log preserved with a visible
compaction marker. **Done when:** a long session never silently overflows
context, and Plan mode is genuinely impossible to escape from the inside.
Verified: `internal/compact` (heuristic estimator erring upward, 80%
trigger, model summary via provider with goal-preserving fallback, last 8
turns kept verbatim). Loop pre-checks every iteration, emits usage +
`● [compacted: N older turns — … · see session log]`, appends the full
summary to the append-only log. Live: `--budget 300` run compacted
mid-session with a correct goal/findings/decisions summary and continued.
Plan-escape proven by test at both layers — loop check and registry gate —
with on-disk assertions that blocked writes create nothing. Budget via
`--budget` / `TILDE_BUDGET` (default 32000, explicit always wins) else the
model catalog window (`provider.BudgetFor`) when known; TUI status bar shows
`model · ctx %` turning amber past 80%.

### Phase 2 — OS-level sandboxing (done, verified 2026-09-05)
bwrap wrapping every shell call on Linux, network egress denied by default,
git worktree isolation alongside it. **Done when:** a malicious or
hallucinated shell command cannot touch anything outside the project
directory or phone home, full stop — verified by trying to break it, not
just by reading the config.
Verified: `internal/sandbox` (fresh tmpfs root, toolchain ro-bound, only
project dir rw, `--unshare-net`; fail-closed when bwrap missing unless
TILDE_NO_SANDBOX=1/`--no-sandbox`). Break attempts: `touch` outside root
lands in throwaway tmpfs (host clean), curl egress fails, env-smuggle write
contained, and a live policy-evasion (curl hidden in probe.sh via
write_file) still failed at the sandbox (curl exit 6, no resolution).
Worktree tools (`git_worktree_list/add/remove`, no --force) round-tripped
with dirty-refusal; both registered, policy-ask-gated, Plan-blocked.

### Phase 3 — Tool contract hardening (done, verified 2026-09-05)
This is where §4 above stops being a description and becomes code across
every existing tool:
- [x] Repair layer for tool-call inputs (highest priority — local models
      need this most): `internal/repair` (null-drop, JSON-string parse,
      single-key unwrap, scalar→array wrap, int/bool coerce) runs on every
      dispatch with a one-line receipt; attempted-JSON that fails to parse
      is left for the validator, never laundered.
- [x] Three read ceilings (line window 2000 / byte cap 128KB / per-line
      clamp 2000 chars) + resume-offset truncation notes + 64MB hard-cap
      refusal naming the sampling fix.
- [x] Unchanged-read dedup with self-expiring cache (mtime+size keyed;
      stub expires on first use; edits break it).
- [x] Read-before-write ledger (any read_file window or grep hit counts
      as seen; new-file creation always allowed; blind overwrite/edit
      refused with the read-first fix).
- [x] Honest shell exit codes (signal death → 128+N via WaitStatus, never
      Go's -1) + benign-exit annotations (grep 1, diff 1).
- [x] Untrusted-output fencing on shell/read/grep output.
- [x] Background shell execution (auto-detach past threshold, explicit
      `background:`, per-call `timeout:` as detach point, BgMax cap;
      `shell_poll` status/log/kill; logs under task dir).
**Done when:** you can throw genuinely malformed model output and hostile
file shapes at every tool and nothing silently corrupts, loops, or lies.
Verified: 12 new tool tests + 9 repair tests green; live — 200KB minified
line clamped with note, blind overwrite refused then allowed after read,
re-read returned the dedup stub, `kill -9` → exit 137 in tests.

### Phase 4 — TUI polish (done, verified 2026-09-05)
Splash screen, slash-command palette (dropdown with fuzzy filter, all rows
real commands), fuzzy `@` file reference (git check-ignore backed, 9-row
cap, bold match highlight), session-resume picker (`/sessions` and
`tilde --resume`; restores compacted state, always opens in Plan), help
overlay, transient mode toasts (Tab + auto-demotion share one shape),
per-mode composer placeholders, `!` shell escape through the same
sandbox/confirm tier as model calls, Ctrl+J newline. Deliberately not
built: multi-question prompts (no trigger yet), `/vim` (no vim
engine — Ctrl+J instead). Spec status table updated to match.
**Done when:** the TUI matches the spec file screen-for-screen
(minus the deferred/blocked items above).
Verified: 14 TUI unit tests (parsers, fuzzy rank/cap, gitignore filter,
compacted-state resume incl. corrupt-line tolerance, cautious-Plan
restore, splash content, blink-proof cursor/dismiss, foreign-log skip);
live non-TTY `--resume` lists sessions; headless regression green.
PTY-verified end to end via tmux (splash, Tab cycle + toast, palette
filter/execute, @ navigate/accept/dismiss, help, ! deny/confirm/run,
/sandbox, /sessions resume, /compact marker, full agent turn with
confirm + file creation, deny path, Ctrl+C cancel + quit). Live testing
caught 4 bugs unit tests missed: picker key routing dropped the updated
model; cursor blink re-ran the filter (cursor snap-back + Esc reopen);
accept read the composer after Reset (always empty); confirm printed
"Run: Run:". All fixed + regression-tested. Resume picker now also
skips foreign log formats sharing ~/.tilde/sessions.

### Phase 5 — Recoverability (done, verified 2026-09-05)
Per-edit undo (exact per-file backups for write/edit — git-independent —
plus `git stash create` snapshots for shell, restored LIFO), `/diff`
(working-tree review) and `/undo [n]` (with count validation, clamping,
and audit logging), stale-read detection (read-state map shared by
read/grep/write/edit; own verified writes refresh it, anything changed
behind the agent's back refuses with re-read-first). Safety rules: failed
calls never pollute the stack; empty stash on a dirty tree refuses rather
than guessing HEAD (guessing wrong wipes uncommitted work); shell-undo
restores tracked files and says untracked effects are not covered.
**Done when:** any single bad edit can be reverted without losing the
rest of the session.
Verified: 17 tool tests (backup/restore, create-delete, LIFO, clamp,
empty/bad-count, discard-on-failure, stash restore, HEAD fallback,
non-repo honesty, dirty-unsnapshotted refusal, 4 stale scenarios,
restore-refreshes-seen) + 5 TUI tests (/undo revert/count/empty,
/diff content/non-repo); live PTY — agent's bad edit → /diff showed it
→ /undo restored exactly that step → file and tree clean, session intact.

### Phase 6 — Extensibility (done, verified 2026-09-05)
Skills loader (progressive disclosure — name/description at startup, full
body loaded on activation) before MCP, because skills are lower-complexity
and prove the "load only what's needed" pattern MCP will also need. MCP
client after that, with lazy tool discovery so connecting a server doesn't
silently eat the context budget.
**Done when:** a project-specific skill changes tilde's behavior without a
restart, and an MCP server's tools appear without bloating every prompt.
Verified: skills — project+user dirs, frontmatter validation with fixes,
project-wins clashes, bad files skipped loudly; activation via /skills
picker (rescan-on-open: mid-session installs appear), load_skill tool, and
--skill preload; prompt carries one-liners only (bodies never leak —
asserted in test). MCP — local stdio and remote HTTP JSON-RPC clients
(initialize/list/call, timeouts,
per-server failure isolation), `mcp.json` user+project merged
(project wins), mcp_list/mcp_call gateway (schemas never enter prompts —
asserted), mcp_call Plan-blocked + ask-gated + fenced; servers run outside
bwrap (documented trust note). Live: real python MCP server listed + called
(HELLO, fenced) through the binary; picker load + no-restart install on
the PTY. Honest note: the default 4B local model reads skill bodies fine
but follows standing instructions weakly — mechanism proven, model is the
ceiling. Project skills also load via recorded `tilde trust`
(missing/corrupt store = untrusted); loader is
`skills.Scan(root, allowProject)`, flag `--skills-project` /
`TILDE_SKILLS_PROJECT`. Marketplace registry is now live: `/plugins` and
`/marketplace` expose one read-only registry across hooks, plugins,
marketplace packages, skills, and MCP servers; local YAML/JSON catalogs are
validated without execution, and installs require confirmation plus a
hash-pinned plugin manifest.

### Phase 7 — Evaluation & consistency (done, first measurement 2026-09-05)
A small trajectory-level eval suite (not just unit tests on harness
behavior) — does tilde solve the *same* task reliably across repeated runs,
not just once. This directly answers guiding principle #1.
**Done when:** you have a number for "how often does tilde succeed on this
task on retry," not just "did it work that one time."
Built: `internal/eval` (5 tasks × N trials, fresh dirs, per-trial timeout
as failure not hang, pass rate + median tool calls + failure reasons +
trajectory digests saved to ~/.tilde/evals/*.json), `tilde --eval`
with --eval-task/--trials filter, exit 1 if any task scores zero.
First number (qwen2.5-coder:7b, 5×3 = 15 trials): **7/15 ≈ 47%** —
write-two-files 3/3, shell-verify 3/3, edit-after-read 1/3, grep-then-edit
0/3, plan-answers-no-touch 0/3. Read: single-shot file/shell work is
consistent; multi-step edit flows and Plan Q&A are flaky on the 4B default
model — failure cluster is identical-failing edit calls retried (the model
ignores the fix named in the error), Plan-mode answer dithering to the
iteration cap, and one Ollama timeout flake. No harness bug was implicated
(the guards fired exactly as designed); the lever is model capability and
task explicitness, and now there is a number to move.
Second number (qwen3.8-4b:16k, native tool calls, same 5×3): **14/15 ≈
93%** — everything 3/3 except grep-then-edit 2/3 (one identical-read
loop). Median tool calls fell 5–8 → 1–4. Re-run on hardened code: **14/15
again** (edit-after-read and grep-then-edit both 3/3 this time; one
shell-verify trial flaked on repeated writes instead of the shell step).
Two-run total 28/30. Model variance trial-to-trial is real — which is
exactly what the suite exists to catch.

### Beyond the roadmap (built on measured need)
Default model is now qwen3.8-4b:16k (native tool calls). OpenAI-compatible
provider (`--provider openai`, BYOK) verified live against Ollama's /v1.
Read-only parallel subagents (`spawn_explore`, depth 1, Plan-locked,
§2.19-style batch rows). Hooks (`.tilde/hooks.yaml` + user file, before
blocks / after observes). Edit got whitespace-tolerant matching (the top
eval failure cluster) with loud disclosure. A PTY-driven review round
closed the loop on spec compliance (colored gutter, diff styling, red
handoff panel, branch status, confirm preemption) and added parallel
read-only tool batches, provider usage accounting, and headless JSON.
New tools `todo_write`, `ask_user` (unwired/denied callback reads as deny),
and `web_fetch` (http(s) only, 30s timeout, 5MB cap, `TILDE_ALLOW_NET`-gated)
registered in `buildHarness`, all ask-tier. Explore children get per-child
`SeenMap`s for Read/Grep/Write/Edit (no shared read cache). Creds store
takes an inter-process `flock`; CI runs in `.github/workflows/ci.yml`, and
`docs/ARCHITECTURE.md` is the module-layout pointer (Plan stays behavior
source of truth).

### Red-team validation (HTB AI Red Teamer curriculum, 8 live drills)
All 8 drills executed against the running binary; every boundary held:
sandbox escape+exfil (contained, exfil dir empty), 7/7 destructive-command
obfuscations denied with proj intact (incl. `;`-chains, env-prefix,
`$()`, `bash -c`, `python -c`, `find -delete`, base64-pipe), MCP project
poisoning (zero servers start without `--mcp-project`; `exec_admin` asked
literally, never auto-ran), skill poisoning (curl-pipe denied, sandbox env
tricks inert, model self-corrected), indirect injection via file (quoted
as data, zero mutating calls), poisoned tool output (ask-tier held,
sandbox HOME contained the read, no exfil channel open), ledger/stale
(read-first refusal, live stale-refuse-then-guided-recovery, shell path
ask-tier + snapshotted), undo blast radius (LIFO exact restore, untracked
correctly left, moved-tree refusal). One live find: transient `git stash
create` contention (status-bar refresh colliding mid-write) correctly
refused by the Dirty guard — fixed at the root with snapshot retries +
`--no-optional-locks` on the status read. Residual notes: model final
messages occasionally misreport exit codes (fidelity, not boundary), and
`--yes` auto-approves ask-tier single-step reads (operator's explicit
choice; every exfil channel out stays independently denied).

### Hardening pass (post-Phase 7 three-agent review + PTY verification)
Correctness, security, and simplicity audits (plus live PTY driving that
caught 4 TUI state bugs unit tests missed) produced this pass, all
regression-tested: project-root containment for every file tool (cwd +
absolute + symlink escapes refused), deterministic consecutive doom guard
with an honest message, policies.yaml actually loaded (deny beats --yes),
quote-aware shell parser judging every pipeline segment (rm -rf, sudo,
curl, force-push…; `echo curl` passes), per-file git undo + shell-restore
refusal when the tree moved on, MCP project servers opt-in only
(--mcp-project), PID-namespace task containment, owner-only log perms,
fencing on all remaining tool outputs, TILDE_ALLOW_NET session opt-in with
policy still judging commands, Loop/session mutexes (race-tested),
greps that close FDs and report skipped lines, real `**` glob semantics,
and ~150 lines of dead code deleted. Deliberately NOT merged: five
near-identical 3-line truncators (a shared package would couple worse
than the duplication costs).

### Review round 2 (three more agents + PTY, all green)
Parser hardening (wrapper-transparent judging, flag clusters, tee-device
allowlist, single-shell `python -c` denial), hooks/MCP project opt-in,
worktree flag-injection rejection + add-undo, undo restore re-containment,
subagent output fencing + session call cap + stray reaping, SeenMap
real-path keys + in-flight refusal, snapshot push/discard accounting,
shell-restore SeenMap refresh, finalize-after-hooks, bool→int coercion
removed, TUI spec-compliance round (colored gutter, diff styling, red
handoff panel, branch status, confirm preemption, Working-seconds
indicator, headless JSON, parallel read-only batches, usage accounting).
Two agent-proposed cuts were rejected with written reasons (interpreter
scripts stay Ask-tier under sandbox; truncator merge). Live PTY caught
one real bug the suite missed (unborn-HEAD branch display).

### Spec-compliance fix round (screenshot review)
A side-by-side read of the running TUI against `docs/tui-design-spec.md`
found real deviations, all fixed: the banner art was never in the spec
(removed — splash matches §2.1 line for line); `✓ Done` rendered twice
(agent event + completion handler both appended — one suppressed);
the hint bar shared the status line and clipped past the edge (own line
now, with the spec's `Session:` id); the composer showed the textarea
widget's prompt bar, line numbers, and 3-row height against §2.3's
single-line mockups (all three reset); splash Model/Mode and
Budget/Sandbox collapsed to the spec's two lines; interactive sessions
now always start in Plan per the §2.1 safety default (the `--mode` flag
still governs headless runs).

### Transcript-density fix round (2026-09-06, reference re-check)
A further reference pass (Cursor Agent CLI, GitHub Copilot CLI, Grok
Build) against the running build turned up nothing wrong, but one real
gap in cleanliness: tilde's timeline prints one `●` line per action even
for a burst of trivial lookups, where the references visibly compress
those into a single grouped line, and none of the three references
render a bare wall of glyph'd action lines with no sense of how long the
model paused to reason first. Two additions, spec-first (`tui-design-spec.md`
§2.20), not yet built:
- **Action grouping** — 2+ consecutive read-only, low-signal actions
  (`list`/`read`/`grep`) with no intervening prose or risk-gated action
  collapse under one parent line naming the kinds + count, individual
  actions nested and glyph-less underneath. Session log is unaffected —
  this is rendering only.
- **Thinking indicator** — a single dim `◆ Thought for N.Ns` line before
  the first action of a turn, only above a floor duration; not an
  expandable reasoning transcript, just a "yes, it was working" receipt.
Explicitly not adopted from the same pass: Grok's inline-diff-in-source
style (kept the separate hunk-block diff — better for tilde's frequently
non-contiguous edits, see spec §2.11); Copilot's per-conversation tab bar
and custom-agent picker (both are multi-session/multi-persona features
tilde doesn't have a use case for yet — not a gap, a different product
shape).

### New-file-write rendering fix (2026-09-06, review comment)
A review pass caught a real gap: the timeline (§2.10) and diff view
(§2.11) only ever specified `Edit` — there was no defined rendering for
`write_file` creating a brand-new file at all. Fixed, spec-first: `Write`
is now its own timeline verb, always distinct from `Edit` (a user
shouldn't have to read the `⎿` line to know "created" vs. "modified"),
and a new file renders as a plain numbered listing headed
`(new file, N lines)` rather than an all-green diff — green is reserved
for *changes relative to something*, and a new file has nothing to be
relative to. Same read-ceiling truncation rules as `read_file` apply.
Not yet built.

### Composer input gaps: paste, images, session handoff (2026-09-06)
A further crosscheck — this time including a live web search of current
field practice, not just the reference screenshots already on file —
turned up three real gaps the spec had no answer for at all, all fixed
spec-first in `tui-design-spec.md` §2.21–§2.22:

- **Large paste collapse.** Every reference tool converged on the same
  pattern: a paste over a line/character threshold collapses to a
  `[Pasted text #N +M lines]` placeholder, deletable as one unit, with the
  real content substituted back in at submission time. tilde adopts the
  same shape rather than inventing a new one, with one addition pulled
  directly from real user complaints found against the reference tools'
  own issue trackers: the threshold is a config value
  (`paste_collapse_lines`), not hardcoded — several of the tools reviewed
  had open, unresolved complaints about exactly that rigidity (voice
  dictation and editor-composed prompts wanting to see what they pasted).
  Don't repeat that mistake.
- **Image paste.** tilde does not attempt in-terminal clipboard-image
  capture — a raw terminal generally has no clipboard-image path, and the
  local default model isn't vision-capable regardless, so building that
  capture path would be solving a problem tilde doesn't currently have.
  Documented path instead: save the image to a file, `@`-reference it like
  any other file (§2.5 already handles this). If a vision-capable
  provider is ever plugged in via the Provider interface, `@`-referencing
  an image sends the real bytes — a tool-registry/provider-capability
  decision, not a TUI one.
- **Session export / cross-agent handoff.** A small but real tooling
  category now exists purely to carry a session from one agent's native
  format to another (reading Claude Code's or Codex's own session stores
  and converting between them), because raw JSONL/SQLite logs are
  agent-specific and meaningless outside the tool that wrote them —
  tilde's own JSONL log is no exception. Rather than trying to be
  bidirectionally compatible with every other tool's format (a fast-moving
  target with real churn even among the tools built to do exactly that),
  tilde gets a one-way `/export` that writes a **distilled markdown brief**
  (goal, decisions, files touched, open steps — not a raw transcript) that
  a human or another agent can read cold and resume from manually.
  Importing a *foreign* session format into tilde is explicitly out of
  scope for v0.1 — revisit only on a concrete need, not speculatively.

---

## 8. Explicitly deferred (and why)

Saying no on purpose, so it doesn't get half-built by accident:

| Deferred | Why not yet |
|---|---|---|
| Parallel-write subagents / worktrees-as-workers | Explore-only subagents shipped; writes stay single-agent until the eval number on parallel work exists. |
| Cloud / background agent runs | Needs the eval suite (Phase 7) first — you can't trust an unattended long-running agent you haven't measured for consistency. |
| Container/microVM isolation (beyond bwrap) | bwrap is the right cost/benefit for a solo project at this stage; heavier isolation is a real upgrade path, not a v0.1 requirement. |
| Full audit-log / SIEM export | Enterprise-governance feature with no user yet. Revisit if tilde is ever used by more than one person. |
| Taste / learned-preference systems | Interesting, unproven ROI for a solo user who already knows their own preferences. Skills cover the same need more simply. |
| In-app session tabs / multiplexing | The terminal already does this well (tmux, panes). Building a second multiplexing layer on top duplicates existing, better tooling — Copilot CLI's per-conversation tab bar is a reasonable choice for its own product shape, not one tilde needs. |
| Custom agent personas / picker | No standing need yet for named personas beyond skills, which already cover "reusable, named behavior for a recurring task" more simply. |
| Importing foreign agents' session formats | tilde exports its own sessions as a portable brief (§2.22) for others to read; parsing *other* tools' native JSONL/SQLite formats to resume them inside tilde is a maintenance burden against a fast-moving target with no concrete need yet. |
| In-terminal clipboard-image capture | No vision-capable model in the default stack to send image bytes to, and no reliable cross-terminal clipboard-image escape sequence to build against. Documented `@`-reference-a-saved-file path (spec §2.21) covers the need today. |

---

## 9. Current status snapshot (update in place, don't append new sections)

As of v0.6+:

| Area | Status |
|---|---|
| Agent loop (ReAct: think/act/observe) | DONE |
| File read/edit/write, shell, grep, git | DONE |
| Mode system (Tab-cycle, registry-enforced) | DONE |
| Auto-compaction (80% threshold, JSONL log) | DONE |
| OS-level sandboxing (bwrap, net egress denied) | DONE |
| Git worktree isolation | DONE |
| Doom-loop guard → Plan handoff | DONE |
| Tool-call repair layer | DONE — Phase 3 |
| Read tool's three ceilings | DONE — Phase 3 |
| Honest shell exit codes / untrusted fencing | DONE — Phase 3 |
| Per-edit undo (git snapshot per step) | DONE — Phase 5 |
| Splash / fuzzy search / session picker UI | DONE — Phase 4 |
| Skills loader | DONE — Phase 6 |
| MCP client | DONE — Phase 6 |
| Trajectory-level eval suite | DONE — Phase 7 (historical 7/15 → 22/24 result; current runs report task set, trials, costs + paths) |
| todo_write / ask_user / web_fetch (ask-tier) | DONE — buildHarness-registered; web_fetch also needs TILDE_ALLOW_NET |
| Secret scrubber + high-risk path notes | DONE — Dispatch Scrub, read annotate |
| Project trust gate (`tilde trust`/`untrust`) | DONE — skills fallback, deny on missing/corrupt store |
| Model-aware compaction budget | DONE — BudgetFor catalog window unless explicit |
| Creds store inter-process lock | DONE — flock read-modify-write |
| CI + ARCHITECTURE pointer | DONE — .github/workflows/ci.yml; docs/ARCHITECTURE.md |
| Timeline action grouping (spec §2.20) | DONE — consecutive groupable pairs buffer, static render |
| Thinking-duration indicator (spec §2.20) | DONE — `◆ Thought for Ns` receipt + rotating status verbs |
| Write-verb / new-file rendering (spec §2.10-2.11) | DONE — `Write` verb + plain numbered listing |
| Large-paste collapse (spec §2.21) | DONE — `[Pasted text #N +M lines]` token, `TILDE_PASTE_LINES` threshold |
| Session export / brief (spec §2.22) | DONE — `/export [id]` palette command, distilled scrubbed brief, traversal-proof id |
| Provider retry/backoff + error classification (§11) | DONE — backoff + Retry-After + fail-fast auth; `Classify` surfaces in transcript + headless JSON class |
| Startup config validation, fail-closed before TUI (§11) | DONE — sandbox/provider/policies refuse pre-TUI; unknown tiers and tool names refuse (exit 2) |
| Crash-line + unclosed-session resume offer (§11) | DONE — panic crash line; stderr hint plus same-project picker auto-offer |
| Classified exit codes (§11) | DONE — headless exits 0/2/3/4/5/1 per the spec §4 table (config/startup, provider-exhausted, handoff, deny-tier, other) |
| web_search (ask-tier) | DONE — keyless DuckDuckGo backend, `TILDE_ALLOW_NET`/allow_net-gated, SSRF-guarded, 5-min cache |
| Per-host net approval (allow_net) | DONE — `policies.yaml allow_net` exact-host match via `policy.NetAllowed`, wired into fetch + search; shell egress stays all-or-nothing |
| Fetch extractor (text/markdown) | DONE — HTML→text/markdown extractor, `format:` arg, Mozilla UA, 5-redirect cap, userinfo refusal |
| Task caps (background + batch fan-out) | DONE — `TaskManager` max 16 running tasks, loop batch flush max 8 (`FlushParallel`) |
| Work subagents (spawn/apply/discard) | DONE — single-writer `WorkState`, 8 calls / 12 iters / 6 min per session, `PlanAllow` worktree writes |
| Podman backend file | DONE — `internal/sandbox/podman.go` digest-pinned ephemeral run, selected via `TILDE_BACKEND=podman` + `TILDE_SANDBOX_IMAGE`; bwrap stays default |
| Creds encryption (envelope) | DONE — AES-GCM `credentials.enc.json` sealed with machine-id key, legacy plaintext kept as compat |
| Audit package | DONE — `internal/audit` append-only `audit.jsonl`, arg hashes never raw args, scrubbed, `0600` |
| Release automation | DONE — `.goreleaser.yml` tag builds + SBOM + checksums, CI dry-run, `install.sh --from-release` sha256-verified |
| Signed update verification | DONE — newer release tags are verified with `git verify-tag --raw` before pull; historical unsigned tags do not block current-source refreshes |
| Session scrub at rest | DONE — `session.Append` scrubs via `tools.Scrub`, `0600` heal, crypto-random IDs |
| Hook safe-env + session hooks | DONE — minimal env (no `*_KEY/*_TOKEN/*_SECRET/*_PASSWORD`), 32KB scrubbed output cap, `session_start`/`session_end` |
| MCP user-authoritative merge | DONE — project config only adds servers or tightens approval; remote type + `headersFile`, SSRF-guarded URLs |
| Streaming providers | DONE — optional `Streamer` (ollama NDJSON + openai SSE) carrying the same tool defs as `Chat`; `Collect` assembles prose + native calls + length-cut signal with `Chat` fallback on any stream error; a text-only fast path that dropped calls was caught live and fixed |
| symbol_search tool | DONE — stdlib definition index (go/py/ts/js/rs) + reference fallback, read-only, counts as seen |
| memory tool | DONE — project `.tilde/memory.md` save/recall/forget, recall Plan-safe, save/forget Plan-blocked + ask-tier |
| Audit wiring | DONE — registry `AuditSink` records one hashed-args event per dispatch into `~/.tilde/audit/audit.jsonl`; eval trials intentionally unaudited |
| Scheduler (no daemon) | DONE — `.tilde/schedule.yaml` + state file, `tilde run-due` reexecs headless per due job; OS owns waking, failed jobs retry next tick |
| Audit export | DONE — `tilde audit [--since] [--tool] [--decision] [--json]`, corrupt-line tolerant |
| Plugin manifest v1 | DONE — `tilde-plugin.yaml` strict validation, `tilde plugin install/verify/list`, sha256 lockfile, drift refuses |
| diagnose tool | DONE — stdlib gofmt/parse/TODO diagnostics, read-only, batches with grep |
| Vector memory | DONE — TF-IDF default + Ollama `/api/embeddings` optional (`TILDE_EMBED_MODEL`), `.tilde/vectors.jsonl`, `remember` index/recall/status; index Plan-blocked + ask |
| Browser screenshots | DONE — `web_shot` via headless Firefox viewport PNG (ask, Plan-OK, SSRF-gated); PNGs are human-review artifacts, no vision pipeline yet |
| IDE stdio bridge | DONE — `tilde ide-bridge` line-JSON (initialize/health/session.create/chat/history), approvals deny-by-default, sessions per-process |
| Sandbox image | DONE — `sandbox.Containerfile` (fedora-minimal, agent uid, no secrets) + `docs/sandbox-image.md` with digest-pin workflow |
| models CLI | DONE — `tilde models [provider]` renders catalog windows + prices, unknown provider fails loud |
| P3 eval tasks | DONE — symbol/diagnose/memory/web-unavailable trajectory tasks with filesystem evidence |
| Project rules | DONE — AGENTS.md/CLAUDE.md/.tilde/RULES.md auto-load under the project-skills trust gate, 8KB cap, per-iteration reload |
| Memory hardening | DONE — recency-weighted recall (opt-in half-life), dated memory saves, poisoning trust-model notes |
| Eval costs | DONE — MED_COST column (median over passing trials, $0 when unpriced), JSON reports carry it |
| Retention | DONE — `tilde prune` for sessions (keep-5 floor) + audit trim (atomic, 0600), dry run by default |
| Path-scoped policy | DONE — `deny_paths` globs deny file-tool paths before tiers (beats --yes); loop names the pattern, audit records it |
| Session fork | DONE — `tilde fork <id> [--at]` byte-identical branch + marker, source untouched |
| Cost meter | DONE — TUI status bar shows session $ (hidden when unpriced, $0.0000 when known-free) |
| Loop-level audit | DONE — mode-gate/policy/user denials audited (previously only dispatched calls were) |
| Headless export | DONE — `tilde --export <id> [--out]` (cwd-contained, 0600, conflict guards) |
| Plan banner + todos | DONE — banner at start/demotion, Update Todos block from manager state |
| Subagent view | DONE — ⋮ running / │ [done\|failed] completion rows, per-row model |

---

## 10. Where the ideas in this plan came from

Kept short on purpose — full detail already lives in the TUI spec's own
"prior art" appendix.

- **Command Code** (the most feature-complete reference reviewed): mode
  cycling, the permission decision ladder, the whole tool-contract
  philosophy in §4, skills' progressive disclosure, session/checkpoint
  design.
- **Cursor Agent CLI, GitHub Copilot CLI, Kimi Code**: specific TUI patterns
  (collapsed tool-call log, live plan checklist, compact status line) — see
  the TUI spec for exactly what was adopted vs. deliberately skipped. The
  2026-09-06 pass added action grouping and the thinking indicator
  (spec §2.20) from this same source set.
- **Grok Build**: plan-review workflow (approve/comment/quit), plugin/skill
  install picker, parallel subagent exploration pattern — useful reference
  for Phase 6, not needed before then.
- **The 2026 AI-agent landscape survey**: the six guiding principles in §1,
  and the general finding that harness quality — not model choice — is what
  actually separates the top tier of agents, which is the thesis this whole
  plan is built on.

---

## 11. Robustness & Failure Modes (added 2026-09-06)

Existing sections already cover two failure classes well: a single tool
call failing (§4's per-tool contract — never silent, never a bare error,
repair before rejecting) and the agent itself getting stuck (§1's guiding
principle 4, the doom-loop guard → Plan handoff, presented in
`tui-design-spec.md` §2.15). What was missing is the third class: the
**infrastructure underneath the loop** failing — the model provider, the
config on disk, or tilde's own process — which a review pass plus a look
at current field practice (Codex CLI's provider-resilience writeups,
CLI error-classification patterns converged on across the space) flagged
as a real gap. Full presentation detail lives in `tui-design-spec.md`
§2.23; this section owns the *behavior*, matching the pattern already set
for the TUI (§5) and permissions (§6) sections above.

**Provider-level resilience.**
- Model-call failures (timeout, connection refused, rate limit, transient
  server error) retry with exponential backoff and jitter, honoring a
  `Retry-After` header when the provider sends one rather than guessing —
  ignoring an explicit wait time the provider already told you is a
  documented, avoidable failure in current agent-CLI implementations.
- Errors are classified into a small, named set (`rate_limited`,
  `timeout`, `unreachable`, `auth_failed`, `unknown`) before ever
  reaching the UI or an exit code — never a raw provider error string or
  HTTP status dumped at the user.
- **Unretryable classes fail fast, not slow.** Auth failure or "no model
  configured" exhausts in one attempt, not five — retrying something
  backoff structurally cannot fix just delays the actionable message the
  user actually needs.
- The composer, Esc Esc, and transcript scrolling stay responsive during
  any retry loop — cooperative cancellation (§1 principle 4, already
  built for tool execution) extends to provider calls too. A retry loop
  that freezes input is, from the user's chair, indistinguishable from a
  crash, and this is the single most-cited failure of weaker CLIs in
  current field reviews.

**Startup and config validation.**
- `policies.yaml`, the sandbox availability check, and provider/model
  configuration are all validated **before** the TUI ever draws a frame.
  A failure here prints a plain, specific stderr message (file, line,
  what's wrong, what to do) and exits — never a half-started session with
  broken chrome.
- Fail-closed extends to the parser itself, not just the three
  deny/ask/allow tiers it produces (§6): an unparseable or partially
  invalid `policies.yaml` is a hard startup refusal, never a silent
  fallback to a permissive default for the lines that didn't parse.

**Crash recovery.** The JSONL append-only log (§2) already makes this
mostly free — a crash loses at most the one in-flight, unwritten turn,
never prior history. Two things this principle implies but hadn't been
made explicit: a caught panic writes one honest line to the session log
before exiting (so a resumed session shows *that it crashed*, not a
silent gap — same "never return silence" rule as the tool contract,
applied to the harness itself), and the next launch in the same project
offers to resume an unclosed session rather than requiring the user to
know to look for it.

**Exit codes are classified**, not a bare `0`/`1` — a script driving
tilde headlessly needs to distinguish a config problem from a provider
outage from an agent that got stuck, without parsing prose. Full table
in `tui-design-spec.md` §4 (Headless Parity): `2` = config/startup
failure, `3` = provider error exhausted retries, `4` = doom-loop handoff,
`5` = denied by policy, `1` = everything else uncategorized.

**MCP/hook isolation is a UI promise, not only a backend one.** Phase 6
already isolates a failing MCP server or hook per-server/per-hook so one
broken connection doesn't take down the session; this section just makes
explicit that the same guarantee has a visible surface (a plain `✗` line
naming what failed) rather than being silently absorbed.

**Done when:** you can kill the model provider mid-session, corrupt
`policies.yaml`, `kill -9` the tilde process mid-write, and crash an MCP
server — one at a time — and in every case get a clear, honest message,
a responsive terminal throughout, and a session you can still resume or
retry, never a silent freeze or a corrupted log.
