// auth.go — headless credential management (`tilde login` / `tilde logout`).
//
// The TUI's /login arms interactive key entry; this is the scriptable
// equivalent for CI, dotfile setup, and anyone who prefers the shell.
// The key is NEVER taken from argv (it would leak through `ps` and shell
// history): it comes from the documented env var, or from stdin (read
// with no echo when a terminal is attached).
//
// Two kinds of credential live in the same sealed store: model-provider
// keys (the ladder in internal/provider resolves them) and non-model
// service tokens (see serviceTargets). The store itself is a generic map,
// so both ride the same envelope, locking, and masking.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"tilde/internal/creds"
	"tilde/internal/provider"
)

// credTarget is one storable credential: its id and the ambient env var
// that can supply it.
type credTarget struct {
	ID     string
	EnvKey string
}

// serviceTarget is a non-model credential (a CLI/service token). The table
// is the one-line extension point for future service keys: add an entry and
// it becomes storable, listable, and removable with no other change. Empty
// today — model providers are derived from the registry above.
type serviceTarget struct {
	ID     string
	EnvKey string
}

var serviceTargets = []serviceTarget{}

// credTargets is the full set `tilde login` accepts, in display order.
func credTargets() []credTarget { return credTargetsWith(serviceTargets) }

// credTargetsWith builds the target list for a given service table (the
// injection point for tests). Model providers with NeedsKey OR OptionalKey
// are included: Ollama Cloud keys are optional but storable.
func credTargetsWith(services []serviceTarget) []credTarget {
	var out []credTarget
	for _, d := range provider.Descriptions {
		if !d.NeedsKey && !d.OptionalKey {
			continue // purely local daemons: nothing to store
		}
		out = append(out, credTarget{ID: d.ID, EnvKey: d.EnvKey})
	}
	for _, s := range services {
		out = append(out, credTarget{ID: s.ID, EnvKey: s.EnvKey})
	}
	return out
}

// lookupCredTarget finds one target by id.
func lookupCredTarget(id string) (credTarget, bool) {
	return lookupCredTargetIn(credTargets(), id)
}

func lookupCredTargetIn(targets []credTarget, id string) (credTarget, bool) {
	for _, t := range targets {
		if t.ID == id {
			return t, true
		}
	}
	return credTarget{}, false
}

// runAuthCmd implements `tilde login [provider]` and
// `tilde logout [provider]`. With no provider it prints the credential
// ladder's status (masked tails only).
func runAuthCmd(verb string, args []string) error {
	store, err := openCredStore()
	if err != nil {
		return err
	}
	rest := args[1:]
	// `--list` (or a bare verb) reports status; any other first arg is
	// the credential id.
	if len(rest) == 0 || rest[0] == "--list" || rest[0] == "-l" {
		printAuthStatus(store)
		return nil
	}
	id := strings.ToLower(strings.TrimSpace(rest[0]))
	if len(rest) > 1 {
		return fmt.Errorf("usage: tilde %s [provider|service]", verb)
	}
	target, ok := lookupCredTarget(id)
	if !ok {
		return fmt.Errorf("unknown credential %q — options: %s", id, strings.Join(credIDs(), ", "))
	}
	if verb == "logout" {
		if err := store.Delete(id); err != nil {
			return err
		}
		fmt.Printf("tilde: removed stored %s credential\n", id)
		return nil
	}
	// login
	key, err := readCredential(target)
	if err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("no %s credential provided — set $%s or pipe it on stdin", id, target.EnvKey)
	}
	if err := store.Set(id, key); err != nil {
		return err
	}
	fmt.Printf("tilde: stored %s credential %s in the sealed credential store\n", id, creds.Mask(key))
	if id == "ollama" {
		fmt.Println("tilde: note: an Ollama key is only sent when $OLLAMA_HOST points at a remote host (e.g. https://ollama.com); the local daemon ignores it.")
	}
	return nil
}

// credIDs lists storable credential ids for usage errors.
func credIDs() []string {
	ts := credTargets()
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

// openCredStore builds the default sealed credential store. A home-dir
// failure is fatal here (unlike startup, which degrades to env-only):
// the whole point of the command is to write the store.
func openCredStore() (*creds.Store, error) {
	p, err := creds.DefaultPath()
	if err != nil {
		return nil, err
	}
	return creds.New(p), nil
}

// readCredential gets a credential without ever touching argv: the
// target's env var wins (so `OPENCODE_API_KEY=… tilde login opencode`
// works in a script), else stdin — hidden when a terminal is attached,
// one line when piped.
func readCredential(t credTarget) (string, error) {
	if t.EnvKey != "" {
		if k := strings.TrimSpace(os.Getenv(t.EnvKey)); k != "" {
			return k, nil
		}
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(os.Stderr, "paste %s credential (input hidden): ", t.ID)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read credential: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	sc := bufio.NewScanner(os.Stdin)
	if sc.Scan() {
		return strings.TrimSpace(sc.Text()), nil
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read credential from stdin: %w", err)
	}
	return "", nil
}

// printAuthStatus renders the ladder's verdict for every model backend
// (masked tails, never a full key), then any extra stored service
// credentials that have no provider row.
func printAuthStatus(store *creds.Store) {
	seen := map[string]bool{}
	for _, st := range provider.Status(store, nil) {
		seen[st.Provider.ID] = true
		tail := ""
		if st.KeyTail != "" {
			tail = " " + st.KeyTail
		}
		fmt.Printf("%-11s %-6s%s\n", st.Provider.ID, st.Source, tail)
	}
	for _, t := range credTargets() {
		if seen[t.ID] {
			continue // provider rows already printed
		}
		if store == nil {
			break
		}
		if k, err := store.Get(t.ID); err == nil && k != "" {
			fmt.Printf("%-11s %-6s %s\n", t.ID, "stored", creds.Mask(k))
		} else {
			fmt.Printf("%-11s %-6s\n", t.ID, "none")
		}
	}
}
