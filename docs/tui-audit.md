# TUI UI/UX audit

> Evidence-based review of tilde's terminal interface.

**Reviewed:** splash, composer, prompt history, slash and file pickers,
approvals, transcript rendering, streamed assistant output, tool results,
thinking state, receipts, status bar, marketplace, mouse selection, resizing,
fallbacks, cancellation, and headless boundaries.

**Assessment:** 86/100 after this pass. The interaction model is coherent and
careful for a coding harness. The main remaining work is accessibility and
terminal-environment coverage, not a visual redesign.

## What is working well

| Area | Evidence |
| --- | --- |
| Hierarchy | The transcript is primary; composer, status, and hints form a stable footer. Splash and Plan use distinct, purposeful emphasis. |
| Input | Enter submits, Ctrl+J inserts a newline, Up/Down preserve prompt history, and large pastes collapse into deletable tokens without losing execution data. |
| Processing | Tool calls and assistant prose stream into the transcript; quiet turns show elapsed status and rotating verbs after a threshold; confirmation waits do not pretend the model is thinking. |
| Output | Tool verbs use fixed columns, read-only bursts group, results stay underneath their call, diffs and new files have distinct rendering, and completion receipts are conditional. |
| Control | Esc returns from history, Esc twice cancels a turn, approvals default to deny, and transcript follow-tail state is preserved while reading older output. |
| Safety UX | Approval panels show the literal action and reason, exact-command approval is restricted to shell calls, errors use text plus symbols, and MCP/extension state remains visible. |
| Responsive layout | Width-aware truncation, centered wide frames, composer growth limits, splash refitting, and tiny-width containment are covered by rendered tests. |
| Consistency | Skills, sessions, marketplace, slash commands, and file references use the same fixed-column list discipline and keyboard vocabulary. |

## Findings fixed

### High — theme contrast

The original palette assumed a dark terminal. Near-white primary text and
light borders were unsafe on light terminal themes. Semantic colors now adapt
to the detected background while preserving the same roles.

### High — selected-row contrast

Selected picker rows previously supplied a background but inherited the
terminal foreground. Selected slash, file, session, skill, and marketplace
rows now set an explicit foreground as well, so selection does not depend on
terminal defaults.

### High — full-screen overlay input leakage

Marketplace was rendered as a full-screen surface but mouse wheel and drag
events could still reach the transcript underneath. Marketplace now owns mouse
events like the other overlays.

### Medium — input discoverability

The persistent hint bar did not state how to submit a prompt. It now exposes
`Enter send`, and the help view includes Enter explicitly.

### Medium — small terminal overflow

Long root names and sandbox diagnostics could exceed the available width at the
floor. The splash now provides a compact truthful card below 32 columns,
hard-cuts the header and status row safely, and has a regression test for
20/24/31-column frames.

### Medium — splash copy wrapping and first-run density

The first-run card used hand-authored line breaks, so a wider terminal still
showed a narrow text island and could split `/login <provider>` as `pro-` /
`vider>`. Splash prose now wraps at word boundaries against the live card
width, and the empty-skills receipt is concise enough to remain useful above
the footer on a normal terminal.

### High — dark-theme guidance contrast

The screenshot review found Bubble's default ANSI-gray placeholder and tilde's
previous dim token were too faint for reliable reading on dark terminals.
Secondary text, hints, metadata, idle borders, and the composer placeholder
now use contrast-safe semantic colors while preserving the primary/secondary
hierarchy. Typed content and safety-critical text remain brighter.

### High — approval surface separation

The approval screenshot showed transcript text bleeding through the bordered
card and the reason running directly into the decision keys. Approval panels
now use an opaque adaptive background, a dedicated `Actions` section, a blank
separator row, and semantic highlighting for approve, deny, reason, and
session-approval keys.

### High — long-output containment

Long command results previously remained visible up to a high global cap and
could dominate the viewport. Tool results now show a compact 12-line preview
with the number of hidden lines and a `Ctrl+O` expansion path. The complete
result is restored in place on demand, while the session log remains complete.

## State-by-state review

### Splash and onboarding

The splash establishes product identity, safety posture, provider guidance,
and the current model/budget. The Plan banner is intentionally separate and
reassuring rather than decorative. The remaining improvement is to make the
first-run help path more prominent on very small terminals, where the compact
card deliberately removes detail.

### Composer and input

The composer is modeless and predictable: text goes to the textarea, `/` and
`@` open contextual pickers, `!` exposes the shell path, and overlays trap
their own keys. It caps height and input size, preserves drafts through
history navigation, and handles bracketed paste as a data-preserving token.

The principal remaining gap is visible capacity feedback: users only learn
that the character ceiling was reached when truncation is reported. A future
pass should add a quiet count near the composer only when the input is near
the limit.

### Processing and thinking

The UI avoids false activity: tool output is the progress signal while tools
run, assistant prose is rendered incrementally when a provider supports
streaming, and rotating reasoning verbs begin only after quiet time. Streamed
prose is provisional until the response completes; if streaming fails, the
preview is removed before the non-streaming retry, so partial text cannot be
duplicated. Hidden `<think>` blocks are filtered from live deltas. The receipt
is deliberately non-expandable and does not expose chain-of-thought. Approval
waits suppress the reasoning indicator because the user—not the model—is the
blocked party.

### Output and transcript

The transcript favors scanability over decoration: stable verbs, grouped
low-signal reads, indented results, explicit failure markers, and width-aware
markdown. Selection copies the same content that is visually selected,
including expansion of collapsed paste bodies.

### Overlays and navigation

Help, sessions, skills, marketplace, login, and approval surfaces have clear
escape paths and keyboard ownership. The marketplace uses one registry with
tabs rather than separate concepts. The remaining gap is consistent empty
state action copy across every picker; some states say what to do next while
others only report that no rows match.

### Color, contrast, and meaning

State is never color-only: mode names, status words, symbols, and layout carry
meaning. The palette now adapts to light/dark terminals. A future accessibility
pass should add deterministic tests for ANSI/256-color profiles and explicit
ASCII glyph fallback, not only `NO_COLOR` color removal.

### Motion and transitions

Motion is intentionally restrained for a terminal: cursor blink, short-lived
toasts, live elapsed time, two-second reasoning cadence, and incremental
assistant rendering. Stream previews replace one block in place, then commit
or roll back atomically; they never leave a trail of per-token rows. There are
no decorative slide transitions or bounce effects competing with output. That
is appropriate for a coding harness; the important transition contract is
state visibility and cancellation, not animation volume.

## Pressure test

| Terminal | Behavior |
| --- | --- |
| 120+ columns | Content is capped and centered to avoid an unreadable full-width status gap. |
| 80×24 | Normal splash, composer, status, and hint fit; long content scrolls in the viewport. |
| 60-column split | Rows truncate rather than wrap; pickers retain their action footer; transcript remains the dominant surface. |
| 20–31 columns | Splash collapses to a compact card and hard-cuts status text without overflow. The app remains truthful but intentionally terse. |

## Verification performed

```sh
GOCACHE=/tmp/tilde-gocache go test -count=1 ./internal/tui/
GOCACHE=/tmp/tilde-gocache go test -race -count=1 ./internal/tui/
GOCACHE=/tmp/tilde-gocache go test -count=1 ./...
go vet ./...
git diff --check
```

The reference review used the supplied TUI, CLI, and agent design material,
plus the local implementations and rendered geometry tests. No visual claim is
treated as verified unless it is backed by a rendered test, a state test, or a
specific implementation path.

## Next focused pass

1. Add near-limit composer capacity feedback.
2. Standardize actionable empty states across all pickers.
3. Add color-profile and ASCII-glyph snapshot coverage.
4. Add a PTY smoke test for resize, Ctrl+C/Esc cancellation, and terminal cleanup.
5. Re-run the audit after browser/vision surfaces become implemented rather than only specified.
