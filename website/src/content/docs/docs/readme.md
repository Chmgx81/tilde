---
title: "tilde documentation"
description: "This directory contains the detailed design, operational, and research documents for tilde. The root [README](../README.md) is the short use"
editUrl: false
---
This directory contains the detailed design, operational, and research documents for tilde. The root [README](https://github.com/Chmgx81/tilde/blob/main/README.md) is the short user guide; these documents preserve the decisions and contracts behind the product.

## Start here

| Document | Use it for |
| --- | --- |
| [Architecture](/docs/architecture/) | module boundaries and dependency rules |
| [Master build plan](/docs/plan/) | product scope, decisions, and implementation status |
| [Marketplace](/docs/marketplace/) | plugins, skills, hooks, MCP, catalogs, and trust |
| [TUI specification](/docs/tui-design-spec/) | interaction patterns and terminal presentation |
| [TUI audit](/docs/tui-audit/) | current UI/UX findings, fixes, and next pass |
| [Vision design](/docs/vision-design/) | image support contract and current limitations |
| [Sandbox image](/docs/sandbox-image/) | reproducible Podman sandbox builds |
| [2026 agent report](/docs/ai-agents-and-terminal-coding-agents-2026/) | ecosystem research and security lessons |

## Reading conventions

- **Implemented** means the current code and tests support the behavior.
- **Planned** or **TODO** means the document describes a target, not a shipped capability.
- The [master build plan](/docs/plan/) is the authority for product scope; the [TUI specification](/docs/tui-design-spec/) is the authority for presentation and interaction.
- Research claims and dated figures are snapshots. Verify external facts before using them for a production or compliance decision.

When code changes behavior, update the relevant contract and the changelog in the same change.
