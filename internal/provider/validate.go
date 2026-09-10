package provider

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// Validate sends a minimal real request to the provider's endpoint and
// reports whether the key is accepted. Used by /login before anything
// is stored (spec §2.24: no save-unvalidated path — a stored-but-broken
// key is worse than an absent one). networkErr distinguishes "endpoint
// unreachable" from "key rejected": the remedies differ (/login retry
// vs. check the key).
// closeBody closes a response body, tolerating nils: http.Do guarantees a
// non-nil body on success today, but a future edit adding a new retry path
// must not turn a bare Close into a nil-deref panic on this hot path.
func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
}

func Validate(providerID, key, base string) (ok bool, networkErr bool, err error) {
	if strings.TrimSpace(key) == "" {
		return false, false, errors.New("empty key")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var req *http.Request
	var err2 error
	switch providerID {
	case "openai", "openrouter", "gemini", "opencode":
		url := strings.TrimRight(base, "/")
		if url == "" {
			url = "https://api.openai.com/v1"
			if providerID == "openrouter" {
				url = DefaultOpenRouterBase
			}
			if providerID == "gemini" {
				url = DefaultGeminiBase
			}
			if providerID == "opencode" {
				url = DefaultOpenCodeBase
			}
		}
		req, err2 = http.NewRequest("GET", url+"/models", nil)
		if err2 == nil {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	case "anthropic":
		url := strings.TrimRight(base, "/")
		if url == "" {
			url = "https://api.anthropic.com"
		}
		req, err2 = http.NewRequest("POST", url+"/v1/messages", strings.NewReader(`{"model":"claude-haiku-4-5","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`))
		if err2 == nil {
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
			req.Header.Set("content-type", "application/json")
		}
	default:
		return false, false, errors.New("provider does not take a key")
	}
	if err2 != nil {
		return false, true, err2
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, true, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return true, false, nil
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return false, false, errors.New("key rejected (auth failed — check the key)")
	case resp.StatusCode == 429:
		return false, true, errors.New("throttled (429) — retry later, key unverified, nothing stored")
	case resp.StatusCode == 404:
		return false, true, errors.New("unverified (404) — check-url/base path, key unverified, nothing stored")
	case resp.StatusCode >= 500:
		return false, true, errors.New("unverified (server error) — retry later, key unverified, nothing stored")
	default:
		// Fail closed: any other non-2xx proves nothing about the
		// key, so never store. Auth stays key-rejected; everything
		// else names retry/check-url instead of auth.
		return false, false, errors.New("unverified — retry/check-url, not auth: check the endpoint URL and retry, nothing stored")
	}
}
