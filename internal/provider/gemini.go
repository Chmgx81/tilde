package provider

import (
	"context"
	"errors"
	"os"
	"strings"
)

// Gemini speaks Google's OpenAI-compatible /chat/completions endpoint —
// no separate wire protocol to maintain, and Gemini's AI Studio free
// tier makes it a genuine $0 starting point alongside OpenRouter's
// :free shelf. Thin wrapper over OpenAI: only the name prefix and the
// base/key/model defaults differ, so fixes to the shared Chat path
// apply here too. (A native Generativelanguage client is a later
// refinement, not v1 — the compatible endpoint covers chat + tools.)
type Gemini struct {
	*OpenAI
}

// DefaultGeminiBase is the endpoint unless overridden by an explicit
// base (Factory/flag) or $GEMINI_BASE_URL.
const DefaultGeminiBase = "https://generativelanguage.googleapis.com/v1beta/openai"

// NewGemini returns the Gemini backend. Base defaults to
// $GEMINI_BASE_URL then the public endpoint, key to $GEMINI_API_KEY
// (falling back to $GOOGLE_API_KEY — Google names it both ways),
// model to the catalog first entry.
func NewGemini(model, base, key string) *Gemini {
	if base == "" {
		base = os.Getenv("GEMINI_BASE_URL")
	}
	if base == "" {
		base = DefaultGeminiBase
	}
	if key == "" {
		key = os.Getenv("GEMINI_API_KEY")
	}
	if key == "" {
		key = os.Getenv("GOOGLE_API_KEY")
	}
	if model == "" {
		model = os.Getenv("TILDE_MODEL")
	}
	if model == "" {
		if ids := CatalogIDs("gemini"); len(ids) > 0 {
			model = ids[0]
		}
	}
	inner := NewOpenAI(model, base, key)
	// NewOpenAI's fallbacks only fire on empty values, and base is
	// non-empty here; a still-empty key is the ladder's error to report
	// (Factory/Resolve), not the constructor's to invent.
	inner.Base = strings.TrimRight(base, "/")
	return &Gemini{OpenAI: inner}
}

// Name prefixes with the provider id so transcripts, logs, and
// /model refs stay unambiguous across OpenAI-compatible backends.
func (o *Gemini) Name() string { return "gemini/" + o.Model }

// Chat delegates to the shared OpenAI-compatible implementation, then
// re-labels failures: a user on gemini must see gemini's remedies,
// never openai's. Single point — future hint changes propagate.
func (o *Gemini) Chat(ctx context.Context, messages []Message, tools []ToolDef) (Response, error) {
	resp, err := o.OpenAI.Chat(ctx, messages, tools)
	if err != nil {
		msg := strings.ReplaceAll(err.Error(), "openai: ", "gemini: ")
		msg = strings.ReplaceAll(msg, "/login openai", "/login gemini")
		return Response{}, errors.New(msg)
	}
	return resp, nil
}
