---
name: commit-guide
description: Write small, reviewable commits with messages that explain why.
---

# Commit guide

Keep history reviewable — future you (or a teammate) bisects it.

1. One logical change per commit; split refactoring from behavior changes.
2. Stage explicit paths (`git add <path>`), never `git add -A` on a dirty
   tree with unrelated edits.
3. Subject line: lowercase, imperative, under 72 chars (`fix retry leak
   in shell poll`, not `Fixed stuff`).
4. Explain *why* in the body when the diff alone doesn't say it.
5. Never commit secrets, `.env` files, session logs, or local tool state.
6. A commit that adds behavior also updates (or explicitly skips) the docs
   and changelog covering it.
