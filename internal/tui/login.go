package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tilde/internal/creds"
	"tilde/internal/provider"
)

// keyentryTimeout bounds the one-token validation request: a hanging
// endpoint must not wedge the composer (§2.23's responsiveness rule
// applies to config flows too).
const keyentryTimeout = 20 * time.Second

// BindCreds attaches the credential store (main constructs it once).
func (m *Model) BindCreds(s *creds.Store) { m.creds = s }

// BindKeys attaches --api-key overrides for this process (flagged keys
// outrank stored and env for the matching provider — spec §2.24 ladder
// step zero, explicit intent wins).
func (m *Model) BindKeys(over map[string]string) { m.keyOverrides = over }

// SetBudgetExplicit records whether the context budget came from the
// user (--budget flag or $TILDE_BUDGET). Catalog model switches
// auto-size the budget from the model's window only when it did not —
// a 1M-window model must not inherit a 32k assumption, but an explicit
// user choice is never second-guessed.
func (m *Model) SetBudgetExplicit(explicit bool) { m.budgetExplicit = explicit }

// composerView renders the composer's content: the masked key input
// while key entry is open, otherwise the normal textarea.
func (m *Model) composerView(box lipgloss.Style) string {
	if m.keyProvider != "" {
		head := lipgloss.NewStyle().Foreground(amber).
			Render("API key for " + m.keyProvider + " — input masked, never logged")
		body := m.keyInput.View()
		if m.keyErr != "" {
			body += "\n" + lipgloss.NewStyle().Foreground(amber).Render(m.keyErr)
		}
		body += "\n" + lipgloss.NewStyle().Foreground(fgDim).
			Render("Enter verify & store · Esc cancel")
		return box.Render(head + "\n" + body)
	}
	return box.Render(m.ta.View())
}

// overlayKeyEntry reports whether the masked key box owns the keyboard.
func (m *Model) overlayKeyEntry() bool { return m.keyProvider != "" }

// loginValidatedMsg carries the /login validation verdict back to the
// loop. NetworkErr separates "endpoint unreachable" from "key
// rejected" — the remedies differ and neither may store a key.
type loginValidatedMsg struct {
	Provider   string
	Key        string
	OK         bool
	NetworkErr bool
	Err        error
}

// startKeyEntry arms the masked key-entry overlay for one cloud
// provider (spec §2.24). The composer textarea is blurred — it must not
// eat keystrokes aimed at the key field — and the border state rides
// the overlay's own flag; View renders the amber key box from it.
func (m *Model) startKeyEntry(providerID string) {
	ti := textinput.New()
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '●'
	ti.Placeholder = "paste the API key, Enter to verify & store"
	ti.Focus()
	ti.Width = max(m.vp.Width-6, 24)
	m.keyProvider = providerID
	m.keyInput = ti
	m.keyErr = ""
	m.ta.Blur()
}

// cancelKeyEntry disarms the overlay and hands focus back.
func (m *Model) cancelKeyEntry() {
	m.keyProvider = ""
	var zero textinput.Model
	m.keyInput = zero
	m.keyErr = ""
	m.ta.Focus()
	m.syncComposer()
}

// validateFn indirection lets tests stub the network check — no test
// ever dials a real provider endpoint.
var validateFn = provider.Validate

// validateKeyCmd runs the real-endpoint check off the render thread.
func validateKeyCmd(providerID, key, base string) tea.Cmd {
	return func() tea.Msg {
		done := make(chan loginValidatedMsg, 1)
		go func() {
			ok, netErr, err := validateFn(providerID, key, base)
			done <- loginValidatedMsg{Provider: providerID, Key: key, OK: ok, NetworkErr: netErr, Err: err}
		}()
		select {
		case msg := <-done:
			return msg
		case <-time.After(keyentryTimeout):
			return loginValidatedMsg{Provider: providerID, Key: key, NetworkErr: true,
				Err: fmt.Errorf("no response in %s", keyentryTimeout)}
		}
	}
}

// finishKeyEntry stores a validated key (0600 file, never echoed) and
// returns the toast's dismiss tick. Failure stores nothing — spec
// §2.24 has no save-unvalidated path.
func (m *Model) finishKeyEntry(msg loginValidatedMsg) tea.Cmd {
	m.cancelKeyEntry()
	if !msg.OK {
		if msg.NetworkErr {
			m.toastAmber = true
			return m.setToast(fmt.Sprintf("✗ could not reach %s to validate — nothing stored", msg.Provider))
		}
		m.toastAmber = true
		return m.setToast(fmt.Sprintf("✗ key rejected by %s — nothing stored", msg.Provider))
	}
	if err := m.creds.Set(msg.Provider, msg.Key); err != nil {
		m.toastAmber = true
		return m.setToast("✗ could not write credentials: " + err.Error())
	}
	if m.loop.Log != nil {
		// The session records that auth changed — never the key.
		_ = m.loop.Log.Append("system", map[string]any{"login": msg.Provider})
	}
	return m.setToast(fmt.Sprintf("✓ %s configured — key %s stored in %s",
		msg.Provider, creds.Mask(msg.Key), m.creds.Path()))
}

// loginStatus renders /login's status matrix as plain transcript rows:
// provider · credential source · masked key · current model.
func (m *Model) loginStatus() {
	dim := lipgloss.NewStyle().Foreground(fgDim)
	head := fmt.Sprintf("%-10s %-8s %-9s %s", "provider", "source", "key", "model")
	m.append(dim.Render(head))
	for _, st := range provider.Status(m.creds, m.keyOverrides) {
		model := "—"
		if ref, err := m.currentModelRef(st.Provider.ID); err == nil {
			model = ref
		}
		key := "—"
		if st.Source != provider.AuthNone {
			key = creds.Mask(st.KeyTail)
		}
		if !st.Provider.NeedsKey {
			key = "—"
		}
		m.append(fmt.Sprintf("%-10s %-8s %-9s %s", st.Provider.ID, st.Source.String(), key, model))
	}
	m.append(dim.Render("login a cloud provider: /login <" + strings.Join(provider.CloudIDs(), "|") + ">"))
}

// currentModelRef reports the model id for providerID ("p/m"): the live
// backend's name when it is current, else the catalog's first entry.
// Factory is deliberately not consulted — it refuses keyless cloud
// construction, and a status matrix must never need a key to render.
func (m *Model) currentModelRef(providerID string) (string, error) {
	if cur := strings.SplitN(m.model, "/", 2); len(cur) == 2 && cur[0] == providerID {
		return m.model, nil
	}
	if ids := provider.CatalogIDs(providerID); len(ids) > 0 {
		return providerID + "/" + ids[0], nil
	}
	for _, d := range provider.Descriptions {
		if d.ID == providerID {
			return providerID + "/—", nil
		}
	}
	return "", fmt.Errorf("unknown provider %s", providerID)
}

// runLogout removes stored auth. With an argument, that provider; bare
// /logout lists cloud providers that hold a stored key and removes
// nothing (delete needs a name — logout is destructive).
func (m *Model) runLogout(arg string) {
	id := strings.ToLower(strings.TrimSpace(arg))
	if id == "" {
		var withKeys []string
		for _, st := range provider.Status(m.creds, m.keyOverrides) {
			if st.Provider.NeedsKey && st.Source == provider.AuthStored {
				withKeys = append(withKeys, st.Provider.ID)
			}
		}
		if len(withKeys) == 0 {
			m.append("● no stored credentials to remove.")
			return
		}
		m.append("● stored credentials: " + strings.Join(withKeys, ", ") + " — usage: /logout <provider>")
		return
	}
	found := false
	for _, d := range provider.Descriptions {
		if d.ID == id {
			found = true
		}
	}
	if !found {
		m.append("✗ unknown provider " + id + " — use /login to see the list.")
		return
	}
	if err := m.creds.Delete(id); err != nil {
		m.append("✗ could not update credentials: " + err.Error())
		return
	}
	if m.loop.Log != nil {
		_ = m.loop.Log.Append("system", map[string]any{"logout": id})
	}
	m.append("● stored credential for " + id + " removed — the env var (if set) applies again.")
}
