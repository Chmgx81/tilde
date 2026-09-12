package tools

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"testing"
)

// Concurrent Start calls must keep the reservation bookkeeping consistent
// under -race: the running-task cap is never exceeded, and a slot is
// released on both the success and failure paths. The command blocks so
// finished tasks can't be pruned mid-test and free a slot.
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
			_, err := m.Start("sleep 3", ctx, cancel, func(io.Writer, io.Writer) *exec.Cmd {
				return exec.Command("sleep", "3")
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
		t.Fatalf("cap exceeded: %d tasks started concurrently, max 4", ok)
	}
	if ok+failed != 32 {
		t.Fatalf("lost calls: ok=%d failed=%d", ok, failed)
	}
	m.KillAll()
	if ids := m.RunningIDs(); len(ids) != 0 {
		t.Fatalf("tasks still running after KillAll: %v", ids)
	}
}
