package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// slowTool blocks until its context is done, exercising the dispatch-level
// cooperative timeout. d<=0 means "no declared timeout".
type slowTool struct{ d time.Duration }

func (s *slowTool) Name() string        { return "slow_tool" }
func (s *slowTool) Description() string { return "test" }
func (s *slowTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (s *slowTool) Timeout() time.Duration { return s.d }
func (s *slowTool) Exec(ctx context.Context, _ map[string]any) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(5 * time.Second):
		return "ok", nil
	}
}

type fastTool struct{}

func (f *fastTool) Name() string        { return "fast_tool" }
func (f *fastTool) Description() string { return "test" }
func (f *fastTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (f *fastTool) Exec(context.Context, map[string]any) (string, error) {
	return "ok", nil
}

func TestDispatchPerToolTimeout(t *testing.T) {
	r := NewRegistry()
	r.Register(&slowTool{d: 20 * time.Millisecond})
	out := r.Dispatch(context.Background(), "slow_tool", nil)
	if !strings.Contains(out, "TOOL_TIMEOUT") {
		t.Fatalf("declared timeout must surface TOOL_TIMEOUT, got %q", out)
	}

	// A tool with no Timeout() method is unaffected.
	r2 := NewRegistry()
	r2.Register(&fastTool{})
	if got := r2.Dispatch(context.Background(), "fast_tool", nil); got != "ok" {
		t.Fatalf("fast tool = %q, want ok", got)
	}

	// A zero/absent timeout on a slow tool must not be invented.
	r3 := NewRegistry()
	r3.Register(&slowTool{})
	if got := r3.Dispatch(context.Background(), "slow_tool", nil); strings.Contains(got, "TOOL_TIMEOUT") {
		t.Fatalf("no declared timeout must not synthesize TOOL_TIMEOUT: %q", got)
	}
}

// wrapSlowTool wraps its context error with %v (no Unwrap), mirroring the
// real network tools — the dispatch must still classify it as TOOL_TIMEOUT.
type wrapSlowTool struct{ d time.Duration }

func (s *wrapSlowTool) Name() string        { return "wrap_slow" }
func (s *wrapSlowTool) Description() string { return "test" }
func (s *wrapSlowTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (s *wrapSlowTool) Timeout() time.Duration { return s.d }
func (s *wrapSlowTool) Exec(ctx context.Context, _ map[string]any) (string, error) {
	<-ctx.Done()
	return "", fmt.Errorf("transport failed: %v", ctx.Err())
}

func TestDispatchTimeoutDespiteWrappedError(t *testing.T) {
	r := NewRegistry()
	r.Register(&wrapSlowTool{d: 20 * time.Millisecond})
	out := r.Dispatch(context.Background(), "wrap_slow", nil)
	if !strings.Contains(out, "TOOL_TIMEOUT") {
		t.Fatalf("a %%v-wrapped deadline must still surface TOOL_TIMEOUT, got %q", out)
	}
}

func TestDispatchCallerCancelIsNotToolTimeout(t *testing.T) {
	r := NewRegistry()
	r.Register(&slowTool{d: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := r.Dispatch(ctx, "slow_tool", nil)
	if strings.Contains(out, "TOOL_TIMEOUT") {
		t.Fatalf("caller cancellation must not read as a tool timeout: %q", out)
	}
	if !strings.Contains(out, "failed") {
		t.Fatalf("cancellation should surface as a normal failure, got %q", out)
	}
}
