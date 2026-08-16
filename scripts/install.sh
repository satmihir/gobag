#!/bin/sh
# Install the latest gobag release. Intended for headless use — CI, provisioning
# scripts, a spot instance with no Claude Code session running.
#
# Interactive users are better served by the plugin, which installs a pinned,
# checksum-verified binary on first use:
#     /plugin marketplace add satmihir/gobag
#     /plugin install gobag@gobag
#
#     curl -fsSL https://raw.githubusercontent.com/satmihir/gobag/main/scripts/install.sh | sh
set -eu

REPO="satmihir/gobag"
INSTALL_DIR="${GOBAG_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${GOBAG_VERSION:-latest}"

log() { printf '%s\n' "$*" >&2; }
die() {
    log "install: $*"
    exit 1
}

fetch() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$1" -O "$2"
    else
        die "neither curl nor wget is available"
    fi
}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
    x86_64 | amd64) arch="amd64" ;;
    arm64 | aarch64) arch="arm64" ;;
    *) die "unsupported architecture: $arch" ;;
esac
case "$os" in
    darwin | linux) ;;
    *) die "unsupported platform: $os (gobag is POSIX-only)" ;;
esac

if [ "$VERSION" = "latest" ]; then
    log "resolving latest release"
    tmpjson=$(mktemp)
    fetch "https://api.github.com/repos/$REPO/releases/latest" "$tmpjson" ||
        die "could not reach the GitHub releases API"
    VERSION=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmpjson" | head -n1)
    rm -f "$tmpjson"
    [ -n "$VERSION" ] || die "could not determine the latest version"
fi

asset="gobag_${VERSION}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

log "downloading gobag $VERSION for ${os}/${arch}"
fetch "$base/$asset" "$tmp/$asset" || die "download failed: $base/$asset"

# Verify against the release's checksums.txt. An install that skips this is an
# install that will happily run whatever a compromised mirror handed it.
if fetch "$base/checksums.txt" "$tmp/checksums.txt" 2>/dev/null; then
    expected=$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)
    if [ -n "$expected" ]; then
        if command -v sha256sum >/dev/null 2>&1; then
            actual=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
        elif command -v shasum >/dev/null 2>&1; then
            actual=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
        else
            die "no sha256 tool available; refusing to install unverified binary"
        fi
        [ "$actual" = "$expected" ] || die "checksum mismatch for $asset
  expected $expected
  got      $actual"
        log "checksum verified"
    else
        die "$asset is not listed in checksums.txt; refusing to install"
    fi
else
    die "could not download checksums.txt; refusing to install unverified binary"
fi

tar -xzf "$tmp/$asset" -C "$tmp" || die "could not extract $asset"
[ -f "$tmp/gobag" ] || die "archive did not contain a gobag binary"

if ! mkdir -p "$INSTALL_DIR" 2>/dev/null || ! mv "$tmp/gobag" "$INSTALL_DIR/gobag" 2>/dev/null; then
    die "could not write to $INSTALL_DIR
Set GOBAG_INSTALL_DIR to a writable directory, or move the binary yourself."
fi
chmod +x "$INSTALL_DIR/gobag"

log "installed $INSTALL_DIR/gobag"
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) log "note: $INSTALL_DIR is not on PATH — add it to your shell profile" ;;
esac
"$INSTALL_DIR/gobag" version
