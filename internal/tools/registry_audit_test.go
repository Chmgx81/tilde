package tools

// P2-A audit trail hook tests: Dispatch records one redacted event per
// policy decision + execution via the local AuditSink (kept decoupled so
// audit → tools never cycles back through registry.go).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

type stubAuditSink struct {
	tools     []string
	decisions []string
	hashes    []string
	details   []string
}

func (s *stubAuditSink) AppendEvent(tool, decision, argsHash, detail string) {
	s.tools = append(s.tools, tool)
	s.decisions = append(s.decisions, decision)
	s.hashes = append(s.hashes, argsHash)
	s.details = append(s.details, detail)
}

type stubAuditTool struct {
	out string
	err error
}

func (s *stubAuditTool) Name() string        { return "audit_stub" }
func (s *stubAuditTool) Description() string { return "stub for audit-hook tests" }
func (s *stubAuditTool) Schema() map[string]any {
	return map[string]any{}
}
func (s *stubAuditTool) Exec(_ context.Context, _ map[string]any) (string, error) {
	return s.out, s.err
}

func scrubbedArgsHash(t *testing.T, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	scrubbed, _ := Scrub(string(raw))
	sum := sha256.Sum256([]byte(scrubbed))
	return hex.EncodeToString(sum[:])
}

func TestDispatchAuditAllowHashesNonRawArgs(t *testing.T) {
	secret := "sk-ant-abcdefghijklmnopqrstuvwxyz"
	args := map[string]any{"path": "a.txt", "token": secret}
	reg := NewRegistry()
	reg.Register(&stubAuditTool{out: "stub-ok"})
	reg.Gate = func(string, map[string]any) (bool, string) { return true, "" }
	sink := &stubAuditSink{}
	reg.Audit = sink

	out := reg.Dispatch(context.Background(), "audit_stub", args)
	if !contains(out, "stub-ok") {
		t.Fatalf("dispatch output changed by audit hook: %q", out)
	}
	if contains(out, "[input repaired:") {
		t.Fatalf("repair rewrote args, hash expectation invalid: %q", out)
	}
	if len(sink.tools) != 1 {
		t.Fatalf("want 1 audit event, got %d", len(sink.tools))
	}
	if sink.tools[0] != "audit_stub" {
		t.Fatalf("event tool = %q", sink.tools[0])
	}
	if sink.decisions[0] != "allow" {
		t.Fatalf("event decision = %q, want allow", sink.decisions[0])
	}
	if want := scrubbedArgsHash(t, args); sink.hashes[0] != want {
		t.Fatalf("args hash = %q, want sha256 of scrubbed args %q", sink.hashes[0], want)
	}
	for _, got := range append([]string{sink.hashes[0]}, sink.details[0]) {
		if strings.Contains(got, secret) {
			t.Fatalf("audit event leaks raw secret: %q", got)
		}
	}
}

func TestDispatchAuditDenyRecordsBlocker(t *testing.T) {
	secret := "sk-ant-abcdefghijklmnopqrstuvwxyz"
	args := map[string]any{"path": "a.txt", "token": secret}
	reg := NewRegistry()
	reg.Register(&stubAuditTool{out: "stub-ok"})
	reg.Gate = func(string, map[string]any) (bool, string) {
		return false, "Plan mode: read-only key=" + secret
	}
	sink := &stubAuditSink{}
	reg.Audit = sink

	out := reg.Dispatch(context.Background(), "audit_stub", args)
	if !contains(out, "blocked") {
		t.Fatalf("deny message changed by audit hook: %q", out)
	}
	if len(sink.tools) != 1 {
		t.Fatalf("want 1 audit event, got %d", len(sink.tools))
	}
	if sink.decisions[0] != "deny" {
		t.Fatalf("event decision = %q, want deny", sink.decisions[0])
	}
	if !contains(sink.details[0], "blocked by gate") {
		t.Fatalf("deny detail must name the blocker: %q", sink.details[0])
	}
	if want := scrubbedArgsHash(t, args); sink.hashes[0] != want {
		t.Fatalf("args hash = %q, want sha256 of scrubbed args %q", sink.hashes[0], want)
	}
	for _, got := range append([]string{sink.hashes[0]}, sink.details[0]) {
		if strings.Contains(got, secret) {
			t.Fatalf("audit event leaks raw secret: %q", got)
		}
	}
	if !contains(sink.details[0], "<<REDACTED:anthropic>>") {
		t.Fatalf("deny detail must scrub the gate reason: %q", sink.details[0])
	}
}

func TestDispatchAuditAskApproved(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubAuditTool{out: "stub-ok"})
	reg.Gate = func(string, map[string]any) (bool, string) { return true, "ask-approved by user" }
	sink := &stubAuditSink{}
	reg.Audit = sink

	reg.Dispatch(context.Background(), "audit_stub", map[string]any{"path": "a.txt"})
	if len(sink.decisions) != 1 || sink.decisions[0] != "ask-approved" {
		t.Fatalf("event decisions = %v, want [ask-approved]", sink.decisions)
	}
}

func TestDispatchAuditNilSinkIsNoop(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubAuditTool{out: "stub-ok"})
	reg.Gate = func(string, map[string]any) (bool, string) { return false, "nope" }
	if out := reg.Dispatch(context.Background(), "audit_stub", nil); !contains(out, "blocked") {
		t.Fatalf("nil-sink deny changed: %q", out)
	}
	reg.Gate = func(string, map[string]any) (bool, string) { return true, "" }
	if out := reg.Dispatch(context.Background(), "audit_stub", nil); !contains(out, "stub-ok") {
		t.Fatalf("nil-sink allow changed: %q", out)
	}
}
