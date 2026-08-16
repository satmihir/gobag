#!/bin/sh
# Rewrite the pinned version and checksums in scripts/gobag-bootstrap.sh from a
# published release.
#
# The pins are what make the skills' first-use download safe, so they must never
# be edited by hand: run this after a release and commit the result.
#
#     sh scripts/release-pin.sh v0.1.0
set -eu

VERSION="${1:-}"
[ -n "$VERSION" ] || {
    echo "usage: sh scripts/release-pin.sh <version-tag>" >&2
    exit 1
}

REPO="satmihir/gobag"
BOOTSTRAP="$(dirname "$0")/gobag-bootstrap.sh"
[ -f "$BOOTSTRAP" ] || {
    echo "release-pin: $BOOTSTRAP not found" >&2
    exit 1
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

url="https://github.com/$REPO/releases/download/$VERSION/checksums.txt"
echo "fetching $url" >&2
curl -fsSL "$url" -o "$tmp/checksums.txt" || {
    echo "release-pin: could not fetch checksums for $VERSION" >&2
    exit 1
}

sed "s/^GOBAG_VERSION=.*/GOBAG_VERSION=\"$VERSION\"/" "$BOOTSTRAP" > "$tmp/out.sh"

for platform in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
    os=${platform%_*}
    arch=${platform#*_}
    asset="gobag_${VERSION}_${os}_${arch}.tar.gz"

    sum=$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1 || true)
    if [ -z "$sum" ]; then
        echo "release-pin: no checksum for $asset in the release" >&2
        exit 1
    fi
    sed "s/^SHA256_${platform}=.*/SHA256_${platform}=\"$sum\"/" "$tmp/out.sh" > "$tmp/next.sh"
    mv "$tmp/next.sh" "$tmp/out.sh"
    echo "  $platform  $sum" >&2
done

# Refuse to leave a half-pinned script behind. Match the assignments only —
# the file's own comment explains what an unpinned value looks like, and a
# whole-file grep matches that prose and rejects a correctly pinned script.
if grep -Eq '^SHA256_[a-z0-9_]+="PLACEHOLDER"' "$tmp/out.sh"; then
    echo "release-pin: some platforms are still unpinned; refusing to write" >&2
    exit 1
fi

chmod +x "$tmp/out.sh"
mv "$tmp/out.sh" "$BOOTSTRAP"
echo "pinned $BOOTSTRAP to $VERSION" >&2
