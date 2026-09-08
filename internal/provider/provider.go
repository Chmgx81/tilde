// Package provider defines the model-access interface.
// Local Ollama is the zero-cost default; any HTTP model plugs in here.
package provider

import "context"

// Message is a single chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ToolDef describes one callable tool to the model.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
}

// ToolCall is a model-requested invocation.
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// Usage counts tokens for one reply. Zero means unreported by the backend.
type Usage struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
	Total      int `json:"total"`
}

// Response is what the model returned: prose and/or tool calls.
type Response struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls"`
	Usage     Usage      `json:"usage"`
	// Thinking carries the model's reasoning trace when the backend
	// exposes one (Anthropic thinking blocks, Ollama thinking models,
	// OpenAI-compatible reasoning_content). Empty means the backend
	// returned none — never an error. The loop surfaces it for display
	// and the session log, but it is not fed back as context.
	Thinking string `json:"thinking,omitempty"`
	// Truncated reports the backend hit its output limit mid-reply, so
	// tool arguments may be cut mid-JSON. The loop fails such calls
	// closed (re-issue with complete arguments) instead of dispatching
	// them. Backends set it from their own signal: OpenAI
	// finish_reason=length, Ollama done=false / done_reason=length,
	// Anthropic stop_reason=max_tokens.
	Truncated bool `json:"truncated,omitempty"`
}

// Provider is the interface every model backend implements.
type Provider interface {
	Chat(ctx context.Context, messages []Message, tools []ToolDef) (Response, error)
	Name() string
}
