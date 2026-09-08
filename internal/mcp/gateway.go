package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"tilde/internal/tools"
)

// ListTool reports servers and tool names/descriptions. Full input
// schemas stay server-side until a call needs them — this listing is the
// whole prompt cost of MCP.
type ListTool struct {
	Mgr *Manager
}

func (t *ListTool) Name() string { return "mcp_list" }
func (t *ListTool) Description() string {
	return "List MCP servers and their tools (names + one-line descriptions). Call mcp_call to invoke one."
}
func (t *ListTool) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"server": map[string]any{"type": "string", "description": "Server name (omit to list all servers)"},
		}}
}

func (t *ListTool) Exec(_ context.Context, args map[string]any) (string, error) {
	if t.Mgr == nil || len(t.Mgr.Live()) == 0 {
		return "", fmt.Errorf("no MCP servers live: configure some in .tilde/mcp.json (project) or ~/.tilde/mcp.json (user) and restart")
	}
	if s, _ := args["server"].(string); s != "" {
		names := t.Mgr.serverToolNames(s)
		if names == nil {
			return "", fmt.Errorf("unknown server %q. Live: %s", s, strings.Join(t.Mgr.Live(), ", "))
		}
		t.Mgr.mu.Lock()
		srv := t.Mgr.servers[s]
		t.Mgr.mu.Unlock()
		srv.mu.Lock()
		var lines []string
		for _, ti := range srv.tools {
			lines = append(lines, fmt.Sprintf("- %s — %s", ti.Name, ti.Description))
		}
		srv.mu.Unlock()
		sort.Strings(lines)
		// Server names and tool descriptions are third-party content:
		// fence them like mcp_call output.
		return tools.Fence(fmt.Sprintf("server %s tools (invoke via mcp_call {\"server\": %q, \"tool\": name, \"arguments\": {...}}):\n%s",
			s, s, strings.Join(lines, "\n"))), nil
	}
	var lines []string
	for srv, names := range t.Mgr.ServerTools() {
		lines = append(lines, fmt.Sprintf("- %s — tools: %s", srv, strings.Join(names, ", ")))
	}
	sort.Strings(lines)
	return tools.Fence("MCP servers (detail via mcp_list {\"server\": name}):\n" + strings.Join(lines, "\n")), nil
}

// CallTool invokes one MCP tool. Arguments are passed through; schema
// mismatches come back as server errors with recovery text.
type CallTool struct {
	Mgr *Manager
}

func (t *CallTool) Name() string { return "mcp_call" }
func (t *CallTool) Description() string {
	return "Call an MCP server tool. Find names via mcp_list first."
}
func (t *CallTool) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"server":    map[string]any{"type": "string"},
			"tool":      map[string]any{"type": "string"},
			"arguments": map[string]any{"type": "object", "description": "Tool arguments object"},
		}, "required": []string{"server", "tool"}}
}

func (t *CallTool) Exec(ctx context.Context, args map[string]any) (string, error) {
	server, _ := args["server"].(string)
	tool, _ := args["tool"].(string)
	if server == "" || tool == "" {
		return "", fmt.Errorf("missing server/tool: send {\"server\": name, \"tool\": name, \"arguments\": {...}} — discover names with mcp_list")
	}
	var argv map[string]any
	if a, ok := args["arguments"].(map[string]any); ok {
		argv = a
	}
	if t.Mgr == nil {
		return "", fmt.Errorf("no MCP servers live")
	}
	out, err := t.Mgr.Call(ctx, server, tool, argv)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("mcp %s.%s returned empty output — not an error by itself; check with mcp_list whether this tool needs different arguments", server, tool)
	}
	// MCP output is third-party content: fence it like shell output.
	return tools.Fence(out), nil
}

var (
	_ tools.Tool = (*ListTool)(nil)
	_ tools.Tool = (*CallTool)(nil)
)
