package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"tilde/internal/tools"
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
	delayed := false
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
			if !delayed {
				if ms, _ := strconv.Atoi(os.Getenv("TILDE_TEST_MCP_DELAY_MS")); ms > 0 {
					time.Sleep(time.Duration(ms) * time.Millisecond)
				}
				delayed = true
			}
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
	return testManagerWith(t, ServerConfig{ApprovalDefault: "auto"})
}

func testManagerWith(t *testing.T, cfg ServerConfig) *Manager {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Command == "" {
		cfg.Command = exe
		cfg.Args = []string{"-test.run", "TestMCPHelper"}
		cfg.Env = map[string]string{"TILDE_TEST_MCP_SERVER": "1"}
	}
	mgr := NewManager(map[string]ServerConfig{
		"fake": cfg,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	return mgr
}

func boolPtr(b bool) *bool { return &b }

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

func TestMergeProjectCannotOverrideUserCommand(t *testing.T) {
	user := FileConfig{Servers: map[string]ServerConfig{
		"s": {Command: "safe", Args: []string{"a"}, Env: map[string]string{"K": "V"},
			Type: "local", HeadersFile: "u.h", Approval: map[string]string{"t": "prompt"}},
	}}
	project := FileConfig{Servers: map[string]ServerConfig{
		"s": {Command: "evil", Args: []string{"x"}, Env: map[string]string{"K": "E"},
			Type: "remote", URL: "http://evil.example/", HeadersFile: "p.h",
			Approval: map[string]string{"t": "auto"}},
		"new": {Command: "added"},
	}}
	merged := Merge(user, project)
	s := merged["s"]
	if s.Command != "safe" || len(s.Args) != 1 || s.Args[0] != "a" || s.Env["K"] != "V" {
		t.Fatalf("project rewired user command: %+v", s)
	}
	if s.Type != "local" || s.URL != "" || s.HeadersFile != "u.h" {
		t.Fatalf("project changed user transport/headers: %+v", s)
	}
	if s.Approval["t"] != "prompt" {
		t.Fatalf("project loosened user approval: %+v", s.Approval)
	}
	if merged["new"].Command != "added" {
		t.Fatalf("brand-new project server must be added: %+v", merged["new"])
	}
}

func TestMergeProjectCanDisable(t *testing.T) {
	user := FileConfig{Servers: map[string]ServerConfig{
		"s": {Command: "x"},
	}}
	project := FileConfig{Servers: map[string]ServerConfig{
		"s": {Enabled: boolPtr(false)},
	}}
	if merged := Merge(user, project); merged["s"].IsEnabled() {
		t.Fatal("project enabled=false must disable a user server")
	}
	// ...but a project enabled=true must not re-enable a user-disabled server.
	userOff := FileConfig{Servers: map[string]ServerConfig{
		"s": {Command: "x", Enabled: boolPtr(false)},
	}}
	projectOn := FileConfig{Servers: map[string]ServerConfig{
		"s": {Enabled: boolPtr(true)},
	}}
	if merged := Merge(userOff, projectOn); merged["s"].IsEnabled() {
		t.Fatal("project must not re-enable a user-disabled server")
	}
}

func TestMergeProjectApprovalTightenOnly(t *testing.T) {
	user := FileConfig{Servers: map[string]ServerConfig{
		"s": {Command: "x", Approval: map[string]string{"a": "auto"}, ApprovalDefault: "auto"},
	}}
	project := FileConfig{Servers: map[string]ServerConfig{
		"s": {Approval: map[string]string{"a": "prompt", "b": "auto"}, ApprovalDefault: "prompt"},
	}}
	merged := Merge(user, project)["s"]
	if merged.Approval["a"] != "prompt" {
		t.Fatalf("project must tighten auto->prompt: %+v", merged.Approval)
	}
	if _, ok := merged.Approval["b"]; ok {
		t.Fatalf("project must not inject auto entries: %+v", merged.Approval)
	}
	if merged.ApprovalDefault != "prompt" {
		t.Fatalf("project must tighten default toward prompt: %q", merged.ApprovalDefault)
	}
	// User's own map must not be mutated by the tighten.
	if user.Servers["s"].Approval["a"] != "auto" {
		t.Fatal("merge mutated the user config approval map")
	}
}

func TestApprovalPromptBlocks(t *testing.T) {
	mgr := testManagerWith(t, ServerConfig{}) // default: everything prompt
	if _, err := mgr.Call(context.Background(), "fake", "shout", map[string]any{"text": "hi"}); err == nil ||
		!strings.Contains(err.Error(), "needs user approval") {
		t.Fatalf("prompt-gated call must block naming approval, got %v", err)
	}
	// The approved path (policy tier already asked) goes through.
	out, err := mgr.CallApproved(context.Background(), "fake", "shout", map[string]any{"text": "hi"}, true)
	if err != nil || out != "HI" {
		t.Fatalf("approved call must run, got %q, %v", out, err)
	}
	// Explicit auto never asks.
	mgrAuto := testManagerWith(t, ServerConfig{Approval: map[string]string{"shout": "auto"}})
	if out, err := mgrAuto.Call(context.Background(), "fake", "shout", map[string]any{"text": "hi"}); err != nil || out != "HI" {
		t.Fatalf("auto call must run, got %q, %v", out, err)
	}
}

func TestGatewayEnforcesNestedApproval(t *testing.T) {
	mgr := testManagerWith(t, ServerConfig{}) // default: every nested tool is prompt-gated
	call := &CallTool{Mgr: mgr}
	if _, err := call.Exec(context.Background(), map[string]any{
		"server": "fake", "tool": "shout", "arguments": map[string]any{"text": "hi"},
	}); err == nil || !strings.Contains(err.Error(), "needs user approval") {
		t.Fatalf("outer mcp_call approval must not bypass nested approval, got %v", err)
	}
	reg := tools.NewRegistry()
	reg.Register(call)
	reg.Gate = func(string, map[string]any) (bool, string) { return true, "approved" }
	if out := reg.Dispatch(context.Background(), "mcp_call", map[string]any{
		"server": "fake", "tool": "shout", "arguments": map[string]any{"text": "hi"},
	}); !strings.Contains(out, "needs user approval") {
		t.Fatalf("registry-approved outer call must not bypass the nested gate, got %q", out)
	}

	auto := testManagerWith(t, ServerConfig{Approval: map[string]string{"shout": "auto"}})
	out, err := (&CallTool{Mgr: auto}).Exec(context.Background(), map[string]any{
		"server": "fake", "tool": "shout", "arguments": map[string]any{"text": "hi"},
	})
	if err != nil || !strings.Contains(out, "HI") {
		t.Fatalf("explicit nested auto approval must run, got %q, %v", out, err)
	}
}

func TestStartIsConcurrentWithOneTotalDeadline(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := func() ServerConfig {
		return ServerConfig{Command: exe, Args: []string{"-test.run", "TestMCPHelper"}, Env: map[string]string{
			"TILDE_TEST_MCP_SERVER":   "1",
			"TILDE_TEST_MCP_DELAY_MS": "200",
		}}
	}
	mgr := NewManager(map[string]ServerConfig{"a": cfg(), "b": cfg(), "c": cfg()})
	defer mgr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("concurrent startup should finish all servers within one deadline: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 400*time.Millisecond {
		t.Fatalf("startup used more than its total deadline: %v", elapsed)
	}
	if live := mgr.Live(); len(live) != 3 {
		t.Fatalf("live=%v, want all servers", live)
	}
}

func TestStartDeadlineStopsSlowServers(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(map[string]ServerConfig{
		"slow-a": {Command: exe, Args: []string{"-test.run", "TestMCPHelper"}, Env: map[string]string{
			"TILDE_TEST_MCP_SERVER":   "1",
			"TILDE_TEST_MCP_DELAY_MS": "2000",
		}},
		"slow-b": {Command: exe, Args: []string{"-test.run", "TestMCPHelper"}, Env: map[string]string{
			"TILDE_TEST_MCP_SERVER":   "1",
			"TILDE_TEST_MCP_DELAY_MS": "2000",
		}},
	})
	defer mgr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := mgr.Start(ctx); err == nil {
		t.Fatal("slow startup must report the deadline failure")
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("startup exceeded its total deadline by too much: %v", elapsed)
	}
	if live := mgr.Live(); len(live) != 0 {
		t.Fatalf("deadline-expired servers must not remain live: %v", live)
	}
}

func TestRemoteURLSSRFBlocked(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()
	for _, raw := range []string{
		"http://127.0.0.1:8080/rpc",
		"http://10.0.0.1/rpc",
		"http://169.254.169.254/latest/meta-data/",
		"http://192.168.1.1/rpc",
		"http://100.64.0.1/rpc",
		"http://[::1]/rpc",
		"http://localhost:3000/rpc",
	} {
		err := m.startOne(ctx, "evil", ServerConfig{Type: "remote", URL: raw})
		if err == nil || (!strings.Contains(err.Error(), "private/loopback") && !strings.Contains(err.Error(), "local host")) {
			t.Fatalf("SSRF url %q must be rejected, got %v", raw, err)
		}
	}
	// A public literal passes the SSRF gate and is a valid remote config even
	// without a local command; remote servers use HTTP JSON-RPC instead.
	if err := checkRemoteURL("pub", "https://8.8.8.8/rpc"); err != nil {
		t.Fatalf("public remote url must pass the gate, got %v", err)
	}
	if err := validateServerConfig("pub", &ServerConfig{Type: "remote", URL: "https://8.8.8.8/rpc"}); err != nil {
		t.Fatalf("remote config without local command must validate, got %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRemoteCallUsesHTTPJSONRPC(t *testing.T) {
	s := &server{name: "test", remoteURL: "https://public.example/rpc", headers: map[string]string{"Authorization": "Bearer test"}, client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("missing configured authorization header")
		}
		var msg rpcMsg
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(rpcMsg{JSONRPC: "2.0", ID: msg.ID, Result: map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
	})}, log: &ringBuffer{max: 4096}}
	var result map[string]any
	if err := s.call(context.Background(), "tools/list", nil, &result); err != nil {
		t.Fatal(err)
	}
	if result["content"] == nil {
		t.Fatalf("unexpected result: %#v", result)
	}
}
