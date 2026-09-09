# tilde agent-execution sandbox image (P4-B).
# See docs/sandbox-image.md for build / pin / usage.
#
# Pin the base before building: pull, then resolve the digest with
#   podman pull quay.io/fedora/fedora-minimal:41
#   podman images --digests quay.io/fedora/fedora-minimal
# and rewrite the FROM below as
#   FROM quay.io/fedora/fedora-minimal@sha256:<hex>
FROM quay.io/fedora/fedora-minimal:41

# Toolchain only: go + git + coreutils + bash. No pentest tools,
# no secrets, no SSH — runtime hardening (--network none, --read-only,
# --cap-drop all) is applied by BuildPodmanArgs at `podman run` time,
# not baked here.
RUN microdnf -y install golang git coreutils bash \
    && microdnf clean all \
    && rm -rf /var/cache/dnf /var/cache/yum

# Non-root execution user; numeric UID keeps --user mapping stable.
RUN useradd -m -u 10000 -s /bin/bash agent

# Read-only-root friendly: Go writes caches under /tmp (tmpfs at run).
ENV GOCACHE=/tmp/gocache \
    GOPATH=/tmp/gopath \
    GOTOOLCHAIN=local \
    TMPDIR=/tmp

WORKDIR /work
RUN chown agent:agent /work
USER agent

# Offline self-check: proves the toolchain works with egress denied.
# (Install layer above is the only step allowed network access.)
RUN --network=none go version && git --version && bash --version
