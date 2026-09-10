---
title: "Sandbox image"
description: "> Reproducible Podman backend for agent execution."
editUrl: false
---
> Reproducible Podman backend for agent execution.

Status: optional. Bubblewrap remains the default sandbox; use this image when
the deployment environment standardizes on Podman.

**Resource model.** Both backends isolate the agent's filesystem and network,
but they cap resources differently. Bubblewrap gives you filesystem/network
isolation (`--unshare-pid --unshare-net --die-with-parent`) yet, by itself,
cannot bound resident memory or process count — a fork/memory-bomb payload
shares the host cgroup. The Podman backend additionally enforces
`--pids-limit 256` and `--memory 4g`, so it is the backend to reach for when
running untrusted or potentially hostile payloads (`TILDE_BACKEND=podman`).

Minimal agent-execution image: `fedora-minimal` + go, git, coreutils,
bash. Chosen over `archlinux:base` because Fedora ships versioned
releases (`:41`) with stable digests, while Arch is rolling — a digest
pin against rolling breaks reproducibility on every rebuild.

## Build

```bash
podman build -f sandbox.Containerfile -t tilde-sandbox
```

Fully offline rebuild (all layers cached, no egress):

```bash
podman build --network none -f sandbox.Containerfile -t tilde-sandbox
```

## Digest retrieval and pinning

`BuildPodmanArgs` (internal/sandbox/podman.go) refuses any image
without `@sha256:`, so resolve the digest after building:

```bash
podman images --digests tilde-sandbox
export TILDE_SANDBOX_IMAGE='localhost/tilde-sandbox@sha256:<hex>'
```

Pin the base the same way (see the `FROM` comment in
sandbox.Containerfile) before rebuilding.

## Usage

```bash
export TILDE_BACKEND=podman
export TILDE_SANDBOX_IMAGE='localhost/tilde-sandbox@sha256:<hex>'
```

Runtime mounts the project dir read-write, root read-only, network
denied unless `TILDE_ALLOW_NET=1` / `AllowNet`. Pass `--tmpfs /tmp`
(or equivalent) so `GOCACHE`/`GOPATH` under `/tmp` stay writable.

The image is defense in depth, not a replacement for approval checks. Do not
place credentials in the image or repository.

## Supply-chain notes

- Always run digest-pinned (`@sha256:`); tags are mutable and refused.
- Rebuild monthly (or on Fedora/cves advisories) and re-pin the digest.
- Never use the personal blackarch-lab privileged image as a sandbox:
  it runs `--privileged` with host networking and a persistent mutable
  root for interactive pentesting, so a compromised agent could escape
  to the host, exfiltrate over the network, and persist implants.

## Verify (no daemon needed)

- Hand-check the Containerfile: one `FROM`, single install layer,
  `USER agent` (uid 10000) before the final `RUN`, no secrets/SSH.
- `podman build` was NOT run here (no daemon guarantee); build in CI
  with podman available, then `podman run --rm --read-only --network
  none <digest> bash -c 'go version && git --version'`.
