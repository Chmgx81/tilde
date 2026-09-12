// doctor.go — `tilde doctor`: one command that answers "is this install
// healthy?" without starting a session.
//
// It checks the things that make tilde refuse to start or silently
// misbehave: the sandbox backstop, the policy file, credential
// availability, session/audit writability, git, and provider selection.
// Read-only except for a temp-file writability probe that is removed
// immediately. Exit 2 when a hard check fails, so it is scriptable.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"tilde/internal/creds"
	"tilde/internal/policy"
	"tilde/internal/provider"
	"tilde/internal/sandbox"
)

// doctorStatus is the severity of one check.
type doctorStatus string

const (
	docOK   doctorStatus = "ok"
	docWarn doctorStatus = "warn"
	docFail doctorStatus = "fail"
	docInfo doctorStatus = "info"
)

// doctorCheck is one reported line.
type doctorCheck struct {
	Name   string       `json:"name"`
	Status doctorStatus `json:"status"`
	Detail string       `json:"detail"`
}

// doctorProviderConfig carries the provider flags main already parsed, so
// the provider check reflects exactly what startup would build.
type doctorProviderConfig struct {
	Provider string
	Model    string
	Base     string
	Key      string
}

// runDoctorCmd implements `tilde doctor [--json]`. It returns the number
// of hard failures (the caller maps that to exit 2) so the checks are
// testable without os.Exit.
func runDoctorCmd(args []string, cfg doctorProviderConfig) (fails int, err error) {
	asJSON := false
	for _, a := range args[1:] {
		switch a {
		case "--json":
			asJSON = true
		case "":
		default:
			return 0, fmt.Errorf("usage: tilde doctor [--json] — got %q", a)
		}
	}
	root, err := os.Getwd()
	if err != nil {
		return 0, fmt.Errorf("cannot determine working dir: %w", err)
	}
	checks := doctorChecks(root, cfg)
	if asJSON {
		for _, c := range checks {
			b, merr := json.Marshal(c)
			if merr != nil {
				return 0, merr
			}
			fmt.Println(string(b))
		}
	} else {
		printDoctor(checks)
	}
	return countStatus(checks, docFail), nil
}

// doctorChecks runs every probe. Ordered so the things that block startup
// come first.
func doctorChecks(root string, cfg doctorProviderConfig) []doctorCheck {
	var out []doctorCheck
	out = append(out, checkSandbox())
	out = append(out, checkPolicy(root))
	out = append(out, checkCredentials())
	out = append(out, checkWritableDir("sessions", sessionDirForDoctor()))
	out = append(out, checkWritableDir("audit", auditDirForDoctor()))
	out = append(out, checkGit(root))
	out = append(out, checkProvider(cfg))
	out = append(out, checkNetwork())
	return out
}

func checkSandbox() doctorCheck {
	line := sandbox.StatusLine()
	switch {
	case sandbox.Disabled():
		return doctorCheck{"sandbox", docWarn, line + " — shell commands run unsandboxed"}
	case sandbox.Enforced():
		return doctorCheck{"sandbox", docOK, line}
	default:
		return doctorCheck{"sandbox", docFail, line + " — install bubblewrap, or pass --no-sandbox to accept the risk"}
	}
}

func checkPolicy(root string) doctorCheck {
	path := filepath.Join(root, "policies.yaml")
	f, err := policy.Load(path)
	if err != nil {
		return doctorCheck{"policy", docFail, fmt.Sprintf("%s: %v", path, err)}
	}
	if f == nil {
		return doctorCheck{"policy", docInfo, "no policies.yaml — built-in defaults apply"}
	}
	return doctorCheck{"policy", docOK, fmt.Sprintf("%s: %d deny, %d ask, %d allow, %d allow_net, %d deny_paths",
		path, len(f.Deny), len(f.Ask), len(f.Allow), len(f.AllowNet), len(f.DenyPaths))}
}

func checkCredentials() doctorCheck {
	store := openCredStoreOrNil()
	var ready, missing []string
	for _, st := range provider.Status(store, nil) {
		switch {
		case st.Provider.NeedsKey:
			if st.Source == provider.AuthNone {
				missing = append(missing, st.Provider.ID)
			} else {
				ready = append(ready, st.Provider.ID+"("+st.Source.String()+")")
			}
		case st.Provider.OptionalKey:
			// Ollama: no key is fine (local daemon); a key means Cloud.
			if st.Source != provider.AuthNone {
				ready = append(ready, st.Provider.ID+"("+st.Source.String()+")")
			}
		}
	}
	if len(ready) == 0 {
		if ollamaRemoteNoKey() {
			return doctorCheck{"credentials", docWarn, "OLLAMA_HOST is remote but no OLLAMA_API_KEY resolved — run `tilde login ollama` (or set the env var), or unset OLLAMA_HOST for the local daemon"}
		}
		return doctorCheck{"credentials", docInfo, "no cloud keys configured — `tilde login <provider>` (local Ollama needs none)"}
	}
	sort.Strings(ready)
	sort.Strings(missing)
	var parts []string
	if len(ready) > 0 {
		parts = append(parts, "ready: "+strings.Join(ready, ", "))
	}
	if len(missing) > 0 {
		parts = append(parts, "no key: "+strings.Join(missing, ", "))
	}
	status := docOK
	if ollamaRemoteNoKey() {
		status = docWarn
		parts = append(parts, "OLLAMA_HOST is remote but no OLLAMA_API_KEY resolved — run `tilde login ollama`")
	}
	return doctorCheck{"credentials", status, strings.Join(parts, " · ")}
}

// ollamaRemoteNoKey reports the classic Ollama Cloud misconfiguration:
// $OLLAMA_HOST points at a remote host but no key resolved from the store
// or the env var. A local/loopback host is fine with no key.
func ollamaRemoteNoKey() bool {
	h := strings.ToLower(strings.TrimSpace(os.Getenv("OLLAMA_HOST")))
	if h == "" || strings.Contains(h, "localhost") || strings.Contains(h, "127.0.0.1") || strings.Contains(h, "::1") {
		return false
	}
	if strings.TrimSpace(os.Getenv("OLLAMA_API_KEY")) != "" {
		return false
	}
	if store := openCredStoreOrNil(); store != nil {
		if k, err := store.Get("ollama"); err == nil && strings.TrimSpace(k) != "" {
			return false
		}
	}
	return true
}

func checkWritableDir(label, dir string) doctorCheck {
	if dir == "" {
		return doctorCheck{label + "-dir", docWarn, "cannot resolve home dir"}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return doctorCheck{label + "-dir", docFail, fmt.Sprintf("%s: %v", dir, err)}
	}
	f, err := os.CreateTemp(dir, ".doctor-probe-*")
	if err != nil {
		return doctorCheck{label + "-dir", docFail, fmt.Sprintf("%s: not writable: %v", dir, err)}
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return doctorCheck{label + "-dir", docOK, dir + " writable"}
}

func checkGit(root string) doctorCheck {
	p, err := exec.LookPath("git")
	if err != nil {
		return doctorCheck{"git", docWarn, "git not on PATH — git tools and /undo shell snapshots are unavailable"}
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return doctorCheck{"git", docInfo, p + " (current dir is not a git repo)"}
	}
	return doctorCheck{"git", docOK, p}
}

func checkProvider(cfg doctorProviderConfig) doctorCheck {
	p, why := selectProvider(cfg.Provider, cfg.Model, cfg.Base, cfg.Key, openCredStoreOrNil())
	if why != "" {
		return doctorCheck{"provider", docFail, why}
	}
	return doctorCheck{"provider", docOK, "constructed: " + p.Name()}
}

func checkNetwork() doctorCheck {
	if sandbox.NetAllowed() {
		return doctorCheck{"network", docWarn, "TILDE_ALLOW_NET=1 — shell egress is open for this session"}
	}
	return doctorCheck{"network", docInfo, "egress denied (set TILDE_ALLOW_NET=1 or policies.yaml allow_net to permit)"}
}

// openCredStoreOrNil builds the default store, or nil when home is
// unresolvable (the checks then report env-only availability).
func openCredStoreOrNil() *creds.Store {
	p, err := creds.DefaultPath()
	if err != nil {
		return nil
	}
	return creds.New(p)
}

func sessionDirForDoctor() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".tilde", "sessions")
}

func auditDirForDoctor() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".tilde", "audit")
}

// printDoctor renders the checks with a redundant glyph+word marker, so
// the report stays readable in monochrome and under NO_COLOR.
func printDoctor(checks []doctorCheck) {
	width := 0
	for _, c := range checks {
		if n := len(c.Name); n > width {
			width = n
		}
	}
	for _, c := range checks {
		fmt.Printf("%s %-*s %s\n", doctorMark(c.Status), width, c.Name, c.Detail)
	}
	if fails := countStatus(checks, docFail); fails > 0 {
		fmt.Fprintf(os.Stderr, "\ntilde doctor: %d check(s) failed — fix the lines marked %s before starting a session.\n", fails, doctorMark(docFail))
	} else if warns := countStatus(checks, docWarn); warns > 0 {
		fmt.Fprintf(os.Stderr, "\ntilde doctor: all required checks passed (%d warning(s)).\n", warns)
	} else {
		fmt.Fprintln(os.Stderr, "\ntilde doctor: all checks passed.")
	}
}

func countStatus(checks []doctorCheck, s doctorStatus) int {
	n := 0
	for _, c := range checks {
		if c.Status == s {
			n++
		}
	}
	return n
}

// doctorMark maps a status to a glyph + word. The word is load-bearing:
// color is never the only signal.
func doctorMark(s doctorStatus) string {
	switch s {
	case docOK:
		return "● ok  "
	case docWarn:
		return "▲ warn"
	case docFail:
		return "✗ FAIL"
	default:
		return "○ info"
	}
}
