// Package provider — OpenAI-compatible chat backend.
package provider

import (
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
				hint = "check that OPENAI_API_KEY is set and valid, then retry."
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
