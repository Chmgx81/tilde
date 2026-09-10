package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// plansRel is the only directory this tool ever writes to, relative to Root.
const plansRel = ".tilde/plans"

// planSlugMax caps the slug length (~60 chars).
const planSlugMax = 60

// SavePlan persists a numbered implementation plan for user approval at
// <Root>/.tilde/plans/<slug>.md. Plan-safe by design: the name is absent
// from the mode gate's mutating set (verified, not wired here), so it stays
// visible and allowed in Plan mode. Overwrite is allowed (plan revision).
// Content is model-authored prose and is stored verbatim (no scrubbing).
type SavePlan struct {
	Root string
}

func (t *SavePlan) Name() string { return "save_plan" }
func (t *SavePlan) Description() string {
	return "Persists a numbered implementation plan for user approval."
}
func (t *SavePlan) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"title":   map[string]any{"type": "string", "description": "Plan title (used for the file slug)"},
		"content": map[string]any{"type": "string", "description": "The plan markdown"},
	}, "required": []string{"title", "content"}}
}

func (t *SavePlan) Exec(_ context.Context, args map[string]any) (string, error) {
	title, err := strArg(args, "title")
	if err != nil {
		return "", err
	}
	content, err := strArg(args, "content")
	if err != nil {
		return "", err
	}
	// Sanitize-then-verify: planSlug strips every non-alnum run (so the
	// slug can never contain / or ..), then contain() + the plansDir
	// prefix check below verify the final path stays under plans/.
	slug, err := planSlug(title)
	if err != nil {
		return "", err
	}
	rel := filepath.Join(plansRel, slug+".md")
	full, err := contain(t.Root, rel)
	if err != nil {
		return "", err
	}
	// Final path must stay under <Root>/.tilde/plans/ (slash-anchored
	// prefix so a sibling like plans-evil never passes).
	plansDir := filepath.Join(t.Root, plansRel)
	if full != plansDir && !strings.HasPrefix(full, plansDir+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing %q: outside the plans dir — work inside %s", rel, plansDir)
	}
	if err := writeFileNoFollow(t.Root, rel, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("saved plan to %s (overwrite allowed — re-saving the same title revises the plan). Present the plan to the user and stop: approval happens when the user switches to Build; this tool approves nothing.", rel), nil
}

// planSlug derives a file slug from the title: lowercase, non-alnum runs to
// a single hyphen, trimmed, capped at planSlugMax chars. Empty result errors.
func planSlug(title string) (string, error) {
	lower := strings.ToLower(title)
	var b strings.Builder
	prevHyphen := true // leading trim: skip hyphens at the start
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		if !prevHyphen {
			b.WriteRune('-')
			prevHyphen = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > planSlugMax {
		slug = strings.Trim(slug[:planSlugMax], "-")
	}
	if slug == "" {
		return "", fmt.Errorf("refusing title %q: nothing usable left for a file name — pick a title with letters or digits", title)
	}
	return slug, nil
}
