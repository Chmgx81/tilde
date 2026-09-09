# Architecture (pointer doc)

Module layout:

```text
main.go (composition root) -> internal/* (flat packages)
```

- `main.go` wires everything; `internal/*` holds flat, single-purpose
  packages (agent, audit, compact, creds, eval, export, hooks, ide,
  mcp, marketplace, mode, plugin, policy, provider, repair, rules, sandbox,
  schedule, scrub, session, skills, tools, trust, tui, update, vec).
- Dependency direction rule: `main.go` may import `internal/*`;
  `internal/*` packages must not import `main.go`, and new
  cross-package deps should point inward toward policy/sandbox/trust/scrub
  style leaf gates, never form cycles (`tools` imports `hooks`, so shared
  secrets patterns live in the `scrub` leaf, never in `tools`).

- Trust denies by default; tool output is scrubbed — see `docs/Plan.md`.
Behavioral spec: see `docs/Plan.md` (source of truth for behavior).
Presentation spec: see `docs/tui-design-spec.md` (source of truth
for TUI/UX). This file intentionally duplicates neither; follow
the pointers above for details.
