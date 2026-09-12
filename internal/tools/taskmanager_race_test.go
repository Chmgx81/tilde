package tools

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"testing"
)

// Concurrent Start calls must keep the reservation bookkeeping consistent
// under -race: the cap is never exceeded, and a slot is released on both
// the success and failure paths.
func TestTaskManagerConcurrentStartAtCap(t *testing.T) {
	m := &TaskManager{MaxTasks: 4, LogDir: t.TempDir()}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, failed := 0, 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			_, err := m.Start("true", ctx, cancel, func(io.Writer, io.Writer) *exec.Cmd {
				return exec.Command("true")
			})
			mu.Lock()
			if err != nil {
				failed++
				cancel() // reservation was released; nothing owns the cancel
			} else {
				ok++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if ok == 0 {
		t.Fatal("expected at least one task to start")
	}
	if ok > 4 {
		t.Fatalf("cap exceeded: %d tasks started, max 4", ok)
	}
	if ok+failed != 32 {
		t.Fatalf("lost calls: ok=%d failed=%d", ok, failed)
	}
	m.KillAll()
	if ids := m.RunningIDs(); len(ids) != 0 {
		t.Fatalf("tasks still running after KillAll: %v", ids)
	}
}
