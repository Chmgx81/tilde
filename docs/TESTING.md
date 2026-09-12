# Testing

How tilde is tested, and how to add tests that fit.

## Running the suite

```sh
go build ./...                    # must pass before any commit
go vet ./...                      # must be clean
gofmt -l $(git ls-files '*.go')   # must print nothing
go test ./...                     # full suite
go test -race ./...               # full suite under the race detector
go test -short ./...              # skip the slow integration tests
```

Optional, but expected before a release or a security-sensitive change:

```sh
staticcheck ./...                 # if installed
govulncheck ./...                 # known-vulnerability scan
go test ./internal/policy/ -run '^$' -fuzz FuzzShellDeny -fuzztime 30s
```

`internal/update` needs `git` on `PATH`. The PTY smoke test needs
`/dev/ptmx` and the `go` toolchain; both skip themselves when absent.

## The shape of the suite

The pyramid runs bottom-heavy on purpose:

- **Unit tests** carry most of the weight (`policy`, `scrub`, `tools`,
  `session`, `repair`, `slopsquatting`, `vec`).
- **Integration tests** cover the boundaries that unit tests cannot: the
  agent loop with a scripted provider (`internal/agent`), the TUI's rendered
  geometry (`internal/tui`), and the composition root (`main_test.go`).
- **One end-to-end test** runs the real binary under a pseudo-terminal
  (`pty_smoke_test.go`) for the OS-level terminal contract.

### Test that behavior, not implementation

A test should fail when behavior changes, not when the code is reorganized.
Two habits follow from that:

- Assert on public outcomes (returned values, files on disk, error text),
  not on internal call order.
- Prefer a real collaborator over a mock when it is cheap and deterministic
  (`t.TempDir()` + a real file, a scripted `provider.Response`, a real
  `TaskManager`) so the test exercises the same path production does.

`internal/agent/loop_test.go` shows the pattern: a `fakeProv` replays a
fixed `[]provider.Response`, so a whole ReAct turn is deterministic without
a network.

## Conventions

| Convention | Why |
|---|---|
| `t.TempDir()` for every filesystem test | Isolation and automatic cleanup; never write into the repo. |
| `t.Setenv("HOME", t.TempDir())` for anything touching `~/.tilde` | Keeps credential, session, and audit state out of the developer's real home. |
| `t.Setenv` to clear irrelevant keys (`OPENAI_API_KEY`, …) | Ambient credentials must not change a test's outcome. |
| Failures name the invariant | `t.Fatalf("refusing %q: expected the glob hint, got %v", …)` beats `t.Fatal("wrong")`. |
| Table-driven where cases share a shape | One loop, many cases; the case name is the failure label. |
| `t.Skip` for unavailable environments, not `t.Fatal` | `git`, bwrap, `/dev/ptmx`, and the `go` toolchain are not always present. |
| `testing.Short()` for build-and-run tests | Keeps the fast loop fast. |

## Security-specific testing

Security properties are the ones most worth pinning, because a regression is
silent until it is exploited. When you fix a security bug, land the
regression test in the same change, and **verify it fails without the fix** —
a test that passes either way proves nothing.

Fuzz targets live next to the code they exercise and run on their seed
corpus in a normal `go test`:

```sh
go test ./internal/policy/ -run '^$' -fuzz FuzzShellDeny -fuzztime 30s
```

`FuzzShellDeny` asserts the deny judge never panics and is deterministic on
arbitrary input; `FuzzSplitSegmentsNeverGrows` asserts the segmenter never
invents text. Command parsing is fully attacker-influenced, so a panic there
is a denial-of-service on every tool call.

Existing security coverage to extend rather than duplicate:

- `internal/policy/policy_test.go` — deny shapes, wrapper/expansion bypasses,
  heredoc handling, session allowlist, path-scoped denies.
- `internal/slopsquatting/slopsquatting_test.go` — hallucination and typo detection.
- `internal/tools/netsafe_test.go` — SSRF address classification and dialer.
- `internal/tools/tools_test.go` — NUL-path refusal, command-length cap,
  read-before-write, path traversal.
- `internal/scrub/scrub_test.go` — pattern coverage **and** over-redaction
  guards (secrets go, surrounding text stays).
- `internal/session/fork_test.go` — traversal ids, byte-identical branches.

## When you change the TUI

Presentation is specified in [`tui-design-spec.md`](tui-design-spec.md), which
is the source of truth. Change the spec in the same commit as the behavior.

Rendered geometry is pinned by tests (`internal/tui/*_test.go`,
`golden_test.go`); they assert on stripped-ANSI output, so color changes do
not churn them but layout and wording do. Use `stripANSI(...)` when asserting
so a theme change cannot break a layout test.

## CI

`.github/workflows/ci.yml` runs the build, vet, format check, and the full
test suite. The `build-test` job checks out full history so the version tests
can see release tags.
