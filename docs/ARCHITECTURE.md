# Architecture

> Current implementation map and dependency rules.

This is intentionally a pointer document, not a package-by-package encyclopedia. Read [Plan.md](Plan.md) for product decisions and [tui-design-spec.md](tui-design-spec.md) for user-facing behavior.

Module layout:

```text
main.go + harness.go (composition root, package main) -> internal/* (flat packages)
```

- `main.go` owns flag parsing and dispatch; `harness.go` owns the core tool
  registry and startup gates (audit sink, provider, policies, hooks, MCP);
  `internal/*` holds flat, single-purpose
  packages (agent, audit, compact, creds, eval, export, hooks, ide,
  mcp, marketplace, mode, plugin, policy, provider, repair, rules, sandbox,
  schedule, scrub, session, skills, slopsquatting, spill, tools, trust, tui,
  update, vec, verify).
- Dependency direction rule: `main.go` may import `internal/*`;
  `internal/*` packages must not import `main.go`, and new
  cross-package deps should point inward toward policy/sandbox/trust/scrub
  style leaf gates, never form cycles (`tools` imports `hooks`, so shared
  secrets patterns live in the `scrub` leaf, never in `tools`).
- `slopsquatting` is a stdlib-only leaf (like `scrub`): it knows the
  hallucinated-package-name database, and `policy` imports it to annotate
  install confirm prompts. The deny/ask machinery itself stays in `policy`.

## Runtime boundaries

- `policy`, `trust`, and `sandbox` decide whether work may execute. The
  sandbox backend is per platform: Linux uses bubblewrap (`sandbox.go`), macOS
  uses Seatbelt (`seatbelt.go`), and anything else fails closed.
- `tools` exposes bounded capabilities; it does not grant trust by itself.
- `provider` adapts model APIs without changing the agent loop.
- `session` and `audit` preserve an inspectable record of work and decisions.
- `plugin`, `skills`, `hooks`, `mcp`, and `marketplace` are extension boundaries; failures are isolated and must not silently widen permissions.

Keep the composition root small, prefer explicit interfaces at external boundaries, and add tests at the boundary whenever a new integration is introduced.

- Trust denies by default; tool output is scrubbed, see `docs/Plan.md`.
Behavioral spec: see `docs/Plan.md` (source of truth for behavior).
Presentation spec: see `docs/tui-design-spec.md` (source of truth
for TUI/UX). This file intentionally duplicates neither; follow
the pointers above for details.
