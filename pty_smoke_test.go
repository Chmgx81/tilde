//go:build linux

// pty_smoke_test.go — drives the real tilde binary under a pseudo-terminal
// to cover the terminal-environment contract the rendered tests cannot:
// alt-screen setup and teardown, live resize, and Ctrl+C quitting cleanly.
//
// This is intentionally an integration test: it builds the binary and runs
// it, because terminal cleanup is an OS-level property, not a Model
// property. Skip with -short or on hosts without /dev/ptmx.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// outputTail is a concurrency-safe accumulating buffer for PTY output.
type outputTail struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *outputTail) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *outputTail) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// waitFor polls until want appears in the output or the deadline passes.
func (o *outputTail) waitFor(want string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(o.String(), want) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return strings.Contains(o.String(), want)
}

// openPTY returns the master and slave ends of a fresh pty sized rows×cols.
func openPTY(rows, cols uint16) (master, slave *os.File, err error) {
	mfd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	if err := unix.IoctlSetPointerInt(mfd, unix.TIOCSPTLCK, 0); err != nil {
		unix.Close(mfd)
		return nil, nil, fmt.Errorf("unlock ptmx: %w", err)
	}
	n, err := unix.IoctlGetInt(mfd, unix.TIOCGPTN)
	if err != nil {
		unix.Close(mfd)
		return nil, nil, fmt.Errorf("get pty number: %w", err)
	}
	if err := unix.IoctlSetWinsize(mfd, unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols}); err != nil {
		unix.Close(mfd)
		return nil, nil, fmt.Errorf("set winsize: %w", err)
	}
	sfd, err := unix.Open(fmt.Sprintf("/dev/pts/%d", n), unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		unix.Close(mfd)
		return nil, nil, fmt.Errorf("open pty slave: %w", err)
	}
	return os.NewFile(uintptr(mfd), "ptmx"), os.NewFile(uintptr(sfd), "pts"), nil
}

func TestTUIPTYSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("pty smoke test skipped in -short mode")
	}
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no /dev/ptmx on this host")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	bin := buildTildeForPTY(t)
	home := t.TempDir()

	master, slave, err := openPTY(24, 80)
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer master.Close()

	var out outputTail
	// Pump the master into the buffer until the child closes its end.
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		buf := make([]byte, 4096)
		for {
			n, rerr := master.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()

	cmd := exec.Command(bin, "--no-sandbox")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"TERM=xterm-256color",
		// Keep the run free of ambient keys/network so startup is deterministic.
		"TILDE_ALLOW_NET=",
		"OPENAI_API_KEY=",
	)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start tilde under pty: %v", err)
	}
	slave.Close() // the child owns the slave now

	// The alt-screen is entered on program start — our "it rendered" signal.
	if !out.waitFor("\x1b[?1049h", 15*time.Second) {
		_ = cmd.Process.Kill()
		t.Fatalf("never entered the alt screen; output tail:\n%q", tail(out.String()))
	}

	// Resize: writing the new winsize to the master delivers SIGWINCH to the
	// foreground process group, so this exercises the live-resize path.
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 20, Col: 40}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	// Give the program a beat to re-render at the new geometry.
	beforeResize := out.String()
	time.Sleep(400 * time.Millisecond)
	if out.String() == beforeResize {
		t.Errorf("resize produced no further output — the UI may not be re-rendering")
	}

	// Ctrl+C on an empty idle composer quits.
	if _, err := master.Write([]byte{0x03}); err != nil {
		t.Fatalf("write ctrl-c: %v", err)
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		var ee *exec.ExitError
		if err != nil && !errors.As(err, &ee) {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("tilde did not exit after Ctrl+C; output tail:\n%q", tail(out.String()))
	}
	<-pumpDone

	final := out.String()
	// Terminal cleanup: restore the main screen, show the cursor, and (if it
	// was enabled) drop mouse tracking. Without these a terminal is left
	// unusable after exit.
	for _, seq := range []struct{ name, want string }{
		{"exit alt screen", "\x1b[?1049l"},
		{"show cursor", "\x1b[?25h"},
	} {
		if !strings.Contains(final, seq.want) {
			t.Errorf("terminal cleanup missing %s (%q)", seq.name, seq.want)
		}
	}
	// The session must not have written a crash line.
	if strings.Contains(final, "crashed:") {
		t.Errorf("tilde reported a crash:\n%q", tail(final))
	}
}

// buildTildeForPTY compiles the current package into a temp binary once.
func buildTildeForPTY(t *testing.T) string {
	t.Helper()
	bin := t.TempDir() + "/tilde"
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Skipf("cannot build tilde for pty test: %v", err)
	}
	return bin
}

// tail returns the last n bytes of s as a string (keeps failure output
// readable — a full-screen TUI transcript is enormous).
func tail(s string) string {
	const n = 600
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
