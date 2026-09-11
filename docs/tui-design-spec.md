# tilde: TUI Design Specification

> The interaction and presentation contract for tilde's terminal UI.

Status: living specification. Entries marked `DONE` describe implemented
behavior; `TODO` entries are design targets and must not be advertised as
available features.

Scope: every screen and interaction surface of tilde's terminal UI, plus the design
system underneath them (color, glyph, spacing, hierarchy, rhythm, alignment,
consistency rules). Stack assumed: Bubble Tea (event loop) + Lip Gloss (styling) +
Glamour (markdown rendering). Mockups are drawn at 78 columns; the real layout is
responsive to terminal width but should degrade toward this reference, not away
from it.

This document is the single source of truth for "what does it look like and why."
Feature *behavior* (mode semantics, compaction thresholds, sandbox tiers) lives in
Plan.md; this doc only overrides Plan.md where the two disagree on presentation.

---

## 0. Design Philosophy

Three things everything below is optimized for, in order:

1. **Legible under stress.** The user is reading this while something might be
   about to run `rm -rf`. Nothing decorative should compete with that decision.
2. **Scannable, not read.** A session log can run to hundreds of lines. The user
   should be able to skim the left gutter (glyphs) and colors alone and know
   what happened, without reading prose.
3. **One meaning per signal.** A color, a glyph, or an indent level means exactly
   one thing everywhere in the app. If you're tempted to reuse amber for
   something that isn't "caution / read-only," that's a sign you need a new
   token, not a shortcut.

A terminal has no font size, no drop shadows, no whitespace-as-luxury. Hierarchy
here is built entirely from five levers, **color, weight, glyph, indentation, 
and blank-line rhythm**, used consistently enough that the user stops noticing
them individually and starts reading meaning directly.

---

## 1. Design System Foundations

### 1.1 Color: semantic, not decorative

Every color is a *token with one meaning*. Never pick a color because it "looks
good" in a given spot, pick the token for the meaning, and let the palette be
what it is.

| Token | Hex | Meaning | Used for |
|---|---|---|---|
| `bg` | `#0D1117` | Base background | Everything sits on this |
| `fg` | `#E6E6E6` | Primary text | Agent prose, user input |
| `fg-muted` | `#A7B2C4` | Secondary text | Timestamps, paths, meta |
| `fg-dim` | `#758198` | Tertiary / disabled | Compacted markers, hints |
| `accent-plan` | `#E5A00D` (amber) | Read-only / caution | Plan mode banner, warnings |
| `accent-build` | `#E6E6E6` (neutral) | Normal editing state | Build mode, deliberately *unmarked* |
| `accent-auto` | `#4FC3F7` (cyan) | Autonomous progression | Auto mode banner, background tasks |
| `accent-select` | `#7E22CE` (magenta) | Active selection / focus | Picker highlight, active tab |
| `success` | `#4CAF50` (green) | Completed, passed, approved | Done checkmarks, passed tests |
| `danger` | `#F44747` (red) | Failed, denied, destructive | Errors, denied commands, deletions |
| `border-idle` | `#465365` | Resting frame | Unfocused boxes |
| `border-focus` | matches active mode token | Focused frame | Composer border in current mode |

**Contrast note on `accent-select` (2026-09-06, resolved 2026-09-09):** the original `#C792EA` was
flagged in review as untested for contrast. It reads fine as text/border
color against `bg`, but `accent-select` is also used as a **background fill**
for a selected row (§1.6) with `fg` (`#E6E6E6`) text drawn on top of it, a
light magenta behind near-white text is exactly the pairing that fails a
contrast check. Darkened first to `#A855D9`, which still measured only
3.39:1 as a fill behind `fg` (computed WCAG relative-luminance), so
darkened again to `#7E22CE` (5.60:1 vs `fg`), which clears WCAG AA for
the fill use. The token is fill-only in code (picker selected rows);
its *meaning* remains what's load-bearing, not the specific value.

Rule: **mode color and risk color are the only two color systems that appear as
borders.** Everything else (success/danger/muted) appears as *text or glyph*
color inside an otherwise neutrally-bordered block. Two accent borders never
appear on screen at once outside of the mode-transition animation (§3.8).

### 1.2 Glyphs: the left gutter is the API

Every line that represents an event starts, after indentation, with exactly one
glyph. The glyph is the fastest thing the eye parses, treat this table as fixed
vocabulary, not a style choice per screen.

| Glyph | Meaning | Color |
|---|---|---|
| `●` | Agent action / system event (filled = happened) | `fg-muted` |
| `○` | Pending / not yet started | `fg-dim` |
| `→` | User input marker / active prompt caret | mode-accent |
| `⎿` | Sub-detail of the line directly above (nested, same event) | `fg-dim` |
| `✓` | Success / done | `success` |
| `✗` | Failed | `danger` |
| `□` | Todo, unchecked | `fg-muted` |
| `☑` | Todo, checked | `success` |
| `⚠` | Needs approval / risk flag | `accent-plan` (amber) or `danger`, by tier |
| `!` | Shell-mode prefix | `fg-muted` |
| `/` | Command-mode prefix | `fg-muted` |
| `@` | File-reference prefix | `fg-muted` |
| `⋯` | In-progress (replaces a braille spinner frame in static renders) | `fg-muted` |
| `◆` | Reasoning / "thought" marker (§2.20) | `fg-dim` |
| `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠿` | Waiting spinner frames, the pre-response thinking row only (§2.20) | `fg-dim` |
| `◇` | Model thinking trace actually emitted by the backend (§2.10) | `fg-dim` |

Rule: **one glyph, one column.** Glyphs never wrap to a second line and never
share a line with a second glyph. `⎿` is the only glyph that implies
indentation by itself, every other nested line indents explicitly (§1.3) even
if it also starts with a glyph.

### 1.3 Spacing, indentation, and the gutter

- **Base unit = 1 line vertically, 2 columns horizontally.** No half-measures, 
  every indent level is exactly 2 columns deeper than its parent.
- **Left gutter is a fixed column 0.** Top-level events (agent turns, user
  input, mode banners) always start at column 0. Nothing is centered. A
  terminal has no visual weight to "center against", centering only reads as
  misalignment here, not balance.
- **Wide-terminal exception (2026-09-07):** past 120 columns the frame holds
  a centered content column instead of stretching full-bleed (a full-width
  status gap on an ultrawide reads as broken, not spacious). Only the column
  is centered, content inside stays left-aligned on the gutter above, so the
  edge stays straight. Narrow terminals are unaffected.
- **One blank line between turns**: zero blank lines within a turn, except the
  one blank row after the user query, which separates the question from
  whatever answers it (tools or prose), and one blank row *before* every
  query after the session's first (2026-09-07), which separates the new
  prompt from the previous turn's closing receipt. A "turn" is
  one user input + everything the agent does in response to it, up to the next
  user input. This is the single most important rhythm rule in the whole spec:
  it's the only whitespace signal the user needs to know where one exchange
  ends and the next begins.
- **No blank line between an action and its `⎿` sub-detail**: they are one
  visual unit.
- **Padding inside bordered boxes (composer, prompts, panels) is 1 line
  vertical, 1 column horizontal**, applied via Lip Gloss `Padding(0,1)` /
  `Padding(1,1)` depending on single- vs multi-line content. Never 0, text
  touching a border reads as a bug, not as density.

### 1.4 Hierarchy without font size

Primary vs. secondary information is distinguished by, in order of how strongly
each reads:

1. **Color intensity**: `fg` for the thing that matters right now, `fg-muted`
   for context, `fg-dim` for stuff that's technically still there but resolved
   (compacted history, past todo items).
2. **Weight**: bold *only* for: the active todo item, file paths in a diff
   header, and command names in the help overlay. Bold is not a general
   emphasis tool; overusing it collapses the hierarchy it's supposed to create.
3. **Indentation**: deeper = more specific / more nested, never "less
   important." A `⎿` detail is not lower-priority than its parent, it's just
   more granular.
4. **Glyph choice**: `●` outranks `○` outranks nothing. A line with no glyph
   (plain prose, e.g. agent explanation text) sits *above* all glyph'd lines in
   the reading order, prose is the headline, actions are the supporting log.

### 1.5 Alignment

- **Status bar splits left/right, nothing centered.** Left = where/what
  (directory, branch, mode). Right = how much (context %, model, shortcuts).
  This mirrors the reference tools reviewed (Kimi Code, Copilot CLI) and is
  worth keeping only because it's already a learned convention, don't invent
  a third zone.
- **The composer border is always full available width.** Content inside it is
  left-aligned; the mode glyph, if shown inside the border, is right-aligned
  in the same line as the placeholder/hint text, never its own line.
  The border uses a neutral focus token; Plan's amber banner remains the
  single caution signal. Masked credential entry may use amber as a security
  focus state.
  ("Available width" is the centered content column past 120 terminal
  columns, see §1.3 wide-terminal exception.)
- **Diffs align on the gutter, not the code.** Line numbers and `+`/`-` markers
  form a fixed-width left column; code starts at the same column regardless of
  indentation depth in the source file.

### 1.6 Rhythm and consistency: the enforceable rules

If a future screen doesn't fit one of these, that's a sign to update this
document, not to make a silent exception:

- Every event line: `[indent][glyph] [Label] [detail]`, never
  `[Label][glyph]` or a glyph with no following space.
- Every actionable list (todos, skill picker, session picker, file picker):
  selected row uses `accent-select` background or left-bar, never inverts the
  whole terminal color scheme.
- Every risk-gated prompt: amber border = "confirm," red border = "deny by
  default, explicit override required." No third border color for risk.
- Every mode banner: exactly one line, exactly the mode name plus one
  descriptor word ("Plan · read-only"), never a paragraph.
- Timestamps, token counts, and percentages are always `fg-muted`, right-aligned
  where they share a line with something more important, they support, they
  never lead.

---

## 2. Screens & Components

### 2.1 Splash / Welcome

Shown once per new session start (not on resume, see §2.13).

```
                     ~/dev/tilde · tilde v0.9.1

╭────────────────────────────────────────────────────────────────────────────╮
│ Welcome to tilde.                                                          │
│                                                                            │
│ tilde can read, edit, and delete files, and run shell commands you         │
│ approve or that fall inside the active sandbox policy. Use in trusted      │
│ environments only. Sandboxed via bubblewrap + network egress denied by     │
│ default on Linux. See policies.yaml to review current rules.               │
╰────────────────────────────────────────────────────────────────────────────╯

  Budget: 32.0k tokens                          Sandbox: ● enforced (OS)
```

Notes:
- **Centered title (2026-09-07):** the context header is the one centered
  line in the UI, a title, not a log event, so the §1.3 gutter rule yields
  for it. Overlong paths fall back to the plain left form, never truncated.
- **Trimmed to what exists nowhere else (2026-09-06):** the splash keeps
  only the Budget ceiling and the Sandbox enforcement state. Model and
  Mode repeat on the ever-present status bar below the composer, so
  printing them again in the splash doubled two signals for zero new
  information, the `Budget: … Sandbox: …` line is space-between (Budget
  docks left, Sandbox docks right, gap computed from the live width, 
  never a fixed pad, which stranded Sandbox mid-line on wide screens).
- The safety notice renders inside a neutral `border-idle` box (same
  RoundedBorder family as composer/confirm/handoff), allowed per §1.1
  because the splash is neither a mode nor a risk signal, so it takes no
  color from either system. Title is plain `fg` text: no glyph, no bold
  (§§1.2/1.4). The box is width-adaptive, rebuilt to the terminal width
  on resize while the session is still fresh (transcript untouched); once
  events land it freezes as ordinary scrollback like every other line.
- The splash transcript ends on the Budget/Sandbox block, the composer, 
  status bar, and hint bar are chrome rendered below the viewport, never
  transcript lines (echoing their wording as transcript content would
  double-render them on the first frame).
- **Resize preserves reading state:** a terminal resize refits the transcript
  and composer without forcing a history reader to the live tail. Follow-tail
  remains enabled only when the user was already at the bottom; fresh splash
  content is the one deliberate exception and is rebuilt to the new width.
- The safety notice is **prose, not a glyph'd line**, it's the one place a
  full paragraph outranks the log format, because it's read exactly once and
  needs to be read as language, not scanned as a status line.
- Session always **starts in Plan mode** regardless of last session's ending
  mode, this is a deliberate safety default, not an oversight.
- The hint bar at the bottom is present on *every* screen where the composer
  is focused, it is the one piece of chrome that never changes position or
  wording, so it becomes muscle memory. It is centered in the frame (the
  footer's quiet closer, like the splash title, not gutter-aligned), and
  hard-cuts with an ellipsis past the frame edge on narrow screens.
- **Correction (2026-09-06), retained as history:** an earlier draft of
  this mockup showed `Mode:  ○ Plan`, reusing the `○` glyph from the todo
  table (§1.2, where it means "pending / not started"). Plan mode is fully
  active, not pending, that was a genuine glyph collision in the spec
  itself, and the kind of thing §1.2's "one glyph, one meaning, everywhere"
  rule exists to catch. Superseded by the trim above: the splash no longer
  shows a Mode field at all (the §2.2 status bar is its home); Mode there
  is plain accent-colored text, no glyph.

### 2.2 Status Bar

Persistent single line, bottom of viewport, always visible even when scrolled
up through history.

```
 Plan · ~/dev/tilde · main [+2]         kimi-k2.7-code · ctx 41% (13.1k/32k)
```

- Left: mode word (color = mode-accent, bold) → cwd (`fg-muted`) → git branch +
  dirty-file count (`fg-dim`).
- Right: active model (`fg-muted`) → context usage as a percentage *and* raw
  count (`fg-muted`, turns `accent-plan` colored text, not border, past 80%, 
  see §2.11). Budget ceiling auto-sizes to the catalog window when known unless `--budget` / `TILDE_BUDGET` was set explicit (§2.24). A session cost readout
  (` $0.0012`, `fg-muted`, hidden when the model has no catalog price) trails
  the context segment, display only, computed from live token totals, never
  width-breaking before the drop order below applies.
- This line never wraps. If the terminal is too narrow, drop the raw token
  count before dropping anything else; drop the branch dirty-count before the
  branch name; never drop the mode word.
- Git branch and dirty-count status is refreshed asynchronously on startup and
  on a 10-second cadence. Rendering never launches `git`, so a slow repository
  or locked index cannot freeze typing, scrolling, streaming output, or an
  approval decision. The first frame may omit the branch for one render while
  the background result is pending.
- One blank spacer separates the composer box from the status bar (2026-09-07)
, the three footer elements read as distinct bands, not one cramped block.
  The viewport gives up exactly one row for it, so the frame still lands on
  the terminal height.

### 2.3 Composer

Three visual states, one per mode, border color is the *only* thing that
changes; box shape, padding, and hint text position stay identical so the eye
isn't relearning layout every time it switches.

```
Plan (amber border):
┌──────────────────────────────────────────────────────────────────────┐
│ → Plan, search, build anything                                             │
└──────────────────────────────────────────────────────────────────────┘

Build (neutral border):
┌──────────────────────────────────────────────────────────────────────┐
│ → Build anything                                                           │
└──────────────────────────────────────────────────────────────────────┘

Auto (cyan border):
┌──────────────────────────────────────────────────────────────────────┐
│ → Give tilde a goal                                                        │
└──────────────────────────────────────────────────────────────────────┘
```

- Placeholder text changes per mode (subtle but real, it's a second signal
  reinforcing the border color for colorblind-safe redundancy).
- A leading `!` arms shell mode: the bang stays in the box (always typeable)
  and a one-line `! run as shell command` hint shows beneath the composer, 
  same slot and manners as the `/` and `@` dropdowns. A placeholder could
  never carry this signal (placeholders only show on an empty box).
- On `/`, `@`, or `!` as the first character, the composer border does **not**
  change, those are sub-modes of input, not agent modes, and conflating their
  color with the Plan/Build/Auto system would break the one-signal-one-meaning
  rule.
- A large text paste into the composer collapses to a placeholder rather
  than rendering inline, see §2.21 for the exact threshold, shape, and
  deletion behavior.

### 2.4 Slash Command Palette (`/`)

Inline dropdown directly beneath the composer, replacing nothing above it.

```
┌──────────────────────────────────────────────────────────────────────┐
│ → /                                                                        │
└──────────────────────────────────────────────────────────────────────┘
  → /model <model>      Set the current model
    /mode <plan|build|auto>  Set mode explicitly (same as Tab)
    /skills               Browse and load a skill
    /compact [focus]      Summarize older turns to reclaim context
    /clear                Start a new session (repeat to confirm the wipe)
    /copy [n]             Copy the transcript (or one line) to the clipboard
    /sandbox              Show current policy tier and overrides
    /diff                 Show working-tree diff for review
    /critic               Score-gated self-review of the working-tree diff
    /apply [path]         Apply the pending work session (keep worktree)
    /discard [path]       Discard the pending work session (remove worktree)
    /undo [n]             Revert the last n mutating steps
    /sessions             List and resume a past session
    /login [provider]     Configure cloud-provider auth (§2.24)
    /logout [provider]    Remove stored provider auth (named provider: repeat to confirm)
    /export [id]           Write a portable markdown brief for handoff
    /quit                 Quit (same as Ctrl+C idle)
    /help                 Full keybinding + command reference
```

- First (highlighted) row = best fuzzy match as the user types, using
  `accent-select` on the row background, not just the text.
- Descriptions are always `fg-muted`, always right of a fixed-width column so
  they align regardless of command name length, pad the command column to the
  longest command name currently in the filtered list, not a hardcoded width.
- `/vim` is deferred (no vim keybinding mode exists yet), so it is documented
  here as deferred rather than listed as available.

### 2.5 Fuzzy File Reference (`@`)

```
┌──────────────────────────────────────────────────────────────────────┐
│ → Look at @conf                                                            │
└──────────────────────────────────────────────────────────────────────┘
  → internal/config/config.go
    internal/config/loader_test.go
    docs/config.md
```

- Respects `.gitignore`, this is non-negotiable; a fuzzy picker that surfaces
  `node_modules/` results has failed at its one job.
- Matched characters within each path are bolded (`fg`, bold) against the
  unmatched portion (`fg-muted`), the only place in the whole spec where bold
  is used for something other than the three cases listed in §1.4, because
  fuzzy-match highlighting is functionally a fourth, narrow case of "this part
  is the reason this row is here."
- Selecting inserts the path as plain text at the cursor, the chip-token treatment is deferred.

### 2.6 Shell Escape (`!`)

```
┌──────────────────────────────────────────────────────────────────────┐
│ ! Run a command — e.g., git status                                         │
└──────────────────────────────────────────────────────────────────────┘
```

- Border stays the current mode's color, shell escape is a convenience, not a
  mode change, and running a raw command still passes through the same
  sandbox/confirm tier as a model-issued shell call. The UI must never imply
  "user typed it directly" means "unsandboxed."

### 2.7 Skill Picker (`/skills`)

```
  Skills                                                    2 project · 1 user
  → 1. code-review           Review a diff against the house style   project
    2. spec-writer           Draft a Given/When/Then spec from a ticket project
    3. commit-message        Write conventional commits from staged diff  user
  ↑↓ select · enter load · / search · esc cancel
```

- Source tag (`project` / `user`) always right-aligned, always `fg-dim`, it's
  provenance metadata, lowest priority information on the row.
- Project skills load only with `--skills-project` / `TILDE_SKILLS_PROJECT=1` (same opt-in as hooks) or a `tilde trust` / `untrust [dir]` record (`~/.tilde/trusted.json`, `0600`, CLI-only, not a `/` command); otherwise the picker shows user skills only with a stderr notice.
- Loading a skill posts a single `●` event into the transcript ("Loaded skill:
  code-review") so it's part of the auditable history, not a silent context
  injection.
- This picker is the entry point into the fuller plugin/extension browser
  once MCP and a marketplace exist, see §2.17.

### 2.8 Mode System (Tab to cycle)

One line, always visible in the status bar (§2.2) and briefly as a full-width
toast on the transition itself:

```
  ⏵ Mode: Plan → Build                          (approved plan, 5 steps)
```

- Toast appears for one render frame equivalent (~600ms in an animated
  terminal, or simply the next full redraw in a plain one) then collapses back
  into the status bar, it should register as an event, not linger as chrome.
- Automatic demotions (confused-task drop back to Plan) use the **same toast
  shape** but amber, with a one-line reason: `⏵ Mode: Build → Plan (3 failed
  edits to the same file, reassessing)`. Consistency here matters more than
  anywhere else in the spec: a silent, unexplained mode change is the single
  fastest way to erode trust in an autonomy feature.
- Full behavioral spec (promotion/demotion triggers, `--mode` flag parity)
  lives in Plan.md §6 (permissions) and §7 (Phase 1); this section only owns how it's *presented*.

### 2.9 Plan-Mode View (read-only banner + todos)

```
 ┌ Plan · read-only ─────────────────────────────────────────────────┐
 │ tilde is researching. No files will be changed in this mode.       │
 └──────────────────────────────────────────────────────────────────┘

● Update Todos
  ☑ Explore current project structure
  ☑ Read config loader and its tests
  □ Draft the provider interface change
  □ Identify affected call sites

  Provider Interface Change Plan
  ──────────────────────────────
  The current Provider interface assumes a single synchronous Complete
  call; adding streaming requires...
```

- The banner is the **only** full-width bordered element that appears
  mid-transcript rather than only at the composer, it needs to interrupt the
  scan pattern on purpose, because "nothing will change right now" is exactly
  the reassurance a cautious user is scanning for. It is live mode chrome: it
  is removed immediately when Tab or `/mode` leaves Plan and restored when the
  session re-enters Plan, so the transcript never claims Build/Auto is still
  read-only.
- Todo list uses `☑`/`□` per §1.2, never a percentage bar, a coding agent's
  progress isn't linear enough for a percentage to be honest.
- **Revision, not duplication (2026-09-06):** when the todo list changes
  (an item completes, a new one is discovered), the agent posts a *new*
  `● Update Todos` block reflecting the full current state, it does not
  edit the previous block in place (Bubble Tea's transcript is append-only)
  and it does not reprint the whole list unchanged just to bold the newly
  active item. Two consecutive `● Update Todos` blocks with only the
  checkbox states and bold target differing is expected and correct; a
  block that repeats with *no* state change at all is a bug (see §2.20 for
  the general "don't re-post what didn't change" rule this falls under).

### 2.10 Tool-Call / Action Timeline

The backbone of the whole transcript, this is what's on screen more than
anything else, so its consistency matters more than its cleverness.

```
● Listed             . 20 files, 12 directories
● Read                 internal/config/config.go
● Grep       "ProviderInterface"  · 3 matches in 2 files
● Write                internal/provider/stream.go
  ⎿ +34 (new file)
● Edit                 internal/provider/provider.go
  ⎿ +12 -3
● Run         go test ./...
  ⎿ ok    tilde/internal/provider   0.412s
✓ Done
```

- Verb (`Listed`, `Read`, `Grep`, `Write`, `Edit`, `Run`) is a fixed-width column,
  left-aligned, always `fg`, it's the one piece of the line allowed to be
  full brightness besides the glyph, because it answers "what kind of thing
  just happened" at a glance.
- `Write` (new-file creation) and `Edit` (existing-file change) are always
  two distinct verbs, never collapsed into one "Edit" label, knowing
  whether a file is being created versus modified is exactly the kind of
  thing a user scanning the gutter needs the verb column to answer without
  reading the `⎿` line underneath.
- Target/argument follows in `fg-muted`.
- New tools keep their raw names (outside the six-verb vocabulary above).
  Most are ask-tier and Plan-allowed, except the mutating writers, which
  are ask-tier *and* Plan-blocked like any other mutation:
  `spawn_work` / `apply_work` / `discard_work` (isolated worktree sessions; `/apply [path]` + `/discard [path]` dispatch with the pending path, fail loud with none),
  `memory` save/forget and `remember` index (op-aware gating).
  `● todo_write` (serial checklist: `add|done|list|clear`); `● ask_user` (routes to the host AskUser callback, nil/unwired or denied reads as denied, propose a safe default); `● web_fetch` (http(s) GET only, 30s timeout, 5MB hard cap, needs `TILDE_ALLOW_NET=1`, denied otherwise without retry; allowed in Plan since it mutates no repo state).
- Result (`⎿`) is optional and only appears when there's something worth
  reporting beyond "it ran", a diff stat, a test summary, an error. A silent
  success with nothing worth surfacing gets no `⎿` line at all; don't manufacture
  one just for rhythm's sake.
- Tool output is secret-scrubbed centrally (`<<REDACTED:name>>`); reads of sensitive paths (`.env`, `*.pem`, `id_*`, …) carry a warn-notice instead of raw content (same discipline as the §2.22 export rule).
- Long results use a 12-line transcript preview with an explicit `↳ N more
  lines · Ctrl+O to expand` affordance. `Ctrl+O` replaces that one preview
  with the complete result; output is never silently discarded, and the
  session log remains the authoritative full record.
- Final `✓ Done` (or `✗ Failed`) closes the group, this is the only place
  `✓`/`✗` appear outside of individual test/check results, reserved for "this
  whole unit of work is over."
- **Conditional receipt (2026-09-07):** `✓ Done` renders only when the turn
  dispatched at least one tool call. A pure chat reply (prose in, prose out,
  no tools) ends without it, a receipt for nothing is clutter, not rhythm.
- **Grouping same-kind actions under one bullet (2026-09-06):** when the
  agent fires a run of same-kind, low-signal actions back to back (e.g. a
  batch of read-only lookups before it says anything), collapse them under
  one parent `●` line naming the kinds and a count, with each individual
  action nested one level as a plain (glyph-less) line, see §2.20 for the
  exact shape and when this does/doesn't apply. This is a presentation
  grouping only: the session log still records each action as its own
  event; nothing about the underlying tool-call ledger changes.
  Implemented 2026-09-07 as static rendering (no expand/collapse toggle:
  `Tab` cycles modes and `Space` types, and there is no focus model to
  hang a toggle on, a toggle key would hijack both). Only runs of 2+
  groupable (read-only) pairs group; a lone pair renders exactly as an
  ungrouped call + result. Results keep their full rendering, indented
  under the parent.
- **Post-turn receipt (2026-09-07):** a successful agent turn closes with
  `◆ Thought for 3.4s · 38 tok/s` (`fg-dim`, single line, after `✓ Done`
  when one rendered). Wall time is measured turn start → done; the rate
  uses provider-reported output tokens and is omitted when unknown, 
  never invented. Shell escapes (no generation) stay receipt-free.
- **Model thinking traces (2026-09-07):** when the backend exposes model
  reasoning (Anthropic `thinking`, Ollama thinking models,
  OpenAI-compatible `reasoning_content`), it renders dim under `◇` ahead
  of the reply, first line carries the glyph, the rest indent, blank
  runs collapse, long traces clamp with a log pointer. It is display +
  session-log only: never mixed into prose, never fed back as context,
  never restored into context on resume (scrollback only).
  `redacted_thinking` stays skipped, opaque ciphertext, nothing to show.

### 2.11 Diff Rendering

```
 internal/provider/provider.go
 ───────────────────────────────
  42   type Provider interface {
  43  -    Complete(ctx context.Context, req Request) (Response, error)
  43  +    Complete(ctx context.Context, req Request) (Response, error)
  44  +    Stream(ctx context.Context, req Request) (<-chan Chunk, error)
  45   }
```

- File path header: bold, `fg`, own line, thin rule beneath it (`─`) rather
  than a box, a diff is dense enough without adding a border around it too.
- Line numbers right-aligned in a fixed 4-column gutter; `+`/`-` immediately
  after, then one space, then code, code always starts at the same column
  whether the line is context, addition, or removal.
- Additions: `success` green text, no background fill. Removals: `danger` red
  text, no background fill. Full-line background highlighting reads as
  aggressive in a dark terminal theme and is avoided everywhere in this spec,
  not just here.
- Reviewed against Grok Build's inline-diff-in-source view: Grok colors
  additions/removals directly inside the surrounding function body rather
  than a separate hunk block. Deliberately not adopted, tilde's edits are
  frequently non-contiguous within a file, and a fixed hunk block with its
  own header keeps the "which file, which lines" answer unambiguous even
  when several files are touched in one turn. Full-file inline coloring is
  a reasonable alternate style, not a gap in this spec.

**New-file writes (`write_file` creating a file that didn't exist) are not
rendered as a diff.** A file where every line is an addition gets no real
benefit from the two-column `+`/gutter treatment above, it's just the file, 
tinted green, with a redundant `+` on every line. Instead:

```
 internal/provider/stream.go  (new file, 34 lines)
 ───────────────────────────────
  1  package provider
  2
  3  type Chunk struct {
  4      Data []byte
  5      Err  error
  6  }
  ...
```

- Header states `(new file, N lines)` in place of the edit header's bare
  path, this is the one piece of information a diff header doesn't need to
  carry (an edit's line count is implicit in the `⎿ +N -N` stat) but a
  write's does, since there's no separate stat line to get it from.
- Content renders in plain `fg`, not `success` green, green is reserved for
  *changes relative to something*, and a new file has nothing to be relative
  to. Coloring the whole body green would also silently break the "additions
  are green" rule the moment the user later edits this same file and sees
  real green-highlighted additions inside a still-green-tinted file.
- Files over the same length ceiling as `read_file` (§4 of Plan.md, 2,000
  lines / 128KB) truncate with the identical resume-offset notice used for
  reads, rather than dumping an unbounded new file into the transcript.
- The line-number gutter still applies (fixed-width, right-aligned, same
  column as the edit diff), the only things that change for a write are the
  header wording and the removal of the `+`/`-` marker column, since every
  line is unambiguously new.

### 2.12 Approval / Confirm Prompts

Two tiers, two border colors, sharing one layout so the *shape* is familiar and
only the *color and default* change:

```
Confirm tier (amber):
┌ Confirm ─────────────────────────────────────────────────────────────┐
│ Run: rm internal/provider/provider_old.go                             │
│ Reason: May I commit and push the verified updater fix that prevents   │
│ historical unsigned tags from blocking current updates?               │
│                                                                       │
│                                                                       │
│ Actions                                                               │
│ [y] approve once   [n] deny (default — Enter denies)                  │
│ [r] hide reason   [a] always this session (exact command — shell only) │
└───────────────────────────────────────────────────────────────────────┘

Deny-tier panel (red, `[o] override once`): DEFERRED — no red deny-tier
panel exists in code; policy denials currently render as an inline
`✗ denied by policy: …` transcript line, not a bordered panel.
```

- The exact command or action is shown verbatim, never summarized, a
  paraphrased confirm prompt ("run a cleanup command") defeats the entire
  purpose of asking.
- The explanation and controls are separated by a blank row. Approval keys
  use semantic color accents (`y` success, `n` danger, `r` muted, `a` amber)
  paired with explicit labels, and the panel has an opaque background so
  transcript content cannot show through the decision surface.
- The reason is optional, user-facing model output, and can be toggled with
  `[r]` (`[r] hide reason` / `[r] show reason`). It is explanatory only and
  never changes policy or authorization. If the tool request supplies no
  reason, the UI uses the truthful policy fallback rather than inventing model
  rationale.
- Default focused option is always the safe one (`n` / deny), Enter with no
  other input denies. This is a deliberate one-way door: it is much cheaper to
  make the user press one extra key to allow something than to make a
  destructive action one accidental Enter away.
- Session-scoped approval (`[a]`) exists in exactly one narrow form:
  the EXACT literal shell command, this session only, dies with the
  process. Deny-tier shapes can never be listed, and any args change
  re-prompts. A pattern-based always-approve remains deliberately
  deferred, the two-key gate stays the default per the deny-by-default
  posture.

### 2.13 Compaction Indicator

Two states: the ambient warning, and the compaction event itself.

```
Ambient (status bar, context crosses 80%):
 Plan · ~/dev/tilde · main [+2]         kimi-k2.7-code · ctx 83% (26.6k/32k)
                                                          ^^^ turns accent-plan

Compaction event (inline, transcript):
● [compacted: 14 older messages — goals, findings & decisions kept · see session log]
```

- The ambient warning is color-only (the percentage text turns `accent-plan`)
, no popup, no interruption. Compaction is routine housekeeping, not an
  incident, and the UI shouldn't treat it with the same visual weight as a
  confirm prompt or a mode demotion.
- The compaction marker line is always `fg-dim` and always collapsed to one
  line, it's a receipt, not a summary the user is meant to read in place. The
  full pre-compaction log remains in the session file per Plan.md §7
  (Phase 1: mode system + auto-compaction);
  the transcript marker exists purely so a scrollback read never has an
  unexplained gap.

### 2.14 Session Resume / List

Shown on `tilde --resume` or `/sessions`.

```
  Resume a session                                          3 sessions

  → session_a1e2…   2h ago    ~/dev/tilde        "add streaming to..."
    session_9f3c…   1d ago    ~/dev/tilde        "fix flaky config test"
    session_04b1…   3d ago    ~/dev/other-repo   "initial scaffold"

  ↑↓ select · enter resume · d delete · esc cancel
```

- Columns are fixed-width and aligned: id, relative time, directory, first
  user message (truncated with `…`, never wrapped). Same fixed-width-column
  discipline as the skill picker and slash palette, one list-row pattern for
  the whole app, not a bespoke layout per picker.
- Resuming restores the mode the session was in *unless* that mode was Build
  or Auto, in which case it restores into Plan with a one-line notice, 
  matching the "sessions always open cautious" rule from §2.1.

### 2.15 Error / Doom-Loop Handoff

```
 ┌ Handoff to Plan ──────────────────────────────────────────────────────┐
 │ ✗ Same edit to config_test.go failed 3 times in a row (compile error). │
 │   Reverting to Plan mode. Nothing further will be changed.            │
 │   Partial diff and full history are preserved below.                  │
 └────────────────────────────────────────────────────────────────────────┘
```

- Red-bordered, this is the one panel that uses `danger` as a border color
  rather than a risk-prompt, because it represents a state the system itself
  judged as failed, not a decision pending the user's input.
- Always states three things and nothing more: what got stuck, what the
  system did about it (revert to Plan), and reassurance that nothing was lost.
  This is the panel most likely to be read while the user is frustrated, it
  is not the place for a stack trace by default (offer it via a keypress,
  don't dump it inline).

### 2.16 Help Overlay (`/help` or `?`)

Full-screen (or large modal) overlay, dismissible with `esc`, laid out as a
plain two-column keybinding table, no glyphs, no color beyond `fg`/`fg-muted`,
because this is a reference screen, not a log, and shouldn't compete with the
timeline vocabulary it's explaining.

```
  Keybindings                                                    tilde v0.9.1

  Tab              Cycle mode: Plan → Build → Auto
  Ctrl+C           Clear draft; quit when empty (running turns use Esc Esc)
  q                Quit when the composer is empty and nothing is running
  Esc Esc          Cancel the running turn (second press inside 2s)
  Ctrl+J           Newline in the composer
  Ctrl+Y           Copy latest assistant response to the clipboard (raw
                   markdown; helper-binary failure falls back to OSC 52,
                   the terminal's own clipboard — no xclip needed)
  /                Command palette
  @                File reference
  !                Shell escape
  ↑↓               Prompt history ring (Up = last prompt, draft stashed
                   at the bottom and restored verbatim); pickers while
                   open; transcript scroll when empty, past the bottom
                   end, scrolled up (reading owns the keys — wheel motion
                   arrives as arrows through tmux alternate-scroll), or
                   off the edge row of a multiline draft (Up recalls from
                   row 0, Down from the last row; inner rows edit).
                   `TILDE_ARROWS=scroll` flips the default for
                   wheel-as-arrows stacks: arrows always scroll, except
                   non-empty Up (still recalls) and navigating Down
                   (still walks). Unparseable values mean history.
  Shift+↑↓         Scroll transcript, even while a picker is open
  PgUp/PgDn        Scroll transcript by half a screen (Ctrl+U/D stay in the composer)
  Wheel / touchpad  Scroll transcript, even while a picker is open
  Home/End         Line home/end with a draft; top of history / back to live when empty
  esc              Dismiss overlay or picker

  Cell-motion mouse tracking is on: wheel motion (a touchpad two-finger
  scroll included) scrolls the transcript natively instead of reaching
  the app as bare ↑/↓ through alternate-scroll, where it recalled prompt
  history from the live tail. Plain left-drag selects transcript characters
  in-app — the selection is transcript-absolute, so wheel scrolling
  mid-drag keeps the highlight glued to the content and holding the
  drag at a screen edge autoscrolls (a turn longer than the viewport
  is one gesture); inverse-video highlight follows the drag, release
  copies the selected characters as plain text (ANSI stripped) through the clipboard
  ladder and toasts the receipt; a bare click copies nothing. Collapsed
  pastes copy expanded (§2.21): tokens never leak into the clipboard.
  Shift+drag still reaches the terminal's own selection and is the
  recommended way to copy arbitrary terminal contents. Alt+M pauses tracking
  entirely for native drag-select with rectangles and terminal copy
  chords — the wheel pauses with it and the hint bar says so until
  Alt+M re-arms scrolling. Clipboard writes go to xclip/xsel first and
  fall back to OSC 52 — the terminal sets its own clipboard, so copy
  works on bare Wayland (kitty et al.), over SSH, and in tmux with
  `set -g set-clipboard on`, without installing anything. Startup still switches every
  mouse mode off before bubbletea raises its own, healing tabs poisoned
  by older crashed builds. The prompt ring holds the last 200 submitted
  prompts (consecutive duplicates collapse); recall lands the cursor at
  the end, and fresh typing abandons the position.
  Rendered transcript lines carry no trailing padding (the viewport's
  full-width space fill is stripped at the render boundary), so copies
  contain only visible characters.

   Slash commands: /mode /compact /clear /copy /sandbox /diff /critic /undo /sessions /export /model /login /logout /skills /plugins /marketplace /quit /help

                                                           press esc to close
```

### 2.17 Plugin / Extension Marketplace (`/plugins`, `/marketplace`)

The fuller browser the skill picker (§2.7) opens into once skills and MCP
servers both exist. A tabbed row-list, not five separate features, every
tab filters the same underlying registry (project-, user-, and
marketplace-sourced items) by kind.

```
  install browser-review and open its skills

 Hooks   Plugins   Marketplace   Skills   MCP Servers
 ─────────────────────────────
  / to search                                              Workspace ▾

  › team-tool              (project)                          [install]
  › browser-review v0.8.2  (workspace)                         [install]
  › github-flow v2.1.0     (workspace)                         [install]
  › xai-code-review v1.0.0 (workspace)                        [installed]

  → install browser-review
```

- Tilde currently exposes all five registry tabs. Hooks and MCP servers are
  discovery-only; public remote marketplace fetching remains deferred. The
  row layout is shared across every kind so a future remote source does not
  require a UI redesign.
- `[install]` renders in `accent-select` and is interactive; `[installed]`
  renders in `success` and is inert, the color alone tells you a row's
  state without reading the word, same principle as the todo checkboxes
  in §2.9.
- Same fixed-width-column discipline as every other list in this spec
  (§2.4, §2.7, §2.14), one list-row pattern for the whole app.

### 2.18 Structured Multi-Question Prompts

For the rare case where tilde genuinely needs more than one independent
piece of information before it can proceed, first-run setup, or a task
with several unrelated unknowns, one radio-style picker beats a chain of
separate prompts:

```
  Waiting on answers for 3 questions                    [turn: 7.1s, ↓53.6k]

  1  ○  Minimal & terminal-native   Clean, keyboard-first, no excess chrome
  2  ○  Bold & expressive           Strong visuals, gradients, animations
  3  ○  Developer-focused           Code-first aesthetic, technical precision
  4  ○  Other                       Define custom principles
  z  ○  Type your own answer here

  [1/3]  ↑/↓ navigate · ←/→ question                          Enter: select
```

- A different surface from the confirm prompt (§2.12): this never gates a
  destructive action, only gathers information, so it never borrows the
  amber/red risk borders, plain `fg`/`fg-muted` throughout, because
  there's no risk being weighed.
- One option is always "type your own answer" (off the numbered list, key
  `z`), so the preset list is never a hard ceiling on what the user can say.
- The `[turn: Ns, ↓Nk]` figure top-right, elapsed time and tokens spent so
  far, in `fg-dim`, is worth using anywhere tilde is waiting mid-turn on
  the user: it costs nothing and answers "is it still doing something"
  without being asked.
- Reach for this rarely. The ordinary path for "the agent needs one
  clarification" is a plain question in the transcript, not a picker, this
  surface is for 2+ genuinely independent unknowns at once, not a substitute
  for normal conversation.

### 2.19 Subagent / Parallel Exploration View

Deferred behind multi-agent orchestration (explicitly out of v0.1 scope,
Plan.md §8), specified now because it's a direct extension of the
tool-call timeline (§2.10), not a new visual language, and shouldn't invent
a competing vocabulary when it eventually ships.

```
 ⋮ explore   Explore checkout flow        explore · tilde
 ⋮ explore   Explore shared Go libraries  explore · tilde
 ⋮ explore   Explore order services       explore · tilde

  find the source of the p99 latency regression

 │ Diff recent deploys        explore · tilde        [done]
 │ Rank slowest endpoints     explore · tilde        [done]
 │ Pull slow query plans      general · tilde        [done]

 Splitting deploys, slow endpoints, DB plans, and cache hit rates into
 parallel digs.
```

- `⋮` (dim vertical ellipsis) marks a still-running subagent; a left `│`
  rule replaces it once a batch finishes issuing and results start
  returning, with `[done]` right-aligned in `success`, the same
  right-aligned-status convention as `[installed]` in §2.17.
- Each row names the subagent's own task, its type (`explore`/`general`),
  and which model it ran on, the one place per-row model attribution
  matters, since a parallel batch can legitimately mix models for cost or
  speed reasons.
- The parent's own synthesis (the plain-prose summary at the bottom) always
  sits below the finished batch, never interleaved with it, prose still
  outranks glyph'd lines, per §1.4.
- Do not build the feature behind this early, it's specified purely so
  that whenever subagents do ship, the visual language is already decided.

### 2.20 Thinking Indicator + Action Grouping (added 2026-09-06)

Two related patterns pulled from a side-by-side read of Cursor Agent CLI,
GitHub Copilot CLI, and Grok Build against tilde's live build: both tools
visibly compress a chatty turn into fewer, denser lines rather than
printing one line per micro-action, and both surface how long the model
spent reasoning before it acted. Neither changes tilde's underlying event
model (the session log still records one entry per real action), both
are transcript-rendering rules only.

**Thinking indicator.** When the model's reasoning step for a turn takes
long enough to be worth naming (a fixed floor, not every turn), render one
dim line before the first action of that turn:

```
◆ Thought for 3.4s
● Read                 internal/middleware/auth.ts
```

- `◆`, `fg-dim`, never bold, never expandable in v0.1, this is a receipt
  ("it was reasoning, not stalled"), not a transcript of the reasoning
  itself. Surfacing the actual chain-of-thought is a separate, larger
  product decision this spec doesn't take a position on.
- Skip the line entirely below the floor, a 1.5 line for every single
  turn is noise, not signal, and trains the eye to stop reading it.
- **Pre-response, the wait itself animates (added 2026-09-10).** Between
  turn start and the first stream delta, one dim row spins in place:
  `<braille frame> thinking <elapsed>s` (e.g. `⠋ thinking 12s`). A
  frozen "thinking" line during a slow model wait reads as hung; motion
  plus elapsed time reads as work. Contract:
  - **Frames:** the ten-cell braille cycle
    `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠿` at ~8fps (`120ms` tick), `fg-dim`, same
    glyph family as `◆`/`◇`/`○`, text, never color-only, and still
    meaningful with all styling stripped.
  - **Rewrite, never append:** the tick rewrites its own transcript row
    in place. Scrollback grows by exactly one row per wait no matter how
    long the model takes.
  - **Row ownership:** the tick fires only while its row is still the
    last transcript row of a live turn. If any other row lands after it
    (or the turn ends), the loop dies silently instead of rewriting the
    wrong row.
  - **No residue:** the row is removed when streaming starts and when
    the turn completes, the wait leaves zero transcript rows behind.
    The post-hoc `◆ Thought for Ns` receipt (above) is the only record.
  - **Cancel-safe:** quitting or cancelling mid-wait kills the tick with
    the turn; no orphaned animation outlives the session intent.

**Live reasoning microcopy (while the wait is happening, not after).**
The post-hoc receipt above only helps in scrollback. While the model is
still reasoning, turn running, transcript quiet, the status bar's
`Working Ns` flips to a rotating verb so the wait itself reads as progress
(dynamic microcopy, Nielsen visibility-of-status; a changing line makes a
long wait feel shorter than a frozen one):

```
◆ Pondering 4s · Esc×2 cancels      →  2s later  →  ◆ Sifting 6s · Esc×2 cancels
```

- Verbs cycle in a **fresh random order each turn** (`Reading`,
  `Mapping`, `Tracing`, `Probing`, `Weighing`, `Drafting`), one step
  every 2s, each verb names something the agent actually does (reads
  files, maps structure, traces calls, probes, weighs options, drafts
  the plan), in tilde's terse voice rather than borrowed whimsy. The
  order is a shuffled deck (Fisher–Yates at turn start), so a verb never
  repeats within a cycle, a repeat would read as frozen, the exact
  failure this feature exists to prevent, while the fixed 2s cadence
  never varies (irregular timing reads as stutter). Randomness lives
  only in the shuffle; picking stays a pure function of order + elapsed,
  so tests inject a fixed order and stay deterministic.
- The flip triggers only after **2s of transcript quiet** (no appends):
  while tools are streaming, the transcript itself is the progress signal
  and a competing verb line would be noise. First new transcript activity
  flips it straight back to `Working Ns`.
- `◆` + verb + elapsed in `fg-dim`, never bold; `· Esc×2 cancels` stays
  put in `fg-muted`, the cancel affordance is never rotated away.
- Never while a confirm prompt is open: that wait is on the *user*, not
  the model, and labeling it reasoning would be dishonest.
- Transient chrome only, it lives in the status bar and leaves zero
  transcript residue when the wait ends. Headless output is unaffected
  (no status bar there to rotate).
- Live cadence (2026-09-07): a 2s tick re-renders while a turn runs, so
  the counter and verb visibly advance through quiet waits instead of
  freezing between messages. The tick re-arms only while running and
  dies silently at done, no perpetual loop, no headless effect.

**Action grouping.** When the agent runs a burst of same-kind, low-signal
actions before saying anything or taking a riskier action, collapse them
under one parent line instead of one `●` per action:

```
● Listed, Read ×4
  Listed apps
  Read package.json
  Listed apps/web
  Read apps/web/package.json
```

- The parent line names the distinct kinds present (comma-separated, in the
  order they occurred) plus the total count (`● Listed, Read ×4`), in the
  same fixed verb-column position a single action would use.
- Nested lines are plain text, `fg-muted`, indented one level, **no
  glyph**, this is the one place §1.2's "every event line gets a glyph"
  rule is deliberately relaxed, because the parent line already carries
  the glyph for the whole group and repeating it four times adds ink
  without adding meaning. Worthwhile results still render indented
  beneath their call line (same §2.10 `⎿` rules).
- Grouping applies to batchable read-only actions (the `ParallelSafe`
  set: list/read/grep plus git status/diff, skill loads, and the newer
  read-only tools) run back-to-back with no intervening prose or
  risk-gated action.
  An `Edit`, `Run`, or anything that produced a `⎿` result worth reporting
  (§2.10) always breaks the group and starts its own `●` line, grouping
  exists to reduce noise from lookups, never to bury something the user
  should actually notice.
- A group of one is just a normal single action line, don't wrap a
  solitary `Read` in group styling to "stay consistent." The rule triggers
  on 2+ consecutive qualifying actions, not on principle.

### 2.21 Large Paste, File, and Image Handling (added 2026-09-06)

Sourced from current practice across the field (Claude Code, Codex CLI,
Cursor CLI, Copilot CLI), this was a real gap: the spec had no answer for
"what happens when the user pastes 400 lines, or a screenshot, into the
composer."

**Large text paste, collapse to a placeholder, not inline content.**

```
┌──────────────────────────────────────────────────────────────────────┐
│ → [Pasted text #1 +214 lines]  fix this stack trace                        │
└──────────────────────────────────────────────────────────────────────┘
```

- Threshold triggers on **either** a line count or a character count,
  whichever is hit first (matching the converged field default, roughly
  4+ lines or ~1,000 characters); a two-line paste stays inline, a
  40-line traceback collapses.
- The placeholder is `fg-dim`, `[Pasted text #N +M lines]`, numbered
  per-paste within the turn (a second large paste in the same composer
  entry becomes `#2`), this is the same placeholder shape converged on
  by every reference tool reviewed, not a bespoke tilde format, since a
  user switching tools shouldn't have to relearn what the bracket means.
- **Deletable as one unit.** Backspace immediately after a placeholder
  removes the whole placeholder (and discards the stored content with
  it) in one keystroke, never character-by-character, the placeholder is
  a token, not abbreviated text, and must behave like one.
- The full pasted content is stored off-screen (keyed to the placeholder)
  and substituted back in at submission time, the model always receives
  the real content in full; only the composer's on-screen rendering is
  collapsed.
- Once submitted, the transcript's user-turn line renders the placeholder
  form too (`→ [Pasted text #1 +214 lines]  fix this stack trace`), never
  the raw pasted body, a 200-line paste dominating the scrollback is
  exactly the noise §2.20's action-grouping section exists to avoid
  elsewhere, and the same principle applies to user input.
- **Collapse is display-only, never copy-only.** Every copy path, 
  drag-select release, `/copy`, `/copy n`, expands submitted tokens
  back to their stored bodies (bodies are retained keyed to the echo's
  transcript line, so token renumbering across turns cannot cross-wire).
  The transcript is a summary view; a copy of it is not. Token text
  never leaks into the clipboard.
- **No silent hard floor.** `TILDE_PASTE_LINES` governs the line threshold
  (default 4, `0` disables collapsing entirely), the env-var convention
  matches `TILDE_BUDGET` and friends, and a bad value falls back to the
  default rather than breaking input. This is a
  direct, deliberate response to real friction reported against the
  reference tools reviewed, voice-dictation and editor-composed prompts
  legitimately want to see what they pasted before sending, and a
  hardcoded, non-configurable threshold was the single most common
  complaint found across those tools' own issue trackers. Don't repeat
  that mistake by hardcoding tilde's threshold.
- **Submit-time rules (2026-09-07):** tokens resolve to bodies for
  execution, history, and the session log; the transcript echo keeps the
  placeholder form. A token destroyed mid-edit strands its body, submit
  announces the drop instead of silently sending a shorter turn. The
  16,000-char input cap binds the expanded payload with the standing
  truncation notice.

**File attachment (`@` or drag-in).** Already covered by fuzzy file
reference (§2.5) for in-repo files. A path dropped or typed that resolves
outside the fuzzy index (an arbitrary file, not necessarily tracked) still
renders as a plain `@path` token, not a placeholder, file attachment and
large-text-paste are different mechanisms and shouldn't share a visual
form, since one names a location and the other embeds content.

**Image paste.** tilde runs in a raw terminal, not a GUI shell, most
terminals have no clipboard-image escape sequence, and tilde's local
model default (a text/tool-call model, not a vision model) has nowhere to
send image bytes even if it did. Given that real constraint, tilde does
**not** attempt in-terminal clipboard-image capture (unlike Cursor CLI /
Copilot CLI, which run inside terminals with OS-level clipboard hooks
wired in for this specific purpose). Instead:

```
┌──────────────────────────────────────────────────────────────────────┐
│ → Look at @screenshot.png — why is the button misaligned?                  │
└──────────────────────────────────────────────────────────────────────┘
```

- The documented path is: save the image to a file (screenshot tool,
  `pngpaste`/`wl-paste` piped to a file, etc.) and reference it with `@`
  like any other file, §2.5's fuzzy picker surfaces image files the same
  as source files. This is slower than a direct clipboard paste, and the
  spec says so plainly rather than implying a capability that isn't there.
- If the active model is vision-capable (a future Provider, not the local
  default), `@`-referencing an image file sends the actual image bytes,
  not a text dump of the path, the tool-registry layer decides this per
  provider capability, not the TUI.
- A `!`-shell-escape'd clipboard-to-file helper (documented, not built
  into the core binary) is the pragmatic bridge for users who want
  something closer to one-step paste without tilde owning OS clipboard
  integration it can't sandbox consistently across platforms.

### 2.22 Session Export / Cross-Agent Handoff (added 2026-09-06)

A gap surfaced by current field practice: several small tools now exist
purely to carry a session from one agent's native format to another's
(reading Claude Code's, Codex's, or OpenCode's own JSONL/SQLite stores and
converting between them), because raw session logs are agent-specific and
don't mean anything outside the tool that wrote them. tilde's own JSONL
log (Plan.md §2) is no different, it's an internal replay format, not
something another agent (or another *person*) can usefully read cold.

**`/export` (and `tilde --export <session-id>`) writes a portable brief,
not the raw log.**

```
  → /export

● Exported                 session_a1e2… → tilde-brief-a1e2.md
  ⎿ 1 file, 3.1 KB — goal, decisions, files touched, next steps
```

- Output is **markdown with YAML frontmatter**, readable, diffable, and
  committable, deliberately not JSON or JSONL. The point of an export is
  a human (or another agent's own summarization step) can read it cold;
  a machine-only format defeats that.
- Frontmatter carries the load-bearing facts an importing agent or human
  actually needs: `session_id`, `project_path`, `model`, `started`,
  `mode_at_export`, `files_touched` (a plain list of paths). Body is
  four fixed sections in this order: **Goal** (the original user request,
  verbatim), **Decisions & constraints** (anything the agent or user
  explicitly settled on mid-session, not restated reasoning), **Files
  touched** (path + one-line description of what changed, not a diff dump),
  **Open / next steps** (what wasn't finished). This mirrors the
  "distilled brief, not full transcript" shape converged on by the
  cross-agent handoff tools reviewed, because a raw pasted transcript is
  exactly the wrong shape to hand to a *different* model with a different
  context budget and no shared history.
- Never includes secrets or credential-shaped content encountered during
  the session, the same untrusted/sensitive-content discipline the tool
  contract already applies to shell output (Plan.md §4) applies here:
  an export is something the user may paste into another tool's chat box
  or commit to a repo, so it gets the same caution as any other
  externally-facing artifact.
- **Import is deliberately out of scope for v0.1.** tilde can produce a
  brief another agent (or a human) can read and resume from manually, 
  pasting it as the opening message of a new session elsewhere, but
  tilde does not parse *other* agents' native session formats itself.
  Building bidirectional format support for every other tool's JSONL/DB
  shape is a maintenance burden with a fast-moving target (the reference
  tools reviewed already show real churn just in *which* formats they
  each support) and isn't needed for tilde's own core loop to work.
  Revisit only if there's a concrete need to resume a *foreign* session
  inside tilde, not just to hand tilde's own sessions elsewhere.
- The export event itself posts as a normal `●` line in the timeline
  (per §2.10's rules, verb, target, an optional `⎿` result) so it's part
  of the same auditable history as everything else, not a side-channel
  action.

### 2.23 Provider, Config, and System Error States (added 2026-09-06)

The spec so far covers two failure classes well, the agent getting stuck
(§2.15's doom-loop handoff) and a single tool call failing (Plan.md §4's
per-tool contract). It had no answer for a third, real class: **the
infrastructure underneath the agent loop failing**, the model provider
itself, the config on disk, or tilde's own process. A frozen terminal with
no visible state and no way to interrupt is the single most-cited
complaint against weaker agent CLIs in current field reviews; this section
exists so tilde is never that tool.

**Provider / network errors (model call fails, not a tool call).**

```
● [retrying — model connection timed out, attempt 2/5, next in 4s]
```

- Rendered as a single `fg-dim` transcript line, same visual weight as a
  compaction marker (§2.13), this is expected, recoverable infrastructure
  noise, not an incident, and shouldn't compete visually with a confirm
  prompt or handoff panel.
- Retries use exponential backoff with jitter, and **honor a `Retry-After`
  header when the provider sends one** rather than guessing, a 429 with
  an explicit wait time ignored by the client is a real, cited failure
  mode in current provider integrations, and there's no excuse to repeat
  it when the information is already on the wire.
- Error *kind* is always named in plain words (`rate limited`, `connection
  timed out`, `authentication failed`, `model unavailable`) — never a raw
  provider error code or enum value dumped into the transcript. A kind the
  user can't act on (auth failure, no working model configured) exhausts
  its retries fast and hands off to a plain, actionable message rather
  than cycling forever on something backoff can't fix:

```
✗ Authentication failed — the stored openai key was rejected.
  Run /login openai to update it. No further retries will help this.
```

- **The composer, Esc Esc, and scroll always remain responsive during a
  retry loop.** A stalled provider must never freeze the TUI, this is
  the single most important guarantee in this whole section, since a
  frozen-looking terminal is functionally indistinguishable from a crash
  to the person staring at it. Double-Esc during a retry cycle cancels the
  in-flight turn cleanly (same cooperative-cancellation path as a normal
  turn, Plan.md §7) rather than requiring a second, harder kill.
- Exhausting all retries hands off exactly like a doom-loop (§2.15's
  panel shape, same three-part message: what failed, what happened as a
  result, what's preserved) rather than a bare stack trace.

**Startup / config errors (fail *before* the TUI, not inside it).**

A malformed `policies.yaml`, a missing model binary, or bwrap being
unavailable on Linux are all things tilde can know about before ever
drawing a frame, so it does, and refuses to start into a broken state
silently:

```
tilde: policies.yaml line 14: invalid tier "mabye" (expected deny/ask/allow/allow_net/deny_paths)
       refusing to start — fix the policy file and try again.
```

- Plain stderr text, no TUI chrome, there's no session to render a
  glyph'd line into yet. Exit code is non-zero and specific (see the
  headless table below), never a bare `1` for every distinct startup
  failure, so scripted invocations can distinguish "bad config" from
  "sandbox unavailable" from "no model reachable" without parsing prose.
- **Fail closed, always.** A config problem never falls back to a
  permissive default silently (e.g. treating an unparseable policy tier
  as `allow`), Plan.md §6's "deny always wins" principle extends to the
  parser itself: an error in the file is not evidence the rule doesn't
  apply, it's evidence the file needs fixing first.

**Crash recovery.** If tilde's own process dies mid-session (panic, OOM
kill, terminal closed) rather than the agent or a tool failing:

- The JSONL session log is append-only and fsynced per entry (Plan.md
  §2), so a crash loses at most the in-flight, unwritten turn, never the
  session history up to that point. Resuming (§2.14) picks up exactly
  where the log left off.
- Next launch in the same project, if an unclosed session is found,
  offers to resume it rather than silently starting fresh, a crashed
  session shouldn't be harder to find than a normally-ended one.
- A caught panic writes one line to the session log naming what happened
  before the process exits, so a resumed session's history has an honest
  record of the crash rather than a silent gap (the same "never return
  silence" principle from the tool contract, applied to the harness
  itself, not just its tools).

**MCP server / hook failure isolation.** An MCP server crashing or a hook
script erroring never takes down the session, per-server/per-hook
failure is caught and reported as a normal `✗` timeline line (its tools
simply become unavailable for the rest of the session, or the hook is
skipped for that one call), exactly matching the per-server isolation
already built in Plan.md's Phase 6. This section just makes explicit that
the *same* isolation guarantee is a UI promise, not only a backend one.

### 2.24 Cloud Provider Onboarding: /login, Model Catalog, First-Run
      Guidance (added 2026-09-08)

§2.23 names the failure modes for the model provider underneath the loop,
but its auth-failure remedy was "edit policies.yaml", a documentation
exercise, not a fix. A user whose PC has no GPU for local models had no
onboardable path to cloud models at all: env vars they must know to set,
model IDs they must guess from provider docs. This section is sourced from
current field practice (pi's provider/auth architecture and cline CLI's
`auth` command, reviewed 2026-09-08): the converged shape is **one command,
a shipped catalog, and command-shaped remedies**, onboarding a cloud key
must never require reading tilde's docs.

**Credentials ladder (unambiguous, no silent fallback).**

0. `--api-key` flag, this process only, wins outright.
1. Stored credential from `/login`, `~/.tilde/credentials.enc.json` (AES-GCM
   envelope; a legacy plaintext `credentials.json` is still read as a
   migration fallback), mode `0600`, one entry per provider.
2. Ambient env var, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` (existing
   behavior).
3. Not configured, never guessed, never prompted for mid-turn.

A stored credential *owns* its provider: env vars are consulted only when
nothing is stored, and a rejected stored key is never silently retried
against an env key, auth errors name which source was in play. This is
pi's resolution rule, adopted because "which key am I actually using?" is
the single most common cloud-auth support question.

**`/login [provider]` / `/logout [provider]`.**

```
  provider     source    key            model
  ollama       local     —              qwen3.8-4b:16k
  openai       stored    ····9f2a       gpt-5.2
  anthropic    env       ····c41d       claude-sonnet-4-6
```

- Bare `/login` renders the status matrix above as a dropdown (same
  machinery as §2.4); `/logout` lists and removes. With an argument, the
  composer becomes the key entry field: border amber, input masked as
  `●` glyphs. Removing a named credential needs a repeat press (same
  two-press idiom as `/clear` and resume deletes); bare `/logout` only
  lists and never deletes.
- While the masked key field is open it owns the complete keyboard surface:
  bracketed paste enters the key field (never the underlying draft), Escape
  cancels and clears it, and resize updates its width with the content column.
- **Keys are never echoed, never rendered into the transcript, and never
  written to the session file**, the session stores only that a login
  happened. Status display shows the last four characters only.
- A key is stored **only after validation**: a one-token request to the
  real endpoint. 401 → `✗ key rejected by openai — nothing was stored`;
  network failure → `✗ could not reach openai to validate — nothing
  stored` (no save-unvalidated path; a stored-but-broken key is worse
  than an absent one). Success toasts `✓ openai configured — key ····9f2a
  stored in ~/.tilde/credentials.enc.json`.
- `/login` and `/logout` are ordinary palette entries (§2.4); both work
  mid-session, switching providers mid-conversation keeps the transcript
  and the session file exactly as-is.

**Model catalog (shipped, small, curated).**

- Each provider ships a hand-curated catalog (~5 current models): model
  id, human name, context window, input/output price per million tokens.
  Data lives in one Go table, dated like this section, at tilde's
  provider count a generation script (pi auto-generates from provider
  APIs) is maintenance overhead, not a win.
- `/model <provider/…>` lists catalog entries in the §2.4 dropdown plus a
  trailing `custom…` row for raw model IDs (proxies, previews). A user
  never has to type a model ID blind. (v1: bare `/model` prints the
  catalog as transcript rows with the live backend marked, same data, 
  no picker state.)
- Selecting a catalog model with no explicit `TILDE_BUDGET` auto-sizes
  the budget from that model's context window, a 1M-window model should
  not inherit a 32k assumption.
- An unknown-model 404 surfaces a command-shaped remedy (`/model`, 
  pick from the catalog) instead of the raw model name as terminal
  truth. Closest-id suggestion is a later refinement, not v1.

**First-run guidance.**

The welcome panel (§2.1) always carries the two runnable lines, local
and cloud, so a user without a GPU sees the path on the very first
screen, never after a failure. Conditional display (only when no
backend is reachable) was considered and rejected: reachability means
a startup dial, which main deliberately skips (construction only, §2.23
rule, a dead daemon surfaces as a provider error once the loop calls
it). A static two-line panel is the whole v1.

```
    local: run `ollama serve` + `ollama pull qwen3.8-4b:16k`
    cloud: /login <provider> arms a key, /model <provider/model> switches
```

Both lines are exact, runnable text; the provider list derives from the
registry so it never goes stale. There is no `no model backend found`
header, the guidance renders unconditionally on fresh splash (reachability
would need a startup dial, which main deliberately skips). The cloud line
works because `/login` exists, guidance without the command behind it is
what §2.23's original auth message got wrong.

**Error remedies become command-shaped (amends §2.23).**

| Wire class | Plain-words kind (§2.23 rule) | Remedy shown |
|---|---|---|
| 401 | `key rejected` | `run /login <provider> to update the stored key` |
| 403 | `not authorized` (org/billing) | same, plus `check billing on the provider console` |
| 404 model | `model unavailable` | `/model`, pick from the catalog |
| 429 | `rate limited` | `Retry-After` honored (§2.23, unchanged) |
| 5xx | `provider outage` | retry with backoff (§2.23, unchanged) |
| conn refused :11434 | `ollama not reachable` | ``run `ollama serve` `` |

§2.23's authentication-failure example is updated accordingly, its
remedy is now executable, not documentary.

**Non-goals (deferred, stated so absence is a decision not an omission):**
OAuth/subscription flows (pi-style device-code login, the status matrix
renders a `subscription` row marked `not yet`), OS keychain integration
(keys live in an AES-GCM envelope beside the `0600` file, not a keychain;
see the creds package), custom base-URL/proxy UI
(`policies.yaml` remains the power path), and automated catalog syncing.

Implementation home: `internal/creds` (store + ladder, beside `internal/
policy`'s provider block), the login/logout overlay and status matrix in
`internal/tui`, catalog data beside the provider constructors in `main`'s
wiring. Tests to pin: ladder precedence and no-silent-fallback, `0600`
enforced on write, masked input absent from transcript and session file,
validation-before-store, catalog dropdown and budget auto-size, splash
guidance on empty config, command-shaped auth remedy.

---

## 3. End-to-End UX Flows

### 3.1 First run
Splash (§2.1) → composer in Plan mode → user states a goal → agent works
read-only, populating the action timeline (§2.10) and, for anything non-trivial,
a todo list (§2.9) → agent presents a plan in prose → Tab (or Auto's own
promotion) moves to Build → composer keeps its neutral focus border → edits and
test runs appear in the timeline, each risky command gated by a confirm prompt
(§2.12) → `✓ Done` closes the turn.

### 3.2 Typical task (steady state)
User goal in Build mode directly (skipping Plan for a task they already trust
tilde with) → timeline fills with Read/Edit/Run entries, low-signal lookups
collapsed per §2.20 → a diff renders inline (§2.11) at the point of the
relevant Edit, not batched at the end → test run result closes the loop →
status bar context percentage ticks up the whole time, unremarked upon
unless it crosses 80%.

### 3.3 Interrupted by a risky command
Timeline is proceeding normally → agent issues a shell call that isn't
allow-listed → confirm prompt (§2.12) appears inline, composer temporarily
disabled → user answers → prompt collapses into a single `⎿` line in the
timeline recording the decision (`⎿ approved once`), preserving the audit
trail without leaving the modal-shaped prompt sitting in scrollback forever.

### 3.4 Context threshold crossed
Status bar percentage turns amber (§2.13, ambient) → next turn, before the
model call, a compaction event fires and posts its one-line marker → turn
proceeds normally immediately after, with no user action required and no
change to their input flow. The only way a user notices, if they're not
watching the status bar, is the dim marker line scrolling past.

### 3.5 Agent gets stuck
Repeated failure detected → Handoff panel (§2.15) renders → mode toast (§2.8)
confirms Build → Plan → the amber banner is the caution signal while the
composer keeps its neutral focus border → user reads the
partial diff already in the timeline above the handoff panel, decides how to
redirect, and continues in Plan.

### 3.6 Resuming later
`tilde --resume` → session list (§2.14) → select → full prior transcript
loads into scrollback exactly as it was written (compaction markers included)
→ composer opens in Plan regardless of prior end-state → status bar starts
with empty context usage (usage events are emit-only; per-session persistence
of the ctx label is deferred) and ticks up from there.

---

## 4. Headless / Non-Interactive Parity

Every visual state above has a flag-driven equivalent so scripted use isn't a
second product:

| TUI element | Headless equivalent |
|---|---|
| Mode (Tab / toast) | `--mode plan\|build\|auto` |
| Confirm prompt | `--yes` (accepts confirm-tier only) / exits non-zero on deny-tier |
| Compaction marker | Written to session log only; no stdout noise on piped/`NO_COLOR` runs (a TTY headless run still prints the marker line) |
| Handoff panel | Printed as plain `ERROR:` line + non-zero exit code |
| Skill picker | `--skill <name>` |
| Session export (§2.22) | `--export <session-id> [--out path.md]` (cwd-contained, 0600) |
| Provider retry loop (§2.23) | Retries silently (initial try + 1 retry = 2 attempts, fail-fast auth, `Retry-After` honored); failures print once as `tilde [class]:`; unretryable errors print once and exit |

Headless output drops all color/glyph styling by default when stdout isn't a
TTY (standard `NO_COLOR`-style detection), the glyph vocabulary in §1.2
degrades to plain word labels (`[action]`, `[done]`) rather than
disappearing silently. There is no `[failed]` label: failures surface as
the event text itself (tool-result error lines, `ERROR (handoff…)`), and
exit codes carry the machine-readable verdict. Action grouping (§2.20) also degrades: headless mode
prints one line per action, ungrouped, since the point of grouping is visual
density and a script parsing output wants one event per line regardless.

Interactive TUI sessions honor `NO_COLOR` as well. Borders, mode state, risk,
and completion remain understandable through their fixed glyph and layout
vocabulary when color is unavailable.

**Exit codes are classified, not a bare `1` for every failure** (§2.23), a
script needs to tell "bad config" from "rate limited" from "agent got stuck"
apart without parsing prose:

| Code | Meaning |
|---|---|
| `0` | Completed normally |
| `1` | General/unclassified error |
| `2` | Config or startup failure (malformed `policies.yaml`, sandbox unavailable) |
| `3` | Provider error exhausted retries (auth, rate limit, unreachable) |
| `4` | Doom-loop / iteration-cap handoff (agent got stuck) |
| `5` | Denied by policy (a deny-tier action was attempted, `--yes` can't override it) |

---

## 5. Implementation Status

For tracking against this spec as of v0.6+, update in place, don't append a
new section per version:

| Component | Status |
|---|---|
| Status bar | DONE (incl. session cost readout, ` $0.0012`, hidden when unpriced) |
| Composer (3 mode states) | DONE |
| Mode toast / Tab cycle | DONE |
| Confirm / deny prompts | DONE (confirm-tier bordered panel + inline `✗ denied by policy` lines; red deny-tier panel deferred per §2.12) |
| Compaction ambient + marker | DONE |
| Tool-call timeline | DONE, fixed verb vocabulary (Read/Listed/Grep/Write/Edit/Run) + fixed-width column (2026-09-08); read-only success carries no result line, failures/notices still show, grep counts lift to the call line |
| Diff rendering | DONE, edit diffs (`⎿ +N -M` + numbered hunk, 2026-09-08) and new-file write rendering (`+N (new file)` + plain numbered body, 2026-09-08); video-regression test replays glob/read/edit/re-read end to end |
| Plan-mode banner + todos | DONE, live banner at session start and per Build→Plan demotion, removed on leaving Plan; `● Update Todos` block from live manager state with □/☑, active bold, unchanged-state suppression |
| Handoff panel | DONE |
| Splash screen | DONE |
| Slash command palette | DONE |
| Fuzzy file reference (`@`) | DONE |
| Skill picker | DONE |
| Session resume list | DONE (picker UI exists) |
| Help overlay | DONE |
| Headless flag parity | DONE (`--mode`/`--model`/`--yes`/`--skill` all exist) |
| Plugin/extension marketplace (§2.17) | DONE, unified local registry, compatible JSON/YAML catalogs, explicit install confirmation, path containment, manifest lockfile |
| Structured multi-question prompts (§2.18) | TODO, no current trigger; spec ready for first-run setup |
| Subagent exploration view (§2.19) | DONE (render-state), `⋮ type · model` running rows, `│ task type · model [done|failed]` completions, prose synthesis below batch; no focus model, no toggles |
| Thinking indicator (§2.20) | DONE, live rotating microcopy (2s tick while running) + post-turn `◆ Thought for 3.4s · 38 tok/s` receipt on success (rate omitted when provider tokens unknown; shell escapes receipt-free) |
| Action grouping (§2.20) | DONE, static parent (`● Listed, Read ×N`) + indented children for runs of 2+ batchable read-only calls; lone pairs render ungrouped; no toggle key; worthwhile results render indented under their call line |
| Large-paste collapse (§2.21) | DONE, token + off-screen body, submit-time substitution, Backspace unit-delete, orphan notice, submit-time cap |
| Mouse scroll + drag-select copy (2026-09-08) | DONE, cell-motion tracking, transcript-absolute drag-select with edge autoscroll, clipboard ladder + OSC 52 fallback, `Alt+M` passthrough |
| Image/file paste path (§2.21) | TODO, `@`-reference of an existing image file works today via §2.5; no clipboard-to-file helper documented or built yet |
| Cloud onboarding (§2.24) | DONE (v1), registry + ladder (flag>stored 0600>env), masked /login with validate-before-store, /logout, /model provider switching + catalog listing, budget auto-size unless explicit, command-shaped 401/404 hints; openrouter (free shelf) + gemini (free tier) + opencode Zen (live-verified catalog, chat/completions-only discipline) first-class; splash cloud list derives from registry; deferred: OAuth, keychain, native clients, conditional reachability splash, closest-id 404 suggestion |
| Session export (§2.22) | DONE, `/export [id]` palette + `tilde --export <id> [--out]` headless; distilled scrubbed brief (goal, files, direction, open steps; 0600, cwd-contained) |
| Provider retry/backoff + classified errors (§2.23) | DONE, backoff + Retry-After + fail-fast auth; `provider.Classify` names every model error in the transcript and headless JSON |
| Startup config validation, fail-closed (§2.23) | DONE, sandbox, provider, and policies refuse pre-TUI with file+line; unknown tiers and unknown tool names refuse (exit 2) |
| Crash recovery / unclosed-session resume offer (§2.23) | DONE, panic writes a crash line; next launch hints, and auto-opens the picker when the unclosed session belongs to the cwd |
| Classified exit codes (§2.23) | DONE, headless exits 0/2/3/4/5/1 per the §4 table (config/startup, provider-exhausted, handoff, deny-tier, other) |
