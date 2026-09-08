package provider

import (
	"context"
	"errors"
	"os"
	"strings"
)

// OpenCode is the OpenCode Zen gateway (opencode.ai/zen): a curated set
// of models the OpenCode team tested and benchmarked specifically for
// coding agents, behind one key and one OpenAI-compatible endpoint.
// Thin wrapper over OpenAI: only the name prefix and the base/key/model
// defaults differ, so fixes to the shared Chat path apply here too.
//
// Catalog discipline (verified live 2026-09-08 against both the docs at
// opencode.ai/docs/zen and GET /v1/models): only models whose documented
// endpoint is /v1/chat/completions are listed. Models served on
// /v1/responses (GPT/Codex rows) or /v1/messages (Claude rows) need
// other protocols and are reachable only as custom /model ids —
// attempting them through this backend surfaces the provider's error,
// not a silent wrong call. Zen ids are bare on the wire (no opencode/
// prefix); tilde's /model ref form already reads that way.
type OpenCode struct {
	*OpenAI
}

// DefaultOpenCodeBase is the endpoint unless overridden by an explicit
// base (Factory/flag) or $OPENCODE_BASE_URL.
const DefaultOpenCodeBase = "https://opencode.ai/zen/v1"

// NewOpenCode returns the Zen backend. Base defaults to
// $OPENCODE_BASE_URL then the public endpoint, key to
// $OPENCODE_API_KEY (from the Zen dashboard: sign in, billing, copy
// key), model to the catalog first entry.
func NewOpenCode(model, base, key string) *OpenCode {
	if base == "" {
		base = os.Getenv("OPENCODE_BASE_URL")
	}
	if base == "" {
		base = DefaultOpenCodeBase
	}
	if key == "" {
		key = os.Getenv("OPENCODE_API_KEY")
	}
	if model == "" {
		model = os.Getenv("TILDE_MODEL")
	}
	if model == "" {
		if ids := CatalogIDs("opencode"); len(ids) > 0 {
			model = ids[0]
		}
	}
	inner := NewOpenAI(model, base, key)
	// NewOpenAI's fallbacks only fire on empty values, and base is
	// non-empty here; a still-empty key is the ladder's error to report
	// (Factory/Resolve), not the constructor's to invent.
	inner.Base = strings.TrimRight(base, "/")
	return &OpenCode{OpenAI: inner}
}

// Name prefixes with the provider id so transcripts, logs, and
// /model refs stay unambiguous across OpenAI-compatible backends.
func (o *OpenCode) Name() string { return "opencode/" + o.Model }

// Chat delegates to the shared OpenAI-compatible implementation, then
// re-labels failures: a user on opencode must see opencode's remedies,
// never openai's. Single point — future hint changes propagate.
func (o *OpenCode) Chat(ctx context.Context, messages []Message, tools []ToolDef) (Response, error) {
	resp, err := o.OpenAI.Chat(ctx, messages, tools)
	if err != nil {
		msg := strings.ReplaceAll(err.Error(), "openai: ", "opencode: ")
		msg = strings.ReplaceAll(msg, "/login openai", "/login opencode")
		return Response{}, errors.New(msg)
	}
	return resp, nil
}
