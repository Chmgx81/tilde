# tilde documentation

This directory contains the detailed design, operational, and research documents for tilde. The root [README](../README.md) is the short user guide; these documents preserve the decisions and contracts behind the product.

## Start here

| Document | Use it for |
| --- | --- |
| [Architecture](ARCHITECTURE.md) | module boundaries and dependency rules |
| [Master build plan](Plan.md) | product scope, decisions, and implementation status |
| [Marketplace](marketplace.md) | plugins, skills, hooks, MCP, catalogs, and trust |
| [TUI specification](tui-design-spec.md) | interaction patterns and terminal presentation |
| [Vision design](vision-design.md) | image support contract and current limitations |
| [Sandbox image](sandbox-image.md) | reproducible Podman sandbox builds |
| [2026 agent report](ai-agents-and-terminal-coding-agents-2026.md) | ecosystem research and security lessons |

## Reading conventions

- **Implemented** means the current code and tests support the behavior.
- **Planned** or **TODO** means the document describes a target, not a shipped capability.
- The [master build plan](Plan.md) is the authority for product scope; the [TUI specification](tui-design-spec.md) is the authority for presentation and interaction.
- Research claims and dated figures are snapshots. Verify external facts before using them for a production or compliance decision.

When code changes behavior, update the relevant contract and the changelog in the same change.
