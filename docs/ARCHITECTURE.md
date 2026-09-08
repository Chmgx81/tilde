# Architecture (pointer doc)

Module layout:

```text
main.go (composition root) -> internal/* (flat packages)
```

- `main.go` wires everything; `internal/*` holds flat, single-purpose
  packages (agent, compact, creds, eval, hooks, mcp, mode, policy,
  provider, repair, sandbox, session, skills, tools, tui, update,
  plus the `trust` gate).
- Dependency direction rule: `main.go` may import `internal/*`;
  `internal/*` packages must not import `main.go`, and new
  cross-package deps should point inward toward policy/sandbox/trust
  style leaf gates, never form cycles.

- Trust denies by default; tool output is scrubbed — see `docs/Plan.md`.
Behavioral spec: see `docs/Plan.md` (source of truth for behavior).
Presentation spec: see `docs/tui-design-spec.md` (source of truth
for TUI/UX). This file intentionally duplicates neither; follow
the pointers above for details.
