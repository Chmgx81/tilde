package tools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// contain resolves p against root and requires the result to stay inside
// root — closing the file-tool path traversal the sandbox never covered
// (shell runs in bwrap; Read/Write/Edit/Grep run as plain host calls).
// Absolute paths are allowed only when contained. Symlinks resolve before
// the check, so link->outside escapes fail closed too.
func contain(root, p string) (string, error) {
	// A NUL byte can never appear in a real path, and every os/filesystem
	// call rejects one with a cryptic EINVAL. Refuse it here with a clear
	// message, once, for every file tool (read/write/edit/grep/glob).
	if strings.IndexByte(p, 0) >= 0 {
		return "", fmt.Errorf("refusing path with a NUL byte: send a normal project-relative path")
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(root, p)
	}
	abs = filepath.Clean(abs)
	// Resolve symlinks on the existing prefix; for not-yet-existing paths
	// resolve the parent and re-attach the final component, refusing if
	// the final component itself is a symlink.
	real := abs
	if st, err := os.Lstat(abs); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
			real = resolved
		} else if st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing %q: symlink target cannot be resolved — point at a real file inside the project and retry", p)
		}
	} else {
		parent := filepath.Dir(abs)
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			real = filepath.Join(resolved, filepath.Base(abs))
		} else {
			real = abs // parent missing too — containment check still applies
		}
	}
	cleanRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		cleanRoot = filepath.Clean(root)
	}
	if real != cleanRoot && !strings.HasPrefix(real, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing %q: outside the project root — work inside %s", p, root)
	}
	return abs, nil
}

// openNoFollow opens a contained path without following a final-component
// symlink (O_NOFOLLOW), then re-resolves the open fd via /proc/self/fd
// and re-runs containment: a link swapped in between contain() and open
// (TOCTOU) aborts here instead of following the planted link. Linux-only
// (/proc + O_NOFOLLOW); callers must Close the returned file.
func openNoFollow(root, p string, flag int, perm os.FileMode) (*os.File, string, error) {
	full, err := contain(root, p)
	if err != nil {
		return nil, "", err
	}
	f, err := os.OpenFile(full, flag|syscall.O_NOFOLLOW, perm)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, "", fmt.Errorf("refusing %q: symlink at the final path — point at a real file inside the project and retry", p)
		}
		return nil, "", err
	}
	resolved, rerr := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", f.Fd()))
	if rerr != nil {
		f.Close()
		return nil, "", fmt.Errorf("refusing %q: cannot verify the open file's identity — retry", p)
	}
	if cerr := recontain(root, resolved); cerr != nil {
		name := resolved
		f.Close()
		if flag&os.O_CREATE != 0 {
			os.Remove(name) // best-effort: undo a first-time create misdirected outside root
		}
		return nil, "", cerr
	}
	return f, full, nil
}

// recontain checks an already-absolute resolved path against root.
func recontain(root, resolved string) error {
	cleanRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		cleanRoot = filepath.Clean(root)
	}
	real := resolved
	if r, err := filepath.EvalSymlinks(resolved); err == nil {
		real = r
	}
	// A "(deleted)" suffix (unlinked between open and verify) matches
	// nothing: fail closed below.
	if real != cleanRoot && !strings.HasPrefix(real, cleanRoot+string(filepath.Separator)) {
		return fmt.Errorf("refusing %q: outside the project root — work inside %s", resolved, root)
	}
	return nil
}

// readFileNoFollow reads a contained file without following symlinks.
func readFileNoFollow(root, p string) ([]byte, error) {
	f, _, err := openNoFollow(root, p, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// writeFileNoFollow creates/truncates a contained file without following
// symlinks, atomically: content lands in a temp file in the same
// directory and is renamed over the target, so a crash or a full disk
// mid-write leaves the old file intact instead of a partial one.
// Parent dirs are created first (lexical path); a parent misdirected
// after the check is still caught by the pre-rename re-verify.
func writeFileNoFollow(root, p string, data []byte, perm os.FileMode) error {
	full, err := contain(root, p)
	if err != nil {
		return err
	}
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create parent dirs for %q: %v", p, err)
	}
	// Refuse a final-component symlink up front (rename would replace
	// the link itself, which is safe — but refusing keeps the error
	// identical to the read/open path and never surprises).
	if st, err := os.Lstat(full); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing %q: symlink at the final path — point at a real file inside the project and retry", p)
	}
	tmp, err := os.CreateTemp(dir, ".tilde-tmp-*")
	if err != nil {
		return fmt.Errorf("cannot create temp file for %q: %v", p, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup on every failure path below; a successful
	// rename removes tmpName implicitly (it becomes the target).
	failed := true
	defer func() {
		if failed {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("cannot write %q: %v", p, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("cannot write %q: %v", p, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cannot write %q: %v", p, err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("cannot write %q: %v", p, err)
	}
	// Pre-rename re-verify (TOCTOU close): the parent must still
	// resolve inside root (catches a dir swapped for a link to
	// outside after MkdirAll), and the target must not have become a
	// symlink while we wrote the temp file.
	if resolved, rerr := filepath.EvalSymlinks(dir); rerr != nil {
		return fmt.Errorf("refusing %q: cannot verify the parent dir's identity — retry", p)
	} else if cerr := recontain(root, resolved); cerr != nil {
		return cerr
	}
	if st, err := os.Lstat(full); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing %q: symlink at the final path — point at a real file inside the project and retry", p)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return fmt.Errorf("cannot write %q: %v", p, err)
	}
	failed = false
	return nil
}
