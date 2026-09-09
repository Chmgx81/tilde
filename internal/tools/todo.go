package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

type TodoItem struct {
	ID   int
	Text string
	Done bool
}
type TodoManager struct {
	mu    sync.Mutex
	items []TodoItem
	next  int
}
type TodoWrite struct{ Mgr *TodoManager }

// Snapshot returns a copy of the current todo list in insertion order.
// The TUI renders its §2.9 Update-Todos block from this (live manager
// state) rather than parsing tool-result text. Nil-safe: a nil manager
// snapshots as empty.
func (m *TodoManager) Snapshot() []TodoItem {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TodoItem, len(m.items))
	copy(out, m.items)
	return out
}

func (t *TodoWrite) Name() string { return "todo_write" }

func (t *TodoWrite) Description() string {
	return "Serial session todo list: add, done, list, clear. In-memory only."
}
func (t *TodoWrite) Schema() map[string]any {
	op := map[string]any{"type": "string", "enum": []string{"add", "done", "list", "clear"}}
	return map[string]any{"type": "object", "properties": map[string]any{"op": op,
		"text": map[string]any{"type": "string"}, "id": map[string]any{"type": "number"}}, "required": []string{"op"}}
}
func (t *TodoWrite) Exec(_ context.Context, args map[string]any) (string, error) {
	m := t.Mgr
	if m == nil {
		return "", fmt.Errorf("todo_write has no manager")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch op := optStr(args, "op", "list"); op {
	case "add":
		text := optStr(args, "text", "")
		if text == "" {
			return "", fmt.Errorf("op \"add\" needs \"text\" as a string")
		}
		m.next++
		m.items = append(m.items, TodoItem{ID: m.next, Text: text})
	case "done":
		id := optInt(args, "id", 0)
		i := -1
		for n := range m.items {
			if m.items[n].ID == id {
				i = n
			}
		}
		if i < 0 {
			return "", fmt.Errorf("no todo with that id: list first, then pass a visible id")
		}
		m.items[i].Done = true
	case "clear":
		m.items = nil
	case "list":
	default:
		return "", fmt.Errorf("unknown op %q: pass one of add|done|list|clear", op)
	}
	if len(m.items) == 0 {
		return "todo list is empty.", nil
	}
	var b strings.Builder
	for _, it := range m.items {
		mark := "□"
		if it.Done {
			mark = "☑"
		}
		fmt.Fprintf(&b, "%s %d: %s\n", mark, it.ID, it.Text)
	}
	return Fence(strings.TrimRight(b.String(), "\n")), nil
}
