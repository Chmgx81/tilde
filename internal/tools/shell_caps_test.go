package tools

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestTaskLogRedactsCommandSecrets(t *testing.T) {
	m := &TaskManager{LogDir: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	secret := "sk-ant-abcdefghij1234567890abcdef"
	task, err := m.Start("echo "+secret, ctx, cancel, func(stdout, stderr io.Writer) *exec.Cmd {
		cmd := exec.Command("true")
		cmd.Stdout, cmd.Stderr = stdout, stderr
		return cmd
	})
	if err != nil {
		t.Fatal(err)
	}
	<-task.done
	data, err := os.ReadFile(task.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(data), secret) || contains(task.Command, secret) {
		t.Fatalf("task command secret leaked into metadata/log: %q", string(data))
	}
}

func startSleepTask(t *testing.T, m *TaskManager, cancels *[]context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	*cancels = append(*cancels, cancel)
	_, err := m.Start("sleep 30", ctx, cancel, func(stdout, stderr io.Writer) *exec.Cmd {
		cmd := exec.Command("sleep", "30")
		cmd.Stdout, cmd.Stderr = stdout, stderr
		return cmd
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTaskManagerMaxTasksRefusesWithFix(t *testing.T) {
	m := &TaskManager{LogDir: t.TempDir(), MaxTasks: 2}
	var cancels []context.CancelFunc
	defer func() {
		m.KillAll()
		for _, c := range cancels {
			c()
		}
	}()
	startSleepTask(t, m, &cancels)
	startSleepTask(t, m, &cancels)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := m.Start("sleep 30", ctx, cancel, func(stdout, stderr io.Writer) *exec.Cmd {
		cmd := exec.Command("sleep", "30")
		cmd.Stdout, cmd.Stderr = stdout, stderr
		return cmd
	}); err == nil {
		t.Fatal("third start past MaxTasks=2 must refuse")
	} else if got := err.Error(); !contains(got, "task limit") || !contains(got, "shell_poll") {
		t.Fatalf("refusal must name the limit and the fix (shell_poll), got: %q", got)
	}
}

func TestTaskManagerReservesSlotsBeforeStart(t *testing.T) {
	m := &TaskManager{LogDir: t.TempDir(), MaxTasks: 1}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	started := make(chan struct{})
	proceed := make(chan struct{})
	build := func(stdout, stderr io.Writer) *exec.Cmd {
		select {
		case <-started:
		default:
			close(started)
		}
		<-proceed
		cmd := exec.Command("sleep", "30")
		cmd.Stdout, cmd.Stderr = stdout, stderr
		return cmd
	}
	first := make(chan error, 1)
	go func() {
		_, err := m.Start("sleep 30", ctx, cancel, build)
		first <- err
	}()
	<-started // the first caller is between reservation and cmd.Start
	if _, err := m.Start("sleep 30", ctx, cancel, build); err == nil {
		t.Fatal("concurrent start must see the reserved slot")
	}
	close(proceed)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	m.KillAll()
}

func TestTaskManagerReleasesReservationOnStartFailure(t *testing.T) {
	m := &TaskManager{LogDir: t.TempDir(), MaxTasks: 1}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := m.Start("bad", ctx, cancel, func(stdout, stderr io.Writer) *exec.Cmd {
		cmd := exec.Command("/definitely/not-a-command")
		cmd.Stdout, cmd.Stderr = stdout, stderr
		return cmd
	})
	if err == nil {
		t.Fatal("invalid command must fail")
	}
	if _, err := m.Start("true", ctx, cancel, func(stdout, stderr io.Writer) *exec.Cmd {
		cmd := exec.Command("true")
		cmd.Stdout, cmd.Stderr = stdout, stderr
		return cmd
	}); err != nil {
		t.Fatalf("failed start leaked its slot: %v", err)
	}
}

func TestTaskManagerDefaultMaxTasks(t *testing.T) {
	m := &TaskManager{}
	if m.maxTasks() != 16 {
		t.Fatalf("zero MaxTasks must default to 16, got %d", m.maxTasks())
	}
	m2 := &TaskManager{MaxTasks: 3}
	if m2.maxTasks() != 3 {
		t.Fatalf("MaxTasks override lost, got %d", m2.maxTasks())
	}
}

func TestTaskManagerLogDirStableUnderConcurrency(t *testing.T) {
	m := &TaskManager{}
	const n = 32
	got := make([]string, n)
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = m.logDir()
		}(i)
	}
	wg.Wait()
	for _, d := range got[1:] {
		if d != got[0] {
			t.Fatalf("concurrent logDir diverged: %q vs %q", got[0], d)
		}
	}
	if got[0] == "" {
		t.Fatal("logDir must never be empty")
	}
}
