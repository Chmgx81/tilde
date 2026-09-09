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
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"tilde/internal/update"
)

// ServerConfig launches one server.
//
// Type is "local" (stdio child, default) or "remote" (HTTP POST
// JSON-RPC). URL is remote-only. Enabled nil means true. HeadersFile
// points at a headers file (never inline secrets in mcp.json).
// Approval gates mcp_call per tool: "auto" or "prompt" (default prompt).
type ServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`

	Type            string            `json:"type"`
	URL             string            `json:"url"`
	Enabled         *bool             `json:"enabled"`
	HeadersFile     string            `json:"headersFile"`
	Approval        map[string]string `json:"approval"`
	ApprovalDefault string            `json:"approvalDefault"`
}

// UnmarshalJSON rejects inline "headers" — secrets must live in a
// HeadersFile, never in mcp.json.
func (c *ServerConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if _, ok := raw["headers"]; ok {
		return fmt.Errorf("inline `headers` is rejected — never put secrets in mcp.json; store headers in a file and set `headersFile` instead")
	}
	type plain ServerConfig
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*c = ServerConfig(p)
	return nil
}

// IsEnabled reports whether the server should start (nil = true).
func (c ServerConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// effType normalizes empty to "local" (direct maps skip LoadFile).
func (c ServerConfig) effType() string {
	if c.Type == "" {
		return "local"
	}
	return c.Type
}

func validateServerConfig(name string, cfg *ServerConfig) error {
	if cfg.Type == "" {
		cfg.Type = "local"
	}
	if cfg.Type != "local" && cfg.Type != "remote" {
		return fmt.Errorf("mcp: server %q has unknown type %q — fix mcp.json: set \"type\" to \"local\" or \"remote\"", name, cfg.Type)
	}
	for tool, v := range cfg.Approval {
		if v != "auto" && v != "prompt" {
			return fmt.Errorf("mcp: server %q tool %q has bad approval %q — fix mcp.json: use \"auto\" or \"prompt\"", name, tool, v)
		}
	}
	if cfg.ApprovalDefault != "" && cfg.ApprovalDefault != "auto" && cfg.ApprovalDefault != "prompt" {
		return fmt.Errorf("mcp: server %q has bad approvalDefault %q — fix mcp.json: use \"auto\" or \"prompt\"", name, cfg.ApprovalDefault)
	}
	if cfg.Type == "local" {
		if cfg.URL != "" {
			return fmt.Errorf("mcp: server %q is local but sets \"url\" — fix mcp.json: \"url\" is remote-only", name)
		}
		return nil
	}
	// Remote.
	if cfg.URL == "" {
		return fmt.Errorf("mcp: server %q is remote but missing \"url\" — fix mcp.json: remote servers need \"url\": \"https://...\"", name)
	}
	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		return fmt.Errorf("mcp: server %q has non-http(s) url %q — fix mcp.json: \"url\" must be http(s)", name, cfg.URL)
	}
	return nil
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
	for name, cfg := range fc.Servers {
		if err := validateServerConfig(name, &cfg); err != nil {
			return fc, err
		}
		fc.Servers[name] = cfg
	}
	return fc, nil
}

// Merge overlays project config on user config. The user config is
// authoritative: a project entry may only ADD a brand-new server name,
// or — on a user-defined name — disable it (enabled=false) or tighten
// per-tool approval toward "prompt". A project command/args/env/url/
// headersFile/type change on a user-defined name is dropped, as is any
// loosening of approval toward "auto": a repo must not rewire or
// silently authorize your trusted servers.
func Merge(user, project FileConfig) map[string]ServerConfig {
	out := map[string]ServerConfig{}
	for n, s := range user.Servers {
		out[n] = s
	}
	for n, p := range project.Servers {
		u, ok := user.Servers[n]
		if !ok {
			out[n] = p // brand-new names stay project-wins
			continue
		}
		merged := u
		if p.Enabled != nil && !*p.Enabled {
			off := false
			merged.Enabled = &off // project may only disable, never enable
		}
		if len(p.Approval) > 0 {
			next := map[string]string{}
			for k, v := range u.Approval {
				next[k] = v
			}
			for k, v := range p.Approval {
				if v == "prompt" {
					next[k] = v // tighten only; "auto" is dropped
				}
			}
			merged.Approval = next
		}
		if p.ApprovalDefault == "prompt" {
			merged.ApprovalDefault = "prompt" // tighten only
		}
		out[n] = merged
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

// server is one live local child process or remote HTTP endpoint.
type server struct {
	name      string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.Reader
	log       *ringBuffer
	remoteURL string
	headers   map[string]string
	client    *http.Client

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
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m.mu.Lock()
	procCtx := m.cmdCtx
	m.mu.Unlock()
	rb := &ringBuffer{max: 4096}
	var cmd *exec.Cmd
	var s *server
	if cfg.effType() == "remote" {
		if err := checkRemoteURL(name, cfg.URL); err != nil {
			return err
		}
		headers, err := loadHeadersFile(cfg.HeadersFile)
		if err != nil {
			return fmt.Errorf("mcp: remote server %q: %v", name, err)
		}
		s = &server{name: name, remoteURL: cfg.URL, headers: headers, log: rb, client: remoteHTTPClient(name, cfg.URL)}
	} else {
		if cfg.Command == "" {
			return fmt.Errorf("missing command — set command/args in mcp.json")
		}
		cmd = exec.CommandContext(procCtx, cfg.Command, cfg.Args...)
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
		cmd.Stderr = rb
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("cannot start (%v) — is %q on PATH? Server stderr: %s", err, cfg.Command, rb.String())
		}
		s = &server{name: name, cmd: cmd, stdin: stdin, stdout: stdout, log: rb, pending: map[int64]chan rpcMsg{}}
		go s.readLoop()
	}
	stopChild := func() {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	// initialize handshake.
	var initResult map[string]any
	if err := s.call(sctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "tilde", "version": update.BuildVersion()},
	}, &initResult); err != nil {
		stopChild()
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
		stopChild()
		return fmt.Errorf("tools/list failed: %v. Server stderr: %s", err, rb.String())
	}
	re, _ := json.Marshal(raw)
	if err := json.Unmarshal(re, &listResult); err != nil {
		stopChild()
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

// cgnat is the carrier-grade NAT shared-address space: not private by
// RFC 1918 but never a public MCP host.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// nonPublicIP reports whether ip is loopback, private, link-local,
// multicast, unspecified, or carrier-grade NAT — never a valid remote
// MCP host (SSRF guard, stdlib only).
func nonPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || !ip.IsValid() || ip.IsUnspecified() ||
		cgnat.Contains(ip)
}

// checkRemoteURL rejects remote server URLs pointing at non-public
// targets: IP literals, localhost-family names, and hostnames resolving
// to non-public addresses (fail closed when unresolvable). Transport
// behavior is unchanged — this is only a gate.
func checkRemoteURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("mcp: server %q has non-http(s) url %q — fix mcp.json: \"url\" must be http(s)", name, raw)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("mcp: server %q url %q targets a local host — fix mcp.json: remote \"url\" must be a public http(s) host", name, raw)
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		if nonPublicIP(ip) {
			return fmt.Errorf("mcp: server %q url %q targets a private/loopback address — fix mcp.json: remote \"url\" must be a public http(s) host", name, raw)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("mcp: server %q url %q does not resolve to a verifiable public address (%v) — fix mcp.json: remote \"url\" must be a public http(s) host", name, raw, err)
	}
	for _, a := range addrs {
		ip, ok := netip.AddrFromSlice(a.IP.To16())
		if !ok || nonPublicIP(ip) {
			return fmt.Errorf("mcp: server %q url %q resolves to a private/loopback address — fix mcp.json: remote \"url\" must be a public http(s) host", name, raw)
		}
	}
	return nil
}

func loadHeadersFile(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read headersFile %q: %v", path, err)
	}
	if len(data) > 64*1024 {
		return nil, fmt.Errorf("headersFile %q exceeds 64 KiB", path)
	}
	var headers map[string]string
	if err := json.Unmarshal(data, &headers); err != nil {
		return nil, fmt.Errorf("headersFile %q must be a JSON object of string headers: %v", path, err)
	}
	return headers, nil
}

// remoteHTTPClient rejects redirects and re-resolves the host for every
// connection. The startup DNS check alone is insufficient against DNS
// rebinding, so the transport repeats the public-address check at dial time.
func remoteHTTPClient(name, rawURL string) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		host := u.Hostname()
		port := u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("mcp: remote server %q DNS lookup failed: %v", name, err)
		}
		for _, ip := range ips {
			addr := netip.MustParseAddr(ip.String())
			if nonPublicIP(addr) {
				continue
			}
			conn, dialErr := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
		}
		return nil, fmt.Errorf("mcp: remote server %q resolved only to unavailable or non-public addresses", name)
	}
	return &http.Client{
		Transport: base,
		Timeout:   120 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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

// toolApproval resolves the effective gate for one tool: the per-tool
// entry, else ApprovalDefault, else "prompt" (fail closed).
func (c ServerConfig) toolApproval(tool string) string {
	if v, ok := c.Approval[tool]; ok && v != "" {
		return v
	}
	if c.ApprovalDefault != "" {
		return c.ApprovalDefault
	}
	return "prompt"
}

// Call invokes server.tool with args (120s cap, fenced by the caller).
// Tools gated approval=prompt need an approved caller — use CallApproved
// once the policy tier has approved the call.
func (m *Manager) Call(ctx context.Context, server, tool string, args map[string]any) (string, error) {
	return m.CallApproved(ctx, server, tool, args, false)
}

// CallApproved is Call with the policy-tier verdict attached: approved
// must be true when the tool resolves to approval=prompt (the mcp_call
// gateway passes true — the agent loop's Ask gate already approved that
// exact call). Unapproved prompt-gated calls fail with the fix attached.
func (m *Manager) CallApproved(ctx context.Context, server, tool string, args map[string]any, approved bool) (string, error) {
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
	if cfg, ok := m.configs[server]; ok {
		if cfg.toolApproval(tool) != "auto" && !approved {
			return "", fmt.Errorf("mcp %s.%s needs user approval (approval=prompt) — ask the user, then retry the call as approved", server, tool)
		}
	} else if !approved {
		return "", fmt.Errorf("mcp %s.%s needs user approval (approval=prompt) — ask the user, then retry the call as approved", server, tool)
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
		if s.remoteURL != "" {
			continue
		}
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
	if s.remoteURL != "" {
		return s.remoteCall(ctx, method, params, out)
	}
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

func (s *server) remoteCall(ctx context.Context, method string, params map[string]any, out any) error {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.mu.Unlock()
	msg := rpcMsg{JSONRPC: "2.0", ID: &id, Method: method, Params: params}
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode request: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.remoteURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range s.headers {
		req.Header.Set(key, value)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024+1))
	if err != nil {
		return fmt.Errorf("read response: %v", err)
	}
	if len(data) > 10*1024*1024 {
		return fmt.Errorf("response exceeds 10 MiB")
	}
	var response rpcMsg
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("decode response: %v", err)
	}
	if response.Error != nil {
		return fmt.Errorf("server error %d: %s", response.Error.Code, response.Error.Message)
	}
	if out != nil && response.Result != nil {
		re, _ := json.Marshal(response.Result)
		if err := json.Unmarshal(re, out); err != nil {
			return fmt.Errorf("bad result shape: %v", err)
		}
	}
	return nil
}

func (s *server) notify(msg map[string]any) error {
	if s.remoteURL != "" {
		return nil // remote MCP notifications are optional for this client
	}
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
