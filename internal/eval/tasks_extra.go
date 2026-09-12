package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tilde/internal/agent"
)

// ExtraTasks extends the suite with skill, MCP, and chain scenarios.
func ExtraTasks() []Task {
	return []Task{
		{
			Name:     "skill-fact",
			Goal:     "Call load_skill for the skill named 'facts', then reply with the vault code it contains.",
			MaxIters: 10,
			Setup: func(root string) error {
				dir := filepath.Join(root, ".tilde", "skills")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
				body := "---\nname: facts\ndescription: Vault facts lookup\n---\nThe vault code is 4815.\n"
				return os.WriteFile(filepath.Join(dir, "facts.md"), []byte(body), 0o644)
			},
			ExpectTools: []string{"load_skill"},
			Verify: func(_, final string, _ []agent.Event) (bool, string) {
				if !strings.Contains(final, "4815") {
					return false, "answer missing '4815': " + trunc(final, 80)
				}
				return true, "ok"
			},
		},
		{
			Name:        "mcp-shout",
			Goal:        "Call mcp_list, then call mcp_call exactly once with server fixture, tool shout, arguments {\"text\": \"hello\"}. Report the result.",
			MaxIters:    10,
			ExpectTools: []string{"mcp_list", "mcp_call"},
			Setup: func(root string) error {
				if err := os.MkdirAll(filepath.Join(root, ".tilde"), 0o755); err != nil {
					return err
				}
				script := filepath.Join(root, "fixture-mcp.sh")
				if err := os.WriteFile(script, []byte(fixtureMCP), 0o644); err != nil {
					return err
				}
				if err := os.Chmod(script, 0o755); err != nil {
					return err
				}
				cfg := fmt.Sprintf(`{"mcpServers":{"fixture":{"command":%q,"args":[]}}}`, script)
				return os.WriteFile(filepath.Join(root, ".tilde", "mcp.json"), []byte(cfg), 0o644)
			},
			Verify: func(_ string, _ string, events []agent.Event) (bool, string) {
				for _, e := range events {
					if e.Kind == "tool_result" && strings.Contains(e.Text, "HELLO") {
						return true, "ok"
					}
				}
				return false, "shout result HELLO never observed in trajectory"
			},
		},
		{
			Name:        "rename-chain",
			Goal:        "Use grep to find all .txt files containing OLDNAME, read each one, then use edit_file on each to replace OLDNAME with NEWNAME. Then report done.",
			MaxIters:    14,
			ExpectTools: []string{"grep", "read_file", "edit_file"},
			Setup: func(root string) error {
				for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
					if err := os.WriteFile(filepath.Join(root, n), []byte("holds OLDNAME token\n"), 0o644); err != nil {
						return err
					}
				}
				return nil
			},
			Verify: func(root, _ string, _ []agent.Event) (bool, string) {
				for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
					data, err := os.ReadFile(filepath.Join(root, n))
					if err != nil {
						return false, n + " missing"
					}
					s := string(data)
					if !strings.Contains(s, "NEWNAME") {
						return false, n + " missing NEWNAME"
					}
					if strings.Contains(s, "OLDNAME") {
						return false, n + " still contains OLDNAME"
					}
				}
				return true, "ok"
			},
		},
	}
}

// fixtureMCP is a bash-only stdio JSON-RPC server: initialize, tools/list
// (one tool shout), tools/call shout (uppercases text). Notifications drop.
const fixtureMCP = `#!/usr/bin/env bash
while IFS= read -r line; do
  case "$line" in
    "") continue ;;
  esac
  nosp="${line// /}"
  case "$nosp" in
    *notifications/*) continue ;;
  esac
  id=""
  if [[ "$nosp" =~ '"id":'([0-9]+) ]]; then
    id="${BASH_REMATCH[1]}"
  else
    continue
  fi
  case "$nosp" in
    *'"method":"initialize"'*)
      printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{},\"serverInfo\":{\"name\":\"fixture\"}}}"
      ;;
    *'"method":"tools/list"'*)
      printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"tools\":[{\"name\":\"shout\",\"description\":\"Uppercase text\",\"inputSchema\":{\"type\":\"object\",\"properties\":{\"text\":{\"type\":\"string\"}},\"required\":[\"text\"]}}]}}"
      ;;
    *'"method":"tools/call"'*)
      text=""
      tmp="${nosp#*'"text":"'}"
      if [[ "$tmp" != "$nosp" ]]; then
        text="${tmp%%'"'*}"
      fi
      upper="${text^^}"
      printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"$upper\"}]}}"
      ;;
  esac
done
`
