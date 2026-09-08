package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMCPHelper doubles as the fake MCP server when re-executed with
// TILDE_TEST_MCP_SERVER=1 (line-delimited JSON-RPC over stdio).
func TestMCPHelper(t *testing.T) {
	if os.Getenv("TILDE_TEST_MCP_SERVER") != "1" {
		return
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1024*1024), 1024*1024)
	enc := json.NewEncoder(os.Stdout)
	idOf := func(m map[string]any) any { return m["id"] }
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		method, _ := msg["method"].(string)
		params, _ := msg["params"].(map[string]any)
		reply := func(result any) {
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": idOf(msg), "result": result})
		}
		switch method {
		case "initialize":
			reply(map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fake"}})
		case "tools/list":
			reply(map[string]any{"tools": []any{
				map[string]any{"name": "shout", "description": "Uppercase text", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []any{"text"}}},
				map[string]any{"name": "boom", "description": "Always fails", "inputSchema": map[string]any{"type": "object"}},
			}})
		case "tools/call":
			name, _ := params["name"].(string)
			switch name {
			case "shout":
				arg, _ := params["arguments"].(map[string]any)
				text, _ := arg["text"].(string)
				reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": strings.ToUpper(text)}}})
			default:
				reply(map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "boom failed as designed"}}})
			}
		}
	}
}

func testManager(t *testing.T) *Manager {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(map[string]ServerConfig{
		"fake": {Command: exe, Args: []string{"-test.run", "TestMCPHelper"}, Env: map[string]string{"TILDE_TEST_MCP_SERVER": "1"}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	return mgr
}

func TestConnectAndList(t *testing.T) {
	mgr := testManager(t)
	if live := mgr.Live(); len(live) != 1 || live[0] != "fake" {
		t.Fatalf("live=%v", live)
	}
	st := mgr.ServerTools()
	if len(st["fake"]) != 2 || st["fake"][0] != "boom" || st["fake"][1] != "shout" {
		t.Fatalf("tools=%v", st)
	}
	if _, ok := mgr.Schema("fake", "shout"); !ok {
		t.Fatal("schema should be cached")
	}
}

func TestCallShout(t *testing.T) {
	mgr := testManager(t)
	out, err := mgr.Call(context.Background(), "fake", "shout", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "HELLO" {
		t.Fatalf("got %q", out)
	}
}

func TestCallErrorSurfaces(t *testing.T) {
	mgr := testManager(t)
	_, err := mgr.Call(context.Background(), "fake", "boom", nil)
	if err == nil || !strings.Contains(err.Error(), "boom failed as designed") {
		t.Fatalf("got %v", err)
	}
}

func TestMissingRequiredFailsFast(t *testing.T) {
	mgr := testManager(t)
	_, err := mgr.Call(context.Background(), "fake", "shout", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "missing required argument(s) text") {
		t.Fatalf("expected instant arg validation, got %v", err)
	}
}

func TestUnknownNamesGuide(t *testing.T) {
	mgr := testManager(t)
	if _, err := mgr.Call(context.Background(), "nope", "shout", nil); err == nil || !strings.Contains(err.Error(), "Live servers") {
		t.Fatalf("got %v", err)
	}
	if _, err := mgr.Call(context.Background(), "fake", "nope", nil); err == nil || !strings.Contains(err.Error(), "shout") {
		t.Fatalf("got %v", err)
	}
}

func TestBadServerDoesNotKillManager(t *testing.T) {
	mgr := NewManager(map[string]ServerConfig{
		"dead": {Command: "tilde-definitely-missing-binary-xyz"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.Start(ctx); err == nil {
		t.Fatal("expected start error")
	}
	if len(mgr.Live()) != 0 {
		t.Fatal("dead server must not be live")
	}
}

func TestConfigLoad(t *testing.T) {
	dir := t.TempDir()
	if fc, err := LoadFile(dir + "/missing.json"); err != nil || len(fc.Servers) != 0 {
		t.Fatalf("missing file must be empty, not error: %v", err)
	}
	bad := dir + "/bad.json"
	os.WriteFile(bad, []byte("{oops"), 0o644)
	if _, err := LoadFile(bad); err == nil {
		t.Fatal("expected JSON error")
	}
	good := dir + "/good.json"
	os.WriteFile(good, []byte(`{"mcpServers":{"a":{"command":"x","args":["1"],"env":{"K":"V"}}}}`), 0o644)
	fc, err := LoadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	if fc.Servers["a"].Command != "x" || fc.Servers["a"].Args[0] != "1" || fc.Servers["a"].Env["K"] != "V" {
		t.Fatalf("%+v", fc)
	}
}

func TestListFenced(t *testing.T) {
	mgr := testManager(t)
	lt := &ListTool{Mgr: mgr}
	for _, args := range []map[string]any{{}, {"server": "fake"}} {
		out, err := lt.Exec(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "begin untrusted output") || !strings.Contains(out, "end untrusted output") {
			t.Fatalf("mcp_list output must be fenced, got %q", out)
		}
	}
	if _, err := lt.Exec(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayStaysLazy(t *testing.T) {
	// The gateway schemas must not contain any server tool shape —
	// connecting fifty tools costs lines, not schemas.
	ct := &CallTool{}
	props, _ := ct.Schema()["properties"].(map[string]any)
	for k := range props {
		if k != "server" && k != "tool" && k != "arguments" {
			t.Fatalf("gateway schema leaked %q", k)
		}
	}
	lt := &ListTool{}
	if _, err := lt.Exec(context.Background(), map[string]any{}); err == nil {
		t.Fatal("nil manager must error honestly")
	}
}
