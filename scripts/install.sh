#!/usr/bin/env bash
# install.sh installs mymo from its official releases.
#
#   curl -fsSL https://raw.githubusercontent.com/elvonpiko/mymo/main/scripts/install.sh | bash
#
# What it does, in order, and says each step out loud:
#   1. detects the machine's architecture
#   2. downloads the versioned release and its checksums
#   3. verifies the archive against the published checksum — a
#      mismatch refuses the install, it never continues
#   4. installs the binary to /usr/local/bin when it can (root),
#      or ~/.local/bin when it cannot
#   5. preserves the previous binary as mymo.old — a bad upgrade
#      has a way back
#
# Overrides, for testing and mirrors:
#   VERSION=0.1.0          which release to install
#   BASE_URL=<url>         where releases live (default: GitHub)
#   MYMO_INSTALL_DIR=<dir> where the binary lands
set -euo pipefail

VERSION="${VERSION:-0.1.0}"
BASE_URL="${BASE_URL:-https://github.com/elvonpiko/mymo/releases/download/v${VERSION}}"
MYMO_INSTALL_DIR="${MYMO_INSTALL_DIR:-}"

fail() {
    echo "mymo install: $*" >&2
    exit 1
}

# 1 — the architecture, from the kernel's own report
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64) ARCH="amd64" ;;
    aarch64 | arm64) ARCH="arm64" ;;
    *) fail "unsupported architecture '$(uname -m)' — mymo builds amd64 and arm64" ;;
esac
OS="$(uname -s)"
[ "$OS" = "Linux" ] || fail "this installer is for Linux; on Windows use install.ps1"

echo "mymo install: ${VERSION} for linux/${ARCH}"

# 2 — the release and its checksums
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
ARCHIVE="mymo-${VERSION}-linux-${ARCH}.tar.gz"

if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q "$1" -O "$2"; }
else
    fail "need curl or wget to download the release"
fi

echo "downloading ${ARCHIVE}"
fetch "${BASE_URL}/${ARCHIVE}" "${TMP}/${ARCHIVE}" || fail "download failed — is v${VERSION} published?"
fetch "${BASE_URL}/SHA256SUMS" "${TMP}/SHA256SUMS" || fail "downloading SHA256SUMS failed"

# 3 — verify before anything touches the disk the binary will live
# on. Only this archive's line is used; the whole file is not
# trusted blindly.
WANT="$(awk -v f="$ARCHIVE" '$2 == f {print $1}' "${TMP}/SHA256SUMS")"
[ -n "$WANT" ] || fail "SHA256SUMS does not name ${ARCHIVE} — the release is not what the installer expected"
if command -v sha256sum >/dev/null 2>&1; then
    GOT="$(sha256sum "${TMP}/${ARCHIVE}" | awk '{print $1}')"
else
    GOT="$(shasum -a 256 "${TMP}/${ARCHIVE}" | awk '{print $1}')"
fi
[ "$GOT" = "$WANT" ] || fail "checksum mismatch — the archive is not what was published; nothing was installed"

echo "checksum verified"

# 4 — where the binary lands
BIN="mymo"
if [ -n "$MYMO_INSTALL_DIR" ]; then
    DEST_DIR="$MYMO_INSTALL_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null; then
    DEST_DIR="/usr/local/bin"
else
    DEST_DIR="${HOME}/.local/bin"
fi
mkdir -p "$DEST_DIR"

# 5 — preserve the previous version before replacing it
if [ -f "${DEST_DIR}/${BIN}" ]; then
    cp "${DEST_DIR}/${BIN}" "${DEST_DIR}/mymo.old"
    echo "previous binary preserved as ${DEST_DIR}/mymo.old"
fi

tar -xzf "${TMP}/${ARCHIVE}" -C "$TMP" "mymo-${VERSION}-linux-${ARCH}/mymo"
mv "${TMP}/mymo-${VERSION}-linux-${ARCH}/mymo" "${DEST_DIR}/${BIN}"
chmod 0755 "${DEST_DIR}/${BIN}"

case ":${PATH}:" in
    *":${DEST_DIR}:"*) ;;
    *) echo "note: ${DEST_DIR} is not on your PATH — add it to use 'mymo'" ;;
esac

echo "installed: ${DEST_DIR}/${BIN} ($("${DEST_DIR}/${BIN}" version))"
