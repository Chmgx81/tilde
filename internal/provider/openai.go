// Package provider — OpenAI-compatible chat backend.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// OpenAI speaks the /chat/completions protocol (non-streaming).
// It also works against any OpenAI-compatible endpoint (local
// gateways, proxies) by overriding base.
type OpenAI struct {
	Base  string
	Key   string
	Model string
	http  *http.Client
}

// NewOpenAI returns an OpenAI provider. Base defaults to
// $OPENAI_BASE_URL then https://api.openai.com/v1, key to $OPENAI_API_KEY,
// model to $TILDE_MODEL or qwen3.8-4b:16k.
func NewOpenAI(model, base, key string) *OpenAI {
	if base == "" {
		base = os.Getenv("OPENAI_BASE_URL")
	}
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if key == "" {
		key = os.Getenv("OPENAI_API_KEY")
	}
	if model == "" {
		model = os.Getenv("TILDE_MODEL")
		if model == "" {
			model = "qwen3.8-4b:16k"
		}
	}
	return &OpenAI{
		Base:  strings.TrimRight(base, "/"),
		Key:   key,
		Model: model,
		http:  &http.Client{Timeout: 5 * time.Minute},
	}
}

func (o *OpenAI) Name() string { return "openai/" + o.Model }

type openAITool struct {
	Type     string         `json:"type"`
	Function map[string]any `json:"function"`
}

func (o *OpenAI) Chat(ctx context.Context, messages []Message, tools []ToolDef) (Response, error) {
	msgs := make([]map[string]string, 0, len(messages))
	for _, m := range messages {
		msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
	}
	payload := map[string]any{
		"model": o.Model, "messages": msgs, "stream": false,
	}
	if len(tools) > 0 {
		ots := make([]openAITool, 0, len(tools))
		for _, t := range tools {
			ots = append(ots, openAITool{Type: "function", Function: map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Schema,
			}})
		}
		payload["tools"] = ots
	}
	body, _ := json.Marshal(payload)
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, retryDelay(attempt-1, nil)); err != nil {
				return Response{}, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", o.Base+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return Response{}, fmt.Errorf("openai: build request for %s: %w — check the base url and retry", o.Base, err)
		}
		req.Header.Set("Content-Type", "application/json")
		if o.Key != "" {
			req.Header.Set("Authorization", "Bearer "+o.Key)
		}
		resp, err := o.http.Do(req)
		if err != nil {
			if attempt < 2 && retryableErr(err) {
				continue
			}
			return Response{}, fmt.Errorf("openai: POST %s/chat/completions: %w — is the endpoint reachable? check the base url and retry", o.Base, err)
		}
		if retryableStatus(resp.StatusCode) && attempt < 2 {
			_, _ = io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			delay := retryDelay(attempt, resp.Header)
			resp.Body.Close()
			if err := sleepCtx(ctx, delay); err != nil {
				return Response{}, err
			}
			continue
		}
		// Cap body reads at 32MB: replies bigger than any sane model output
		// are rejected outright instead of eaten silently mid-JSON (a 4KB cap
		// once truncated long tool-call payloads into decode errors).
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		status := resp.StatusCode
		resp.Body.Close()
		if err != nil {
			return Response{}, fmt.Errorf("openai: read response from %s: %w — retry the request", o.Base, err)
		}
		if status != 200 {
			snip := strings.TrimSpace(string(raw))
			if len(snip) > 300 {
				snip = snip[:300] + "…"
			}
			hint := "retry the request or check the endpoint."
			if status == 401 {
				hint = "run /login openai to update the stored key, then retry."
			}
			if status == 403 {
				hint = "check billing on the OpenAI console, then retry."
			}
			if status == 404 {
				hint = "run /model to pick from the catalog — the model id may be wrong or retired."
			}
			return Response{}, fmt.Errorf("openai: status %d for model %q: %s — %s", status, o.Model, snip, hint)
		}
		var out struct {
			Choices []struct {
				// FinishReason is the backend's stop signal. "length" means
				// the output cap cut the reply mid-stream: tool arguments
				// may be truncated, so the loop fails those calls closed
				// (see Response.Truncated) instead of dispatching them.
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content string `json:"content"`
					// reasoning_content is emitted by thinking models behind
					// OpenAI-compatible endpoints (e.g. DeepSeek-R1): absent
					// on stock OpenAI, captured when present.
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string          `json:"name"`
							Arguments json.RawMessage `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return Response{}, fmt.Errorf("openai: decode response: %w — the model returned malformed JSON; retry the request", err)
		}
		if len(out.Choices) == 0 {
			// Flaky gateways answer 200 with an empty choices array
			// (observed live on free-tier proxies): retry it like any
			// other transient failure instead of killing the turn.
			if attempt < 2 {
				continue
			}
			return Response{}, fmt.Errorf("openai: empty response (no choices) for model %q — retry the request", o.Model)
		}
		msg := out.Choices[0].Message
		r := Response{Content: msg.Content, Thinking: msg.ReasoningContent, Usage: Usage{
			Prompt:     out.Usage.PromptTokens,
			Completion: out.Usage.CompletionTokens,
			Total:      out.Usage.TotalTokens,
		},
			Truncated: out.Choices[0].FinishReason == "length",
		}
		for i, tc := range msg.ToolCalls {
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			args, err := parseToolArgs(tc.Function.Name, tc.Function.Arguments)
			if err != nil {
				// Per-call degrade (mirrors the Ollama content path): one
				// malformed call must not abort the whole turn. Keep the
				// call with empty args so dispatch reports it as a
				// model-readable tool-result error, and pin the parse
				// failure in prose so it survives in context.
				r.ToolCalls = append(r.ToolCalls, ToolCall{ID: id, Name: tc.Function.Name, Args: map[string]any{}})
				r.Content = joinText(r.Content, fmt.Sprintf("tool %q returned malformed tool arguments — retry the call with valid JSON arguments", tc.Function.Name))
				continue
			}
			r.ToolCalls = append(r.ToolCalls, ToolCall{ID: id, Name: tc.Function.Name, Args: args})
		}
		if r.Content == "" && len(r.ToolCalls) == 0 {
			return r, fmt.Errorf("openai: empty response (no content, no tool calls) for model %q — retry with a more explicit instruction", o.Model)
		}
		return r, nil
	}
	return Response{}, fmt.Errorf("openai: no response from %s — retry the request", o.Base)
}

// joinText appends a note to prose with a blank-line separator.
func joinText(content, note string) string {
	if strings.TrimSpace(content) == "" {
		return note
	}
	return content + "\n\n" + note
}

// parseToolArgs decodes the JSON-string arguments of one tool call.
// A malformed string is a model error the model can fix, so report it
// loudly instead of substituting silent empty args.
func parseToolArgs(name string, raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == `""` {
		return map[string]any{}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			return map[string]any{}, nil
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return nil, fmt.Errorf("openai: tool %q returned malformed tool arguments — retry the call with valid JSON arguments", name)
		}
		if args == nil {
			return map[string]any{}, nil
		}
		return args, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("openai: tool %q returned malformed tool arguments — retry the call with valid JSON arguments", name)
	}
	if args == nil {
		return map[string]any{}, nil
	}
	return args, nil
}

// Stream implements Streamer: POST /chat/completions with stream:true
// (tools included, same shape as Chat), yielding
// choices[0].delta.content text and assembling indexed
// delta.tool_calls fragments (id/name first-seen wins, arguments
// concatenated) until the [DONE] terminator, when the complete call
// set goes out as one event with the finish_reason truncation signal.
// Malformed argument payloads degrade exactly like Chat: the call is
// kept with empty args and a retry note lands in prose. Add-only fast
// path — Chat (retry/backoff) is untouched, and there is no retry
// here: any failure is one error on errs so the caller falls back.
func (o *OpenAI) Stream(ctx context.Context, messages []Message, defs []ToolDef) (<-chan StreamEvent, <-chan error) {
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
			ots := make([]openAITool, 0, len(defs))
			for _, t := range defs {
				ots = append(ots, openAITool{Type: "function", Function: map[string]any{
					"name": t.Name, "description": t.Description, "parameters": t.Schema,
				}})
			}
			payload["tools"] = ots
		}
		body, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, "POST", o.Base+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			errs <- fmt.Errorf("openai: build stream request for %s: %w — check the base url and retry", o.Base, err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if o.Key != "" {
			req.Header.Set("Authorization", "Bearer "+o.Key)
		}
		resp, err := o.http.Do(req)
		if err != nil {
			errs <- fmt.Errorf("openai: POST %s/chat/completions: %w — is the endpoint reachable? check the base url and retry", o.Base, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			errs <- fmt.Errorf("openai: status %d for model %q: %s — retry the request or check the endpoint", resp.StatusCode, o.Model, strings.TrimSpace(string(raw)))
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
		type asmCall struct {
			id, name string
			args     strings.Builder
			hasArgs  bool
		}
		var asm []asmCall
		finishReason := ""
		// Deltas are small, but a proxy may emit long lines: 1MB cap.
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		finish := func(truncated bool) bool {
			var calls []ToolCall
			var notes strings.Builder
			for i, a := range asm {
				if !a.hasArgs && a.name == "" {
					continue
				}
				id := a.id
				if id == "" {
					id = fmt.Sprintf("call_%d", i)
				}
				if !a.hasArgs {
					// Zero-argument call: no fragments arrived, which
					// is valid — not malformed.
					calls = append(calls, ToolCall{ID: id, Name: a.name, Args: map[string]any{}})
					continue
				}
				args, err := parseToolArgs(a.name, json.RawMessage(a.args.String()))
				if err != nil {
					calls = append(calls, ToolCall{ID: id, Name: a.name, Args: map[string]any{}})
					notes.WriteString(fmt.Sprintf("tool %q returned malformed tool arguments — retry the call with valid JSON arguments", a.name))
					continue
				}
				calls = append(calls, ToolCall{ID: id, Name: a.name, Args: args})
			}
			if notes.Len() > 0 {
				if !emit(StreamEvent{Text: notes.String()}) {
					return false
				}
			}
			ev := StreamEvent{Truncated: truncated}
			if len(calls) > 0 {
				ev.Calls = calls
			}
			return emit(ev)
		}
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				finish(finishReason == "length")
				return
			}
			if data == "" {
				continue
			}
			var ev struct {
				Choices []struct {
					Delta struct {
						Content   string `json:"content"`
						ToolCalls []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				errs <- fmt.Errorf("openai: decode stream event: %w — the model returned malformed JSON; retry the request", err)
				return
			}
			for _, ch := range ev.Choices {
				if finishReason == "" && ch.FinishReason != "" {
					finishReason = ch.FinishReason
				}
				if ch.Delta.Content != "" {
					if !emit(StreamEvent{Text: ch.Delta.Content}) {
						return
					}
				}
				for _, tc := range ch.Delta.ToolCalls {
					for len(asm) <= tc.Index {
						asm = append(asm, asmCall{})
					}
					a := &asm[tc.Index]
					if tc.ID != "" && a.id == "" {
						a.id = tc.ID
					}
					if tc.Function.Name != "" && a.name == "" {
						a.name = tc.Function.Name
					}
					if tc.Function.Arguments != "" {
						a.args.WriteString(tc.Function.Arguments)
						a.hasArgs = true
					}
				}
			}
		}
		if err := sc.Err(); err != nil {
			errs <- fmt.Errorf("openai: read stream from %s: %w — retry the request", o.Base, err)
			return
		}
		// EOF without [DONE] (some proxies just close): assemble what
		// arrived rather than dropping the turn.
		finish(finishReason == "length")
	}()
	return events, errs
}
