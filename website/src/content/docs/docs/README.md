---
title: "tilde documentation"
description: "This directory contains the detailed design, operational, and research documents for tilde. The root [README](../README.md) is the short use"
editUrl: false
---
This directory contains the detailed design, operational, and research documents for tilde. The root [README](/docs/reference/../README/) is the short user guide; these documents preserve the decisions and contracts behind the product.

## Start here

| Document | Use it for |
| --- | --- |
| [Architecture](/docs/reference/ARCHITECTURE/) | module boundaries and dependency rules |
| [Master build plan](/docs/reference/Plan/) | product scope, decisions, and implementation status |
| [Marketplace](/docs/reference/marketplace/) | plugins, skills, hooks, MCP, catalogs, and trust |
| [TUI specification](/docs/reference/tui-design-spec/) | interaction patterns and terminal presentation |
| [TUI audit](/docs/reference/tui-audit/) | current UI/UX findings, fixes, and next pass |
| [Vision design](/docs/reference/vision-design/) | image support contract and current limitations |
| [Sandbox image](/docs/reference/sandbox-image/) | reproducible Podman sandbox builds |
| [2026 agent report](/docs/reference/ai-agents-and-terminal-coding-agents-2026/) | ecosystem research and security lessons |

## Reading conventions

- **Implemented** means the current code and tests support the behavior.
- **Planned** or **TODO** means the document describes a target, not a shipped capability.
- The [master build plan](/docs/reference/Plan/) is the authority for product scope; the [TUI specification](/docs/reference/tui-design-spec/) is the authority for presentation and interaction.
- Research claims and dated figures are snapshots. Verify external facts before using them for a production or compliance decision.

When code changes behavior, update the relevant contract and the changelog in the same change.
