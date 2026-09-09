// Package tui — tilde's terminal UI (docs/tui-design-spec.md is source of truth
// for presentation; behavior lives in docs/Plan.md).
package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"golang.org/x/term"

	"tilde/internal/agent"
	"tilde/internal/creds"
	"tilde/internal/marketplace"
	"tilde/internal/mode"
	"tilde/internal/skills"
	"tilde/internal/update"
)

var (
	borderPlan   = lipgloss.Color("#E5A00D")
	borderBuild  = lipgloss.Color("#E6E6E6")
	borderAuto   = lipgloss.Color("#4FC3F7")
	borderIdle   = lipgloss.Color("#30363D")
	fg           = lipgloss.Color("#E6E6E6")
	fgMuted      = lipgloss.Color("#8B95A6")
	fgDim        = lipgloss.Color("#565F71")
	amber        = lipgloss.Color("#E5A00D")
	accentSelect = lipgloss.Color("#7E22CE")
	fgOnSelect   = lipgloss.Color("#FFFFFF")
	success      = lipgloss.Color("#4CAF50")
	danger       = lipgloss.Color("#F44747")
)

// configurePalette adapts semantic colors to the terminal background. A
// fixed dark-theme foreground is unreadable in light terminals, while a
// fixed light-theme foreground is equally poor on dark ones. Keep the
// semantic roles stable and change only their contrast-safe values.
func configurePalette() {
	if os.Getenv("NO_COLOR") != "" || lipgloss.HasDarkBackground() {
		borderPlan, borderBuild, borderAuto, borderIdle =
			lipgloss.Color("#E5A00D"), lipgloss.Color("#E6E6E6"),
			lipgloss.Color("#4FC3F7"), lipgloss.Color("#30363D")
		fg, fgMuted, fgDim = lipgloss.Color("#E6E6E6"), lipgloss.Color("#8B95A6"), lipgloss.Color("#565F71")
		amber, accentSelect = lipgloss.Color("#E5A00D"), lipgloss.Color("#7E22CE")
		success, danger = lipgloss.Color("#4CAF50"), lipgloss.Color("#F44747")
		fgOnSelect = lipgloss.Color("#FFFFFF")
		return
	}
	borderPlan, borderBuild, borderAuto, borderIdle =
		lipgloss.Color("#9A6700"), lipgloss.Color("#57606A"),
		lipgloss.Color("#0969DA"), lipgloss.Color("#8C959F")
	fg, fgMuted, fgDim = lipgloss.Color("#1F2328"), lipgloss.Color("#57606A"), lipgloss.Color("#6E7781")
	amber, accentSelect = lipgloss.Color("#9A6700"), lipgloss.Color("#0969DA")
	success, danger = lipgloss.Color("#1A7F37"), lipgloss.Color("#CF222E")
	fgOnSelect = lipgloss.Color("#FFFFFF")
}

// Composer and transcript sizing limits. The composer grows with its
// content up to maxComposerRows so a long multiline draft never swallows
// the status bar, and pastes beyond maxInputChars are truncated with a
// notice instead of pushing the whole UI off-screen.
//
// maxAppWidth caps the frame on wide terminals: past this the transcript,
// composer, and status bar stretch into dead space (a full-bleed 200-column
// status gap reads as broken, not spacious), so the frame holds this width
// and is centered as a column instead. Narrow terminals are unaffected —
// the frame still fills them exactly. Kept at 120 so the resize geometry
// tests (widths 100/120) exercise the uncapped path.
const (
	maxComposerRows = 8
	maxInputChars   = 16000
	maxAppWidth     = 120
)

// appVersion is shown on splash and help. It includes the source revision for
// development/post-release builds while retaining the release version.
var appVersion = update.BuildVersion()

// Model is the Bubble Tea app.
type Model struct {
	loop               *agent.Loop
	progPtr            **tea.Program // indirection: the program holds a copy of this Model,
	ta                 textarea.Model
	vp                 viewport.Model
	mdR                *glamour.TermRenderer // cached markdown renderer (see markdown.go)
	mdW                int                   // viewport width mdR was built for
	lines              []string
	curMode            mode.Mode
	running            bool
	confirm            *confirmState
	cancel             context.CancelFunc
	escArmedAt         time.Time // first Esc of a double-Esc interrupt (zero = disarmed)
	root               string
	model              string
	costIn             float64 // cached per-1M USD in-price for m.model (see cost.go)
	costOut            float64 // cached per-1M USD out-price for m.model
	costOK             bool    // false = unknown price → cost readout hidden, never fabricated
	budget             int
	ctx                string      // "41% (13.1k/32k)"
	ctxHot             bool        // true past 80% — status text turns amber, never a popup
	branch             string      // asynchronously refreshed `main [+2]`; never computed during View
	turnStart          time.Time   // zero when idle; drives the Working Ns indicator
	turnDidWork        bool        // any tool_call dispatched this turn — gates the ✓ Done receipt
	turnBaseCompletion int         // provider completion tokens at turn start (per-turn tok/s math)
	groupBuf           []groupItem // consecutive read-only pairs awaiting grouped render
	todoPending        bool        // a todo_write call landed; its result earns the §2.9 block
	lastTodoDigest     string      // digest of the last rendered Update-Todos block (revision, not duplication)
	subPending         []subSpawn  // dispatched spawn_explore/spawn_work awaiting results (§2.19)
	quietSince         time.Time   // last transcript append (or turn start); drives the live reasoning verbs
	verbOrder          []int       // per-turn shuffle of reasonVerbs; empty means natural order
	termW              int         // last terminal width (WindowSizeMsg)
	termH              int         // last terminal height — viewport refits against it
	toastAmber         bool        // demotion toasts share the shape, in amber
	stick              bool        // follow-tail: appends auto-scroll only when true
	// mousePass arms mouse passthrough (Alt+M): cell-motion tracking is
	// paused so drag selection is the terminal's native selection; wheel
	// scrolling pauses with it. Toggled back off to re-arm wheel scroll.
	mousePass bool
	// Drag-select state: anchor/head are transcript-line indexes (not
	// viewport rows) so wheel scrolling mid-drag keeps the highlight
	// glued to the content and a drag can span more than one screenful
	// via edge autoscroll. selActive is true between a left press and
	// its release; selMoved separates a bare click (no copy) from a
	// real drag.
	selAnchor  int
	selHead    int
	selAnchorX int
	selHeadX   int
	selActive  bool
	selMoved   bool
	// pasteEcho retains submitted large-paste bodies keyed to the echo
	// line that carries their token. Display stays collapsed (spec
	// §2.21) but every copy path expands tokens back to the real
	// content — the transcript is a summary view, a copy of it is not.
	pasteEcho map[int][]pasteSeg
	// provider abstraction (spec §2.24): creds is the on-disk key store
	// (nil in tests without BindCreds — env-only), keyOverrides holds
	// --api-key values for this process. keyProvider non-empty arms the
	// masked key-entry overlay.
	creds        *creds.Store
	keyOverrides map[string]string
	keyProvider  string
	keyInput     textinput.Model
	keyErr       string
	// budgetExplicit is set once at startup (main): true when --budget
	// or $TILDE_BUDGET was given. See SetBudgetExplicit in login.go.
	budgetExplicit bool

	// Phase 4 surfaces.
	helpOpen           bool
	resumeOpen         bool
	resumeItems        []SessionItem
	resumeCursor       int
	skillsOpen         bool
	skillsItems        []skills.Skill
	skillsCursor       int
	skillsQuery        string
	marketplaceOpen    bool
	marketplaceItems   marketplace.Registry
	marketplaceCursor  int
	marketplaceQuery   string
	marketplaceTab     int
	marketplacePending *marketplace.Item
	marketplaceDetail  *marketplace.Item
	marketplaceAction  marketplaceAction
	slashOpen          bool
	slashItems         []slashRow
	slashCursor        int
	slashFilter        string // last filter (cursor resets only when it changes)
	atOpen             bool
	atItems            []atRow
	atCursor           int
	atQuery            string
	dismissed          string   // composer text for which pickers stay shut (Esc)
	atFiles            []string // session-cached file list for @ (reloaded on /clear)
	atLoaded           bool
	hist               []string // submitted prompts, oldest first (Up/Down ring)
	histIdx            int      // len(hist) = live draft; below = recalled entry
	histDraft          string   // stashed draft restored at the bottom of Down
	// pasteSegs holds collapsed large pastes: the composer shows only
	// the "[Pasted text #N +M lines]" token while the full body waits
	// here for submit-time substitution (spec §2.21).
	pasteSegs    []pasteSeg
	pasteSeq     int  // per-entry paste counter feeding the #N numbering
	shellArmed   bool // box leads with "!": shell hint row shows, submit takes the shell path
	toast        string
	toastAt      time.Time
	pendingShell string // shell-escape command awaiting confirm
	splashN      int    // transcript line count of the fresh-session splash block
	// planBannerShown records the once-per-session §2.9 banner at session
	// start (interactive sessions open in Plan). Demotion banners bypass
	// it — each Build→Plan drop earns its own, while Plan turns never do.
	planBannerShown bool
	updateNote      string // cached update-available line ("" = none); re-appended on splash refits
	// lastAssistant is the latest assistant prose verbatim (raw markdown,
	// not the Glamour rendering) — the Ctrl+Y copy source.
	lastAssistant string
}

type confirmState struct {
	Tool         string
	Args         map[string]any
	Done         chan bool
	reasonHidden bool
}

// groupItem is one buffered read-only call + its result, waiting to see
// whether siblings follow (grouped parent) or it stands alone (rendered
// exactly as an ungrouped pair).
type groupItem struct {
	call   string
	result string
}

type agentEventMsg agent.Event
type agentDoneMsg struct {
	Text string
	Err  error
	// NoDone suppresses the closing ✓ Done line: agent turns already
	// emitted it as a done event; only non-loop paths (shell escape)
	// need it appended here.
	NoDone bool
}
type showConfirmMsg struct {
	Tool string
	Args map[string]any
	Done chan bool
}

type toastTickMsg struct{}

// verbTickMsg re-renders on a 2s cadence while a turn runs. Renders are
// otherwise message-driven, so without it the Working-seconds counter
// and the reasoning verb would freeze mid-wait on quiet turns. Bounded:
// each tick re-arms only while a turn is still running.
type verbTickMsg struct{}

func tickVerbCmd() tea.Cmd {
	return tea.Tick(reasonEvery, func(time.Time) tea.Msg { return verbTickMsg{} })
}

type compactDoneMsg struct{ Marker string }

// branchRefreshMsg carries the result of the background git status probe.
// Rendering must never start a subprocess: a large repository or a locked
// index must not freeze typing, scrolling, or approval prompts.
type branchRefreshMsg struct{ value string }
type branchRefreshTickMsg struct{}

func branchRefreshCmd(root string) tea.Cmd {
	return func() tea.Msg { return branchRefreshMsg{value: gitBranch(root)} }
}

func branchRefreshTickCmd() tea.Cmd {
	return tea.Tick(10*time.Second, func(time.Time) tea.Msg { return branchRefreshTickMsg{} })
}

// New builds the TUI around a configured loop. The transcript opens with
// the splash screen (fresh sessions only — resume loads history instead).
func New(loop *agent.Loop, m mode.Mode, root, modelName string, budget int) Model {
	configurePalette()
	// Respect the de facto terminal convention without changing the normal
	// adaptive/true-colour profile. This keeps copy/paste logs and monochrome
	// terminals readable while the glyph and border vocabulary still carries
	// state semantically.
	if os.Getenv("NO_COLOR") != "" {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
	ta := textarea.New()
	ta.Focus()
	// Spec §2.3: the composer is a plain single-line box — placeholder
	// text only, left-aligned. No prompt bar, no line numbers; those are
	// editor chrome that competes with the Placeholder signal. It grows
	// with content up to maxComposerRows so a multiline draft never
	// swallows the status bar, and a runaway paste is capped (the
	// truncation posts a transcript notice) instead of flooding rows.
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = maxInputChars
	ta.MaxHeight = maxComposerRows
	ta.SetHeight(1)
	vp := viewport.New(78, 20)
	mdl := Model{loop: loop, ta: ta, vp: vp, curMode: m,
		root: root, model: modelName, budget: budget, stick: true,
		pasteEcho: map[int][]pasteSeg{}}
	mdl.refreshCost()
	mdl.refreshPlaceholder()
	// Fresh sessions open on splash plus, in Plan (the always-cautious
	// default), the one §2.9 read-only banner — once per session, never
	// once per Plan turn.
	mdl.lines = mdl.freshLines(78)
	mdl.planBannerShown = mdl.curMode == mode.Plan
	mdl.splashN = len(mdl.lines)
	mdl.vp.SetContent(strings.Join(mdl.lines, "\n"))
	// A fresh session starts pinned to the tail: the splash+ banner can
	// exceed the placeholder viewport height, and SetContent leaves the
	// offset at zero — without this the session would boot "scrolled up,"
	// breaking Up-recall and the hint bar until the user pressed End.
	mdl.vp.GotoBottom()
	return mdl
}

// refreshPlaceholder swaps the composer hint per mode — a second,
// colorblind-safe signal reinforcing the border color.
func (m *Model) refreshPlaceholder() {
	switch m.curMode {
	case mode.Plan:
		m.ta.Placeholder = "→ Plan, search, build anything"
	case mode.Build:
		m.ta.Placeholder = "→ Build anything"
	case mode.Auto:
		m.ta.Placeholder = "→ Give tilde a goal"
	}
}

// setToast shows a one-frame mode toast; it collapses on its own.
// Every toast starts non-amber — callers that need amber (demotions)
// set it explicitly after this returns.
func (m *Model) setToast(s string) tea.Cmd {
	m.toast, m.toastAt, m.toastAmber = s, time.Now(), false
	return tea.Tick(600*time.Millisecond, func(time.Time) tea.Msg { return toastTickMsg{} })
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, branchRefreshCmd(m.root))
}

// BindProgram wires the running program so background agent events
// can Send messages back onto the render thread. Pass a pointer to the
// variable that will hold the program (it doesn't exist yet at New time).
func (m *Model) BindProgram(pp **tea.Program) { m.progPtr = pp }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case branchRefreshMsg:
		m.branch = msg.value
		return m, branchRefreshTickCmd()
	case branchRefreshTickMsg:
		return m, branchRefreshCmd(m.root)
	case tea.WindowSizeMsg:
		// Resizing must preserve the user's reading position. Re-anchoring
		// every resize to the tail makes a user who is reading history lose
		// their place as soon as a terminal pane changes size.
		followTail := m.stick && m.vp.AtBottom()
		m.termW, m.termH = msg.Width, msg.Height
		// Size from the frame width, not the raw terminal: on wide
		// screens the content column is capped (see maxAppWidth) and
		// View centers it, so the viewport/composer must match the
		// column — never the full bleed.
		m.vp.Width = max(frameWidth(msg.Width)-4, 20)
		m.fitViewport()
		// The composer box adds padding(0,1) + border(2) = 4 columns on
		// top of the textarea width, so size from the viewport (not the
		// window) to land the box exactly on the transcript width.
		m.ta.SetWidth(max(m.vp.Width-4, 20))
		m.syncComposer()
		if m.keyProvider != "" {
			m.keyInput.Width = max(m.vp.Width-6, 24)
		}
		m.stick = followTail
		if m.splashN > 0 && len(m.lines) == m.splashN {
			m.lines = m.freshLines(m.vp.Width)
			if m.updateNote != "" {
				m.lines = append(m.lines, lipgloss.NewStyle().Foreground(fgDim).Render(m.updateNote))
			}
			m.splashN = len(m.lines)
			m.vp.SetContent(strings.Join(m.lines, "\n"))
			m.vp.GotoBottom()
		}
		return m, nil
	case toastTickMsg:
		if m.toast != "" && time.Since(m.toastAt) >= 600*time.Millisecond {
			m.toast, m.toastAmber = "", false
		}
		return m, nil
	case verbTickMsg:
		// Re-render (Working Ns, reasoning verb) and re-arm only while
		// a turn is still running — a finished or cancelled turn lets
		// the loop die silently instead of ticking forever.
		if m.running {
			return m, tickVerbCmd()
		}
		return m, nil
	case compactDoneMsg:
		m.renderEvent(agent.Event{Kind: "compacted", Text: msg.Marker})
		return m, nil
	case showConfirmMsg:
		// Approvals preempt everything: an open overlay must never
		// starve a blocked agent turn of its y/n answer.
		m.helpOpen, m.resumeOpen, m.skillsOpen, m.marketplaceOpen = false, false, false, false
		m.marketplacePending = nil
		m.slashOpen, m.atOpen = false, false
		m.confirm = &confirmState{Tool: msg.Tool, Args: msg.Args, Done: msg.Done}
		return m, nil
	case tea.KeyMsg:
		if m.helpOpen {
			if msg.Type == tea.KeyEsc || msg.String() == "?" {
				m.helpOpen = false
				return m, nil
			}
			if msg.Type == tea.KeyCtrlC {
				return m, tea.Quit // help never traps quit
			}
			return m, nil
		}
		if m.resumeOpen {
			return m.updateResume(msg)
		}
		if m.skillsOpen {
			return m.updateSkills(msg)
		}
		if m.marketplaceOpen {
			if m.marketplacePending != nil {
				return m.updateMarketplaceConfirm(msg)
			}
			if m.marketplaceDetail != nil {
				return m.updateMarketplaceDetail(msg)
			}
			return m.updateMarketplace(msg)
		}
		if m.confirm != nil {
			return m.updateConfirm(msg)
		}
		// The masked login field is a true modal surface. It must receive
		// every key, including Escape, arrows, and bracketed paste, before
		// history, scrolling, pickers, or global shortcuts inspect them.
		if m.overlayKeyEntry() {
			switch msg.Type {
			case tea.KeyEnter:
				return m, validateKeyCmd(m.keyProvider, m.keyInput.Value(), "")
			case tea.KeyEsc:
				m.cancelKeyEntry()
				return m, nil
			case tea.KeyCtrlC:
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.keyInput, cmd = m.keyInput.Update(msg)
			return m, cmd
		}
		// Alt+M toggles mouse passthrough. Every overlay owns all keys
		// (the gates above returned), so the toggle lives below them: it
		// answers only when the composer is the active surface.
		if msg.Alt && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 &&
			(msg.Runes[0] == 'm' || msg.Runes[0] == 'M') {
			return m, m.toggleMouse()
		}
		if m.slashOpen || m.atOpen {
			if mdl, cmd, handled := m.updatePicker(msg); handled {
				return mdl, cmd
			}
			// Typing continues into the composer; fall through.
		}
		// Transcript scrolling: pickers own ↑↓ for row selection, so
		// scroll keys only apply with the composer closed. Appends
		// stick to the tail; scrolling up unpins until the user walks
		// back down (or End). Plain ↑↓ is tried as prompt history first
		// (shell contract) except while reading history — history
		// declines there (and when empty/multiline-inner/past-bottom),
		// and scrolling gets the key.
		if m.navigateHistory(msg) {
			return m, nil
		}
		if handled, cmd := m.updateScroll(msg); handled {
			return m, cmd
		}
		// esc: while reading history it returns to the live tail; at the
		// tail of a running turn it arms the interrupt — a second Esc
		// inside the window cancels (double-Esc replaces Ctrl+C, which
		// collides with copy muscle memory now that selection is native).
		if msg.Type == tea.KeyEsc {
			if !m.vp.AtBottom() {
				m.vp.GotoBottom()
				m.stick = true
				return m, nil
			}
			if m.running {
				if !m.escArmedAt.IsZero() && time.Since(m.escArmedAt) < escArmWindow {
					m.escArmedAt = time.Time{}
					if m.cancel != nil {
						m.cancel()
					}
					return m, nil
				}
				m.escArmedAt = time.Now()
				return m, m.setToast("⏵ Esc again cancels the current turn")
			}
			m.escArmedAt = time.Time{}
		}
		// Bracketed paste: bubbletea delivers the whole chunk as one
		// KeyMsg. Large pastes collapse to a token (spec §2.21), small
		// ones insert inline under the input cap — either way the paste
		// is fully consumed here, never falling through to "?" or the
		// textarea default path.
		if msg.Paste {
			m.insertPaste(string(msg.Runes))
			return m, nil
		}
		// "?" on an empty composer opens the help overlay (spec §2.16).
		// Pickers own every other key; typing keeps flowing into the box.
		if msg.Type == tea.KeyRunes && msg.String() == "?" &&
			m.ta.Length() == 0 && !m.slashOpen && !m.atOpen {
			m.helpOpen = true
			return m, nil
		}
		switch msg.Type {
		case tea.KeyTab:
			if m.running {
				cmd := m.setToast("⏵ Esc twice cancels the current turn")
				return m, cmd
			}
			cmd := m.handleModeCmd(m.curMode.Cycle().String())
			return m, cmd
		case tea.KeyCtrlC:
			// Quit when idle. While a turn runs this deliberately does
			// NOT cancel (that moved to double-Esc — Ctrl+C now collides
			// with copy muscle memory under native selection); it points
			// at the real interrupt instead.
			if m.running {
				return m, m.setToast("⏵ Esc twice cancels the current turn")
			}
			return m, tea.Quit
		case tea.KeyCtrlJ:
			m.ta.InsertString("\n")
			m.syncComposer()
			return m, nil
		case tea.KeyCtrlY:
			// Copy the latest assistant response to the OS clipboard.
			// Placed with Tab/Ctrl+C (after the overlay early-returns),
			// so help/resume/pickers/confirm never see it — and the
			// textarea has no Ctrl+Y binding to fight over.
			return m, m.copyLastResponse()
		case tea.KeyBackspace:
			// Collapsed-paste tokens delete as one unit (see
			// deletePasteToken); anything else falls through to the
			// textarea default below. Picker-open Backspace was already
			// consumed by updatePicker above.
			if m.deletePasteToken() {
				return m, nil
			}
		case tea.KeyEnter:
			return m.submit()
		}
	case loginValidatedMsg:
		return m, m.finishKeyEntry(msg)
	case tea.MouseMsg:
		// Overlays own the screen: a wheel over help / resume / skills /
		// confirm must not scroll the transcript underneath.
		if m.helpOpen || m.resumeOpen || m.skillsOpen || m.marketplaceOpen || m.confirm != nil {
			return m, nil
		}
		// While mouse passthrough is armed (Alt+M) every event is inert:
		// tracking is off, so these can only be leftovers in flight.
		if m.mousePass {
			return m, nil
		}
		// Drag-select owns left-button traffic inside the transcript:
		// press anchors, motion extends the highlight, release copies
		// the selected rows and toasts (never faking it). The toast's
		// dismiss tick rides the returned command — dropping it froze
		// the "Copied selection" toast on screen for good.
		if handled, cmd := m.updateSelection(msg); handled {
			return m, cmd
		}
		// Cell-motion tracking also streams stray motion for other
		// buttons; only wheel ticks scroll the transcript. The composer
		// consumes no mouse events.
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.scrollRows(true, 3)
			return m, nil
		case tea.MouseButtonWheelDown:
			m.scrollRows(false, 3)
			return m, nil
		}
	case agentEventMsg:
		ev := agent.Event(msg)
		if cmd := m.renderEvent(ev); cmd != nil {
			return m, cmd
		}
		return m, nil
	case agentDoneMsg:
		m.running = false
		m.escArmedAt = time.Time{} // a dead turn owns no interrupt arm
		hadTurn := !m.turnStart.IsZero()
		turnElapsed := time.Since(m.turnStart)
		m.turnStart = time.Time{}
		m.cancel = nil
		m.quietSince = time.Time{}
		m.verbOrder = nil
		if msg.Err != nil {
			m.append(lipgloss.NewStyle().Foreground(danger).Render("✗ " + msg.Err.Error()))
		} else if !msg.NoDone {
			m.append(lipgloss.NewStyle().Foreground(success).Render("✓ Done"))
		}
		// Post-turn performance receipt, on success only and at or above
		// the live-verb floor: a successful agent turn closes with its
		// wall time and — when the provider reported output tokens — the
		// generation rate. Below reasonFloor the line is skipped entirely
		// (spec §2.20: a ~1s line on every turn is noise, not signal).
		// Shell escapes never set turnStart, so they stay receipt-free
		// (nothing was generated). Unknown token counts omit the rate
		// rather than inventing one.
		if msg.Err == nil && hadTurn && turnElapsed >= reasonFloor {
			dc := 0
			if m.loop != nil {
				dc = m.loop.TotCompletion - m.turnBaseCompletion
				if dc < 0 {
					dc = 0
				}
			}
			m.append(lipgloss.NewStyle().Foreground(fgDim).Render(thoughtReceipt(turnElapsed, dc)))
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.syncComposer()
	m.refreshPickers()
	return m, cmd
}

// maxHistoryEntries caps the prompt ring: sessions run long, and an
// unbounded recall buffer is just scrollback with extra steps.
const maxHistoryEntries = 200

// arrowsScroll reports the TILDE_ARROWS=scroll contract: arrow keys
// scroll the transcript instead of walking prompt history. Some stacks
// deliver wheel motion as arrow keys (tmux alternate-scroll and friends
// emit bytes identical to real presses — no app-layer test can tell a
// wheel tick from a keypress), which makes history-on-arrows unusable
// from the live tail: every upward tick recalls instead of scrolling.
// Default stays history (shell contract); unparseable values fail open
// to it. Scroll mode keeps the ring alive two narrow ways — Up with a
// non-empty box still recalls (draft completion), and Down while
// navigating still walks back — so no second keybind is needed.
func arrowsScroll() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TILDE_ARROWS")), "scroll")
}

// pushHistory records one submitted prompt for Up/Down recall.
// Consecutive duplicates collapse (holding Enter on one command must not
// fill the ring with itself); navigation re-arms at the bottom.
func (m *Model) pushHistory(s string) {
	if s == "" {
		return
	}
	if n := len(m.hist); n == 0 || m.hist[n-1] != s {
		m.hist = append(m.hist, s)
		if len(m.hist) > maxHistoryEntries {
			m.hist = m.hist[len(m.hist)-maxHistoryEntries:]
		}
	}
	m.histIdx = len(m.hist)
	m.histDraft = ""
}

// navigateHistory drives prompt recall on plain Up/Down — the shell
// contract (Up = last command), with the live draft stashed at index
// len(hist) so typing is never lost and Down restores it verbatim.
// TILDE_ARROWS=scroll flips the default for wheel-as-arrows stacks (see
// arrowsScroll): then only non-empty Up and navigating Down recall.
// Pickers own ↑↓ while open. Reading wins over recalling: a scrolled-up
// viewport owns both keys (wheel motion arrives as arrow keys through
// tmux alternate-scroll and friends — without this, scrolling would
// cycle prompts). Within a multiline draft, arrows recall only from the
// edge row in their direction (Up on row 0, Down on the last row);
// inner rows keep arrow editing. Anything else, an empty ring, or Down
// already at the bottom declines (false) and transcript scrolling still
// gets the key. SetValue lands the cursor at the end of recalled text.
func (m *Model) navigateHistory(msg tea.KeyMsg) bool {
	if m.slashOpen || m.atOpen {
		return false
	}
	var dir int
	switch msg.Type {
	case tea.KeyUp:
		dir = -1
	case tea.KeyDown:
		dir = 1
	default:
		return false
	}
	if len(m.hist) == 0 {
		return false
	}
	if !m.vp.AtBottom() {
		return false
	}
	if arrowsScroll() {
		// Scroll contract: the view owns the arrows. Empty-box Up and
		// bottomed-out Down decline to transcript scrolling; the two
		// ring-preserving exceptions fall through to the shared paths
		// below (non-empty Up recalls, navigating Down walks).
		if dir > 0 && m.histIdx >= len(m.hist) {
			return false
		}
		if dir < 0 && strings.TrimSpace(m.ta.Value()) == "" {
			return false
		}
	}
	if dir < 0 {
		if m.ta.Line() != 0 {
			return false
		}
		if m.histIdx == len(m.hist) {
			m.histDraft = m.ta.Value()
		}
		if m.histIdx == 0 {
			return true // top of ring: hold position, swallow the key
		}
		m.histIdx--
		m.ta.SetValue(m.hist[m.histIdx])
	} else {
		if m.histIdx >= len(m.hist) {
			return false
		}
		if m.ta.Line() != m.ta.LineCount()-1 {
			return false
		}
		m.histIdx++
		if m.histIdx == len(m.hist) {
			m.ta.SetValue(m.histDraft)
		} else {
			m.ta.SetValue(m.hist[m.histIdx])
		}
	}
	m.syncComposer()
	m.refreshPickers()
	return true
}

// updateScroll routes transcript-navigation keys. Pickers own ↑↓ (they
// handle those before this runs), so plain ↑↓ here means no picker is
// open. Shift+↑/↓ always scroll — even from inside a multiline draft.
// Pointer receiver: scroll position and the follow-tail pin must
// survive back into the caller's model.
func (m *Model) updateScroll(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch msg.Type {
	case tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd,
		tea.KeyShiftUp, tea.KeyShiftDown, tea.KeyCtrlU, tea.KeyCtrlD:
		// always transcript navigation
	case tea.KeyUp, tea.KeyDown:
		if m.ta.LineCount() > 1 {
			return false, nil // multiline draft: arrows edit the draft first
		}
	default:
		return false, nil
	}
	switch {
	case msg.Type == tea.KeyPgUp || msg.Type == tea.KeyCtrlU:
		m.scrollRows(true, m.vp.Height/2)
	case msg.Type == tea.KeyPgDown || msg.Type == tea.KeyCtrlD:
		m.scrollRows(false, m.vp.Height/2)
	case msg.Type == tea.KeyHome:
		m.stick = false
		m.vp.SetYOffset(0)
	case msg.Type == tea.KeyEnd:
		m.vp.GotoBottom()
		m.stick = true
	case msg.Type == tea.KeyShiftUp || msg.Type == tea.KeyUp:
		m.scrollRows(true, 1)
	case msg.Type == tea.KeyShiftDown || msg.Type == tea.KeyDown:
		m.scrollRows(false, 1)
	default:
		return false, nil
	}
	return true, nil
}

// scrollRows moves the transcript n rows with the shared pin discipline:
// upward unpins the follow-tail (a later append must not yank the view
// down), downward re-pins only at the very bottom. Keyboard scrolling
// and mouse-wheel scrolling land here so both behave identically.
func (m *Model) scrollRows(up bool, n int) {
	if n <= 0 {
		n = 1
	}
	if up {
		m.stick = false
		m.vp.ScrollUp(n)
	} else {
		m.vp.ScrollDown(n)
		m.stick = m.vp.AtBottom()
	}
}

// fitViewport sizes the transcript viewport so the whole frame lands
// exactly on the terminal height — chrome pinned to the bottom edge, no
// dead rows beneath the hint bar. Frame rows = viewport + composer box
// (ta.Height+2) + spacer + status + spacer + hint, so viewport takes the
// remainder. Dropdowns, toasts, and the confirm panel overflow upward
// past the top (transcript scrolls, nothing breaks) and the fit restores
// when they close. Called on resize and whenever the composer height
// changes.
func (m *Model) fitViewport() {
	if m.termH <= 0 {
		return
	}
	height := m.termH - 6 - m.ta.Height()
	if m.confirm != nil {
		// The approval panel replaces the one-line hint bar and adds a
		// wrapped bordered block. Reserve its rendered height here so the
		// action keys remain visible on small terminals.
		panel := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(borderPlan).Padding(1, 1).
			Render(confirmFooter(m.confirm.Tool, m.confirm.Args, m.vp.Width, m.confirm.reasonHidden))
		height -= strings.Count(panel, "\n") + 1
	}
	m.vp.Height = max(height, 1)
}

// syncComposer keeps the textarea's height in step with its content
// (1..maxComposerRows) so a long multiline draft grows the box instead
// of silently swallowing the status bar. The CharLimit + paste guard in
// Update caps runaway input before this runs.
func (m *Model) syncComposer() {
	h := min(max(m.ta.LineCount(), 1), maxComposerRows)
	if h != m.ta.Height() {
		m.ta.SetHeight(h)
		m.fitViewport() // composer growth steals rows from the transcript
	}
}

// toggleMouse flips mouse passthrough (Alt+M). With tracking paused the
// terminal regains its native selection — drag to select, Ctrl+Shift+C
// (or the plain Ctrl+C many terminals map to copy) to copy — while wheel
// scrolling pauses with it. Toggling back re-arms cell-motion tracking so
// the wheel scrolls the transcript again. Runtime commands (not startup
// options) so the sequence lands through the renderer.
func (m *Model) toggleMouse() tea.Cmd {
	m.mousePass = !m.mousePass
	if m.mousePass {
		// Mouse command first: the renderer effect must land
		// immediately, not behind setToast's 600ms dismiss tick.
		return tea.Sequence(
			tea.DisableMouse,
			m.setToast("mouse passthrough: drag to select · Alt+M re-arms wheel scroll"),
		)
	}
	return tea.Sequence(
		tea.EnableMouseCellMotion,
		m.setToast("wheel scroll re-armed · Alt+M pauses it for native selection"),
	)
}

// updateSelection runs the in-app drag-select: press anchors, motion
// extends, release copies the selected transcript characters to the clipboard
// (OSC 52 included, so it works without xclip) and toasts. Anchor and
// head are transcript-absolute lines, so wheel scrolling mid-drag keeps
// the highlight on the same content and drags past the screen edge
// autoscroll — a turn longer than the viewport is selectable in one
// gesture. Reports whether the event was consumed, plus the toast's
// dismiss command when one fired — wheel and stray-button events fall
// through to scrolling.
func (m *Model) updateSelection(msg tea.MouseMsg) (bool, tea.Cmd) {
	x := m.transcriptX(msg.X)
	switch {
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		if msg.Y >= 0 && msg.Y < m.vp.Height && len(m.lines) > 0 {
			m.selActive = true
			m.selMoved = false
			m.selAnchor = min(m.vp.YOffset+msg.Y, len(m.lines)-1)
			m.selHead = m.selAnchor
			m.selAnchorX = x
			m.selHeadX = m.selAnchorX
		}
		return true, nil
	case msg.Action == tea.MouseActionMotion && m.selActive:
		switch {
		case msg.Y <= 0 && m.vp.YOffset > 0:
			// Autoscroll up: hold the drag at the top edge.
			m.scrollRows(true, 2)
			m.selHead = m.vp.YOffset
			m.selHeadX = x
		case msg.Y >= m.vp.Height-1 && !m.vp.AtBottom():
			// Autoscroll down: hold the drag at the bottom edge.
			m.scrollRows(false, 2)
			m.selHead = min(m.vp.YOffset+m.vp.Height-1, len(m.lines)-1)
			m.selHeadX = x
		default:
			m.selHead = min(m.vp.YOffset+msg.Y, len(m.lines)-1)
			m.selHeadX = x
		}
		if m.selHead != m.selAnchor || m.selHeadX != m.selAnchorX {
			m.selMoved = true
		}
		return true, nil
	case msg.Action == tea.MouseActionRelease:
		wasActive := m.selActive
		m.selActive = false
		if wasActive && m.selMoved {
			if text := m.selectedText(); text != "" {
				return true, m.copySelected(text)
			}
		}
		return true, nil
	}
	return false, nil
}

// transcriptX converts terminal coordinates to the left-aligned transcript
// column. Wide terminals center the capped frame; without this correction a
// drag would copy characters several columns to the right of the pointer.
func (m Model) transcriptX(x int) int {
	if m.termW > maxAppWidth && m.vp.Width > 0 {
		x -= max((m.termW-m.vp.Width)/2, 0)
	}
	return max(x, 0)
}

// selectedText returns the cell range between the drag endpoints.
// Anchor and head are transcript-absolute indexes, so a selection may span
// more than one screenful. Paste tokens expand and ANSI is stripped.
func (m *Model) selectedText() string {
	total := len(m.lines)
	startRow, startX, endRow, endX := m.selAnchor, m.selAnchorX, m.selHead, m.selHeadX
	if startRow > endRow || (startRow == endRow && startX > endX) {
		startRow, startX, endRow, endX = endRow, endX, startRow, startX
	}
	lo, hi := startRow, endRow
	if lo < 0 {
		lo = 0
	}
	if lo >= total {
		return ""
	}
	if hi >= total {
		hi = total - 1
	}
	out := make([]string, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		line := m.copyLineForm(i)
		lineWidth := ansi.StringWidth(line)
		from, to := 0, lineWidth
		if i == startRow {
			from = min(max(startX, 0), lineWidth)
		}
		if i == endRow {
			to = min(max(endX, 0), lineWidth)
		}
		if from > to {
			from, to = to, from
		}
		out = append(out, ansi.Cut(line, from, to))
	}
	return strings.TrimRight(strings.Join(out, "\n"), " \t\n")
}

// copyLineForm renders transcript line i for clipboard use: collapsed-
// paste tokens on a user echo expand back to the stored body — the
// transcript displays the §2.21 placeholder, a copy of it carries the
// real content. Bodies are keyed by the echo's physical line, so token
// renumbering across turns can never cross-wire two pastes.
func (m *Model) copyLineForm(i int) string {
	ln := stripANSI(m.lines[i])
	for _, seg := range m.pasteEcho[i] {
		ln = strings.ReplaceAll(ln, seg.token, seg.body)
	}
	return ln
}

// copySelected ships a drag-selection the same ladder as every other
// copy path: OS clipboard helper first, OSC 52 (the terminal sets its
// own clipboard) as the no-helper fallback, honest failure last.
// Returns the toast's dismiss tick — dropping it freezes the toast on
// screen for good.
func (m *Model) copySelected(text string) tea.Cmd {
	if err := clipboardWrite(text); err == nil {
		return m.setToast("✓ Copied selection to clipboard")
	}
	if osc52Write(text) {
		return m.setToast("✓ Copied selection via OSC 52 (terminal clipboard)")
	}
	cmd := m.setToast("clipboard unavailable — install xclip or xsel, then retry")
	m.toastAmber = true
	return cmd
}

// selectionRange returns the half-open cell range selected on an absolute
// transcript row. Rendering this range makes the visual selection agree with
// the text that will actually be copied.
func (m *Model) selectionRange(row int) (int, int, bool) {
	if !m.selActive || len(m.lines) == 0 {
		return 0, 0, false
	}
	startRow, startX, endRow, endX := m.selAnchor, m.selAnchorX, m.selHead, m.selHeadX
	if startRow > endRow || (startRow == endRow && startX > endX) {
		startRow, startX, endRow, endX = endRow, endX, startRow, startX
	}
	if row < startRow || row > endRow {
		return 0, 0, false
	}
	lineLen := ansi.StringWidth(m.copyLineForm(row))
	from, to := 0, lineLen
	if row == startRow {
		from = min(max(startX, 0), lineLen)
	}
	if row == endRow {
		to = min(max(endX, 0), lineLen)
	}
	if from > to {
		from, to = to, from
	}
	return from, to, from != to || startRow != endRow
}

// selVisible is retained for callers that only need to know whether a row is
// part of the live selection (tests and lightweight layout checks).
func (m *Model) selVisible(y int) bool {
	if !m.selActive {
		return false
	}
	return y >= min(m.selAnchor, m.selHead) && y <= max(m.selAnchor, m.selHead)
}

// pasteSeg is one collapsed large paste: token is the bracket text
// sitting in the composer, body the full pasted content.
type pasteSeg struct {
	seq   int
	token string
	body  string
}

// pasteCollapseLines governs the large-paste collapse threshold (spec
// §2.21): TILDE_PASTE_LINES lines or more collapses, default 4, and 0
// disables collapsing entirely. Unparseable values fall back to the
// default — a display threshold must never break input.
func pasteCollapseLines() int {
	if v := strings.TrimSpace(os.Getenv("TILDE_PASTE_LINES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 4
}

// pasteLineCount counts content lines (a trailing newline is not a line).
func pasteLineCount(s string) int {
	return len(strings.Split(strings.TrimRight(s, "\n"), "\n"))
}

// insertPaste handles one bracketed paste: large pastes collapse to a
// deletable-as-unit token (body stored off-screen), small ones insert
// inline under the pre-existing input cap. Always consumes the paste.
func (m *Model) insertPaste(text string) {
	lines := pasteLineCount(text)
	if n := pasteCollapseLines(); n > 0 && strings.TrimSpace(text) != "" &&
		(lines >= n || len([]rune(text)) >= 1000) {
		m.pasteSeq++
		tok := fmt.Sprintf("[Pasted text #%d +%d lines]", m.pasteSeq, lines)
		m.pasteSegs = append(m.pasteSegs, pasteSeg{seq: m.pasteSeq, token: tok, body: text})
		m.ta.InsertString(tok)
	} else if room := max(maxInputChars-m.ta.Length(), 0); len([]rune(text)) > room {
		// Cap it before the textarea sees it — CharLimit would silently
		// drop the tail, and an unbounded paste would flood the composer
		// with thousands of wrapped rows. Say so instead.
		m.ta.InsertString(string([]rune(text)[:room]))
		m.append(lipgloss.NewStyle().Foreground(fgDim).Render(
			fmt.Sprintf("● large paste truncated to %s chars — send it, then continue in a follow-up.", commaInt(room))))
	} else {
		m.ta.InsertString(text)
	}
	m.syncComposer()
	m.refreshPickers()
}

// expandPastes substitutes collapsed tokens with their stored bodies
// for execution: the model always receives the real content in full.
func (m *Model) expandPastes(s string) string {
	for _, seg := range m.pasteSegs {
		s = strings.ReplaceAll(s, seg.token, seg.body)
	}
	return s
}

// deletePasteToken removes a whole collapsed-paste token when Backspace
// lands right after one (suffix match — the just-pasted case). Tokens
// are bracketed display text, not real content: eating them
// character-by-character would strand the stored body, so they delete
// as one unit or fall through to normal editing. Mid-line tokens edit
// as plain text; bodies orphaned that way are reported at submit, never
// silently dropped.
func (m *Model) deletePasteToken() bool {
	if len(m.pasteSegs) == 0 {
		return false
	}
	val := m.ta.Value()
	for i, seg := range m.pasteSegs {
		if strings.HasSuffix(val, seg.token) {
			m.ta.SetValue(strings.TrimSuffix(val, seg.token))
			m.pasteSegs = append(m.pasteSegs[:i], m.pasteSegs[i+1:]...)
			m.syncComposer()
			m.refreshPickers()
			return true
		}
	}
	return false
}

// submit handles one Enter on the composer: slash command, shell escape,
// @-accept, or a fresh agent turn.
func (m Model) submit() (tea.Model, tea.Cmd) {
	if m.running {
		return m, nil
	}
	goal := strings.TrimSpace(m.ta.Value())
	if goal == "" {
		return m, nil
	}
	// A new turn takes the eye to the live tail: submitting re-pins the
	// viewport even if the user had scrolled up reading (the turn they
	// just started is what they will want to watch).
	m.vp.GotoBottom()
	m.stick = true
	// Turn-boundary padding: every query after the session's first starts
	// with one blank row, so a new prompt never sits flush against the
	// previous turn's closing receipt (this also gives the post-turn ◆
	// Thought line its bottom air — no receipt-side change needed).
	// Fresh sessions (splash untouched) and cleared views need none.
	if len(m.lines) > 0 && (m.splashN <= 0 || len(m.lines) > m.splashN) {
		m.append("")
	}
	// Every submitted prompt — agent goals, shell escapes, slash
	// commands alike — joins the Up/Down ring.
	// Resolve collapsed pastes to the real payload the model executes;
	// the transcript echo below keeps the placeholder form (spec §2.21).
	// The ring stores the real payload too, so a recalled prompt
	// re-sends exactly what ran (tokens never outlive their entry).
	sendGoal := m.expandPastes(goal)
	// Bodies orphaned by mid-token edits are reported, never silently
	// dropped: without its token the body would vanish from the turn.
	for _, seg := range m.pasteSegs {
		if !strings.Contains(goal, seg.token) {
			m.append(lipgloss.NewStyle().Foreground(fgDim).Render(
				fmt.Sprintf("● pasted content #%d was edited away and dropped — re-paste to include it.", seg.seq)))
		}
	}
	// The input cap binds the real payload, not the token text.
	if len([]rune(sendGoal)) > maxInputChars {
		sendGoal = string([]rune(sendGoal)[:maxInputChars])
		m.append(lipgloss.NewStyle().Foreground(fgDim).Render(
			fmt.Sprintf("● large paste truncated to %s chars — send it, then continue in a follow-up.", commaInt(maxInputChars))))
	}
	// Retain the paste bodies (keyed to the echo line appended below) so
	// copy paths can expand tokens back to real content. The display
	// keeps the collapsed token (§2.21); only copies expand.
	echoSegs := m.pasteSegs
	m.pasteSegs = nil
	m.pasteSeq = 0
	m.pushHistory(sendGoal)
	armed := m.shellArmed
	m.ta.Reset()
	m.shellArmed = false
	m.refreshPlaceholder()
	m.syncComposer()
	if m.slashOpen && len(m.slashItems) > 0 {
		sel := m.slashItems[min(m.slashCursor, len(m.slashItems)-1)]
		m.dismissed = ""
		m.slashOpen = false
		m.atOpen = false
		cmd, args := parseSlash(sendGoal)
		slashCmd := m.runSlash(cmd, args, sel)
		return m, slashCmd
	}
	m.dismissed = ""
	m.slashOpen, m.atOpen = false, false
	if strings.HasPrefix(sendGoal, "/") {
		cmd, args := parseSlash(sendGoal)
		slashCmd := m.runSlash(cmd, args, slashRow{})
		return m, slashCmd
	}
	if armed || strings.HasPrefix(sendGoal, "!") {
		m.shellEscape(strings.TrimSpace(strings.TrimPrefix(sendGoal, "!")))
		return m, nil
	}
	if q, ok := activeAtQuery(sendGoal); ok {
		// @-reference left unaccepted (no matches): the agent gets raw
		// text rather than a silent drop — never swallow user input.
		_ = q
	}
	m.append(lipgloss.NewStyle().Foreground(m.modeColor()).Render("→ ") + ansi.Strip(goal))
	// Retain the paste bodies keyed to the echo line just appended, so
	// copy paths can expand tokens back to real content later. The
	// display keeps the collapsed token (§2.21); only copies expand.
	if len(echoSegs) > 0 {
		if m.pasteEcho == nil {
			m.pasteEcho = map[int][]pasteSeg{}
		}
		m.pasteEcho[len(m.lines)-1] = echoSegs
	}
	// Breathing room between the user query and whatever answers it
	// (tools or prose): one blank row, part of the turn's rhythm.
	m.append("")
	m.running = true
	m.turnDidWork = false // a fresh turn has done nothing yet; tool calls set it
	m.turnStart = time.Now()
	if m.loop != nil {
		m.turnBaseCompletion = m.loop.TotCompletion
	}
	m.quietSince = m.turnStart
	m.verbOrder = shuffleVerbs(rand.New(rand.NewSource(time.Now().UnixNano())))
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	// The verb tick rides alongside the agent command so long model
	// waits animate instead of freezing; it stops itself at done.
	return m, tea.Batch(m.runAgentCmd(ctx, sendGoal), tickVerbCmd())
}

func (m Model) runAgentCmd(ctx context.Context, goal string) tea.Cmd {
	loop := m.loop
	pp := m.progPtr // shared pointer; program exists by the time this runs
	return func() tea.Msg {
		defer func() {
			if m.cancel != nil {
				m.cancel()
			}
		}()
		send := func(msg tea.Msg) {
			if pp != nil && *pp != nil {
				(*pp).Send(msg)
			}
		}
		loop.Cfg.AskUser = func(tool string, args map[string]any) bool {
			done := make(chan bool, 1)
			if pp == nil || *pp == nil {
				return false // no UI to ask through: fail closed
			}
			(*pp).Send(showConfirmMsg{Tool: tool, Args: args, Done: done})
			ok, open := <-done
			return open && ok
		}
		text, err := loop.Run(ctx, goal, func(e agent.Event) {
			send(agentEventMsg(e))
		})
		return agentDoneMsg{Text: text, Err: err, NoDone: true}
	}
}

// append adds one already-styled line to the transcript and refreshes the
// viewport. Follow-tail: auto-scroll only when the user is at (or was
// pinned to) the bottom — reading history must not be yanked away by
// streaming events. Content is soft-wrapped to the viewport width so the
// scroll geometry matches what's on screen.
func (m *Model) append(s string) {
	stick := m.stick || m.vp.AtBottom()
	m.quietSince = time.Now() // any transcript activity resets the reasoning-verb clock
	for _, ln := range strings.Split(s, "\n") {
		m.lines = append(m.lines, wrapLine(ln, m.vp.Width-1))
	}
	m.vp.SetContent(strings.Join(m.lines, "\n"))
	if stick {
		m.vp.GotoBottom()
	}
}

// clipboardWrite pushes text to the OS clipboard (xclip/xsel on Linux).
// A package var so tests can stub it — headless CI has no X server, and
// a copy path must be verified without one.
var clipboardWrite = clipboard.WriteAll

// osc52Write emits an OSC 52 clipboard-set sequence straight to the
// terminal device: the terminal itself copies the payload — no helper
// binary, works in kitty/WezTerm/alacritty and over SSH (tmux needs
// `set -g set-clipboard on`). The renderer owns stdout, so the sequence
// goes to /dev/tty instead — same terminal, separate fd, written
// synchronously while the UI sits idle and never interleaved with a
// frame write. Empty and oversized payloads (common terminal limits)
// decline rather than truncating silently. A package var so tests can
// observe and stub it.
var osc52Write = func(s string) bool {
	if s == "" || len(s) > 56000 {
		return false
	}
	seq := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\x07"
	if f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		_, _ = f.WriteString(seq)
		_ = f.Close()
		return true
	}
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Print(seq)
		return true
	}
	return false
}

// copyLastResponse copies the latest assistant prose to the OS clipboard.
// Raw markdown (not the Glamour rendering) so pasting elsewhere keeps
// the formatting markers. Empty (no response yet) reports; helper-binary
// failure falls back to OSC 52 (the terminal copies its own clipboard —
// no xclip needed), and only when both refuse is the honest failure shown.
func (m *Model) copyLastResponse() tea.Cmd {
	if strings.TrimSpace(m.lastAssistant) == "" {
		return m.setToast("nothing to copy yet — no assistant response this session")
	}
	if err := clipboardWrite(m.lastAssistant); err == nil {
		return m.setToast("✓ Copied response to clipboard")
	}
	if osc52Write(m.lastAssistant) {
		return m.setToast("✓ Copied via OSC 52 (terminal clipboard)")
	}
	cmd := m.setToast("clipboard unavailable — install xclip or xsel, then retry")
	m.toastAmber = true
	return cmd
}

// copyLines copies transcript lines to the clipboard (/copy [n]). With
// no argument the whole transcript ships; a positive n copies that one
// visible line. Every line renders in copy form (collapsed-paste tokens
// expand to real content) and ANSI styling is stripped — clipboards
// want plain text, and pasted escape codes wreck other apps' inputs.
// Assistant prose keeps its own path (Ctrl+Y): raw markdown, verbatim.
// Returns the toast's dismiss tick — dropping it freezes the toast.
func (m *Model) copyLines(arg string) tea.Cmd {
	start, end := 0, len(m.lines)-1
	if n, err := strconv.Atoi(strings.TrimSpace(arg)); err == nil && n > 0 {
		if n > len(m.lines) {
			m.append(fmt.Sprintf("✗ /copy %d: the transcript has %d lines.", n, len(m.lines)))
			return nil
		}
		start, end = n-1, n-1
	}
	out := make([]string, 0, max(end-start+1, 0))
	for i := start; i <= end; i++ {
		out = append(out, m.copyLineForm(i))
	}
	text := strings.TrimRight(strings.Join(out, "\n"), " \t\n")
	if strings.TrimSpace(text) == "" {
		m.append("✗ /copy: nothing to copy — the transcript is empty.")
		return nil
	}
	if err := clipboardWrite(text); err == nil {
		return m.setToast("✓ Copied transcript to clipboard")
	}
	if osc52Write(text) {
		return m.setToast("✓ Copied transcript via OSC 52 (terminal clipboard)")
	}
	cmd := m.setToast("clipboard unavailable — install xclip or xsel, then retry")
	m.toastAmber = true
	return cmd
}

// commaInt formats 16000 as "16,000" for notices.
func commaInt(n int) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var b []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			b = append(b, ',')
		}
		b = append(b, c)
	}
	return string(b)
}

func (m *Model) renderEvent(e agent.Event) tea.Cmd {
	// Read-only grouping: consecutive groupable call/result pairs buffer
	// here and flush as one parent + indented children on the next
	// anything-else (the loop always emits call then result back to back,
	// so pairing is structural — a result without a preceding buffered
	// call, e.g. a deny, flushes and renders normally instead).
	if e.Kind == "tool_call" {
		// Any dispatched call marks the turn as real work, arming the
		// closing ✓ Done receipt (a pure chat reply leaves it disarmed).
		m.turnDidWork = true
		if verb, _ := splitVerb(e.Text); verb == "todo_write" {
			// A todo_write call arms the §2.9 block for its result: the
			// structured list renders from live manager state, never
			// from parsing the result text (see todoSnapshot).
			m.todoPending = true
		}
		if verb, _ := splitVerb(e.Text); isSubagentSpawn(verb) {
			// Subagent spawns never join the read-only group: flush any
			// buffered pairs first so the ⋮ row landmarks a clean break.
			m.flushGroup()
			m.pushSubagentRun(verb, e.Text)
			return nil
		}
		if verb, _ := splitVerb(e.Text); agent.ParallelSafe(verb) {
			m.groupBuf = append(m.groupBuf, groupItem{call: e.Text})
			return nil
		}
		m.flushGroup()
		m.renderCallLine(e.Text, false, "")
		return nil
	}
	if e.Kind == "tool_result" {
		// A pending spawn owns the next result: it arrives strictly
		// back to back with its call, so FIFO pop is exact — and a
		// spawn result must never be swallowed into a read-only group.
		if len(m.subPending) > 0 {
			m.flushGroup()
			m.completeSubagentRun(e.Text)
			return nil
		}
		if n := len(m.groupBuf); n > 0 && m.groupBuf[n-1].result == "" {
			m.groupBuf[n-1].result = e.Text
			return nil
		}
		m.flushGroup()
		m.append(renderToolResult(e.Text))
		// A todo_write result keeps its raw rendering (audit trail) and
		// then earns the structured §2.9 block from live manager state —
		// skipped when the state hasn't changed since the last block.
		m.maybeAppendTodoBlock()
		return nil
	}
	m.flushGroup()
	switch e.Kind {
	case "assistant":
		// Agent prose goes through Glamour (spec stack) so markdown
		// renders instead of leaking raw asterisks into the transcript.
		// The raw text is also kept as the Ctrl+Y copy source.
		m.lastAssistant = e.Text
		m.append(m.renderMarkdown(e.Text, m.vp.Width-1))
	case "thinking":
		// Model reasoning trace: dim under ◇ (spec §1.2 — ◇ is thinking
		// the model actually emitted, never the ◆ timing receipt).
		// Long traces clamp like tool results; the session log keeps
		// the full text for audit.
		m.append(renderThinking(e.Text))
	case "handoff":
		// Red-bordered panel (spec §2.15): a failed state, not a pending
		// decision — danger border, three facts, no stack trace inline.
		panel := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(danger).Padding(0, 1).Render(
			"Handoff to Plan\n" +
				"✗ " + firstLine(e.Text) + "\n" +
				"Reverting to Plan mode. Nothing further will be changed.\n" +
				"Partial diff and full history are preserved below.")
		for _, ln := range strings.Split(panel, "\n") {
			m.append(ln)
		}
		m.curMode = mode.Plan
		m.loop.SetMode(mode.Plan)
		m.refreshPlaceholder()
		// Automatic demotions use the same toast shape as Tab, amber with
		// a one-line reason — a silent mode change erodes trust fastest.
		reason := e.Text
		if i := strings.Index(reason, "—"); i >= 0 {
			reason = strings.TrimSpace(reason[:i])
		}
		if len(reason) > 80 {
			reason = reason[:77] + "…"
		}
		cmd := m.setToast("⏵ Mode: Build → Plan (" + reason + ")")
		m.toastAmber = true
		return cmd
	case "system":
		m.append(lipgloss.NewStyle().Foreground(fgMuted).Render("● [system] " + ansi.Strip(e.Text)))
	case "done":
		// Receipt for real work only: a plain chat reply ending the turn
		// needs no applause line — Done is for tool calls, commands, and
		// edits. (Shell escapes bypass this: they always ran a command.)
		if m.turnDidWork {
			m.append(lipgloss.NewStyle().Foreground(success).Render("✓ Done"))
		}
	case "usage":
		m.ctx, m.ctxHot = e.Text, e.Pct >= 80
	case "compacted":
		// Receipt, not a summary to read in place — always one dim line.
		m.append(lipgloss.NewStyle().Foreground(fgDim).Render("● " + e.Text))
	}
	return nil
}

// renderCallLine renders one tool-call row in the fixed vocabulary
// (spec §1.2: muted glyph, full-bright verb, muted target; spec §2.10:
// the verb is a fixed-width left-aligned column). Grouped children
// trade the glyph for a 2-space indent — same words, quieter gutter.
// Callers append through m.append so wrapping still applies.
func (m *Model) renderCallLine(text string, grouped bool, extra string) {
	text = ansi.Strip(text)
	verb, rest := splitVerb(text)
	disp := toolDisplayVerb(verb)
	if len(disp) < verbWidth {
		disp += strings.Repeat(" ", verbWidth-len(disp))
	}
	prefix := lipgloss.NewStyle().Foreground(fgMuted).Render("● ")
	if grouped {
		prefix = "  "
	}
	m.append(prefix +
		lipgloss.NewStyle().Foreground(fg).Render(disp) +
		lipgloss.NewStyle().Foreground(fgMuted).Render(rest+extra))
}

// toolDisplayVerb maps raw tool names to the fixed transcript
// vocabulary (spec §2.10): Read, Listed, Grep, Write, Edit, Run.
// Anything outside the six keeps its raw name — the vocabulary covers
// the hot path, not every future tool.
func toolDisplayVerb(raw string) string {
	switch raw {
	case "read_file":
		return "Read"
	case "glob":
		return "Listed"
	case "grep":
		return "Grep"
	case "write_file":
		return "Write"
	case "edit_file":
		return "Edit"
	case "shell_command", "shell_poll":
		return "Run"
	default:
		return raw
	}
}

// verbWidth pads the verb column: fixed-width, left-aligned (spec
// §2.10) — "Listed" is the longest of the six. Longer raw names are
// never truncated, only shorter ones padded.
const verbWidth = 6

// showReadOnlyResult reports whether a read-only (ParallelSafe) result
// earns transcript space (spec §2.10): a silent success gets no ⎿ line
// — the call line IS the receipt. Failures, empty-match notices,
// input-repair receipts, unchanged-read stubs, and ask-decision
// receipts always show: those are results, not rhythm. The match is
// against Dispatch/loop templates (single sources), never content.

func showReadOnlyResult(result string) bool {
	t := strings.TrimSpace(result)
	for _, p := range []string{`tool "`, "[", "unknown tool", "Plan mode is read-only", "identical read-only"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return strings.Contains(t, "→ denied") || strings.Contains(t, "→ approved")
}

// groupResultText returns the result worth rendering for one buffered
// pair, or "" when a read-only success carries nothing beyond "it
// ran" — the caller must skip it entirely (no blank line left
// behind, or suppression would still cost a row per call).
func groupResultText(it groupItem) string {
	verb, _ := splitVerb(it.call)
	if agent.ParallelSafe(verb) && !showReadOnlyResult(it.result) {
		return ""
	}
	return it.result
}

// grepCountSuffix lifts a match count onto the call line (spec §2.10's
// `● Grep "x" · 3 matches in 2 files`): the result body itself stays
// suppressed, but its headline survives on the call. Empty when the
// result renders normally (failures, notices) or holds no matches.
func grepCountSuffix(it groupItem) string {
	verb, _ := splitVerb(it.call)
	if verb != "grep" || showReadOnlyResult(it.result) {
		return ""
	}
	matches, files := countGrepMatches(it.result)
	if matches == 0 {
		return ""
	}
	ms := "match"
	if matches != 1 {
		ms = "matches"
	}
	fs := "file"
	if files != 1 {
		fs = "files"
	}
	return fmt.Sprintf("  · %d %s in %d %s", matches, ms, files, fs)
}

// countGrepMatches counts `path:line: text` rows, skipping the fence
// markers and bracketed notices the tools wrap output in.
func countGrepMatches(result string) (matches int, files int) {
	seen := map[string]bool{}
	for _, ln := range strings.Split(result, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "---") || strings.HasPrefix(ln, "[") {
			continue
		}
		i := strings.Index(ln, ":")
		if i <= 0 {
			continue
		}
		rest := ln[i+1:]
		j := strings.Index(rest, ":")
		if j <= 0 || !isDigits(rest[:j]) {
			continue
		}
		matches++
		seen[ln[:i]] = true
	}
	return matches, len(seen)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ungrouped call + result (no parent for one child — that would be noise),
// a run as one parent landmark plus indented children. Results keep their
// full rendering, indented to sit under the parent.
func (m *Model) flushGroup() {
	items := m.groupBuf
	m.groupBuf = nil
	if len(items) == 0 {
		return
	}
	if len(items) == 1 {
		m.renderCallLine(items[0].call, false, grepCountSuffix(items[0]))
		if res := groupResultText(items[0]); res != "" {
			m.append(renderToolResult(res))
		}
		return
	}
	var names []string
	seen := map[string]bool{}
	for _, it := range items {
		verb, _ := splitVerb(it.call)
		disp := toolDisplayVerb(verb)
		if !seen[disp] {
			seen[disp] = true
			names = append(names, disp)
		}
	}
	m.append(lipgloss.NewStyle().Foreground(fgMuted).Render("● ") +
		lipgloss.NewStyle().Foreground(fg).Render(strings.Join(names, ", ")+fmt.Sprintf(" ×%d", len(items))))
	for _, it := range items {
		m.renderCallLine(it.call, true, grepCountSuffix(it))
		res := groupResultText(it)
		if res == "" {
			continue
		}
		for _, ln := range strings.Split(renderToolResult(res), "\n") {
			if strings.TrimSpace(stripANSI(ln)) == "" {
				continue
			}
			m.append("  " + ln)
		}
	}
}

// SetUpdateNotice inserts the cached update-available line at the end
// of a pristine splash (spec §2.1 real estate, dim, no glyph). It is a
// no-op once the transcript has moved past the splash — a notice must
// never splice into the middle of a live session. The width-resize
// path below re-appends it via updateNote so it survives refits.
func (m *Model) SetUpdateNotice(note string) {
	if note == "" || m.updateNote != "" {
		return
	}
	if !(m.splashN > 0 && len(m.lines) == m.splashN) {
		return
	}
	m.updateNote = note
	m.lines = append(m.lines, lipgloss.NewStyle().Foreground(fgDim).Render(note))
	m.splashN = len(m.lines)
}

func (m Model) modeColor() lipgloss.Color {
	switch m.curMode {
	case mode.Plan:
		return borderPlan
	case mode.Auto:
		return borderAuto
	default:
		return borderBuild
	}
}

// Live reasoning microcopy (spec §2.20): while a turn runs and the
// transcript has been quiet past reasonFloor, the status bar shows a
// rotating verb instead of the frozen "Working Ns". Terse, harness-flavored
// verbs in tilde's own voice — each names something the agent actually
// does. The per-turn order is shuffled (no repeats within a cycle); the
// 2s cadence never varies.
var reasonVerbs = []string{"Reading", "Mapping", "Tracing", "Probing", "Weighing", "Drafting"}

const (
	reasonFloor = 2 * time.Second // quiet below this stays "Working Ns"
	reasonEvery = 2 * time.Second // verb rotation cadence
	// escArmWindow bounds the double-Esc interrupt: the second Esc must
	// land inside it, or the first one expires back to a hint.
	escArmWindow = 2 * time.Second
)

// reasonVerb reports the live reasoning prefix ("◆ Sifting 6s") when the
// model is being waited on: turn running, transcript quiet past the floor,
// no confirm open (that wait is on the user, not the model).
func (m Model) reasonVerb() (string, bool) {
	if m.turnStart.IsZero() || m.quietSince.IsZero() || m.confirm != nil {
		return "", false
	}
	now := time.Now()
	if now.Sub(m.quietSince) < reasonFloor {
		return "", false
	}
	verb := pickReasonVerb(m.verbOrder, now.Sub(m.quietSince))
	return fmt.Sprintf("◆ %s %ds", verb, int(now.Sub(m.turnStart).Seconds())), true
}

// pickReasonVerb is the deterministic core of reasonVerb (pure in order +
// quiet-elapsed), kept separate so tests don't depend on wall time or
// randomness. An empty order means natural list order.
func pickReasonVerb(order []int, quietElapsed time.Duration) string {
	if len(order) == 0 {
		return reasonVerbs[int(quietElapsed/reasonEvery)%len(reasonVerbs)]
	}
	return reasonVerbs[order[int(quietElapsed/reasonEvery)%len(order)]%len(reasonVerbs)]
}

// thoughtReceipt renders the post-turn performance receipt: wall time
// always, generation rate only when the provider reported output tokens
// (completion <= 0 means unknown — the rate is omitted, never invented).
func thoughtReceipt(elapsed time.Duration, completion int) string {
	s := elapsed.Seconds()
	age := fmt.Sprintf("%ds", int(s+0.5))
	if s < 10 {
		age = fmt.Sprintf("%.1fs", s)
	}
	line := "◆ Thought for " + age
	if completion > 0 && s > 0 {
		line += fmt.Sprintf(" · %d tok/s", int(float64(completion)/s+0.5))
	}
	return line
}

// shuffleVerbs returns a fresh permutation of the verb indices — a full
// cycle with no repeats. Randomness lives only here, at turn start.
func shuffleVerbs(r *rand.Rand) []int {
	order := r.Perm(len(reasonVerbs))
	return order
}

// statusBar splits left/right, nothing centered (spec §1.5): left is
// where/what (mode · root · branch), right is how much (model · ctx, or
// Working Ns mid-turn). It never wraps: on narrow screens the raw token
// counts drop first, then the dirty count, then the branch name, then the
// root shortens — the mode word is never dropped. Right side docks right.
func (m *Model) statusBar() string {
	root, ctx, branch := m.root, m.ctx, m.branch
	right := func() string {
		if !m.turnStart.IsZero() {
			if v, ok := m.reasonVerb(); ok {
				return v + " · Esc×2 cancels"
			}
			return fmt.Sprintf("Working %ds · Esc×2 cancels", int(time.Since(m.turnStart).Seconds()))
		}
		if ctx == "" {
			return m.model
		}
		return m.model + " · ctx " + ctx
	}
	// Session cost meter (cost.go): idle only, appended after ctx — the
	// Working indicator mid-turn is never reflowed. "" when the price is
	// unknown (hidden, never fabricated).
	cost := ""
	if m.turnStart.IsZero() {
		cost = m.costSuffix()
	}
	left := func() string {
		s := m.curMode.String() + " · " + root
		if branch != "" {
			s += " · " + branch
		}
		return s
	}
	if w := m.vp.Width; w > 0 {
		for runeLen(left()+"  "+right()+cost) > w {
			if i := strings.Index(ctx, " ("); i >= 0 {
				ctx = ctx[:i]
				continue
			}
			if j := strings.Index(branch, " [+"); j >= 0 {
				branch = branch[:j]
				continue
			}
			if branch != "" {
				branch = ""
				continue
			}
			if r := []rune(root); len(r) > 24 {
				root = "…" + string(r[len(r)-23:])
				continue
			}
			break
		}
	}
	modeSt := lipgloss.NewStyle().Foreground(m.modeColor()).Bold(true)
	mutSt := lipgloss.NewStyle().Foreground(fgMuted)
	dimSt := lipgloss.NewStyle().Foreground(fgDim)
	lineLeft := modeSt.Render(m.curMode.String()) + mutSt.Render(" · "+root)
	if branch != "" {
		lineLeft += dimSt.Render(" · " + branch)
	}
	var lineRight string
	if !m.turnStart.IsZero() {
		// Live reasoning verbs render dim; the cancel half stays muted.
		if v, ok := m.reasonVerb(); ok {
			lineRight = dimSt.Render(v) + mutSt.Render(" · Esc×2 cancels")
		} else {
			lineRight = mutSt.Render(right())
		}
	} else if m.ctxHot {
		// Only the percentage text ambers — never the model name, never
		// the cost readout (it stays muted).
		if i := strings.Index(right(), "ctx "); i >= 0 {
			lineRight = mutSt.Render(right()[:i]) + lipgloss.NewStyle().Foreground(amber).Render(right()[i:]) + mutSt.Render(cost)
		} else {
			lineRight = mutSt.Render(right()) + mutSt.Render(cost)
		}
	} else {
		lineRight = mutSt.Render(right() + cost)
	}
	// Dock right: pad between, hard-cut when nothing fits.
	gap := 2
	if w := m.vp.Width; w > 0 {
		if pad := w - runeLen(stripANSI(lineLeft)) - runeLen(stripANSI(lineRight)); pad > 2 {
			gap = pad
		} else if pad >= 0 {
			gap = pad
		} else {
			return hardCut(stripANSI(lineLeft+"  "+lineRight), w)
		}
	}
	return lineLeft + strings.Repeat(" ", gap) + lineRight
}

// runeLen is the historical name used by the layout code; it returns
// terminal cell width, not Go rune count, so CJK, emoji, and combining marks
// cannot shift columns or overflow a frame.
func runeLen(s string) int { return ansi.StringWidth(s) }

// frameWidth maps a terminal width to the app frame width: terminals
// wider than maxAppWidth get a centered content column instead of a
// full-bleed stretch. Zero/negative (no resize seen yet) passes through
// untouched — callers clamp against their own minimums as before.
func frameWidth(termW int) int {
	if termW <= 0 {
		return termW
	}
	return min(termW, maxAppWidth)
}

// centerFrame docks the rendered frame horizontally in the terminal.
// It engages only past maxAppWidth: narrower screens keep their exact
// legacy full-bleed geometry (gutter at column 0, slack on the right),
// byte for byte. On wide screens every line gets the same left margin,
// so the frame reads as one centered column with a single straight left
// edge — content inside stays left-aligned (spec §1.3 gutter), only the
// column itself is centered. Vertical geometry is untouched: no lines
// are added or removed, so the height contract (frame rows == terminal
// height) still holds exactly.
func (m Model) centerFrame(s string) string {
	if m.termW <= maxAppWidth {
		return s
	}
	widest := 0
	lines := strings.Split(s, "\n")
	for _, ln := range lines {
		if w := runeLen(stripANSI(ln)); w > widest {
			widest = w
		}
	}
	margin := (m.termW - widest) / 2
	if margin <= 0 {
		return s
	}
	pad := strings.Repeat(" ", margin)
	for i, ln := range lines {
		if ln != "" {
			lines[i] = pad + ln
		}
	}
	return strings.Join(lines, "\n")
}

// vpView renders the transcript viewport with trailing padding stripped
// per line. Bubbles pads short lines to the full viewport width with
// spaces — invisible on screen, but every one of those spaces rides
// along into terminal drag-select copy/paste, and the codebase holds a
// no-trailing-whitespace rule everywhere else (trimLinePad, render
// tests). Row count is untouched, so scroll geometry and the exact
// frame-height contract hold.
// inverseSel paints the live drag-highlight bar.
var inverseSel = lipgloss.NewStyle().Reverse(true)

func (m Model) vpView() string {
	lines := strings.Split(m.vp.View(), "\n")
	for i, ln := range lines {
		ln = trimLinePad(ln)
		if from, to, ok := m.selectionRange(m.vp.YOffset + i); ok {
			// Selection is rendered from the plain copy form so ANSI styling
			// cannot swallow or shift the selected cells. Losing syntax colour
			// for the selected row is preferable to showing a highlight that
			// copies different text than it appears to select.
			plain := m.copyLineForm(m.vp.YOffset + i)
			lineWidth := ansi.StringWidth(plain)
			from = min(from, lineWidth)
			to = min(to, lineWidth)
			if from < to {
				ln = ansi.Cut(plain, 0, from) + inverseSel.Render(ansi.Cut(plain, from, to)) + ansi.Cut(plain, to, lineWidth)
			}
		}
		lines[i] = ln
	}
	return strings.Join(lines, "\n")
}

func (m Model) View() string {
	if m.helpOpen {
		return m.centerFrame(helpView(m.vp.Width))
	}
	if m.resumeOpen {
		return m.centerFrame(m.resumeView())
	}
	if m.skillsOpen {
		return m.centerFrame(m.skillsView())
	}
	if m.marketplaceOpen {
		if m.marketplacePending != nil {
			return m.centerFrame(m.marketplaceConfirmView())
		}
		if m.marketplaceDetail != nil {
			return m.centerFrame(m.marketplaceDetailView())
		}
		return m.centerFrame(m.marketplaceView())
	}
	border := borderBuild
	switch m.curMode {
	case mode.Plan:
		border = borderPlan
	case mode.Auto:
		border = borderAuto
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1)
	if m.keyProvider != "" {
		// Masked key entry (§2.24): amber owns the border while open.
		box = box.BorderForeground(amber)
	}
	composer := m.composerView(box)
	// Inline dropdown directly beneath the composer, replacing nothing.
	var dropdown string
	if m.slashOpen {
		dropdown = "\n" + slashDropdown(m.slashItems, m.slashCursor, m.vp.Width)
	} else if m.atOpen {
		dropdown = "\n" + atDropdown(m.atItems, m.atCursor, m.vp.Width)
	} else if m.shellArmed {
		dropdown = "\n" + shellDropdown(m.vp.Width)
	}
	// Status bar: split left/right, never wraps (see statusBar).
	statusBar := m.statusBar()
	// Mode toast: one render frame (~600ms), then collapses to the bar.
	// Demotions share the shape in amber.
	var toast string
	if m.toast != "" {
		c := fgMuted
		if m.toastAmber {
			c = amber
		}
		toast = "\n" + lipgloss.NewStyle().Foreground(c).Render("  "+m.toast)
	}
	if m.confirm != nil {
		// View is a value receiver, so this also protects callers that set a
		// confirmation state directly (tests and non-agent shell paths) before
		// the next Update has a chance to refit the viewport. Keep the live
		// transcript at the tail when the panel takes rows away.
		followTail := m.stick || m.vp.AtBottom()
		m.fitViewport()
		if followTail {
			m.vp.GotoBottom()
		}
		p := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(borderPlan).Padding(1, 1).
			Render(confirmFooter(m.confirm.Tool, m.confirm.Args, m.vp.Width, m.confirm.reasonHidden))
		return m.centerFrame(m.vpView() + "\n" + p + "\n" + composer + dropdown + toast + "\n\n" + statusBar)
	}
	hint := lipgloss.NewStyle().Foreground(fgDim).Render("Enter send  •  / commands  •  @ files  •  ! shell  •  Drag select  •  Ctrl+Y copy  •  Tab mode  •  Esc×2 cancel" + m.sessionHint())
	return m.centerFrame(m.vpView() + "\n" + composer + dropdown + toast + "\n\n" + statusBar + "\n\n" + m.centerHint(m.hintBar(hint)))
}

// centerHint centers the keybind hint bar within the frame: it is the
// quiet closer of the footer stack, centered like the splash title rather
// than aligned to the working gutter. On narrow terminals the frame is
// the full terminal, so this is full-width centering; on wide ones the
// hint centers within the content column like everything else. Overlong
// lines (narrow screens) hard-cut with an ellipsis instead of spilling
// past the edge. Color stays fgDim per the spec token set. Padding is
// left-side only: the content lands centered with no trailing whitespace
// to pollute terminal copy/paste.
func (m Model) centerHint(s string) string {
	w := m.vp.Width
	if w <= 0 {
		return s
	}
	vw := runeLen(stripANSI(s))
	if vw > w {
		return hardCut(s, w)
	}
	return strings.Repeat(" ", (w-vw)/2) + s
}

// hintBar is the ever-present hint line; while the user is scrolled up
// through history it swaps in the scroll position and the way back down,
// so "am I seeing everything?" is always answerable at a glance. While
// mouse passthrough is armed it says so — a paused wheel is easy to
// forget about.
func (m Model) hintBar(base string) string {
	if m.mousePass {
		scroll := "mouse select on · Alt+M to re-arm wheel scroll"
		return lipgloss.NewStyle().Foreground(amber).Render(scroll)
	}
	if m.vp.AtBottom() {
		return base
	}
	pct := int(m.vp.ScrollPercent()*100 + 0.5)
	scroll := fmt.Sprintf("↑ %d%% of history · ↓/End back to live", pct)
	return lipgloss.NewStyle().Foreground(amber).Render(scroll)
}

// sessionHint appends the session id to the ever-present hint bar
// (spec §2.1), so the one piece of chrome stays complete everywhere.
func (m Model) sessionHint() string {
	if m.loop == nil || m.loop.Log == nil {
		return ""
	}
	id := m.loop.Log.Path
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	id = strings.TrimSuffix(id, ".jsonl")
	if len([]rune(id)) > 16 {
		id = string([]rune(id)[:15]) + "…"
	}
	return "  •  Session: " + id
}

// shellDropdown renders the one-line shell hint beneath the composer
// while the box leads with "!": same slot and manners as the / and @
// dropdowns (muted single row, trimmed to the composer width — never
// wraps). A placeholder could never do this job: placeholders only show
// on an empty box, and an armed box always holds the "!" plus command.
func shellDropdown(width int) string {
	row := lipgloss.NewStyle().Foreground(fgMuted).Render("  !  run as shell command — sandboxed · Enter runs")
	return truncANSI(row, width)
}

// atDropdown renders @-matches: matched chars bold fg on muted paths.
// Rows trim to the composer width — a dropdown row never wraps. The
// selected row keeps the bold match spans: the accentSelect background
// is composed per highlight run (see highlightSelected) instead of the
// plain truncMiddle path, which would drop the spans.
func atDropdown(items []atRow, cursor, width int) string {
	var b strings.Builder
	for i, r := range items {
		prefix := "  "
		if i == cursor {
			prefix = "→ "
			w := width - 2
			if w < 1 {
				w = 1
			}
			sel := lipgloss.NewStyle().Background(accentSelect)
			b.WriteString(sel.Render(prefix))
			b.WriteString(truncANSI(highlightSelected(r.path, r.idx), w))
			b.WriteString("\n")
			continue
		}
		b.WriteString(prefix)
		b.WriteString(truncANSI(r.rendered, width-2))
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}
