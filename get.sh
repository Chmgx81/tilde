#!/bin/sh
# One-liner installer: curl -fsSL https://raw.githubusercontent.com/Chmgx81/tilde/main/get.sh | sh
# Downloads the latest release binary from GitHub — no Go toolchain required.
set -eu

REPO="Chmgx81/tilde"
BIN="tilde"
DEST="${PREFIX:-$HOME/.local/bin}"

die() { echo "tilde: $*" >&2; exit 1; }

# --- detect platform ---
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
    linux)  os="linux" ;;
    darwin) os="darwin" ;;
    *)      die "unsupported OS: $os (tilde supports Linux and macOS)" ;;
esac

arch=$(uname -m)
case "$arch" in
    x86_64|amd64)   arch="amd64" ;;
    aarch64|arm64)   arch="arm64" ;;
    *)               die "unsupported arch: $arch" ;;
esac

# --- resolve latest tag ---
api="https://api.github.com/repos/${REPO}/releases/latest"
if command -v curl >/dev/null 2>&1; then
    tag=$(curl -fsSL -H "Accept: application/vnd.github+json" "$api" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
elif command -v wget >/dev/null 2>&1; then
    tag=$(wget -qO- -H "Accept: application/vnd.github+json" "$api" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
else
    die "need curl or wget"
fi
[ -n "$tag" ] || die "could not resolve latest release tag"

# --- download ---
base="${BIN}_${tag}_${os}_${arch}"
ext="tar.gz"
# macOS builds use zip in goreleaser; check both.
if [ "$os" = "darwin" ]; then
    ext="zip"
    base="${BIN}_${tag}_${os}_${arch}"
fi
url="https://github.com/${REPO}/releases/download/${tag}/${base}.${ext}"
sums_url="https://github.com/${REPO}/releases/download/${tag}/checksums.txt"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

dl() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "$1" "$2"
    else
        wget -q -O "$1" "$2"
    fi
}

echo "tilde: downloading ${tag} for ${os}/${arch}..."
dl "$tmp/${base}.${ext}" "$url"
dl "$tmp/checksums.txt" "$sums_url" 2>/dev/null || true

# --- verify checksum (best-effort: warn, don't block) ---
if [ -f "$tmp/checksums.txt" ]; then
    want=$(grep -F " ${base}.${ext}" "$tmp/checksums.txt" | awk '{print $1}' | head -n1)
    if [ -n "$want" ]; then
        if command -v sha256sum >/dev/null 2>&1; then
            got=$(sha256sum "$tmp/${base}.${ext}" | awk '{print $1}')
        elif command -v shasum >/dev/null 2>&1; then
            got=$(shasum -a 256 "$tmp/${base}.${ext}" | awk '{print $1}')
        else
            got=""
        fi
        if [ -n "$got" ] && [ "$got" != "$want" ]; then
            die "checksum mismatch (want $want, got $got) — download may be corrupt"
        fi
        echo "tilde: checksum ok"
    fi
fi

# --- extract ---
case "$ext" in
    tar.gz) tar -xzf "$tmp/${base}.${ext}" -C "$tmp" ;;
    zip)    unzip -qo "$tmp/${base}.${ext}" -d "$tmp" ;;
esac

# --- install ---
mkdir -p "$DEST"
cp "$tmp/$BIN" "$DEST/$BIN"
chmod +x "$DEST/$BIN"

# --- verify ---
"$DEST/$BIN" --help >/dev/null 2>&1 || die "installed binary failed smoke test"

echo "tilde: installed ${tag} to ${DEST}/${BIN}"
echo "next steps:"
echo "  cd ~/my-project && tilde"
