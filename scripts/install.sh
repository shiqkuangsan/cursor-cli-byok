#!/bin/sh
set -eu

REPOSITORY=shiqkuangsan/cursor-cli-byok
VERSION=${CURSOR_CLI_BYOK_VERSION:-latest}
INSTALL_DIR=${CURSOR_CLI_BYOK_INSTALL_DIR:-}
RELEASE_BASE_URL=${CURSOR_CLI_BYOK_RELEASE_BASE_URL:-}
SKIP_CURSOR_INSTALL=0
TEMP_DIR=
STAGED_BINARY=

usage() {
  cat <<'EOF'
Usage: install.sh [options]

Options:
  --version VERSION          Install a release tag instead of latest.
  --install-dir DIRECTORY    Install cursor-cli-byok into DIRECTORY.
  --skip-cursor-install      Do not install the official Cursor CLI when absent.
  -h, --help                 Show this help.
EOF
}

fail() {
  printf 'cursor-cli-byok installer: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  code=$?
  trap - EXIT HUP INT TERM
  if [ -n "$STAGED_BINARY" ] && [ -e "$STAGED_BINARY" ]; then
    rm -f "$STAGED_BINARY" 2>/dev/null || true
  fi
  if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
    rm -r "$TEMP_DIR" 2>/dev/null || true
  fi
  exit "$code"
}
trap cleanup EXIT HUP INT TERM

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      shift
      [ "$#" -gt 0 ] || fail '--version requires a value'
      VERSION=$1
      ;;
    --install-dir)
      shift
      [ "$#" -gt 0 ] || fail '--install-dir requires a value'
      INSTALL_DIR=$1
      ;;
    --skip-cursor-install)
      SKIP_CURSOR_INSTALL=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
  shift
done

case ${HOME:-} in
  /*) ;;
  *) fail 'HOME must be an absolute path' ;;
esac
if [ -z "$INSTALL_DIR" ]; then
  INSTALL_DIR=$HOME/.local/bin
fi
case $INSTALL_DIR in
  /*) ;;
  *) fail '--install-dir must be an absolute path' ;;
esac
case $VERSION in
  ''|*[!A-Za-z0-9._-]*) fail 'version contains unsupported characters' ;;
esac

command -v curl >/dev/null 2>&1 || fail 'curl is required'
if command -v sha256sum >/dev/null 2>&1; then
  CHECKSUM_TOOL=sha256sum
elif command -v shasum >/dev/null 2>&1; then
  CHECKSUM_TOOL=shasum
else
  fail 'sha256sum or shasum is required for checksum verification'
fi

case $(uname -s) in
  Linux) OS=linux ;;
  *) fail 'only Linux is supported by this installer' ;;
esac
case $(uname -m) in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) fail 'supported Linux architectures are amd64 and arm64' ;;
esac

ASSET=cursor-cli-byok-$OS-$ARCH
if [ -z "$RELEASE_BASE_URL" ]; then
  if [ "$VERSION" = latest ]; then
    RELEASE_BASE_URL=https://github.com/$REPOSITORY/releases/latest/download
  else
    RELEASE_BASE_URL=https://github.com/$REPOSITORY/releases/download/$VERSION
  fi
fi
RELEASE_BASE_URL=${RELEASE_BASE_URL%/}
case $RELEASE_BASE_URL in
  https://*) ;;
  *) fail 'release base URL must use HTTPS' ;;
esac

TEMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/cursor-cli-byok-install.XXXXXX") || fail 'could not create a temporary directory'
umask 077

download() {
  source_url=$1
  destination=$2
  curl --proto '=https' --tlsv1.2 -fsSL -o "$destination" "$source_url" || fail 'download failed'
}

CHECKSUMS_FILE=$TEMP_DIR/checksums.txt
DOWNLOADED_BINARY=$TEMP_DIR/$ASSET
download "$RELEASE_BASE_URL/checksums.txt" "$CHECKSUMS_FILE"
download "$RELEASE_BASE_URL/$ASSET" "$DOWNLOADED_BINARY"

MATCH_COUNT=$(awk -v asset="$ASSET" '$2 == asset || $2 == "*" asset { count++ } END { print count + 0 }' "$CHECKSUMS_FILE")
[ "$MATCH_COUNT" -eq 1 ] || fail "checksum manifest must contain exactly one entry for $ASSET"
EXPECTED_CHECKSUM=$(awk -v asset="$ASSET" '$2 == asset || $2 == "*" asset { print $1 }' "$CHECKSUMS_FILE")
case $EXPECTED_CHECKSUM in
  *[!0-9A-Fa-f]*) fail 'checksum manifest contains an invalid SHA-256 value' ;;
esac
[ "${#EXPECTED_CHECKSUM}" -eq 64 ] || fail 'checksum manifest contains an invalid SHA-256 value'

if [ "$CHECKSUM_TOOL" = sha256sum ]; then
  ACTUAL_CHECKSUM=$(sha256sum "$DOWNLOADED_BINARY" | awk '{ print $1 }')
else
  ACTUAL_CHECKSUM=$(shasum -a 256 "$DOWNLOADED_BINARY" | awk '{ print $1 }')
fi
[ "$ACTUAL_CHECKSUM" = "$EXPECTED_CHECKSUM" ] || fail "checksum verification failed for $ASSET"

mkdir -p "$INSTALL_DIR" || fail 'could not create the installation directory'
TARGET=$INSTALL_DIR/cursor-cli-byok
if [ -h "$TARGET" ]; then
  fail 'refusing to replace a symbolic link at the installation target'
fi
STAGED_BINARY=$INSTALL_DIR/.cursor-cli-byok.tmp.$$
cp "$DOWNLOADED_BINARY" "$STAGED_BINARY" || fail 'could not stage the binary'
chmod 0755 "$STAGED_BINARY" || fail 'could not mark the binary executable'
mv -f "$STAGED_BINARY" "$TARGET" || fail 'could not replace the installed binary'
STAGED_BINARY=
printf 'Installed cursor-cli-byok (%s) to %s\n' "$VERSION" "$TARGET"

if command -v cursor-agent >/dev/null 2>&1 || [ -x "$HOME/.local/bin/cursor-agent" ]; then
  printf 'Official cursor-agent is already installed.\n'
elif [ "$SKIP_CURSOR_INSTALL" -eq 1 ]; then
  printf 'Official cursor-agent is not installed; skipped by request.\n'
else
  printf 'Official cursor-agent is not installed; running the Cursor installer.\n'
  CURSOR_INSTALLER=$TEMP_DIR/cursor-install.sh
  download https://cursor.com/install "$CURSOR_INSTALLER"
  chmod 0700 "$CURSOR_INSTALLER" || fail 'could not prepare the Cursor installer'
  "$CURSOR_INSTALLER" || fail 'the official Cursor installer failed'
  if ! command -v cursor-agent >/dev/null 2>&1 && [ ! -x "$HOME/.local/bin/cursor-agent" ]; then
    fail 'the official Cursor installer completed without installing cursor-agent'
  fi
fi
