package tui

// helpView renders the keybinding reference (§2.16): a plain two-column
// table, no glyphs or color beyond fg/muted — a reference screen must not
// compete with the timeline vocabulary it explains.
func helpView(width int) string {
	if width < 20 {
		width = 20
	}
	rows := [][2]string{
		{"Enter", "Send the prompt"},
		{"Tab", "Cycle mode: Plan → Build → Auto"},
		{"Ctrl+C", "Quit when idle"},
		{"Esc Esc", "Cancel the running turn"},
		{"Ctrl+J", "Newline in the composer"},
		{"Ctrl+Y", "Copy latest assistant response"},
		{"/", "Command palette"},
		{"@", "File reference"},
		{"!", "Shell escape"},
		{"↑↓", "Prompt history; pickers while open; transcript scroll otherwise"},
		{"Shift+↑↓", "Scroll transcript, even while a picker is open"},
		{"PgUp/PgDn · Ctrl+U/D", "Scroll transcript by half a screen"},
		{"Wheel / touchpad", "Scroll transcript, even while a picker is open"},
		{"Drag", "Select transcript text and copy on release"},
		{"Alt+M", "Mouse passthrough: terminal-native select + copy (rectangles, terminal chords); wheel pauses"},
		{"Home/End", "Jump to top of history / back to live"},
		{"esc", "Dismiss overlay or picker"},
	}
	s := truncANSI("  Keybindings"+"                                                    tilde "+appVersion, width) + "\n\n"
	for _, r := range rows {
		s += truncANSI("  "+padRunesRight(r[0], 22)+r[1], width) + "\n"
	}
	s += "\n" + truncANSI("  Slash commands: /mode /compact /clear /copy /sandbox /diff /undo /sessions /export /model /login /logout /skills /plugins /marketplace /help /quit", width) + "\n"
	s += truncANSI("  press esc to close", width)
	return s
}
