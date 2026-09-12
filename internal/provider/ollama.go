// Package provider — Ollama default backend (local, near-zero cost).
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Ollama speaks the native /api/chat protocol (non-streaming for Phase 0).
type Ollama struct {
	Host  string
	Model string
	// Key is an explicit cloud credential from the credential ladder. Empty
	// means "read $OLLAMA_API_KEY per request" so a test can set the env
	// after construction (and a local daemon stays keyless).
	Key  string
	http *http.Client
}

// NewOllama returns an Ollama provider with the ambient host and no
// explicit key: $OLLAMA_HOST or http://localhost:11434, $TILDE_MODEL or
// qwen3.8-4b:16k, and $OLLAMA_API_KEY read per request.
func NewOllama(model string) *Ollama { return newOllama(model, "", "") }

// NewOllamaAuth returns an Ollama provider with an explicit base/key from
// the credential ladder (Ollama Cloud). Empty values fall back to
// $OLLAMA_HOST / $OLLAMA_API_KEY and then the localhost default.
func NewOllamaAuth(model, base, key string) *Ollama { return newOllama(model, base, key) }

func newOllama(model, base, key string) *Ollama {
	host := strings.TrimSpace(base)
	if host == "" {
		host = strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
	}
	if host == "" {
		host = "http://localhost:11434"
	}
	if model == "" {
		model = os.Getenv("TILDE_MODEL")
		if model == "" {
			model = "qwen3.8-4b:16k"
		}
	}
	return &Ollama{Host: host, Model: model, Key: strings.TrimSpace(key), http: &http.Client{Timeout: 5 * time.Minute}}
}

// authKey is the key to send: an explicit ladder credential wins, else the
// ambient $OLLAMA_API_KEY (read per request so local use stays keyless).
func (o *Ollama) authKey() string {
	if o.Key != "" {
		return o.Key
	}
	return strings.TrimSpace(os.Getenv("OLLAMA_API_KEY"))
}

func (o *Ollama) Name() string { return "ollama/" + o.Model }

type ollamaTool struct {
	Type     string         `json:"type"`
	Function map[string]any `json:"function"`
}

// splitThinkTags separates inline reasoning blocks from prose. Thinking
// models served through Ollama (qwen3 with thinking on, deepseek-r1
// builds) sometimes emit <think>...</think> inside message.content
// instead of — or alongside — the dedicated thinking field. Raw, that
// text pollutes prose and breaks the strict nothing-but-JSON
// content-call gate below (a think block around the call JSON would
// silently demote a real tool call to prose). Every closed block is cut
// out and concatenated (blank-line separated) into thinking; a trailing
// unclosed <think> (truncated output) takes the remainder as thinking
// too — partial reasoning stays visible instead of leaking into prose.
// Tag matching is case-insensitive; everything outside blocks is prose
// with inner whitespace intact. With no tags this returns (content, "")
// untouched. Note there is no partial-chunk hazard here: this endpoint
// is called non-streaming, so content always arrives complete —
// unclosed can only mean the model was cut off, never a split packet.
func splitThinkTags(content string) (prose, thinking string) {
	const openTag, closeTag = "<think>", "</think>"
	var proseParts, thinkParts []string
	rest := content
	for {
		lower := strings.ToLower(rest)
		i := strings.Index(lower, openTag)
		if i < 0 {
			proseParts = append(proseParts, rest)
			break
		}
		proseParts = append(proseParts, rest[:i])
		after := rest[i+len(openTag):]
		j := strings.Index(strings.ToLower(after), closeTag)
		if j < 0 {
			thinkParts = append(thinkParts, after)
			break
		}
		thinkParts = append(thinkParts, after[:j])
		rest = after[j+len(closeTag):]
	}
	if len(thinkParts) == 0 {
		return content, ""
	}
	var tp []string
	for _, p := range thinkParts {
		if strings.TrimSpace(p) != "" {
			tp = append(tp, strings.TrimSpace(p))
		}
	}
	return strings.TrimSpace(strings.Join(proseParts, "")), strings.Join(tp, "\n\n")
}

// parseContentCalls extracts {"name","arguments"} call objects from raw
// text content. STRICT: the trimmed text (or the inside of a single
// ```json fence) must be nothing but one JSON object or one JSON array
// of objects — anything else (prose, prose + snippet, concatenated
// objects, trailing garbage) stays plain assistant prose and returns
// nil. Executing a snippet embedded in prose would run prose-described
// calls the model never cleanly issued.
func parseContentCalls(content string) []ToolCall {
	text := strings.TrimSpace(content)
	if i := strings.Index(text, "```"); i >= 0 {
		end := strings.LastIndex(text, "```")
		if end > i {
			inner := text[i+3 : end]
			inner = strings.TrimPrefix(strings.TrimSpace(inner), "json")
			text = strings.TrimSpace(inner)
		}
	}
	if text == "" {
		return nil
	}
	// json.Unmarshal rejects trailing data after one top-level value,
	// which is exactly the nothing-but-JSON gate we want.
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return nil
	}
	var raw []map[string]any
	switch t := v.(type) {
	case map[string]any:
		raw = []map[string]any{t}
	case []any:
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				raw = append(raw, m)
			}
		}
	default:
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	var out []ToolCall
	for i, m := range raw {
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		args := map[string]any{}
		for _, k := range []string{"arguments", "parameters", "args"} {
			if a, ok := m[k].(map[string]any); ok {
				args = a
				break
			}
			// stringified args: one repair step, then give up loudly.
			if s, ok := m[k].(string); ok && s != "" {
				var a map[string]any
				if err := json.Unmarshal([]byte(s), &a); err == nil {
					args = a
					break
				}
			}
		}
		out = append(out, ToolCall{ID: fmt.Sprintf("content_%d", i), Name: name, Args: args})
	}
	return out
}

// retryBaseDelay is the base backoff between Chat attempts (doubled per
// attempt + jitter; Retry-After overrides when the server sends one).
var retryBaseDelay = 200 * time.Millisecond

// retryableStatus reports transient HTTP statuses worth one more attempt.
func retryableStatus(code int) bool { return code == http.StatusTooManyRequests || code >= 500 }

// retryableErr reports transient transport failures. Context cancellation
// is never retried — the caller asked to stop.
func retryableErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return true
		}
	}
	s := strings.ToLower(err.Error())
	for _, sub := range []string{"connection reset", "connection refused", "eof", "timeout"} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// retryDelay computes the wait before the next attempt: Retry-After
// (seconds, capped) wins when present, else exponential backoff + jitter.
func retryDelay(attempt int, header http.Header) time.Duration {
	if ra := strings.TrimSpace(header.Get("Retry-After")); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 && secs <= 60 {
			return time.Duration(secs) * time.Second
		}
	}
	d := retryBaseDelay << attempt // 200ms, 400ms, 800ms
	return d + time.Duration(rand.Int63n(int64(d)/2+1))
}

// sleepCtx waits d or returns ctx.Err() on cancellation.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (o *Ollama) Chat(ctx context.Context, messages []Message, tools []ToolDef) (Response, error) {
	msgs := make([]map[string]string, 0, len(messages))
	for _, m := range messages {
		msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
	}
	payload := map[string]any{
		"model": o.Model, "messages": msgs, "stream": false,
	}
	if len(tools) > 0 {
		ots := make([]ollamaTool, 0, len(tools))
		for _, t := range tools {
			ots = append(ots, ollamaTool{Type: "function", Function: map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Schema,
			}})
		}
		payload["tools"] = ots
	}
	body, _ := json.Marshal(payload)
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			// header is empty here (no response yet): pure backoff.
			if err := sleepCtx(ctx, retryDelay(attempt-1, nil)); err != nil {
				return Response{}, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", o.Host+"/api/chat", bytes.NewReader(body))
		if err != nil {
			return Response{}, fmt.Errorf("ollama: build request: %w — is Ollama running at %s? Start it with `ollama serve` and pull a model with `ollama pull %s`", err, o.Host, o.Model)
		}
		req.Header.Set("Content-Type", "application/json")
		// Cloud API (https://ollama.com/api via $OLLAMA_HOST) needs the
		// key; localhost ignores it. authKey prefers a ladder-supplied
		// credential, else reads the env per request.
		if key := o.authKey(); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err = o.http.Do(req)
		if err != nil {
			if attempt < 2 && retryableErr(err) {
				continue
			}
			return Response{}, fmt.Errorf("ollama: POST %s/api/chat: %w — is Ollama running? Start it with `ollama serve`", o.Host, err)
		}
		if retryableStatus(resp.StatusCode) && attempt < 2 {
			// Drain the body (bounded) so the connection can be reused,
			// then back off per Retry-After or exponentially.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			delay := retryDelay(attempt, resp.Header)
			closeBody(resp)
			if err := sleepCtx(ctx, delay); err != nil {
				return Response{}, err
			}
			continue
		}
		break
	}
	defer resp.Body.Close()
	// Cap body reads at 32MB (mirrors openai.go): replies bigger than any
	// sane model output are rejected instead of eaten mid-JSON.
	limited := io.LimitReader(resp.Body, 32<<20)
	if resp.StatusCode != 200 {
		var em map[string]any
		_ = json.NewDecoder(limited).Decode(&em)
		return Response{}, fmt.Errorf("ollama: status %d for model %q: %v — try `ollama pull %s`", resp.StatusCode, o.Model, em, o.Model)
	}
	var out struct {
		Message struct {
			Content  string `json:"content"`
			Thinking string `json:"thinking"`
			// reasoning_content is emitted instead by thinking models
			// behind OpenAI-compatible shims of this endpoint: absent
			// natively, captured when present.
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		// Top-level thinking is not native Ollama shape, but some
		// proxies hoist reasoning out of message: capture when present.
		Thinking string `json:"thinking"`
		// Done/DoneReason are the backend's stop signal. done=false or
		// done_reason=length means the output cap cut the reply
		// mid-stream: tool arguments may be truncated, so the loop
		// fails those calls closed (see Response.Truncated). Done is a
		// pointer so an omitted field (older mocks/proxies) reads as
		// "no signal" rather than a false truncation.
		Done            *bool  `json:"done"`
		DoneReason      string `json:"done_reason"`
		PromptEvalCount int    `json:"prompt_eval_count"`
		EvalCount       int    `json:"eval_count"`
	}
	if err := json.NewDecoder(limited).Decode(&out); err != nil {
		return Response{}, fmt.Errorf("ollama: decode response: %w — the model returned malformed JSON; retry the request", err)
	}
	// Thinking assembly, first non-empty wins: dedicated fields carry
	// the backend's own signal; inline <think> blocks are the fallback
	// for models that reason inside content. Tags are stripped from
	// prose regardless (a tag never is prose), so the strict
	// content-call gate below sees clean JSON and the loop's context
	// never carries reasoning markup.
	content, inlineThinking := splitThinkTags(out.Message.Content)
	thinking := out.Message.Thinking
	if thinking == "" {
		thinking = out.Message.ReasoningContent
	}
	if thinking == "" {
		thinking = out.Thinking
	}
	if thinking == "" {
		thinking = inlineThinking
	}
	r := Response{Content: content, Thinking: thinking, Usage: ollamaUsage(out.PromptEvalCount, out.EvalCount)}
	if out.Done != nil && !*out.Done {
		r.Truncated = true
	}
	if out.DoneReason == "length" {
		r.Truncated = true
	}
	for i, tc := range out.Message.ToolCalls {
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		args := tc.Function.Arguments
		if args == nil {
			args = map[string]any{}
		}
		r.ToolCalls = append(r.ToolCalls, ToolCall{ID: id, Name: tc.Function.Name, Args: args})
	}
	// Phase 0 fallback: small local models often echo the tool call as
	// JSON inside content instead of the structured field. Parse one level
	// so the default model actually works. (The full repair layer — null
	// omission, stringified arrays, wrapped args — is Phase 3 work.)
	// Content here is already think-tag-stripped, so a reasoning block
	// around the call JSON no longer defeats the gate.
	if len(r.ToolCalls) == 0 {
		if calls := parseContentCalls(content); len(calls) > 0 {
			r.ToolCalls = calls
			r.Content = ""
		}
	}
	if r.Content == "" && len(r.ToolCalls) == 0 {
		return r, fmt.Errorf("ollama: empty response (no content, no tool calls) — retry with a more explicit instruction")
	}
	return r, nil
}

// Stream implements Streamer: POST /api/chat with stream:true (tools
// included, same shape as Chat) and yields content deltas plus the
// assembled tool calls. Tool calls arrive on the final message(s) and
// replace any earlier set; the terminal event carries the assembled
// calls and the done_reason truncation signal. Add-only fast path —
// Chat (retry/backoff) is untouched, and there is no retry here: any
// failure is one error on errs so the caller falls back to Chat.
func (o *Ollama) Stream(ctx context.Context, messages []Message, defs []ToolDef) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent, 16)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		msgs := make([]map[string]string, 0, len(messages))
		for _, m := range messages {
			msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
		}
		payload := map[string]any{
			"model": o.Model, "messages": msgs, "stream": true,
		}
		if len(defs) > 0 {
			ots := make([]ollamaTool, 0, len(defs))
			for _, t := range defs {
				ots = append(ots, ollamaTool{Type: "function", Function: map[string]any{
					"name": t.Name, "description": t.Description, "parameters": t.Schema,
				}})
			}
			payload["tools"] = ots
		}
		body, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, "POST", o.Host+"/api/chat", bytes.NewReader(body))
		if err != nil {
			errs <- fmt.Errorf("ollama: build stream request: %w — is Ollama running at %s? Start it with `ollama serve`", err, o.Host)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if key := o.authKey(); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := o.http.Do(req)
		if err != nil {
			errs <- fmt.Errorf("ollama: POST %s/api/chat: %w — is Ollama running? Start it with `ollama serve`", o.Host, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			errs <- fmt.Errorf("ollama: status %d for model %q: %s — try `ollama pull %s`", resp.StatusCode, o.Model, strings.TrimSpace(string(raw)), o.Model)
			return
		}
		emit := func(ev StreamEvent) bool {
			select {
			case events <- ev:
				return true
			case <-ctx.Done():
				errs <- ctx.Err()
				return false
			}
		}
		var calls []ToolCall
		dec := json.NewDecoder(resp.Body)
		for {
			var line struct {
				Message struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				Error      string `json:"error"`
				Done       bool   `json:"done"`
				DoneReason string `json:"done_reason"`
			}
			if err := dec.Decode(&line); err != nil {
				if err == io.EOF {
					// Closed without done (proxies do this): still
					// deliver what assembled, like the SSE path.
					if len(calls) > 0 {
						emit(StreamEvent{Calls: calls})
					}
					return
				}
				errs <- fmt.Errorf("ollama: decode stream: %w — the model returned malformed JSON; retry the request", err)
				return
			}
			if line.Error != "" {
				errs <- fmt.Errorf("ollama: stream error for model %q: %s — retry the request", o.Model, line.Error)
				return
			}
			if line.Message.Content != "" {
				if !emit(StreamEvent{Text: line.Message.Content}) {
					return
				}
			}
			if len(line.Message.ToolCalls) > 0 {
				// Same parse as Chat: missing ids get call_N, missing
				// args become {}.
				calls = calls[:0]
				for i, tc := range line.Message.ToolCalls {
					id := tc.ID
					if id == "" {
						id = fmt.Sprintf("call_%d", i)
					}
					args := tc.Function.Arguments
					if args == nil {
						args = map[string]any{}
					}
					calls = append(calls, ToolCall{ID: id, Name: tc.Function.Name, Args: args})
				}
			}
			if line.Done {
				ev := StreamEvent{Truncated: line.DoneReason == "length"}
				if len(calls) > 0 {
					ev.Calls = calls
				}
				emit(ev)
				return
			}
		}
	}()
	return events, errs
}
