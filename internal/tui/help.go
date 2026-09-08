package tui

// helpView renders the keybinding reference (§2.16): a plain two-column
// table, no glyphs or color beyond fg/muted — a reference screen must not
// compete with the timeline vocabulary it explains.
func helpView() string {
	rows := [][2]string{
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
		{"Alt+M", "Mouse passthrough: terminal-native select + copy (rectangles, terminal chords); wheel pauses"},
		{"Home/End", "Jump to top of history / back to live"},
		{"esc", "Dismiss overlay or picker"},
	}
	s := "  Keybindings" + "                                                    tilde " + appVersion + "\n\n"
	for _, r := range rows {
		s += "  " + padRunesRight(r[0], 22) + r[1] + "\n"
	}
	s += "\n  Slash commands: /mode /compact /clear /copy /sandbox /diff /undo /sessions /model /skills /help /quit\n"
	s += "                                                           press esc to close"
	return s
}
