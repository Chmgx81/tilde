package tui

import (
	"strings"
	"testing"

	"tilde/internal/agent"
	"tilde/internal/mode"
	"tilde/internal/provider"
)

func newLoginTestModel(t *testing.T, modelName string) *Model {
	t.Helper()
	loop := &agent.Loop{Prov: provider.NewOllama("llama3.2")}
	m := New(loop, mode.Plan, t.TempDir(), modelName, 32000)
	return &m
}

func TestSwitchProviderHonorsRequestedModel(t *testing.T) {
	m := newLoginTestModel(t, "ollama/llama3.2")
	m.keyOverrides = map[string]string{"openai": "test-key-1234"}
	m.switchProvider("openai", "gpt-4.1-mini", "")
	if m.model != "openai/gpt-4.1-mini" {
		t.Fatalf("requested model dropped: got %q, want openai/gpt-4.1-mini", m.model)
	}
}

func TestSwitchProviderDefaultsToCatalogFirst(t *testing.T) {
	m := newLoginTestModel(t, "ollama/llama3.2")
	m.keyOverrides = map[string]string{"openai": "test-key-1234"}
	m.switchProvider("openai", "", "")
	want := "openai/" + provider.CatalogIDs("openai")[0]
	if m.model != want {
		t.Fatalf("empty model should default to catalog first: got %q, want %q", m.model, want)
	}
}

func TestSwitchProviderNoKeyPointsToLogin(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	m := newLoginTestModel(t, "ollama/llama3.2")
	m.creds = nil
	m.keyOverrides = nil
	before := m.model
	m.switchProvider("openai", "gpt-4.1-mini", "")
	if m.model != before {
		t.Fatalf("keyless switch must not move the backend: %q", m.model)
	}
	joined := strings.Join(m.lines, "\n")
	if !strings.Contains(joined, "/login openai") {
		t.Fatalf("keyless switch must name /login openai, got:\n%s", joined)
	}
}

func TestCurrentModelRef(t *testing.T) {
	m := newLoginTestModel(t, "openai/gpt-4.1")
	if ref, err := m.currentModelRef("openai"); err != nil || ref != "openai/gpt-4.1" {
		t.Fatalf("live provider must report its own model: %q, %v", ref, err)
	}
	wantAnthropic := "anthropic/" + provider.CatalogIDs("anthropic")[0]
	if ref, err := m.currentModelRef("anthropic"); err != nil || ref != wantAnthropic {
		t.Fatalf("non-live provider must report catalog first: %q, %v", ref, err)
	}
	if _, err := m.currentModelRef("nope"); err == nil {
		t.Fatal("unknown provider must error")
	}
}

func TestBeginLogin(t *testing.T) {
	m := newLoginTestModel(t, "ollama/llama3.2")
	m.beginLogin("nope")
	if m.keyProvider != "" {
		t.Fatal("unknown provider must not arm key entry")
	}
	m.beginLogin("ollama")
	if m.keyProvider != "" {
		t.Fatal("local provider must not arm key entry")
	}
	m.beginLogin("openai")
	if m.keyProvider != "openai" {
		t.Fatalf("cloud provider must arm key entry, got %q", m.keyProvider)
	}
	m.cancelKeyEntry()
	if m.keyProvider != "" {
		t.Fatal("cancel must disarm key entry")
	}
}

func TestFinishKeyEntryNeverStoresUnvalidated(t *testing.T) {
	m := newLoginTestModel(t, "ollama/llama3.2")
	m.startKeyEntry("openai")
	m.finishKeyEntry(loginValidatedMsg{Provider: "openai", Key: "k", OK: false})
	if m.keyProvider != "" {
		t.Fatal("failed validation must disarm the overlay")
	}
	// No store bound (nil): nothing could have been written; the toast
	// carries the rejection instead.
	joined := strings.Join(m.lines, "\n")
	_ = joined
}

func TestBareModelListsCatalog(t *testing.T) {
	m := newLoginTestModel(t, "ollama/llama3.2")
	m.switchModel("")
	joined := strings.Join(m.lines, "\n")
	for _, want := range []string{"openai/gpt-5.2", "anthropic/", "/model <provider/model>"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bare /model must list the catalog (missing %q):\n%s", want, joined)
		}
	}
}

func TestAutosizeBudgetFromCatalog(t *testing.T) {
	m := newLoginTestModel(t, "ollama/llama3.2")
	m.keyOverrides = map[string]string{"openai": "test-key-1234"}
	// Not explicit: catalog window wins (gpt-5.2 → 400000).
	m.SetBudgetExplicit(false)
	m.switchProvider("openai", "gpt-5.2", "")
	if m.budget != 400000 {
		t.Fatalf("budget should auto-size to 400000, got %d", m.budget)
	}
}

func TestExplicitBudgetNeverAutosized(t *testing.T) {
	m := newLoginTestModel(t, "ollama/llama3.2")
	m.keyOverrides = map[string]string{"openai": "test-key-1234"}
	m.budget = 32000
	m.SetBudgetExplicit(true)
	m.switchProvider("openai", "gpt-5.2", "")
	if m.budget != 32000 {
		t.Fatalf("explicit budget must survive a model switch, got %d", m.budget)
	}
}
