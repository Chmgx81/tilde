// Package provider — native Anthropic Messages API backend.
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

// Anthropic speaks the native /v1/messages protocol (non-streaming).
// Base overrides the endpoint root (tests, proxies); it defaults to
// https://api.anthropic.com and the request goes to {Base}/v1/messages.
type Anthropic struct {
	Base  string
	Key   string
	Model string
	http  *http.Client
}

// anthropicVersion is the API version header every request must carry.
const anthropicVersion = "2023-06-01"

// anthropicMaxTokens is the required per-request output cap. The loop
// sends short ReAct turns, so 4096 is ample; large summaries still fit.
const anthropicMaxTokens = 4096

// NewAnthropic returns an Anthropic provider. Base defaults to
// https://api.anthropic.com, key to $ANTHROPIC_API_KEY, model to
// $TILDE_MODEL or claude-sonnet-4-20250514.
func NewAnthropic(model, base, key string) *Anthropic {
	if base == "" {
		base = "https://api.anthropic.com"
	}
	if key == "" {
		key = os.Getenv("ANTHROPIC_API_KEY")
	}
	if model == "" {
		model = os.Getenv("TILDE_MODEL")
		if model == "" {
			model = "claude-sonnet-4-20250514"
		}
	}
	return &Anthropic{
		Base:  strings.TrimRight(base, "/"),
		Key:   key,
		Model: model,
		http:  &http.Client{Timeout: 5 * time.Minute},
	}
}

func (a *Anthropic) Name() string { return "anthropic/" + a.Model }

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

func (a *Anthropic) Chat(ctx context.Context, messages []Message, tools []ToolDef) (Response, error) {
	// System prompt is top-level `system`, not a message: collect every
	// system-role message (loop preamble, compaction summary, skill
	// preload) in order. Everything else becomes user/assistant turns.
	var systemParts []string
	type wireMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var msgs []wireMsg
	for _, m := range messages {
		if m.Role == "system" {
			systemParts = append(systemParts, m.Content)
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = "assistant"
		}
		// The API rejects consecutive same-role messages (the loop
		// emits one user message per tool result, so back-to-back
		// user turns are routine): fold them into one turn.
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content += "\n\n" + m.Content
			continue
		}
		msgs = append(msgs, wireMsg{Role: role, Content: m.Content})
	}
	if len(msgs) == 0 {
		return Response{}, fmt.Errorf("anthropic: no user/assistant messages for model %q — the loop always sends at least the goal; check the caller", a.Model)
	}
	payload := map[string]any{
		"model": a.Model, "max_tokens": anthropicMaxTokens, "messages": msgs,
	}
	if len(systemParts) > 0 {
		payload["system"] = strings.Join(systemParts, "\n\n")
	}
	if len(tools) > 0 {
		ats := make([]anthropicTool, 0, len(tools))
		for _, t := range tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			ats = append(ats, anthropicTool{Name: t.Name, Description: t.Description, InputSchema: schema})
		}
		payload["tools"] = ats
	}
	body, _ := json.Marshal(payload)
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, retryDelay(attempt-1, nil)); err != nil {
				return Response{}, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", a.Base+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return Response{}, fmt.Errorf("anthropic: build request for %s: %w — check the base url and retry", a.Base, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-version", anthropicVersion)
		if a.Key != "" {
			req.Header.Set("x-api-key", a.Key)
		}
		resp, err = a.http.Do(req)
		if err != nil {
			if attempt < 2 && retryableErr(err) {
				continue
			}
			return Response{}, fmt.Errorf("anthropic: POST %s/v1/messages: %w — is the endpoint reachable? check the base url and retry", a.Base, err)
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
		break
	}
	defer resp.Body.Close()
	// Cap body reads at 32MB (mirrors openai.go): replies bigger than any
	// sane model output are rejected instead of eaten mid-JSON.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return Response{}, fmt.Errorf("anthropic: read response from %s: %w — retry the request", a.Base, err)
	}
	if resp.StatusCode != 200 {
		snip := strings.TrimSpace(string(raw))
		if len(snip) > 300 {
			snip = snip[:300] + "…"
		}
		hint := "retry the request or check the endpoint."
		if resp.StatusCode == 401 {
			hint = "run /login anthropic to update the stored key, then retry."
		}
		if resp.StatusCode == 403 {
			hint = "check billing on the Anthropic console, then retry."
		}
		if resp.StatusCode == 404 {
			hint = "run /model to pick from the catalog — the model id may be wrong or retired."
		}
		return Response{}, fmt.Errorf("anthropic: status %d for model %q: %s — %s", resp.StatusCode, a.Model, snip, hint)
	}
	var out struct {
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
		// stop_reason is informational here: the content blocks carry the
		// semantics. tool_use → tool_use blocks below; end_turn →
		// text; max_tokens / stop_sequence → whatever partial content
		// arrived is returned as-is (no error while anything usable
		// came back). Thinking blocks are captured to Response.Thinking
		// (never mixed into prose); redacted_thinking stays skipped —
		// it is opaque ciphertext with nothing to display.
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Response{}, fmt.Errorf("anthropic: decode response: %w — the model returned malformed JSON; retry the request", err)
	}
	var sb strings.Builder
	var notes []string
	r := Response{Usage: Usage{
		Prompt:     out.Usage.InputTokens,
		Completion: out.Usage.OutputTokens,
		Total:      out.Usage.InputTokens + out.Usage.OutputTokens,
	}}
	for i, b := range out.Content {
		switch b.Type {
		case "text":
			sb.WriteString(b.Text)
		case "tool_use":
			if b.Name == "" {
				continue
			}
			id := b.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			args, err := parseAnthropicInput(b.Name, b.Input)
			if err != nil {
				// Per-call degrade (mirrors openai.go): one malformed
				// call must not abort the whole turn. Keep the call
				// with empty args so dispatch reports it as a
				// model-readable tool-result error, and pin the parse
				// failure in prose so it survives in context.
				r.ToolCalls = append(r.ToolCalls, ToolCall{ID: id, Name: b.Name, Args: map[string]any{}})
				notes = append(notes, fmt.Sprintf("tool %q returned malformed tool arguments — retry the call with valid JSON arguments", b.Name))
				continue
			}
			r.ToolCalls = append(r.ToolCalls, ToolCall{ID: id, Name: b.Name, Args: args})
		case "thinking":
			// Reasoning trace: captured for display and the session
			// log, kept out of prose (see Response.Thinking).
			if strings.TrimSpace(b.Thinking) != "" {
				r.Thinking = joinText(r.Thinking, b.Thinking)
			}
		default:
			// redacted_thinking and any future block type: deliberately
			// skipped — opaque or unknown, nothing honest to display.
		}
	}
	r.Content = sb.String()
	for _, n := range notes {
		r.Content = joinText(r.Content, n)
	}
	if r.Content == "" && len(r.ToolCalls) == 0 {
		return r, fmt.Errorf("anthropic: empty response (no content, no tool calls) for model %q (stop_reason %q) — retry with a more explicit instruction", a.Model, out.StopReason)
	}
	// max_tokens truncates mid-stream: tool calls (if any) carry partial
	// arguments, so flag it — the loop fails such calls closed instead of
	// dispatching them.
	r.Truncated = out.StopReason == "max_tokens"
	return r, nil
}

// parseAnthropicInput decodes one tool_use input object. A missing input
// is an empty object; anything that is not a JSON object degrades loudly
// (the caller keeps the call with empty args plus a prose note).
func parseAnthropicInput(name string, raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("anthropic: tool %q returned malformed tool arguments — retry the call with valid JSON arguments", name)
	}
	if args == nil {
		return map[string]any{}, nil
	}
	return args, nil
}
