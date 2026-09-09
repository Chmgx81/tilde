# Tilde marketplace and extension registry

`/plugins` and `/marketplace` open one registry with five filters:

```text
Hooks   Plugins   Marketplace   Skills   MCP Servers
```

The registry is a read-only discovery surface. Opening it never executes a
hook, starts an MCP server, loads a skill body, or installs a package.

Hook rows come from the project and user hook configuration files
(`.tilde/hooks.yaml` and `~/.tilde/hooks.yaml`). Installed plugins also expose
their declared hook and MCP manifests as capability rows. These rows describe
what is available; they do not grant trust or enable execution.

## Local marketplace catalog

Marketplace packages are declared by a project-owned catalog at:

```text
.tilde/marketplace/catalog.yaml
```

Tilde also reads compatible local marketplace layouts from
`.tilde/marketplace/catalog.json`, `.agents/plugins/marketplace.json`,
`.claude-plugin/marketplace.json`, and `.cursor-plugin/marketplace.json`.
User-scoped catalogs are read from `~/.tilde/marketplace/catalog.yaml` and
`~/.agents/plugins/marketplace.json`.

Sources must be local directories containing a valid `tilde-plugin.yaml`.
Remote URLs are intentionally rejected. A catalog item is installable only
when its name and version exactly match the source manifest.

Example:

```yaml
items:
  - name: browser-review
    version: 0.8.2
    description: Review browser flows with audited UI and accessibility checks.
    source: packages/browser-review
```

The source path is relative to the project root. A plugin may use the
progressive-disclosure layout from the Agent Skills standard: `skills/` for
entrypoints, `references/` for conditional reading, `scripts/` for reviewed
deterministic helpers, and `assets/` for output templates. Tilde copies only
manifest-listed files and never executes scripts during discovery or install.

The user selects the row and presses `i`, then confirms with `y` or Enter. The
existing plugin installer then copies only manifest-listed files and creates a
SHA-256 lockfile. Installation never executes installed content. Catalog paths
must remain inside their project or user root, including after symlink
resolution.

Press Enter on a plugin to inspect its listed skills. Press Enter on a skill to
load its body into the current session. Skill names are deduplicated by the
underlying registry, so project, user, bundled, and plugin-provided copies do
not create duplicate rows.

## Layout contract

- `/` focuses search.
- Left/right or Tab changes the registry kind.
- Up/down changes the selected row.
- Enter opens a skill or shows a plugin's skills.
- `i` opens an explicit install confirmation for a verified local marketplace
  source; installed packages land on their skill list.
- Esc clears search first, then closes the browser.

This keeps hooks, plugins, marketplace packages, skills, and MCP servers in
one spatial model while preserving their different action boundaries.

## Signed remote catalog foundation

`internal/marketplace/remote.go` provides bounded HTTPS retrieval, Ed25519
catalog verification, artifact SHA-256 verification, and owner-only atomic
catalog caching. Verification is read-only and does not execute or activate
anything. Remote installation remains deliberately disabled until the
verified bytes pass archive containment checks and are staged through the same
plugin lifecycle used for local packages.

## Capability and lifecycle safety

Plugins may declare `skills`, `agents`, `hooks`, and `mcp` resources. Agent
manifests are metadata-only until an explicit activation model exists. Plugin
installation, upgrade, enable, disable, remove, and rollback are separate
operations; newly discovered metadata never enables execution by itself.
