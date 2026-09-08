package provider

import (
	"context"
	"errors"
	"os"
	"strings"
)

// OpenRouter speaks OpenAI-compatible /chat/completions through
// openrouter.ai — one key reaching many vendors, including :free
// suffixed models that cost nothing per token (account + key still
// required). Thin wrapper over OpenAI: only the name prefix and the
// base/key/model defaults differ, so fixes to the shared Chat path
// apply to both.
type OpenRouter struct {
	*OpenAI
}

// DefaultOpenRouterBase is the endpoint unless overridden by an
// explicit base (Factory/flag) or $OPENROUTER_BASE_URL.
const DefaultOpenRouterBase = "https://openrouter.ai/api/v1"

// NewOpenRouter returns the OpenRouter backend. Base defaults to
// $OPENROUTER_BASE_URL then the public endpoint, key to
// $OPENROUTER_API_KEY, model to the free-catalog first entry (a
// no-GPU user should land on a $0 model, not a paid one).
func NewOpenRouter(model, base, key string) *OpenRouter {
	if base == "" {
		base = os.Getenv("OPENROUTER_BASE_URL")
	}
	if base == "" {
		base = DefaultOpenRouterBase
	}
	if key == "" {
		key = os.Getenv("OPENROUTER_API_KEY")
	}
	if model == "" {
		model = os.Getenv("TILDE_MODEL")
	}
	if model == "" {
		if ids := CatalogIDs("openrouter"); len(ids) > 0 {
			model = ids[0]
		}
	}
	inner := NewOpenAI(model, base, key)
	// NewOpenAI's fallbacks only fire on empty values, and base/key are
	// non-empty here unless nothing was configured anywhere — in which
	// case the Factory/Resolve ladder (not the constructor) owns the
	// "no key" error. The model fallback above runs first so an empty
	// model never inherits OpenAI's default.
	inner.Base = strings.TrimRight(base, "/")
	return &OpenRouter{OpenAI: inner}
}

// Name prefixes with the provider id so transcripts, logs, and
// /model refs stay unambiguous across OpenAI-compatible backends.
func (o *OpenRouter) Name() string { return "openrouter/" + o.Model }

// Chat delegates to the shared OpenAI-compatible implementation, then
// re-labels failures: a user on openrouter must see openrouter's
// remedies, never openai's. Single point — future hint changes in the
// shared path propagate automatically.
func (o *OpenRouter) Chat(ctx context.Context, messages []Message, tools []ToolDef) (Response, error) {
	resp, err := o.OpenAI.Chat(ctx, messages, tools)
	if err != nil {
		msg := strings.ReplaceAll(err.Error(), "openai: ", "openrouter: ")
		msg = strings.ReplaceAll(msg, "/login openai", "/login openrouter")
		return Response{}, errors.New(msg)
	}
	return resp, nil
}
