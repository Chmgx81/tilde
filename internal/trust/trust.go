// Package trust implements the project-folder trust gate (Pi
// resolveProjectTrusted pattern): a root is trusted only if explicitly
// recorded in ~/.tilde/trusted.json; there is no auto-trust.
// Headless default deny: miss, missing/unreadable file, or corrupt JSON
// all resolve to untrusted, and headless callers must never write trust
// without an explicit user allow action.
package trust

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// StorePath returns ~/.tilde/trusted.json.
func StorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tilde", "trusted.json"), nil
}

// load reads the store; missing file or corrupt JSON yields an empty
// map (deny) rather than failing open.
func load(path string) map[string]bool {
	m := map[string]bool{}
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]bool{}
	}
	return m
}

// IsTrusted reports whether absRoot (absolute) is recorded as trusted.
// Any failure or unknown key returns false.
func IsTrusted(absRoot string) bool {
	if absRoot == "" {
		return false
	}
	path, err := StorePath()
	if err != nil {
		return false
	}
	return load(path)[filepath.Clean(absRoot)]
}

// SetTrusted records (v=true) or clears (v=false) trust for absRoot.
// Creates ~/.tilde with 0700; writes atomically via temp file + rename
// with the store file set to 0600.
func SetTrusted(absRoot string, v bool) error {
	if absRoot == "" {
		return nil
	}
	path, err := StorePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	m := load(path)
	key := filepath.Clean(absRoot)
	if v {
		m[key] = true
	} else {
		delete(m, key)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "trusted-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	writeErr := func() error {
		if _, err := tmp.Write(data); err != nil {
			return err
		}
		return tmp.Close()
	}()
	if writeErr != nil {
		os.Remove(name)
		return writeErr
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
