#!/bin/sh
# tilde installer — build from source and install to $PREFIX.
# No releases exist yet, so there is no curl-pipe download URL;
# this script builds the binary locally with `go build`.
set -eu

die() { echo "tilde: $*" >&2; exit 1; }
warn() { echo "tilde: warning: $*" >&2; }

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

dest=${PREFIX:-$HOME/.local/bin}
mkdir -p "$dest"
cp ./tilde "$dest/tilde"

"$dest/tilde" --help >/dev/null 2>&1 || die "installed binary failed its --help smoke test"

# Remember the source checkout so `tilde update` knows what to pull.
# A warning, never fatal: the install itself already succeeded.
src=$(pwd -P)
esc=$(printf '%s' "$src" | sed 's/\\/\\\\/g; s/"/\\"/g')
mkdir -p "$HOME/.tilde"
printf '{"source":"%s","installed_at":"%s"}\n' "$esc" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$HOME/.tilde/install.json" \
    || warn "could not write ~/.tilde/install.json — \`tilde update\` will not know its source"

echo "installed to $dest/tilde"
echo "next steps:"
echo "  ollama serve & ollama pull qwen3.8-4b:16k"
echo "  cd ~/my-project && ./tilde"
