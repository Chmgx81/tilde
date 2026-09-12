#!/bin/sh
# tilde installer — build from source and install to $PREFIX.
# Release tarballs (see .goreleaser.yml) install via --from-release TAG;
# source build remains the default.
set -eu

# Keep in sync with Version in internal/update/update.go.
VERSION="v0.10.0"

die() { echo "tilde: $*" >&2; exit 1; }
warn() { echo "tilde: warning: $*" >&2; }

usage() {
    cat <<'EOF'
usage: install.sh [--version] [--uninstall] [--from-release TAG] [--help]
  (no flags)           build from source, install to $PREFIX or ~/.local/bin
  --from-release TAG   download TAG tarball from GitHub releases (sha256-verified)
  --uninstall          remove the installed binary
  --version            print installer version
EOF
}

FROM_RELEASE=""
DO_UNINSTALL=0
for arg in "$@"; do
    case "$arg" in
        --version) echo "tilde installer $VERSION"; exit 0 ;;
        --uninstall) DO_UNINSTALL=1 ;;
        --from-release=*) FROM_RELEASE=${arg#--from-release=} ;;
        --from-release) die "--from-release needs a tag: --from-release=v0.9.0" ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown flag: $arg (see --help)" ;;
    esac
done

dest=${PREFIX:-$HOME/.local/bin}

if [ "$DO_UNINSTALL" -eq 1 ]; then
    rm -f "$dest/tilde"
    echo "removed $dest/tilde (if present; ~/.tilde state kept)"
    exit 0
fi

# verify_checksum FILE SUMS — fail closed on mismatch or missing entry.
verify_checksum() {
    base=$(basename "$1")
    want=$(grep -F " $base" "$2" | awk '{print $1}' | head -n1)
    [ -n "$want" ] || die "no checksum entry for $base in $2"
    if command -v sha256sum >/dev/null 2>&1; then
        got=$(sha256sum "$1" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        got=$(shasum -a 256 "$1" | awk '{print $1}')
    else
        die "no sha256 tool (sha256sum/shasum) — refusing unverified download"
    fi
    [ "$got" = "$want" ] || die "checksum mismatch for $base (download may be corrupt)"
    echo "checksum ok: $base"
}

if [ -n "$FROM_RELEASE" ]; then
    [ "$(uname -s)" = "Linux" ] || die "--from-release supports Linux only"
    case "$(uname -m)" in
        x86_64) arch=amd64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) die "unsupported arch: $(uname -m)" ;;
    esac
    base="tilde_${FROM_RELEASE}_linux_${arch}.tar.gz"
    rel="https://github.com/Chmgx81/tilde/releases/download/${FROM_RELEASE}"
    tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT INT TERM
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "$tmp/$base" "$rel/$base"
        curl -fsSL -o "$tmp/checksums.txt" "$rel/checksums.txt"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "$tmp/$base" "$rel/$base"
        wget -q -O "$tmp/checksums.txt" "$rel/checksums.txt"
    else
        die "need curl or wget for --from-release"
    fi
    verify_checksum "$tmp/$base" "$tmp/checksums.txt"
    tar -xzf "$tmp/$base" -C "$tmp"
    mkdir -p "$dest"
    cp "$tmp/tilde" "$dest/tilde"
    "$dest/tilde" --help >/dev/null 2>&1 || die "installed binary failed its --help smoke test"
    # Record a release install so `tilde update` follows the release channel.
    mkdir -p "$HOME/.tilde" 2>/dev/null || true
    printf '{"kind":"release","tag":"%s","installed_at":"%s"}\n' \
        "$FROM_RELEASE" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$HOME/.tilde/install.json" 2>/dev/null \
        || warn "could not write ~/.tilde/install.json — \`tilde update\` will still use the release channel"
    echo "installed to $dest/tilde ($FROM_RELEASE)"
    exit 0
fi

cd "$(dirname "$0")"

command -v go >/dev/null 2>&1 || die "go not found — install Go 1.25+ (see go.mod) then retry"

ver=$(go version 2>/dev/null | sed -n 's/.* go\([0-9]*\)\.\([0-9]*\).*/\1 \2/p')
if [ -z "$ver" ]; then
    warn "could not parse 'go version' output — need Go 1.25+"
else
    major=${ver%% *}
    minor=${ver##* }
    if [ "$major" -lt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -lt 25 ]; }; then
        warn "go $major.$minor looks older than the required 1.25+ — build may fail"
    fi
fi

command -v bwrap >/dev/null 2>&1 || die "bubblewrap (bwrap) not found — shell calls refuse to run without it. Install bubblewrap, then retry (this script never auto-installs system packages)"

command -v ollama >/dev/null 2>&1 || warn "ollama not found — install it, then: ollama pull qwen3.8-4b:16k"

go build -o tilde .

mkdir -p "$dest"
cp ./tilde "$dest/tilde"

"$dest/tilde" --help >/dev/null 2>&1 || die "installed binary failed its --help smoke test"

# Remember the source checkout so `tilde update` knows what to pull.
# A warning, never fatal: the install itself already succeeded.
src=$(pwd -P)
esc=$(printf '%s' "$src" | sed 's/\\/\\\\/g; s/"/\\"/g')
mkdir -p "$HOME/.tilde"
printf '{"kind":"source","source":"%s","installed_at":"%s"}\n' "$esc" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$HOME/.tilde/install.json" \
    || warn "could not write ~/.tilde/install.json — \`tilde update\` will not know its source"

echo "installed to $dest/tilde"
echo "next steps:"
echo "  ollama serve & ollama pull qwen3.8-4b:16k"
echo "  cd ~/my-project && tilde"
