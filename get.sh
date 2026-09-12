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
    tag=$(curl -fsSL --connect-timeout 10 --max-time 60 --retry 2 --retry-delay 2 \
        -H "Accept: application/vnd.github+json" "$api" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
elif command -v wget >/dev/null 2>&1; then
    tag=$(wget -qO- --timeout=20 --tries=3 -H "Accept: application/vnd.github+json" "$api" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
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

# Bounded, resumable downloads: a stalled/throttled link must fail with a
# clear error, never hang — and each attempt resumes from where the last
# stopped (GitHub release assets support range requests) instead of
# restarting, so a slow connection can still finish.
dl() {
    tries=0
    while [ "$tries" -lt 5 ]; do
        tries=$((tries + 1))
        if command -v curl >/dev/null 2>&1; then
            curl -fsSL --connect-timeout 10 --max-time 300 -C - -o "$1" "$2" && return 0
        else
            wget -q --timeout=20 --tries=1 -c -O "$1" "$2" && return 0
        fi
        echo "tilde: download interrupted (attempt ${tries}/5) — resuming..." >&2
        sleep 2
    done
    return 1
}

echo "tilde: downloading ${tag} for ${os}/${arch}..."
dl "$tmp/${base}.${ext}" "$url" || true
dl "$tmp/checksums.txt" "$sums_url" || true

# Fallback: an authenticated gh occasionally routes around anonymous-CDN
# throttling. Bounded so a stall cannot hang the installer.
if { [ ! -f "$tmp/${base}.${ext}" ] || [ ! -f "$tmp/checksums.txt" ]; } \
    && command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    echo "tilde: retrying via gh..." >&2
    if command -v timeout >/dev/null 2>&1; then
        timeout 900 gh release download "$tag" -R "${REPO}" -p "${base}.${ext}" -p checksums.txt -D "$tmp" --clobber >/dev/null 2>&1 || true
    else
        gh release download "$tag" -R "${REPO}" -p "${base}.${ext}" -p checksums.txt -D "$tmp" --clobber >/dev/null 2>&1 || true
    fi
fi
{ [ -f "$tmp/${base}.${ext}" ] && [ -f "$tmp/checksums.txt" ]; } \
    || die "download failed (network stalled or unavailable) — nothing was installed; check your connection or install from source (./install.sh)"

# --- verify checksum (fail closed: no verification, no install) ---
[ -f "$tmp/checksums.txt" ] || die "missing checksums.txt for ${tag} — refusing unverified install"
want=$(grep -F " ${base}.${ext}" "$tmp/checksums.txt" | awk '{print $1}' | head -n1)
[ -n "$want" ] || die "no checksum entry for ${base}.${ext} — refusing unverified install"
if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$tmp/${base}.${ext}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    got=$(shasum -a 256 "$tmp/${base}.${ext}" | awk '{print $1}')
else
    die "need sha256sum or shasum to verify the download — refusing unverified install"
fi
[ "$got" = "$want" ] || die "checksum mismatch (want $want, got $got) — download may be corrupt or tampered"
echo "tilde: checksum ok"

# --- extract ---
case "$ext" in
    tar.gz) tar -xzf "$tmp/${base}.${ext}" -C "$tmp" ;;
    zip)    unzip -qo "$tmp/${base}.${ext}" -d "$tmp" ;;
esac

# --- install ---
# The archive was checksum-verified as a whole, but re-check the entry
# before copying: refuse a symlink or directory 'tilde' (a crafted
# archive member would make cp follow it and copy the wrong bytes).
[ -f "$tmp/$BIN" ] && [ ! -L "$tmp/$BIN" ] \
    || die "release archive did not contain a regular file '$BIN' — refusing install"
mkdir -p "$DEST"
cp "$tmp/$BIN" "$DEST/$BIN"
chmod +x "$DEST/$BIN"

# --- verify ---
"$DEST/$BIN" --help >/dev/null 2>&1 || die "installed binary failed smoke test"

# --- record provenance so `tilde update` follows the release channel ---
# (best effort: a read-only HOME must not fail the install)
mkdir -p "$HOME/.tilde" 2>/dev/null || true
printf '{"kind":"release","tag":"%s","installed_at":"%s"}\n' \
    "$tag" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$HOME/.tilde/install.json" 2>/dev/null \
    || echo "tilde: note: could not write ~/.tilde/install.json — \`tilde update\` will still use the release channel" >&2

echo "tilde: installed ${tag} to ${DEST}/${BIN}"

# --- PATH check: the most common first-run surprise ---
case ":${PATH}:" in
    *":${DEST}:"*) ;;
    *)
        echo "tilde: note: ${DEST} is not on your PATH — add it, then reopen your shell:"
        echo "  export PATH=\"${DEST}:\$PATH\""
        ;;
esac
echo "next steps:"
echo "  cd ~/my-project && tilde"
