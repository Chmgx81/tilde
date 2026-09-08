package tui

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// gitBranch returns `main [+2]` for root, or "" outside a repo / on error.
// One fork per call — always go through branchInfo's TTL cache.
// --no-optional-locks: the bar must never contend with real git work
// (a status refresh colliding with a snapshot write was observed live).
func gitBranch(root string) string {
	cmd := exec.Command("git", "--no-optional-locks", "-C", root, "status", "--short", "--branch")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return ""
	}
	branch := strings.TrimPrefix(lines[0], "## ")
	// Unborn HEAD: "No commits yet on main".
	branch = strings.TrimPrefix(branch, "No commits yet on ")
	// Strip upstream tracking ("main...origin/main", "main...origin/main [ahead 1]").
	if i := strings.Index(branch, "..."); i >= 0 {
		branch = branch[:i]
	}
	if i := strings.Index(branch, " "); i >= 0 {
		branch = branch[:i]
	}
	if branch == "" || branch == "HEAD" {
		branch = "detached"
	}
	dirty := 0
	for _, ln := range lines[1:] {
		if strings.TrimSpace(ln) != "" {
			dirty++
		}
	}
	if dirty > 0 {
		branch += " [+" + strconv.Itoa(dirty) + "]"
	}
	return branch
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// stripANSI removes SGR color codes for width math.
func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// truncANSI truncates a styled string to a visible width (ANSI
// sequences are preserved), marking the cut with an ellipsis.
func truncANSI(s string, w int) string {
	if w <= 0 || ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// hardCut hard-truncates a styled string to width w, keeping ANSI escape
// sequences intact (rune-level cuts would split SGR sequences and bleed
// colors into the status bar).
func hardCut(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	if w <= 1 {
		return ""
	}
	return ansi.Cut(s, 0, w-1) + "…"
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// splitVerb divides "shell_command echo hi" into ("shell_command", " echo hi").
func splitVerb(s string) (verb, rest string) {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}
