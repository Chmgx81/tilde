// Package provider — minimal streaming support (P2-D).
//
// Streamer is an OPTIONAL interface a backend may implement for
// streaming chat. The Provider interface is unchanged: backends that
// do not implement Streamer keep compiling untouched and keep working
// via Chat. Callers type-assert and fall back to Chat on any stream
// failure, so retry/Classify/backoff behavior of the non-stream path
// is intact and stream errors surface as ordinary provider errors.
//
// Correctness rule (learned the hard way): the stream request carries
// the same tool definitions as Chat, and streamed tool calls are
// assembled and returned — a text-only fast path silently drops native
// tool calls whenever a turn mixes prose with calls, which reads as
// "the model stopped using tools". Never take a fast path that cannot
// see calls.
package provider

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// StreamEvent is one backend-produced stream item: a prose delta, a
// snapshot of the calls assembled so far, or both. Backends emit text
// deltas as they arrive and the complete call set once assembled
// (Ollama replaces on each non-empty tool_calls message; OpenAI
// assembles indexed argument fragments and emits at [DONE]/EOF).
// Truncated marks a length-cut reply (Ollama done_reason, OpenAI
// finish_reason), mirroring Response.Truncated in Chat.
type StreamEvent struct {
	Text      string
	Calls     []ToolCall
	Truncated bool
}

// Streamer is implemented by backends with a streaming chat endpoint
// (Ollama NDJSON /api/chat with stream:true, OpenAI-compatible SSE
// /chat/completions with stream:true). Thinking traces, usage, and
// truncation signals do NOT travel uniformly — Collect fills what the
// transport carries and leaves the rest zero, exactly like Chat does
// when a backend omits them. Close both channels when done; send at
// most one non-nil error on errs (buffered, so a gone consumer never
// wedges the producer).
type Streamer interface {
	Stream(ctx context.Context, messages []Message, defs []ToolDef) (<-chan StreamEvent, <-chan error)
}

// StreamFirstChunkTimeout bounds time-to-first-event before the caller
// gives up on the stream and falls back to Chat. Var (like
// retryBaseDelay) so tests can shrink it.
var StreamFirstChunkTimeout = 30 * time.Second

// Collect drains a Stream into one Response mirroring Chat semantics:
// concatenated prose (think-tag-stripped, thinking preserved), the
// assembled tool calls, and the truncation signal. Any stream error —
// transport failure, first-event timeout, mid-stream abort, ctx
// cancellation — discards the partial result and returns ("", err) so
// the caller falls back to Chat. A nil event or error channel from a
// misbehaving backend is itself an error. With no calls assembled, the
// Chat content-call fallback applies (local models echo calls as JSON
// in prose); with calls, prose stays untouched.
func Collect(ctx context.Context, s Streamer, messages []Message, defs []ToolDef) (Response, error) {
	events, errs := s.Stream(ctx, messages, defs)
	if events == nil || errs == nil {
		return Response{}, fmt.Errorf("stream: backend returned nil channels — retry the request")
	}
	timer := time.NewTimer(StreamFirstChunkTimeout)
	defer timer.Stop()
	var b strings.Builder
	var calls []ToolCall
	var truncated bool
	waitingFirst := true
	for events != nil || errs != nil {
		var timeout <-chan time.Time
		if waitingFirst {
			timeout = timer.C
		}
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return Response{}, err
			}
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if waitingFirst {
				waitingFirst = false
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			b.WriteString(ev.Text)
			if ev.Calls != nil {
				calls = ev.Calls
			}
			if ev.Truncated {
				truncated = true
			}
		case <-timeout:
			return Response{}, fmt.Errorf("stream: first event timeout after %s — retry the request", StreamFirstChunkTimeout)
		}
	}
	text := b.String()
	resp := Response{ToolCalls: calls, Truncated: truncated}
	prose, thinking := splitThinkTags(text)
	resp.Content, resp.Thinking = prose, thinking
	if len(resp.ToolCalls) == 0 {
		if fb := parseContentCalls(prose); len(fb) > 0 {
			resp.ToolCalls = fb
			resp.Content = ""
		}
	}
	return resp, nil
}
