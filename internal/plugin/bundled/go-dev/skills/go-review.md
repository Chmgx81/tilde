---
name: go-review
description: Review Go changes the way this repo expects — vet, format, tests, error handling.
---

# Go review

When reviewing or finishing Go changes, work through this list in order.
Stop at the first failure and fix it before moving on.

1. `go vet ./...` is clean.
2. `gofmt -l` prints nothing (run `gofmt -w` on files you touched).
3. Table-driven tests cover the new branches, including the error paths.
4. Errors wrap context (`%w`) instead of discarding it; no silent `_ =` on
   fallible calls that matter.
5. New goroutines exit (no leaks on the cancel path); shared state is
   mutex-guarded with a comment naming the lock's scope.
6. Public symbols have doc comments; package layout follows the existing
   `internal/<area>/` split instead of inventing a new top-level dir.
