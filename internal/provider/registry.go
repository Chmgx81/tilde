package provider

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// AuthSource records where a provider's credential came from. The
// ladder is unambiguous (spec §2.24): a stored credential owns the
// provider outright, the ambient env var is the fallback, and a
// rejected key is never silently retried against a different source.
type AuthSource int

const (
	AuthNone AuthSource = iota
	AuthStored
	AuthEnv
	AuthFlag
)

func (s AuthSource) String() string {
	switch s {
	case AuthStored:
		return "stored"
	case AuthEnv:
		return "env"
	case AuthFlag:
		return "flag"
	default:
		return "none"
	}
}

// ProviderDesc describes one selectable backend: display name, how it
// authenticates, and whether the credential is required up front
// (cloud) or supplied by a local daemon (ollama).
type ProviderDesc struct {
	ID       string // ollama | openai | anthropic
	Name     string // human name for dropdowns and status lines
	NeedsKey bool   // true = cloud provider; a key is required up front
	// OptionalKey marks a backend whose key is storable and validatable but
	// NOT required: Ollama runs locally with no key and talks to Ollama
	// Cloud when a key/host is configured. /login accepts it.
	OptionalKey bool
	EnvKey      string // ambient env var, "" for a purely local daemon
}

// Descriptions lists every backend in the registry's canonical order.
var Descriptions = []ProviderDesc{
	{ID: "ollama", Name: "Ollama (local or Cloud)", OptionalKey: true, EnvKey: "OLLAMA_API_KEY"},
	{ID: "openai", Name: "OpenAI", NeedsKey: true, EnvKey: "OPENAI_API_KEY"},
	{ID: "anthropic", Name: "Anthropic", NeedsKey: true, EnvKey: "ANTHROPIC_API_KEY"},
	{ID: "openrouter", Name: "OpenRouter (incl. free models)", NeedsKey: true, EnvKey: "OPENROUTER_API_KEY"},
	{ID: "gemini", Name: "Google Gemini (free tier)", NeedsKey: true, EnvKey: "GEMINI_API_KEY"},
	{ID: "opencode", Name: "OpenCode Zen (curated for coding agents)", NeedsKey: true, EnvKey: "OPENCODE_API_KEY"},
}

// CloudIDs lists the backends that REQUIRE a cloud key (splash/status use).
func CloudIDs() []string {
	var out []string
	for _, d := range Descriptions {
		if d.NeedsKey {
			out = append(out, d.ID)
		}
	}
	return out
}

// LoginIDs lists the backends /login accepts: cloud providers plus
// optional-key backends (Ollama Cloud). Bare local Ollama still needs none.
func LoginIDs() []string {
	var out []string
	for _, d := range Descriptions {
		if d.NeedsKey || d.OptionalKey {
			out = append(out, d.ID)
		}
	}
	return out
}

// CatalogModel is one shipped catalog entry (spec §2.24): a model a
// user can pick by name, with the window geometry the budget auto-size
// needs. Prices are USD per million tokens; 0 means free, negative
// means unreported pay-per-use (rendered as such — never as free). A 0
// Context means unreported: budget auto-size skips the entry by design.
type CatalogModel struct {
	ID      string // wire model id
	Name    string // human label
	Context int    // context window in tokens
	InCost  float64
	OutCost float64
}

// Catalog maps provider id → curated entries. Hand-maintained on
// purpose (spec §2.24): at tilde's provider count a generation script
// is maintenance overhead. Refresh when providers ship notable models;
// keep entries to ~5 current picks.
var Catalog = map[string][]CatalogModel{
	"ollama": {
		{"qwen3.8-4b:16k", "Qwen3 4B (measured default)", 16384, 0, 0},
		{"llama3.1:8b", "Llama 3.1 8B", 131072, 0, 0},
		// Ollama Cloud (set $OLLAMA_HOST=https://ollama.com plus a key).
		// Cloud reports neither window nor per-token price (subscription):
		// Context 0 skips budget auto-size and -1 renders pay-per-use,
		// never as free.
		{"gpt-oss:120b", "GPT-OSS 120B (Cloud)", 0, -1, -1},
		{"qwen3-coder:480b", "Qwen3 Coder 480B (Cloud)", 0, -1, -1},
		{"deepseek-v3.1:671b", "DeepSeek V3.1 671B (Cloud)", 0, -1, -1},
	},
	"openai": {
		{"gpt-5.2", "GPT-5.2", 400000, 1.25, 10},
		{"gpt-5.2-mini", "GPT-5.2 mini", 400000, 0.25, 2},
		{"gpt-4.1", "GPT-4.1", 1047576, 2, 8},
		{"gpt-4.1-mini", "GPT-4.1 mini", 1047576, 0.4, 1.6},
		{"o4-mini", "o4-mini (reasoning)", 200000, 1.1, 4.4},
	},
	"anthropic": {
		{"claude-sonnet-4-6", "Claude Sonnet 4.6", 200000, 3, 15},
		{"claude-opus-4-6", "Claude Opus 4.6", 200000, 5, 25},
		{"claude-haiku-4-5", "Claude Haiku 4.5", 200000, 1, 5},
	},
	// Free per-token models (account + key still required). Free-tier
	// ids rotate as vendors join/leave — refreshed 2026-09-08; if a 404
	// points here, check openrouter.ai/models or pass any custom id to
	// /model. Prices are 0: the point of this shelf is $0 experiments.
	"openrouter": {
		{"meta-llama/llama-3.3-70b-instruct:free", "Llama 3.3 70B (free)", 128000, 0, 0},
		{"qwen/qwen-2.5-72b-instruct:free", "Qwen 2.5 72B (free)", 32768, 0, 0},
		{"mistralai/mistral-small-3.1-24b-instruct:free", "Mistral Small 3.1 (free)", 128000, 0, 0},
		{"google/gemma-3-27b-it:free", "Gemma 3 27B (free)", 128000, 0, 0},
		{"deepseek/deepseek-chat:free", "DeepSeek Chat (free)", 64000, 0, 0},
	},
	// Gemini via the OpenAI-compatible endpoint (chat + tools covered;
	// native client deferred). Prices move — verify on ai.google.dev.
	// Refreshed 2026-09-08. AI Studio serves these on a free tier, so
	// Gemini is a $0 start with only a Google account.
	"gemini": {
		{"gemini-2.5-flash", "Gemini 2.5 Flash", 1048576, 0.30, 2.50},
		{"gemini-2.5-flash-lite", "Gemini 2.5 Flash-Lite", 1048576, 0.10, 0.40},
		{"gemini-2.5-pro", "Gemini 2.5 Pro", 1048576, 1.25, 10.00},
		{"gemini-2.0-flash", "Gemini 2.0 Flash", 1048576, 0.10, 0.40},
	},
	// OpenCode Zen, verified live 2026-09-08 (docs + GET /v1/models = 70
	// models). Only /v1/chat/completions models are listed — /responses
	// and /messages rows need other protocols (see OpenCode doc above).
	// Windows and prices are unreported by Zen (per-request billing on
	// the dashboard): Context 0 skips budget auto-size by design, and
	// negative prices render as pay-per-use, never as free. NOTE (verified
	// 2026-09-12): Zen's free-tier rows are gated to the OpenCode app
	// itself ("can only be used in OpenCode") and are rejected for
	// third-party clients, so they are listed for reference only.
	"opencode": {
		{"kimi-k2.7-code", "Kimi K2.7 Code", 0, -1, -1},
		{"minimax-m3", "MiniMax M3", 0, -1, -1},
		{"glm-5.3", "GLM 5.3", 0, -1, -1},
		{"deepseek-v4-pro", "DeepSeek V4 Pro", 0, -1, -1},
		{"nemotron-3-ultra-free", "Nemotron 3 Ultra (free tier, OpenCode app only)", 0, 0, 0},
	},
}

// CatalogIDs returns the model ids for one provider, catalog order.
func CatalogIDs(providerID string) []string {
	var out []string
	for _, cm := range Catalog[providerID] {
		out = append(out, cm.ID)
	}
	return out
}

// BudgetFor returns the compaction budget for a provider/model pair:
// the catalog context window when known, else 0 (caller keeps default).
// A 0 Context means unreported and is skipped by design.
func BudgetFor(providerID, model string) int {
	for _, cm := range Catalog[strings.ToLower(providerID)] {
		if cm.ID == model && cm.Context > 0 {
			return cm.Context
		}
	}
	return 0
}

// PriceFor returns the catalog's stored per-1M USD prices for a
// provider/model pair: (in, out, true) on a hit, (0, 0, false) when the
// model is not in the catalog or its price is unreported (negative =
// pay-per-use, rendered as such — never as free, never fabricated).
// Free models (0/0) report (0, 0, true): known-free, not unknown.
// Provider id match is case-insensitive (same as BudgetFor); model id
// match is exact (catalog wire id). Read-only: no network, no mutation.
func PriceFor(providerID, model string) (in, out float64, ok bool) {
	for _, cm := range Catalog[strings.ToLower(providerID)] {
		if cm.ID == model {
			if cm.InCost < 0 || cm.OutCost < 0 {
				return 0, 0, false
			}
			return cm.InCost, cm.OutCost, true
		}
	}
	return 0, 0, false
}

// ParseModelRef splits "provider/model" (the /model select form). The
// one-element form means "model on the current provider" and is the
// caller's to interpret; on !ok both returns are empty.
func ParseModelRef(ref string) (providerID, model string, ok bool) {
	if i := strings.Index(ref, "/"); i > 0 && i < len(ref)-1 {
		return strings.ToLower(ref[:i]), ref[i+1:], true
	}
	return "", "", false
}

// AuthStatus is one row of /login's status matrix.
type AuthStatus struct {
	Provider ProviderDesc
	Source   AuthSource
	KeyTail  string // last four characters, "" when no key
}

// CredentialStore is the read surface the registry needs from the
// on-disk key store (internal/creds). An interface keeps provider
// decoupled from the store's file handling — and lets tests pass a stub.
type CredentialStore interface {
	Get(id string) (string, error)
}

// tail4 masks a key for display: only the last four characters ever
// render — a full key must not be recoverable from a screenshot, a
// scrollback copy, or a session log.
func tail4(k string) string {
	if len(k) <= 4 {
		return "••••"
	}
	return "…" + k[len(k)-4:]
}

// Status reports the credential ladder's verdict for every backend:
// stored first (a stored key owns the provider), then the ambient env
// var, then nothing — never a guess.
func Status(store CredentialStore, flagOverrides map[string]string) []AuthStatus {
	out := make([]AuthStatus, 0, len(Descriptions))
	for _, d := range Descriptions {
		st := AuthStatus{Provider: d}
		if k, ok := flagOverrides[d.ID]; ok && k != "" {
			st.Source, st.KeyTail = AuthFlag, tail4(k)
		} else if store != nil {
			if k, err := store.Get(d.ID); err == nil && k != "" {
				st.Source, st.KeyTail = AuthStored, tail4(k)
			}
		}
		if st.Source == AuthNone && d.EnvKey != "" {
			if k := os.Getenv(d.EnvKey); k != "" {
				st.Source, st.KeyTail = AuthEnv, tail4(k)
			}
		}
		out = append(out, st)
	}
	return out
}

// Resolve is the read side of the ladder: the key that actually ships
// on the wire for providerID, plus its source (or AuthNone). flagOverrides
// (explicit --api-key) outrank everything for this process; a stored
// credential outranks the env var; nothing is invented.
func Resolve(store CredentialStore, providerID string, flagOverrides map[string]string) (string, AuthSource) {
	if k, ok := flagOverrides[providerID]; ok && k != "" {
		return k, AuthFlag
	}
	if store != nil {
		if k, err := store.Get(providerID); err == nil && k != "" {
			return k, AuthStored
		}
	}
	for _, d := range Descriptions {
		if d.ID == providerID && d.EnvKey != "" {
			if k := os.Getenv(d.EnvKey); k != "" {
				return k, AuthEnv
			}
		}
	}
	return "", AuthNone
}

// Factory builds the backend for one provider id from explicit config
// values (each may be ""). It is the single construction point —
// selection happens on data, never a switch scattered through the app.
// Cloud providers default their model to the catalog's first entry and
// refuse to construct without a key (the caller's ladder — flag, store,
// env — must have produced one; nothing is invented here). Unknown ids
// fail loud with the valid set.
func Factory(providerID, model, base, key string) (Provider, error) {
	switch strings.ToLower(providerID) {
	case "", "ollama":
		return NewOllamaAuth(model, base, key), nil
	case "openai", "anthropic", "openrouter", "gemini", "opencode":
		id := strings.ToLower(providerID)
		if key == "" {
			envKey := "OPENAI_API_KEY"
			if id == "anthropic" {
				envKey = "ANTHROPIC_API_KEY"
			}
			if id == "openrouter" {
				envKey = "OPENROUTER_API_KEY"
			}
			if id == "gemini" {
				envKey = "GEMINI_API_KEY"
			}
			if id == "opencode" {
				envKey = "OPENCODE_API_KEY"
			}
			return nil, fmt.Errorf("no %s API key — run /login %s or set $%s", id, id, envKey)
		}
		if model == "" {
			if ids := CatalogIDs(id); len(ids) > 0 {
				model = ids[0]
			}
		}
		if id == "openai" {
			return NewOpenAI(model, base, key), nil
		}
		if id == "openrouter" {
			return NewOpenRouter(model, base, key), nil
		}
		if id == "gemini" {
			return NewGemini(model, base, key), nil
		}
		if id == "opencode" {
			return NewOpenCode(model, base, key), nil
		}
		return NewAnthropic(model, base, key), nil
	default:
		ids := make([]string, 0, len(Descriptions))
		for _, d := range Descriptions {
			ids = append(ids, d.ID)
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("unknown provider %q (use %s)", providerID, strings.Join(ids, "|"))
	}
}
