// Package schedule — local-first job scheduler (no daemon).
//
// The OS owns waking (cron/systemd/launchd); tilde owns deciding what's
// due. Config lives in .tilde/schedule.yaml, a list of:
//
//	id: nightly-review
//	prompt: "Review today's sessions and write a brief."
//	every: 24h
//	at: "09:00"     # optional HH:MM daily local; overrides interval anchor
//	enabled: true
//
// State lives in .tilde/schedule-state.json: {id: lastRun RFC3339}.
//
// The CLI is single-shot, but run-due uses an inter-process lock so two OS
// wakeups cannot execute the same due jobs or race their state writes.
package schedule

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Job is one schedule entry. Every is parsed via time.ParseDuration;
// At is "" or HH:MM daily local.
type Job struct {
	ID      string        `yaml:"id"`
	Prompt  string        `yaml:"prompt"`
	Every   time.Duration `yaml:"every"`
	At      string        `yaml:"at,omitempty"`
	Enabled bool          `yaml:"enabled"`
}

// fileJob is the on-disk shape (every as duration string). Decoded with
// KnownFields so unknown keys are rejected (strict unmarshal).
type fileJob struct {
	ID      string `yaml:"id"`
	Prompt  string `yaml:"prompt"`
	Every   string `yaml:"every"`
	At      string `yaml:"at"`
	Enabled bool   `yaml:"enabled"`
}

// State maps job id -> last run time (RFC3339 in JSON).
type State map[string]time.Time

// ErrLocked reports that another scheduler invocation owns the run lock.
var ErrLocked = errors.New("scheduler already running")

// AcquireLock takes a non-blocking advisory lock on path and returns its
// release function. The file descriptor must remain open for the lifetime of
// the lock; closing it releases the OS lock even if the process exits.
//
// The lock file is intentionally persistent (and empty). flock ownership is
// attached to the open descriptor, not the directory entry, so a stale file
// after a crash does not strand future runs.
func AcquireLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("schedule: cannot create lock directory %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("schedule: cannot open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("schedule: cannot lock %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// LoadFile reads a schedule.yaml file. Missing or empty file = no jobs,
// not an error. Malformed YAML, unknown keys, bad intervals, and empty
// id/prompt fail closed with an error naming the fix.
func LoadFile(path string) ([]Job, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("schedule: cannot read %s: %v", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var raw []fileJob
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("schedule: bad YAML in %s: %v — fix the YAML syntax (list of {id, prompt, every, at?, enabled}); unknown keys are rejected", path, err)
	}
	jobs := make([]Job, 0, len(raw))
	seen := map[string]bool{}
	for i, r := range raw {
		where := fmt.Sprintf("%s job #%d", path, i+1)
		if r.ID != "" {
			where = fmt.Sprintf("%s job %q", path, r.ID)
		}
		if strings.TrimSpace(r.ID) == "" {
			return nil, fmt.Errorf("schedule: %s has empty id — fix by setting a unique non-empty id per entry", where)
		}
		if strings.TrimSpace(r.Prompt) == "" {
			return nil, fmt.Errorf("schedule: %s has empty prompt — fix by setting the prompt text to run when due", where)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("schedule: %s has duplicate id %q — fix by giving every entry a unique id", where, r.ID)
		}
		seen[r.ID] = true
		every, err := time.ParseDuration(strings.TrimSpace(r.Every))
		if err != nil || every <= 0 {
			return nil, fmt.Errorf("schedule: %s has bad every %q: %v — fix with a positive Go duration like 30m or 24h", where, r.Every, err)
		}
		if r.At != "" {
			if err := checkAt(r.At); err != nil {
				return nil, fmt.Errorf("schedule: %s has bad at %q: %v — fix with HH:MM daily local like \"09:00\"", where, r.At, err)
			}
		}
		jobs = append(jobs, Job{
			ID:      r.ID,
			Prompt:  r.Prompt,
			Every:   every,
			At:      r.At,
			Enabled: r.Enabled,
		})
	}
	return jobs, nil
}

// checkAt validates HH:MM (00:00-23:59).
func checkAt(s string) error {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return fmt.Errorf("want HH:MM")
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return fmt.Errorf("hour must be 00-23")
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return fmt.Errorf("minute must be 00-59")
	}
	if len(parts[0]) != 2 || len(parts[1]) != 2 {
		return fmt.Errorf("want zero-padded HH:MM")
	}
	return nil
}

// parseAt splits a validated HH:MM.
func parseAt(s string) (h, m int) {
	parts := strings.SplitN(s, ":", 2)
	h, _ = strconv.Atoi(parts[0])
	m, _ = strconv.Atoi(parts[1])
	return h, m
}

// Due returns the enabled jobs due at now: never run, or now-last >=
// every, or (at set and today's at has passed since last run).
func Due(now time.Time, jobs []Job, state State) []Job {
	var out []Job
	for _, j := range jobs {
		if !j.Enabled {
			continue
		}
		last, ok := state[j.ID]
		if !ok || last.IsZero() {
			out = append(out, j)
			continue
		}
		if now.Before(last) {
			continue // clock skew: fail closed, not due
		}
		if now.Sub(last) >= j.Every {
			out = append(out, j)
			continue
		}
		if j.At != "" && passedAtSince(now, j.At, last) {
			out = append(out, j)
		}
	}
	return out
}

// NextRun returns when job next becomes due at or after now: now when
// already due, last+every otherwise (earliest of that and the next daily
// at strictly after both now and last when at is set). ok is false for
// disabled jobs, which never run.
func NextRun(now time.Time, j Job, state State) (next time.Time, ok bool) {
	if !j.Enabled {
		return time.Time{}, false
	}
	if len(Due(now, []Job{j}, state)) > 0 {
		return now, true
	}
	last := state[j.ID]
	next = last.Add(j.Every)
	if j.At == "" {
		return next, true
	}
	h, m := parseAt(j.At)
	anchor := now
	if last.After(anchor) {
		anchor = last
	}
	day := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), h, m, 0, 0, anchor.Location())
	if !day.After(anchor) {
		day = day.Add(24 * time.Hour)
	}
	if day.Before(next) {
		next = day
	}
	return next, true
}

// passedAtSince reports whether today's at (daily local, in now's
// location) is at or before now and strictly after last.
func passedAtSince(now time.Time, at string, last time.Time) bool {
	h, m := parseAt(at)
	todayAt := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	return !todayAt.After(now) && last.Before(todayAt)
}

// MarkRun records id as run at now, initializing s when nil, and
// returns the updated state.
func MarkRun(s State, id string, now time.Time) State {
	if s == nil {
		s = State{}
	}
	s[id] = now.UTC()
	return s
}

// LoadState reads a schedule-state.json file ({id: lastRun RFC3339}).
// Missing or empty file = empty state, not an error. Corrupt JSON
// fails closed with an error naming the fix.
func LoadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return nil, fmt.Errorf("schedule: cannot read %s: %v", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return State{}, nil
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("schedule: bad JSON in %s: %v — fix by deleting it (it rebuilds) or repairing the {id: RFC3339} map", path, err)
	}
	if s == nil {
		s = State{}
	}
	return s, nil
}

// SaveState writes state atomically (temp file + rename), dir 0700 /
// file 0600 like session logs (owner-only).
func SaveState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("schedule: cannot create %s: %w", filepath.Dir(path), err)
	}
	if s == nil {
		s = State{}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("schedule: marshal state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "schedule-state-*.tmp")
	if err != nil {
		return fmt.Errorf("schedule: cannot write %s: %w", path, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		os.Remove(name)
		return fmt.Errorf("schedule: cannot write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("schedule: cannot write %s: %w", path, err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return fmt.Errorf("schedule: cannot chmod %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600) // tighten pre-existing; best effort
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("schedule: cannot write %s: %w", path, err)
	}
	return nil
}
