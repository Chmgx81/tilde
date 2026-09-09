package skills

import (
	"embed"
	"fmt"
	"io/fs"
)

// bundledFS is part of the tilde binary. Bundled skills are instruction-only,
// versioned source files; they do not execute scripts or gain tool access.
//
//go:embed bundled/*.md
var bundledFS embed.FS

// Bundled returns the reviewed skills shipped with this tilde build.
func Bundled() ([]Skill, error) {
	var out []Skill
	err := fs.WalkDir(bundledFS, "bundled", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := bundledFS.ReadFile(path)
		if err != nil {
			return err
		}
		sk, err := ParseText(d.Name(), "bundled", string(data), "embedded://"+path)
		if err != nil {
			return fmt.Errorf("bundled skill %s: %w", path, err)
		}
		sk.Version = bundledVersion
		sk.Verified = true
		out = append(out, sk)
		return nil
	})
	return out, err
}

const bundledVersion = "1.0.0"
