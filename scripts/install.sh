#!/usr/bin/env bash
# install.sh installs mymo from its official releases.
#
#   curl -fsSL https://raw.githubusercontent.com/elvonpiko/mymo/main/scripts/install.sh | bash
#
# What it does, in order, and says each step out loud:
#   1. detects the machine's architecture
#   2. downloads the versioned release, showing its progress
#   3. verifies the archive against the published checksum — a
#      mismatch refuses the install, it never continues
#   4. installs the binary to /usr/local/bin when it can (root),
#      or ~/.local/bin when it cannot
#   5. preserves the previous binary as mymo.old — a bad upgrade
#      has a way back
#
# The face is mymo's own: statuses in caps, a progress bar built
# from the same stem block the TUI's mark is made of, mymo's
# palette — and none of it when the terminal cannot take color,
# because a first touch must never print garbage.
#
# Overrides, for testing and mirrors:
#   VERSION=0.1.0          which release to install
#   BASE_URL=<url>         where releases live (default: GitHub)
#   MYMO_INSTALL_DIR=<dir> where the binary lands
set -euo pipefail

VERSION="${VERSION:-0.1.0}"
BASE_URL="${BASE_URL:-https://github.com/elvonpiko/mymo/releases/download/v${VERSION}}"
MYMO_INSTALL_DIR="${MYMO_INSTALL_DIR:-}"

# mymo's palette, truecolor. Every constant empties when stdout is
# not a terminal, so the words stay plain and pasteable there.
if [ -t 1 ]; then
    C_MAUVE=$'\e[38;2;203;166;247m'
    C_BLUE=$'\e[38;2;180;190;254m'
    C_TEAL=$'\e[38;2;148;226;213m'
    C_GREEN=$'\e[38;2;166;227;161m'
    C_SUB=$'\e[38;2;166;173;200m'
    R=$'\e[0m'
else
    C_MAUVE='' C_BLUE='' C_TEAL='' C_GREEN='' C_SUB='' R=''
fi

mauve() { printf '%s%s%s' "$C_MAUVE" "$1" "$R"; }
blue()  { printf '%s%s%s' "$C_BLUE" "$1" "$R"; }
teal()  { printf '%s%s%s' "$C_TEAL" "$1" "$R"; }
green() { printf '%s%s%s' "$C_GREEN" "$1" "$R"; }
sub()   { printf '%s%s%s' "$C_SUB" "$1" "$R"; }

# step prints a status line: the status in caps, the truth of the
# step in plain English beside it
step() { # $1 = status, $2 = rest of the line
    printf '%s  %s\n' "$(blue "$(printf '%-8s' "$1")")" "$2"
}

fail() {
    # failures stay plain: they travel inside bug reports, and
    # escapes would garble them there
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

echo "$(mauve mymo) $(sub "${VERSION}")"
echo
step DETECT "linux/${ARCH}"

# 2 — the release and its checksums
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
ARCHIVE="mymo-${VERSION}-linux-${ARCH}.tar.gz"
ARCHIVE_PATH="${TMP}/${ARCHIVE}"

if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsL --connect-timeout 15 "$1" -o "$2"; }
    FETCHER=curl
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q "$1" -O "$2"; }
    FETCHER=wget
else
    fail "need curl or wget to download the release"
fi

hsize() { awk -v b="$1" 'BEGIN{ printf "%.1f", b/1048576 }'; }

bar_line() { # $1 = bytes so far, $2 = total ("" when unknown)
    if [ -n "$2" ] && [ "$2" -gt 0 ] 2>/dev/null; then
        local pct filled i bar=""
        pct=$(( $1 * 100 / $2 ))
        [ "$pct" -gt 100 ] && pct=100
        filled=$(( ($1 * 10 + $2 - 1) / $2 ))
        [ "$filled" -gt 10 ] && filled=10
        i=0
        while [ "$i" -lt "$filled" ]; do bar="${bar}█"; i=$((i + 1)); done
        while [ "$i" -lt 10 ]; do bar="${bar}░"; i=$((i + 1)); done
        printf '%s  %s%% · %s/%s MiB' \
            "$(teal "$bar")" "$pct" "$(hsize "$1")" "$(hsize "$2")"
    else
        printf '%s MiB so far' "$(hsize "$1")"
    fi
}

# the archive's size, asked before the download so the bar can tell
# the truth; when nothing answers, the bar falls back to honest
# byte counting with no percentage. The question is allowed to
# fail — the download itself reports the failure that matters.
TOTAL="$( { curl -fsIL --connect-timeout 15 "${BASE_URL}/${ARCHIVE}" 2>/dev/null || true; } \
    | tr -d '\r' | awk 'tolower($1)=="content-length:"{v=$2} END{print v}')"

# the bar redraws in place while the download moves; a stalled
# link shows as a stalled bar, which is the truth of it
(
    while :; do
        now="$(wc -c < "$ARCHIVE_PATH" 2>/dev/null || echo 0)"
        printf '\r%s  %s' "$(blue "DOWNLOAD")" "$(bar_line "$now" "$TOTAL")"
        sleep 0.2
    done
) &
POLL_PID=$!

fetch_code=0
fetch "${BASE_URL}/${ARCHIVE}" "${ARCHIVE_PATH}" || fetch_code=$?

kill "$POLL_PID" 2>/dev/null || true
wait "$POLL_PID" 2>/dev/null || true

if [ "$fetch_code" -ne 0 ]; then
    if { [ "$FETCHER" = curl ] && [ "$fetch_code" = 22 ]; } || \
       { [ "$FETCHER" = wget ] && [ "$fetch_code" = 8 ]; }; then
        # an HTTP error answered: the release question
        fail "download failed — is v${VERSION} published?"
    fi
    # the network never got an answer: the path question
    fail "download failed — the network path to github's release servers is not answering; a retry or a VPN often helps"
fi
now="$(wc -c < "$ARCHIVE_PATH" 2>/dev/null || echo 0)"
printf '\r%s  %s\n' "$(blue "DOWNLOAD")" "$(bar_line "$now" "$TOTAL")"

# 3 — verify before anything touches the disk the binary will
# live on. Only this archive's line is used; the whole file is not
# trusted blindly.
fetch "${BASE_URL}/SHA256SUMS" "${TMP}/SHA256SUMS" \
    || fail "downloading SHA256SUMS failed"
WANT="$(awk -v f="$ARCHIVE" '$2 == f {print $1}' "${TMP}/SHA256SUMS")"
[ -n "$WANT" ] || fail "SHA256SUMS does not name ${ARCHIVE} — the release is not what the installer expected"
if command -v sha256sum >/dev/null 2>&1; then
    GOT="$(sha256sum "${ARCHIVE_PATH}" | awk '{print $1}')"
else
    GOT="$(shasum -a 256 "${ARCHIVE_PATH}" | awk '{print $1}')"
fi
[ "$GOT" = "$WANT" ] || fail "checksum mismatch — the archive is not what was published; nothing was installed"
step VERIFY "checksum matches SHA256SUMS"

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
    step PRESERVE "the previous binary is kept as ${DEST_DIR}/mymo.old"
fi

tar -xzf "${ARCHIVE_PATH}" -C "$TMP" "mymo-${VERSION}-linux-${ARCH}/mymo"
mv "${TMP}/mymo-${VERSION}-linux-${ARCH}/mymo" "${DEST_DIR}/${BIN}"
chmod 0755 "${DEST_DIR}/${BIN}"

step INSTALL "${DEST_DIR}/${BIN} ($("${DEST_DIR}/${BIN}" version))"

echo
echo "$(green "mymo is installed — run 'mymo' to open your fleet")"
case ":${PATH}:" in
    *":${DEST_DIR}:"*) ;;
    *) echo; sub "note: ${DEST_DIR} is not on your PATH — add it to use 'mymo'" ;;
esac
