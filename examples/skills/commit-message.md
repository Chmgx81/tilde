---
name: commit-message
description: Write conventional commits from a staged diff
---
# Commit message style

When asked to write a commit message:

1. Run `git diff --cached --stat` and `git diff --cached` to see what's staged.
2. Write ONE line: `<type>(<scope>): <subject>` — imperative mood, no period, ≤72 chars.
3. Types: feat, fix, docs, refactor, test, chore. Scope is the package or area.
4. Say nothing else. No body unless the change needs justification.
