// Package mcp — minimal MCP client: stdio servers, tools/list, tools/call
// (docs/Plan.md Phase 6). Deliberately NOT a full SDK: no resources, prompts,
// or sampling. The laziness story: full input schemas never enter the
// prompt — the model sees server + tool NAMES via the prompt extras and
// mcp_list, and calls through the mcp_call gateway. Connecting a server
// with fifty tools costs lines, not thousands of tokens.
//
// Trust note: servers are child processes running OUTSIDE the bwrap
// sandbox with your user privileges. Only configure servers you trust,
// for the same reason you only install skills you trust.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// ServerConfig launches one server.
type ServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// FileConfig matches the {"mcpServers": {...}} convention.
type FileConfig struct {
	Servers map[string]ServerConfig `json:"mcpServers"`
}

// LoadFile reads one config file. Missing file = empty, not an error.
func LoadFile(path string) (FileConfig, error) {
	var fc FileConfig
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fc, nil
		}
		return fc, fmt.Errorf("mcp: cannot read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, &fc); err != nil {
		return fc, fmt.Errorf("mcp: bad JSON in %s: %v — expected {\"mcpServers\": {name: {command, args?, env?}}}", path, err)
	}
	if fc.Servers == nil {
		fc.Servers = map[string]ServerConfig{}
	}
	return fc, nil
}

// Merge overlays project config on user config (project wins per server).
func Merge(user, project FileConfig) map[string]ServerConfig {
	out := map[string]ServerConfig{}
	for n, s := range user.Servers {
		out[n] = s
	}
	for n, s := range project.Servers {
		out[n] = s
	}
	return out
}

// ToolInfo is one discovered tool (schema cached, prompt gets name only).
type ToolInfo struct {
	Server      string
	Name        string
	Description string
	Schema      map[string]any
}

type rpcMsg struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      *int64         `json:"id,omitempty"`
	Method  string         `json:"method,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
	Result  map[string]any `json:"result,omitempty"`
	Error   *rpcError      `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// server is one live child process.
type server struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	log    *ringBuffer

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcMsg
	tools   []ToolInfo
}

type ringBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf.Write(p)
	if r.buf.Len() > r.max {
		extra := r.buf.Len() - r.max
		r.buf.Next(extra)
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// Manager owns configured servers.
type Manager struct {
	mu      sync.Mutex
	servers map[string]*server
	configs map[string]ServerConfig
	// cmdCtx owns process lifetimes (cancelled in Close). It must NOT be
	// the start-timeout context — that would kill servers right after the
	// handshake. Handshake/call timeouts live on the call path instead.
	cmdCtx    context.Context
	cmdCancel context.CancelFunc
}

// NewManager builds (not starts) the configured servers.
func NewManager(configs map[string]ServerConfig) *Manager {
	return &Manager{servers: map[string]*server{}, configs: configs}
}

// ServerNames returns sorted configured names.
func (m *Manager) ServerNames() []string {
	var out []string
	for n := range m.configs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Live returns sorted successfully-started servers.
func (m *Manager) Live() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for n := range m.servers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ServerTools implements agent.MCPStatus: server → sorted tool names.
func (m *Manager) ServerTools() map[string][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]string{}
	for n, s := range m.servers {
		s.mu.Lock()
		var names []string
		for _, t := range s.tools {
			names = append(names, t.Name)
		}
		s.mu.Unlock()
		sort.Strings(names)
		out[n] = names
	}
	return out
}

// Start launches every configured server, handshakes, and lists tools.
// One bad server never kills the rest: failures return as a joined error
// naming each, while working servers stay live.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.cmdCtx, m.cmdCancel = context.WithCancel(context.Background())
	m.mu.Unlock()
	var failures []string
	for _, name := range m.ServerNames() {
		if err := m.startOne(ctx, name, m.configs[name]); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("mcp: %s", strings.Join(failures, "; "))
	}
	return nil
}

func (m *Manager) startOne(ctx context.Context, name string, cfg ServerConfig) error {
	if cfg.Command == "" {
		return fmt.Errorf("missing command — set command/args in mcp.json")
	}
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m.mu.Lock()
	procCtx := m.cmdCtx
	m.mu.Unlock()
	cmd := exec.CommandContext(procCtx, cfg.Command, cfg.Args...)
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %v", err)
	}
	rb := &ringBuffer{max: 4096}
	cmd.Stderr = rb
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start (%v) — is %q on PATH? Server stderr: %s", err, cfg.Command, rb.String())
	}
	s := &server{name: name, cmd: cmd, stdin: stdin, stdout: stdout, log: rb, pending: map[int64]chan rpcMsg{}}
	go s.readLoop()
	// initialize handshake.
	var initResult map[string]any
	if err := s.call(sctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "tilde", "version": "v0.7.0"},
	}, &initResult); err != nil {
		cmd.Process.Kill()
		_ = cmd.Wait() // reap, never zombie on a failed handshake
		return fmt.Errorf("initialize handshake failed: %v. Server stderr: %s", err, rb.String())
	}
	_ = s.notify(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	// tools/list.
	var listResult struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	var raw map[string]any
	if err := s.call(sctx, "tools/list", map[string]any{}, &raw); err != nil {
		cmd.Process.Kill()
		_ = cmd.Wait() // reap, never zombie on a failed handshake
		return fmt.Errorf("tools/list failed: %v. Server stderr: %s", err, rb.String())
	}
	re, _ := json.Marshal(raw)
	if err := json.Unmarshal(re, &listResult); err != nil {
		cmd.Process.Kill()
		_ = cmd.Wait() // reap, never zombie on a bad shape
		return fmt.Errorf("tools/list returned an undecodable shape: %v. Server stderr: %s", err, rb.String())
	}
	for _, t := range listResult.Tools {
		sch := t.InputSchema
		if sch == nil {
			sch = map[string]any{"type": "object"}
		}
		s.tools = append(s.tools, ToolInfo{Server: name, Name: t.Name, Description: t.Description, Schema: sch})
	}
	m.mu.Lock()
	m.servers[name] = s
	m.mu.Unlock()
	return nil
}

// Schema returns a cached input schema without touching the prompt.
func (m *Manager) Schema(server, tool string) (map[string]any, bool) {
	m.mu.Lock()
	s, ok := m.servers[server]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tools {
		if t.Name == tool {
			return t.Schema, true
		}
	}
	return nil, false
}

// missingRequired lists required schema properties absent from args.
func missingRequired(schema map[string]any, args map[string]any) []string {
	req, _ := schema["required"].([]any)
	var out []string
	for _, r := range req {
		if name, _ := r.(string); name != "" {
			if _, ok := args[name]; !ok {
				out = append(out, name)
			}
		}
	}
	return out
}
func (m *Manager) serverToolNames(server string) []string {
	m.mu.Lock()
	s, ok := m.servers[server]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, t := range s.tools {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

// Call invokes server.tool with args (120s cap, fenced by the caller).
func (m *Manager) Call(ctx context.Context, server, tool string, args map[string]any) (string, error) {
	m.mu.Lock()
	s, ok := m.servers[server]
	m.mu.Unlock()
	if !ok {
		live := m.Live()
		if len(live) == 0 {
			return "", fmt.Errorf("unknown server %q and no MCP servers are live — check .tilde/mcp.json and restart", server)
		}
		return "", fmt.Errorf("unknown server %q. Live servers: %s", server, strings.Join(live, ", "))
	}
	names := m.serverToolNames(server)
	known := false
	for _, n := range names {
		if n == tool {
			known = true
		}
	}
	if !known {
		return "", fmt.Errorf("unknown tool %q on server %q. Tools: %s", tool, server, strings.Join(names, ", "))
	}
	if args == nil {
		args = map[string]any{}
	}
	// Cheap pre-call validation against the cached schema: missing
	// required fields fail here in milliseconds with a fix, instead of a
	// 120s round-trip ending in a server error.
	if sch, ok := m.Schema(server, tool); ok {
		if missing := missingRequired(sch, args); len(missing) > 0 {
			return "", fmt.Errorf("mcp %s.%s: missing required argument(s) %s — resend with arguments object including them", server, tool, strings.Join(missing, ", "))
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	var raw map[string]any
	if err := s.call(cctx, "tools/call", map[string]any{"name": tool, "arguments": args}, &raw); err != nil {
		return "", fmt.Errorf("mcp %s.%s failed: %v. Server stderr: %s", server, tool, err, s.log.String())
	}
	if isErr, _ := raw["isError"].(bool); isErr {
		return "", fmt.Errorf("mcp %s.%s returned an error: %s", server, tool, renderContent(raw))
	}
	return renderContent(raw), nil
}

// Close stops all servers: cancel the process context, kill stragglers,
// then Wait each child with a timeout so none linger as zombies and the
// stdout readers observe EOF instead of orphaning.
func (m *Manager) Close() {
	m.mu.Lock()
	servers := make([]*server, 0, len(m.servers))
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	m.servers = map[string]*server{}
	cancel := m.cmdCancel
	m.cmdCancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, s := range servers {
		_ = s.stdin.Close() // unblock the reader on our end first
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		done := make(chan struct{})
		go func(s *server) {
			defer close(done)
			_ = s.cmd.Wait()
		}(s)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

func renderContent(raw map[string]any) string {
	arr, _ := raw["content"].([]any)
	var parts []string
	for _, item := range arr {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		if t, _ := m["text"].(string); t != "" {
			parts = append(parts, t)
		} else if d, _ := m["data"].(string); d != "" {
			parts = append(parts, "[binary data omitted, "+d+"]")
		}
	}
	if len(parts) == 0 {
		return "[empty result from MCP tool]"
	}
	return strings.Join(parts, "\n")
}

// call performs one request/response exchange.
func (s *server) call(ctx context.Context, method string, params map[string]any, out any) error {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan rpcMsg, 1)
	s.pending[id] = ch
	msg := rpcMsg{JSONRPC: "2.0", ID: &id, Method: method, Params: params}
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	_, werr := s.stdin.Write(data)
	s.mu.Unlock()
	if werr != nil {
		return fmt.Errorf("write to server: %v (it may have crashed; stderr: %s)", werr, s.log.String())
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("server error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		if out != nil && resp.Result != nil {
			re, _ := json.Marshal(resp.Result)
			if err := json.Unmarshal(re, out); err != nil {
				return fmt.Errorf("bad result shape: %v", err)
			}
		}
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return fmt.Errorf("timed out waiting for %s (server may be hung; stderr: %s)", method, s.log.String())
	}
}

func (s *server) notify(msg map[string]any) error {
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stdin.Write(data)
	return err
}

// readLoop routes responses to pending calls; notifications drop.
// One malformed line never kills the server link.
func (s *server) readLoop() {
	br := bufio.NewReader(s.stdout)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			s.failAll(fmt.Sprintf("server connection closed: %v", err))
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg rpcMsg
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // one bad line never kills the server link
		}
		if msg.ID == nil {
			continue // notification — nothing waits on these
		}
		s.mu.Lock()
		ch, ok := s.pending[*msg.ID]
		if ok {
			delete(s.pending, *msg.ID)
		}
		s.mu.Unlock()
		if ok {
			ch <- msg
		}
	}
}

func (s *server) failAll(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ch := range s.pending {
		ch <- rpcMsg{Error: &rpcError{Code: -32000, Message: reason}}
		delete(s.pending, id)
	}
}
