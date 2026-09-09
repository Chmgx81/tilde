// Package skills — project/user skill loader with progressive disclosure
// (docs/Plan.md Phase 6). At startup only name+description enter the prompt;
// a body loads on activation (/skills picker, load_skill tool, --skill),
// so installed-but-unused skills cost ~one line each instead of their
// full text. Rescanning happens on every picker open and every prompt
// build — a skill dropped into the directory mid-session works with no
// restart.
//
// Trust note: a skill body is instructions the agent will follow. Only
// install skills you trust, especially project ones from cloned repos.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"tilde/internal/tools"

	"gopkg.in/yaml.v3"
)

// Skill is one parsed skill file.
type Skill struct {
	Name        string // frontmatter name, else filename stem
	Description string // frontmatter description (required)
	Body        string // everything below the frontmatter
	Scope       string // "bundled" | "project" | "user"
	Path        string
	Version     string // bundled skills expose an immutable content version
	Verified    bool   // true only for skills shipped and reviewed by tilde
}

// Index is a scanned set of skills. Project wins on name clash.
type Index struct {
	skills []Skill
	byName map[string]Skill
}

// Scan rebuilds the index from <root>/.tilde/skills (project) and
// ~/.tilde/skills (user). Missing directories are empty, not errors.
// Pass allowProject=false to skip project skills (untrusted source) —
// mirrors --hooks-project / --mcp-project opt-in.
func Scan(root string, allowProject ...bool) (*Index, error) {
	allow := true
	if len(allowProject) > 0 {
		allow = allowProject[0]
	}
	userDir := ""
	if home, _ := os.UserHomeDir(); home != "" {
		userDir = filepath.Join(home, ".tilde", "skills")
	}
	projDir := filepath.Join(root, ".tilde", "skills")
	if !allow {
		if _, err := os.Stat(projDir); err == nil {
			fmt.Fprintf(os.Stderr, "tilde: project skills present but NOT loaded (untrusted source) — pass --skills-project to opt in\n")
		}
		ix, err := ScanDirs("", userDir)
		return ix, err
	}
	ix, err := ScanDirs(root, userDir)
	if ix != nil {
		if n := ix.projectCount(); n > 0 {
			// Project skills arrive with cloned repos (same trust
			// class as project MCP servers / hooks): announce the
			// untrusted source at load. Gating behind a
			// --skills-project opt-in flag is deferred (see report).
			fmt.Fprintf(os.Stderr, "tilde: %d project skill(s) loaded (untrusted source) — review bodies with load_skill before following them\n", n)
		}
	}
	return ix, err
}

// ScanDirs is Scan with an explicit user dir (evals isolate here).
// An empty root skips project skills entirely.
func ScanDirs(root, userDir string) (*Index, error) {
	ix := &Index{byName: map[string]Skill{}}
	dirs := []struct {
		dir   string
		scope string
	}{
		{userDir, "user"},
	}
	if root != "" {
		dirs = append(dirs, struct {
			dir   string
			scope string
		}{filepath.Join(root, ".tilde", "skills"), "project"})
	}
	var errs []string
	for _, d := range dirs {
		if d.dir == "" {
			continue
		}
		entries, err := os.ReadDir(d.dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, fmt.Sprintf("%s: %v", d.dir, err))
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			sk, err := ParseFile(filepath.Join(d.dir, e.Name()), d.scope)
			if err != nil {
				errs = append(errs, err.Error())
				continue // one bad file never kills the index
			}
			ix.skills = append(ix.skills, sk)
		}
	}
	// Index: project wins on clash; list shows each name once.
	seen := map[string]bool{}
	var ordered []Skill
	for _, sk := range ix.skills {
		if sk.Scope == "project" && !seen[sk.Name] {
			seen[sk.Name] = true
			ordered = append(ordered, sk)
		}
	}
	for _, sk := range ix.skills {
		if !seen[sk.Name] {
			seen[sk.Name] = true
			ordered = append(ordered, sk)
		}
	}
	ix.skills = ordered
	for _, sk := range ordered {
		// First occurrence wins; project entries were ordered first.
		if _, ok := ix.byName[sk.Name]; !ok {
			ix.byName[sk.Name] = sk
		}
	}
	if len(errs) > 0 {
		return ix, fmt.Errorf("skills: %s", strings.Join(errs, "; "))
	}
	return ix, nil
}

// projectCount reports how many indexed skills are project-scoped.
func (ix *Index) projectCount() int {
	if ix == nil {
		return 0
	}
	n := 0
	for _, sk := range ix.skills {
		if sk.Scope == "project" {
			n++
		}
	}
	return n
}

// PromptLine renders the one-line prompt entry for this skill. Project
// (third-party) descriptions are untrusted prompt content: they are fenced
// so injected instructions cannot read as harness directives. Prompt
// assemblers must use this instead of raw Description (Description stays
// raw so pickers/tests keep working).
func (sk Skill) PromptLine() string {
	desc := sk.Description
	if sk.Scope == "project" {
		desc = tools.Fence(desc)
	}
	return fmt.Sprintf("- %s — %s (%s)", sk.Name, desc, sk.Scope)
}

// List returns skills, project first, alphabetical within scope.
func (ix *Index) List() []Skill {
	if ix == nil {
		return nil
	}
	var proj, user []Skill
	for _, sk := range ix.skills {
		if sk.Scope == "project" {
			proj = append(proj, sk)
		} else {
			user = append(user, sk)
		}
	}
	byName := func(a, b Skill) bool { return a.Name < b.Name }
	sort.Slice(proj, func(i, j int) bool { return byName(proj[i], proj[j]) })
	sort.Slice(user, func(i, j int) bool { return byName(user[i], user[j]) })
	return append(proj, user...)
}

// Get resolves by name (project wins). Counts and names for errors.
func (ix *Index) Get(name string) (Skill, bool) {
	if ix == nil {
		return Skill{}, false
	}
	sk, ok := ix.byName[name]
	return sk, ok
}

// Names returns sorted skill names for error messages and prompts.
func (ix *Index) Names() []string {
	if ix == nil {
		return nil
	}
	var out []string
	for n := range ix.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// AddBuiltin adds an embedded, verified skill without allowing a project or
// user file to replace it silently. Built-ins are inserted only when their
// name is not already present; explicit overrides remain a future, visible
// feature rather than an accidental precedence rule.
func (ix *Index) AddBuiltin(sk Skill) {
	if ix == nil || sk.Name == "" {
		return
	}
	if _, exists := ix.byName[sk.Name]; exists {
		return
	}
	sk.Scope = "bundled"
	sk.Verified = true
	ix.byName[sk.Name] = sk
	ix.skills = append(ix.skills, sk)
}

// ParseFile parses one skill file.
func ParseFile(path, scope string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("skill %q: cannot read: %v", path, err)
	}
	return ParseText(filepath.Base(path), scope, string(data), path)
}

// ParseText parses a skill from an in-memory source such as an embedded
// bundled skill. It shares the same caps and validation as filesystem skills.
func ParseText(filename, scope, text, path string) (Skill, error) {
	name := strings.TrimSuffix(filename, ".md")
	desc, body := "", text
	if strings.HasPrefix(text, "---\n") || strings.HasPrefix(text, "---\r\n") {
		rest := text[4:]
		end := strings.Index(rest, "\n---")
		if end < 0 {
			return Skill{}, fmt.Errorf("skill %q: frontmatter opened with --- but never closed — close it with --- on its own line", path)
		}
		var fm struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		}
		if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
			return Skill{}, fmt.Errorf("skill %q: bad frontmatter YAML: %v — fix the name:/description: lines", path, err)
		}
		if fm.Name != "" {
			name = fm.Name
		}
		desc = strings.TrimSpace(fm.Description)
		body = strings.TrimSpace(rest[end+4:])
	}
	if desc == "" {
		return Skill{}, fmt.Errorf("skill %q: missing description — add a `description:` line to the frontmatter (one line is what enters the prompt)", path)
	}
	// The description IS the routing algorithm (front-loaded keywords +
	// explicit scope); a stub description routes nowhere or everywhere.
	if len([]rune(desc)) < 12 {
		return Skill{}, fmt.Errorf("skill %q: description too short (%d chars, need 12+) — write what the skill is FOR so the model can route to it", path, len([]rune(desc)))
	}
	if strings.TrimSpace(body) == "" {
		return Skill{}, fmt.Errorf("skill %q: empty body — write what the agent should do when this skill loads", path)
	}
	// Context discipline: an unbounded body eats the window of every
	// session that loads it. Split into scripts/references instead.
	if words := len(strings.Fields(body)); words > 8000 {
		return Skill{}, fmt.Errorf("skill %q: body is %d words (cap 8000) — split logic into scripts/ and details into references/", path, words)
	}
	return Skill{Name: name, Description: desc, Body: body, Scope: scope, Path: path}, nil
}
