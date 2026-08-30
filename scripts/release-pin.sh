#!/bin/sh
# Rewrite the pinned version and checksums in scripts/gobag-bootstrap.sh from a
# published release, and keep the Claude Code plugin metadata on that version.
#
# Plugin metadata has to change before the tag exists. Bootstrap checksums can
# only change after the release assets exist, so the script supports both
# halves of the release:
#
#     sh scripts/release-pin.sh v0.2.1 --metadata-only  # before tagging
#     sh scripts/release-pin.sh v0.2.1                  # after publishing
set -eu

VERSION="${1:-}"
[ -n "$VERSION" ] || {
    echo "usage: sh scripts/release-pin.sh <version-tag> [--metadata-only]" >&2
    exit 1
}
MODE="${2:-}"
[ -z "$MODE" ] || [ "$MODE" = "--metadata-only" ] || {
    echo "usage: sh scripts/release-pin.sh <version-tag> [--metadata-only]" >&2
    exit 1
}

PLUGIN_VERSION=${VERSION#v}
if [ "$PLUGIN_VERSION" = "$VERSION" ] ||
    ! printf '%s\n' "$PLUGIN_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$'; then
    echo "release-pin: version must be a tag such as v0.2.1" >&2
    exit 1
fi

REPO="satmihir/gobag"
SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
ROOT=$(CDPATH= cd "$SCRIPT_DIR/.." && pwd)
BOOTSTRAP="$SCRIPT_DIR/gobag-bootstrap.sh"
PLUGIN_MANIFEST="$ROOT/.claude-plugin/plugin.json"
MARKETPLACE="$ROOT/.claude-plugin/marketplace.json"

for file in "$BOOTSTRAP" "$PLUGIN_MANIFEST" "$MARKETPLACE"; do
    [ -f "$file" ] || {
        echo "release-pin: $file not found" >&2
        exit 1
    }
done

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

# Plugin manifests use SemVer without the tag's leading v. Validate the number
# of fields before replacing them so a schema change cannot silently leave one
# stale or rewrite an unrelated value.
[ "$(grep -c '^[[:space:]]*"version":' "$PLUGIN_MANIFEST")" -eq 1 ] || {
    echo "release-pin: expected one version field in $PLUGIN_MANIFEST" >&2
    exit 1
}
[ "$(grep -c '^[[:space:]]*"version":' "$MARKETPLACE")" -eq 2 ] || {
    echo "release-pin: expected two version fields in $MARKETPLACE" >&2
    exit 1
}
sed "s/^\([[:space:]]*\"version\":[[:space:]]*\)\"[^\"]*\"/\1\"$PLUGIN_VERSION\"/" \
    "$PLUGIN_MANIFEST" > "$tmp/plugin.json"
sed "s/^\([[:space:]]*\"version\":[[:space:]]*\)\"[^\"]*\"/\1\"$PLUGIN_VERSION\"/" \
    "$MARKETPLACE" > "$tmp/marketplace.json"

if [ "$MODE" = "--metadata-only" ]; then
    mv "$tmp/plugin.json" "$PLUGIN_MANIFEST"
    mv "$tmp/marketplace.json" "$MARKETPLACE"
    echo "set plugin metadata to $PLUGIN_VERSION" >&2
    exit 0
fi

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
mv "$tmp/plugin.json" "$PLUGIN_MANIFEST"
mv "$tmp/marketplace.json" "$MARKETPLACE"
echo "pinned $BOOTSTRAP to $VERSION" >&2
echo "set plugin metadata to $PLUGIN_VERSION" >&2
