#!/bin/sh
# Ensure a verified gobag binary is on PATH, printing its path on stdout.
#
# Both skills call this instead of describing the download in prose: the
# version and checksums below are the security boundary, and they belong in one
# auditable file that release automation rewrites (scripts/release-pin.sh).
#
# Exit codes: 0 ready (path on stdout), 1 user/environment problem.
set -eu

GOBAG_VERSION="v0.1.0"

# sha256 of each release asset. "PLACEHOLDER" means this version has not been
# released yet; the script refuses to install rather than skip verification.
SHA256_darwin_arm64="0ddac3109d93197c1f1c345263d3d1a0ca5a7b33570613cf00d849060041a688"
SHA256_darwin_amd64="68618f8dd1663ec535455c3d42a7dbd6703477d50551958359fd53d5ee84e760"
SHA256_linux_arm64="2d0a89c6ccc95296b7ae750e4f10c659815a9c90436cb3d116c85e1983d91ac5"
SHA256_linux_amd64="5155988ae03d9fca910f942b480bb557faea2d98fa14ed38b81596dbc1b4b08f"

INSTALL_DIR="${GOBAG_INSTALL_DIR:-$HOME/.local/bin}"
REPO="satmihir/gobag"

log() { printf '%s\n' "$*" >&2; }
die() { log "gobag-bootstrap: $*"; exit 1; }

# Already available? Nothing to do.
if command -v gobag >/dev/null 2>&1; then
    command -v gobag
    exit 0
fi
if [ -x "$INSTALL_DIR/gobag" ]; then
    printf '%s\n' "$INSTALL_DIR/gobag"
    exit 0
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
    x86_64 | amd64) arch="amd64" ;;
    arm64 | aarch64) arch="arm64" ;;
    *) die "unsupported architecture: $arch" ;;
esac
case "$os" in
    darwin | linux) ;;
    *) die "unsupported platform: $os (gobag is POSIX-only; Windows is not supported)" ;;
esac

eval "expected=\${SHA256_${os}_${arch}}"
if [ "$expected" = "PLACEHOLDER" ]; then
    die "gobag $GOBAG_VERSION has no published binary for ${os}/${arch} yet.
Build it from source instead:
    go install github.com/satmihir/gobag/cmd/gobag@latest
Then re-run this command."
fi

asset="gobag_${GOBAG_VERSION}_${os}_${arch}.tar.gz"
url="https://github.com/$REPO/releases/download/$GOBAG_VERSION/$asset"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

log "downloading gobag $GOBAG_VERSION for ${os}/${arch}"
if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$tmp/$asset" || die "download failed: $url"
elif command -v wget >/dev/null 2>&1; then
    wget -q "$url" -O "$tmp/$asset" || die "download failed: $url"
else
    die "neither curl nor wget is available"
fi

if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
else
    die "no sha256 tool available; refusing to install unverified binary"
fi

if [ "$actual" != "$expected" ]; then
    die "checksum mismatch for $asset
  expected $expected
  got      $actual
Refusing to install. This is worth reporting at https://github.com/$REPO/issues"
fi

tar -xzf "$tmp/$asset" -C "$tmp" || die "could not extract $asset"
[ -f "$tmp/gobag" ] || die "archive did not contain a gobag binary"

mkdir -p "$INSTALL_DIR"
mv "$tmp/gobag" "$INSTALL_DIR/gobag"
chmod +x "$INSTALL_DIR/gobag"

log "installed $INSTALL_DIR/gobag"
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) log "note: $INSTALL_DIR is not on PATH" ;;
esac
printf '%s\n' "$INSTALL_DIR/gobag"
