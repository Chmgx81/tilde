// Package rules — project rules auto-load (docs/Plan.md Phase 6, P6-A).
// At prompt-build time the harness prepends at most one rules file as
// ground truth about the project's conventions, architecture, and
// explicit "never do X" boundaries
// (docs/ai-agents-and-terminal-coding-agents-2026.md §6: CLAUDE.md,
// AGENTS.md, .cursor/rules and equivalents).
//
// Search order is fixed and documented — AGENTS.md, then CLAUDE.md, then
// .tilde/RULES.md — and the first existing non-empty file wins. (.cursor/
// rules predates this harness's convention and is intentionally NOT in the
// list; extend Candidates to add it.)
//
// Context discipline (same as read ceilings and the skills 8000-word cap):
// one rules file is capped at MaxBytes (8KB). Anything longer is truncated
// with an explicit marker naming the fix — never silently eaten.
//
// Trust note: a rules body is instructions the agent will follow, same
// trust class as project skills from cloned repos. Only honor rules you
// trust; whatever trust gate the owner already enforces for project
// skills must cover this loader too. Gating is DOCUMENTED here, not
// implemented — the owner's prompt-assembly wiring enforces it.
// Malformed input is N/A: rules are freeform markdown, no schema to
// validate (unlike skills frontmatter).
//
// The package is intentionally leaf (stdlib only): in particular it does
// NOT import internal/tools for Fence. tools.Fence uses --- markers;
// prompt assembly here wants a fenced markdown block, and coupling the
// loader to the tool graph would break leaf discipline (same reason audit
// defines its own sink interface instead of importing tools' callers —
// and skills.go's PromptLine already depends on tools, so rules staying
// import-free keeps at least one prompt input dependency-free). Hence the
// local fence() below.
package rules

import (
	"os"
	"path/filepath"
	"strings"
)

// MaxBytes caps one rules file at 8KB. Overlong files truncate with
// TruncNote (naming the fix) instead of failing or eating context.
const MaxBytes = 8 * 1024

// TruncNote marks a capped body and names the fix: move detail out of the
// rules file and into a skill, which loads on demand instead of every
// prompt.
const TruncNote = "\n[... truncated at 8KB — move detail to skills]\n"

// Candidates is the priority-ordered search list, first existing non-empty
// file wins. Paths are root-relative; Load joins each under root so probes
// can never escape it (no absolute paths, no .. segments).
var Candidates = []string{"AGENTS.md", "CLAUDE.md", filepath.Join(".tilde", "RULES.md")}

// Rules is one loaded project rules file. Body is the raw markdown
// (truncated at MaxBytes with TruncNote when over cap); PromptBlock
// renders the fenced, prompt-ready section — assemblers must use that,
// never Body, so rule text cannot read as harness directives (same split
// as Skill.Description raw vs Skill.PromptLine fenced).
type Rules struct {
	Path      string // absolute probe path of the winning file
	Body      string // raw markdown, possibly truncated (see Truncated)
	Truncated bool   // true when Body was capped at MaxBytes + TruncNote
}

// Load returns the first existing non-empty candidate under root. Missing
// everything — or an empty root — is Found=false, not an error. An empty
// (or whitespace-only) file counts as not found and falls through to the
// next candidate. Freeform markdown is never "malformed": no validation.
func Load(root string) (Rules, bool) {
	if root == "" {
		return Rules{}, false
	}
	for _, c := range Candidates {
		p := filepath.Join(root, c)
		data, err := os.ReadFile(p)
		if err != nil {
			continue // missing/unreadable: try next candidate
		}
		if strings.TrimSpace(string(data)) == "" {
			continue // empty file = not found
		}
		body, truncated := capBytes(string(data))
		return Rules{Path: p, Body: body, Truncated: truncated}, true
	}
	return Rules{}, false
}

// capBytes enforces MaxBytes, backing off to a UTF-8 rune boundary so the
// cut never splits a multibyte rune, then appends TruncNote.
func capBytes(s string) (string, bool) {
	if len(s) <= MaxBytes {
		return s, false
	}
	cut := MaxBytes
	for cut > 0 && s[cut]>>6 == 0x2 {
		cut-- // continuation byte (10xxxxxx): not a rune start
	}
	return s[:cut] + TruncNote, true
}

// PromptBlock renders the fenced prompt section: provenance header plus
// ```markdown block. Prepend to the system prompt when Found.
func (r Rules) PromptBlock() string {
	return "Project rules (" + r.Path + ") — ground truth for conventions and never-do-X boundaries:\n" + fence(r.Body)
}

// fence wraps body in a ```markdown block so rule text can never read as
// harness directives. The fence run lengthens past any backtick run in
// the body so embedded triple-backticks cannot break out of the block.
func fence(body string) string {
	f := "```"
	for strings.Contains(body, f) {
		f += "`"
	}
	return f + "markdown\n" + body + "\n" + f
}
