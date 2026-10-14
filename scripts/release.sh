#!/usr/bin/env bash
# release.sh builds mymo for every supported platform from one
# tree. Pure Go throughout — no cgo, no platform toolchains — so
# any machine with Go builds the same bytes. The version is read
# from the source, never passed by word of mouth.
set -euo pipefail

VERSION="${VERSION:?VERSION is set by make, from the source}"
DIST="dist"
REPO="$(cd "$(dirname "$0")/.." && pwd)"

targets=(
    "linux amd64 tar.gz"
    "linux arm64 tar.gz"
    "windows amd64 zip"
    "windows arm64 zip"
)

rm -rf "$DIST"
mkdir -p "$DIST"
cd "$REPO"

for target in "${targets[@]}"; do
    read -r GOOS GOARCH PKG <<< "$target"
    name="mymo-${VERSION}-${GOOS}-${GOARCH}"
    ext=""
    if [ "$GOOS" = "windows" ]; then ext=".exe"; fi

    mkdir -p "$DIST/$name"
    echo "building $name"
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
        go build -trimpath -ldflags "-s -w" \
        -o "$DIST/$name/mymo${ext}" ./cmd/mymo
    cp LICENSE README.md "$DIST/$name/"

    case "$PKG" in
        tar.gz)
            tar -czf "$DIST/$name.tar.gz" -C "$DIST" "$name"
            ;;
        zip)
            (cd "$DIST" && zip -qr "$name.zip" "$name")
            ;;
    esac
    rm -rf "$DIST/$name"
done

cd "$DIST"
sha256sum mymo-*.tar.gz mymo-*.zip > SHA256SUMS
echo ""
cat SHA256SUMS
echo ""
echo "release $VERSION ready in $DIST/"
