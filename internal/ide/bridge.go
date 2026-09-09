// Package ide implements the P4-A IDE bridge.
//
// The bridge speaks line-delimited JSON over stdin/stdout so an editor
// (or any JSON-aware client) can drive tilde without a TTY. Each input
// line is one request:
//
//	{"id":1,"method":"initialize","params":{...}}
//
// and each output line is one response:
//
//	{"id":1,"ok":true,"result":{...}}
//	{"id":2,"ok":false,"error":"..."}
//
// Methods:
//   - initialize     -> {version, protocol, tools}
//   - health         -> {ok, sandbox, backend}
//   - session.create -> {session_id} (random hex)
//   - session.chat   -> {session_id, text} (runs the injected RunFunc)
//   - session.history -> {entries, count} (last N final texts, capped)
//
// The bridge is deliberately a leaf package: stdlib only, never
// importing agent or tools (agent imports tools, so importing either
// here would risk a cycle). Behaviour is injected through New: the tool
// name list surfaced by initialize, and the RunFunc that executes one
// prompt for session.chat. The owner wires the real agent loop — the
// headless-loop shape in main.go runHeadless (AskUser deny-by-default,
// event callback) — via `tilde ide-bridge --stdio`.
//
// Concurrency model: Serve runs one synchronous reader loop and spawns
// no goroutines. Chats run serially, each with a per-chat context
// timeout (chatTimeout, default 30s). A re-entrant chat while one is in
// flight is refused with a "busy" error instead of queueing.
//
// History redaction: only final result text is stored — never prompts,
// tool names, or arguments — capped at maxHistoryEntries entries of
// maxHistoryText bytes each.
package ide

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Protocol is the bridge protocol version advertised by initialize.
const Protocol = 1

// Version is the bridge implementation version advertised by initialize.
const Version = "1.0.0"

const (
	// maxHistoryEntries bounds process-lifetime memory for session.history.
	maxHistoryEntries = 20
	// maxHistoryText truncates each stored result (≈4KB).
	maxHistoryText = 4 * 1024
)

// chatTimeout is the default per-chat context deadline. A var (not a
// const) so tests in this package can shrink it; production keeps 30s.
var chatTimeout = 30 * time.Second

// validMethods is the full method set, named back in unknown-method errors.
var validMethods = []string{
	"initialize",
	"health",
	"session.create",
	"session.chat",
	"session.history",
}

// RunFunc executes one prompt and returns the final text. The owner
// injects the real agent loop here; the bridge never imports agent.
type RunFunc func(ctx context.Context, prompt string) (string, error)

// Request is one input line: {id, method, params}. ID is opaque and
// echoed back verbatim; params shape depends on the method.
type Request struct {
	ID     any             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Response is one output line: {id, ok, result|error}.
type Response struct {
	ID     any    `json:"id"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// historyEntry is one stored chat result: final text only, no prompt.
type historyEntry struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// Bridge is the IDE bridge. Build it with New; drive it with Serve.
type Bridge struct {
	tools []string
	run   RunFunc

	mu       sync.Mutex
	sessions map[string]bool
	history  []historyEntry

	// busy guards re-entrant chats: serial Serve never contends it,
	// but concurrent handle calls refuse instead of interleaving.
	busy atomic.Bool
}

// New builds a Bridge. tools are the tool names advertised by
// initialize (injected, never imported); run executes session.chat
// prompts (may be nil, in which case chat reports an error and health
// reports backend "none").
func New(tools []string, run RunFunc) *Bridge {
	return &Bridge{
		tools:    append([]string(nil), tools...),
		run:      run,
		sessions: map[string]bool{},
	}
}

// Serve runs the line-delimited JSON loop: one response per non-blank
// input line, malformed lines answered with an error response, looping
// until EOF. It spawns no goroutines and returns nil on clean EOF.
func (b *Bridge) Serve(r io.Reader, w io.Writer) error {
	br := bufio.NewReader(r)
	bw := bufio.NewWriter(w)
	for {
		line, rerr := br.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			b.write(bw, b.dispatch([]byte(trimmed)))
			if ferr := bw.Flush(); ferr != nil {
				return ferr
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return bw.Flush()
			}
			return rerr
		}
	}
}

// write encodes one response as a single JSON line.
func (b *Bridge) write(w *bufio.Writer, resp Response) {
	raw, err := json.Marshal(resp)
	if err != nil {
		raw = []byte(`{"id":null,"ok":false,"error":"encode response"}`)
	}
	raw = append(raw, '\n')
	_, _ = w.Write(raw)
}

// dispatch decodes one line; undecodable lines become error responses
// (never an exit) with a null id, since no id survived parsing.
func (b *Bridge) dispatch(raw []byte) Response {
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return Response{OK: false, Error: fmt.Sprintf("malformed request (want {\"id\",\"method\",\"params\"}): %v", err)}
	}
	return b.handle(req)
}

// handle routes one decoded request.
func (b *Bridge) handle(req Request) Response {
	switch req.Method {
	case "initialize":
		tools := append([]string(nil), b.tools...)
		if tools == nil {
			tools = []string{}
		}
		return Response{ID: req.ID, OK: true, Result: map[string]any{
			"version":  Version,
			"protocol": Protocol,
			"tools":    tools,
		}}
	case "health":
		backend := "none"
		if b.run != nil {
			backend = "injected"
		}
		return Response{ID: req.ID, OK: true, Result: map[string]any{
			"ok":      true,
			"sandbox": false, // leaf package: no sandbox view; owner may extend
			"backend": backend,
		}}
	case "session.create":
		id, err := newSessionID()
		if err != nil {
			return Response{ID: req.ID, OK: false, Error: fmt.Sprintf("session.create: %v", err)}
		}
		b.mu.Lock()
		for b.sessions[id] {
			id, err = newSessionID()
			if err != nil {
				b.mu.Unlock()
				return Response{ID: req.ID, OK: false, Error: fmt.Sprintf("session.create: %v", err)}
			}
		}
		b.sessions[id] = true
		b.mu.Unlock()
		return Response{ID: req.ID, OK: true, Result: map[string]any{"session_id": id}}
	case "session.chat":
		return b.chat(req.ID, req.Params)
	case "session.history":
		return b.historyList(req.ID, req.Params)
	default:
		return Response{ID: req.ID, OK: false, Error: fmt.Sprintf("unknown method %q (valid: %s)", req.Method, strings.Join(validMethods, ", "))}
	}
}

// chat validates params, enforces serial execution, runs the injected
// RunFunc under a per-chat timeout, and records the (truncated) final
// text for session.history.
func (b *Bridge) chat(id any, raw json.RawMessage) Response {
	var p struct {
		SessionID string `json:"session_id"`
		Prompt    string `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return Response{ID: id, OK: false, Error: fmt.Sprintf("bad session.chat params (want {\"session_id\",\"prompt\"}): %v", err)}
	}
	b.mu.Lock()
	known := b.sessions[p.SessionID]
	b.mu.Unlock()
	if p.SessionID == "" || !known {
		return Response{ID: id, OK: false, Error: fmt.Sprintf("unknown session_id %q (call session.create first)", p.SessionID)}
	}
	if strings.TrimSpace(p.Prompt) == "" {
		return Response{ID: id, OK: false, Error: "empty prompt"}
	}
	if !b.busy.CompareAndSwap(false, true) {
		return Response{ID: id, OK: false, Error: "busy: one chat at a time (previous chat still running)"}
	}
	defer b.busy.Store(false)
	if b.run == nil {
		return Response{ID: id, OK: false, Error: "no RunFunc configured"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatTimeout)
	defer cancel()
	text, err := b.run(ctx, p.Prompt)
	if err != nil {
		return Response{ID: id, OK: false, Error: err.Error()}
	}
	b.mu.Lock()
	stored := text
	if len(stored) > maxHistoryText {
		stored = stored[:maxHistoryText]
	}
	b.history = append(b.history, historyEntry{SessionID: p.SessionID, Text: stored})
	if len(b.history) > maxHistoryEntries {
		b.history = append([]historyEntry(nil), b.history[len(b.history)-maxHistoryEntries:]...)
	}
	b.mu.Unlock()
	return Response{ID: id, OK: true, Result: map[string]any{"session_id": p.SessionID, "text": text}}
}

// historyList returns the last N stored final texts (this process).
// Params are optional: {"session_id","limit"} with "n" accepted as a
// limit alias; absent/zero limit returns everything kept (≤20).
func (b *Bridge) historyList(id any, raw json.RawMessage) Response {
	var p struct {
		SessionID string `json:"session_id"`
		Limit     int    `json:"limit"`
		N         int    `json:"n"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return Response{ID: id, OK: false, Error: fmt.Sprintf("bad session.history params: %v", err)}
		}
	}
	want := p.Limit
	if want <= 0 {
		want = p.N
	}
	b.mu.Lock()
	out := make([]historyEntry, 0, len(b.history))
	for _, e := range b.history {
		if p.SessionID != "" && e.SessionID != p.SessionID {
			continue
		}
		out = append(out, e)
	}
	b.mu.Unlock()
	if want > 0 && len(out) > want {
		out = out[len(out)-want:]
	}
	return Response{ID: id, OK: true, Result: map[string]any{"entries": out, "count": len(out)}}
}

// newSessionID mints 8 random bytes as 16 hex chars.
func newSessionID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
