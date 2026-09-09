# tilde (~)

```text
   ▄▄▄▄▄▄▄
▄▄█▀▀▀▀▀▀▀█▄
▀▀         ▀███▄         ▄
               ▀█▄▄▄▄▄▄▄█▀
                 ▀▀▀▀▀▀▀
```

> A security-first terminal coding agent for real repositories.

tilde is a local-first coding harness for software work that needs more than a chat window: repository inspection, controlled tool use, explicit approvals, resumable sessions, plugins, MCP servers, and scriptable output.

It is built for engineers who want an agent that is useful in production codebases without giving up visibility or control.

## Why tilde

- **Local-first:** your repository, session history, and approvals stay on your machine.
- **Inspectable:** tool calls, outputs, decisions, and failures are visible in the transcript and session log.
- **Safe by default:** destructive actions are gated; sandboxing, timeouts, process cleanup, and fail-closed policy checks are built in.
- **Composable:** use built-in tools, MCP servers, plugins, skills, hooks, and providers without changing the core loop.
- **Automation-ready:** interactive TUI and headless modes share the same execution model.
- **Honest under failure:** provider, tool, hook, MCP, updater, and startup errors remain actionable and diagnosable.

## Quickstart

### Install (no clone needed)

```sh
curl -fsSL https://raw.githubusercontent.com/Chmgx81/tilde/main/get.sh | sh
```

Downloads the latest release binary to `~/.local/bin/tilde`. No Go toolchain required.

### Install from source

```sh
git clone https://github.com/Chmgx81/tilde.git
cd tilde
go build -o tilde .
./tilde
```

Go 1.25 or newer and Linux with [bubblewrap](https://github.com/containers/bubblewrap) are required for the default sandbox. To use the local Ollama provider, install Ollama and make sure a model is available:

```sh
ollama pull qwen2.5-coder:7b
./tilde --provider ollama --model qwen2.5-coder:7b
```

Run `./tilde --help` for the current command and flag reference.

## Operating model

tilde separates intent, execution, and trust:

| Mode | Best for | Behavior |
| --- | --- | --- |
| **Plan** | review and design | inspect and propose; changes require approval |
| **Build** | normal implementation | execute approved work with visible tool calls |
| **Auto** | unattended workflows | bounded automation under narrower approval rules |

The agent loop is bounded by task, tool, timeout, and budget limits. Sessions are append-only and resumable, so a crash does not silently erase the work already recorded.

## Providers

The provider layer keeps the session model independent from the model backend. Configure a supported provider through flags or environment variables; credentials are never meant to be committed to the repository.

Common environment variables include:

```sh
export TILDE_MODEL=qwen2.5-coder:7b
```

Use `./tilde --help` and the provider documentation in `docs/` for the exact options available in your checkout.

## Everyday commands

### Interactive controls

- `Enter` — submit the prompt
- `Esc` twice — cancel a running turn
- `Ctrl+C` — clear a non-empty draft; quit when the composer is empty
- `/` — open command/search input
- `?` — show help
- `q` — quit when no confirmation is active

Long waits show live status. Tool activity is rendered as a compact, auditable timeline rather than an opaque spinner.

### Headless and automation

Run one prompt non-interactively with standard exit codes. Select text or JSON output explicitly:

```sh
./tilde --prompt "run the unit tests and summarize failures" --output json
```

Use `--eval`, `--export`, and `--resume` for evaluation and session workflows. `./tilde --help` is the authoritative reference for the current flags.

Useful administrative commands:

```sh
./tilde plugin list
./tilde plugin verify <name>
./tilde audit --json
./tilde models ollama
./tilde update
```

Plugin lifecycle commands also include `install`, `upgrade`, `enable`, `disable`, `remove`, and `rollback`. Update refuses dirty or unexpected source checkouts and never overwrites a working binary after a failed build.

## Plugins, skills, hooks, and MCP

tilde treats extensions as registered capabilities with provenance and lifecycle state:

- **Skills** provide focused instructions and workflows.
- **Plugins** package related skills, tools, hooks, and metadata.
- **Hooks** run at defined lifecycle points under policy and process-group cleanup.
- **MCP servers** expose external tools with per-server isolation, nested approval enforcement, and startup deadlines.
- **Agent manifests** describe optional personas or specialized agents without silently changing the host policy.

The marketplace registry distinguishes project, user, workspace, and remote sources. It reports name collisions, source provenance, version state, and install/update failures instead of silently choosing one item. Plugin installs and upgrades are staged and atomic, with rollback support.

See [docs/marketplace.md](docs/marketplace.md) for the registry model and lifecycle details.

## Web, MCP, and vision status

- **MCP:** supported as a controlled extension boundary; a failed server does not take down the session.
- **Web access:** provider/tool dependent. tilde does not pretend that every configured model can browse the web.
- **Vision:** image support is specified but not yet implemented. `web_shot` can produce screenshots for human review; it does not currently send image bytes to a model. See [docs/vision-design.md](docs/vision-design.md).
- **Browser automation:** available only when an appropriate browser tool or MCP server is installed and approved.

This separation is deliberate: capability discovery is explicit, and unavailable integrations fail clearly rather than producing invented results.

## Safety details

tilde is designed around least privilege and recoverable failure:

- approval checks are enforced at the execution boundary, including nested MCP calls;
- background task slots are reserved atomically to avoid overcommit races;
- hooks can run in a sandbox and are terminated as complete process groups;
- scheduler locks prevent overlapping unattended runs;
- marketplace metadata and artifacts can be verified with signatures, digests, caching, and staged installation;
- updater checks validate the canonical remote, branch, and exact target commit;
- terminal metadata is sanitized before it reaches the UI;
- provider retries use bounded backoff and remain cancellable;
- configuration errors fail closed before the TUI starts.

No security feature is a substitute for reviewing a plugin, hook, MCP server, or remote marketplace before granting it trust.

## Configuration

Configuration precedence is intentionally predictable:

1. command-line flags;
2. environment variables;
3. project configuration;
4. user configuration;
5. built-in defaults.

Keep credentials in the environment or an external secret store. Do not place tokens, private keys, or provider credentials in project config, session exports, plugin manifests, or logs.

Useful runtime controls include budget, timeout, task-slot, paste, and unattended-approval settings. Run `./tilde --help` to see the exact names and defaults for the current build.

## Updating

The updater verifies the expected canonical repository, branch, and exact commit before applying changes. A dirty source tree is intentionally refused:

```text
tilde: update: source tree ... has uncommitted changes — commit or stash them, then retry
```

That message refers to the local checkout being updated, not the GitHub repository itself. Commit or stash local changes first, then retry. If you want to update from GitHub, ensure the local checkout's `origin` remote points to the intended repository URL.

## Evaluation and development

Run the same checks used by CI before sending a change:

```sh
go test -count=1 ./...
go test -race -count=1 ./internal/agent/ ./internal/tools/
go vet ./...
go build -o /tmp/tilde-smoke .
/tmp/tilde-smoke --help
test -z "$(gofmt -l $(find . -name '*.go' -not -path './.git/*'))"
```

CI also runs vulnerability checks and a release dry run. Keep tests deterministic, add regression coverage for every fixed bug, and document behavior changes with the code that introduces them.

## Architecture

The system is organized around a small execution core with explicit boundaries for:

```text
CLI / TUI
  └─ session + transcript
      └─ agent loop + budgets
          ├─ policy / approval
          ├─ tool registry
          │   ├─ built-in tools
          │   ├─ MCP servers
          │   └─ plugins / hooks / skills
          ├─ provider adapters
          └─ append-only session log
```

Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the detailed design, [docs/Plan.md](docs/Plan.md) for the delivery plan, and [docs/vision-design.md](docs/vision-design.md) for vision behavior.

## Project status

tilde is under active development. The core harness, safety boundaries, extension lifecycle, marketplace model, and operational failure handling are implemented and tested. Integrations that depend on an external provider, browser, MCP server, or remote marketplace still require explicit configuration and trust.

Contributions are welcome. Please include focused tests, security implications, and documentation updates with each change.
