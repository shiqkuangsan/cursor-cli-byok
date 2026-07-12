#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
INSTALLER=$ROOT_DIR/scripts/install.sh
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/cursor-cli-byok-install-test.XXXXXX")
ORIGINAL_PATH=$PATH
TESTS_RUN=0

cleanup() {
  local code=$?
  trap - EXIT
  case $TEST_ROOT in
    "${TMPDIR:-/tmp}"/cursor-cli-byok-install-test.*) rm -r "$TEST_ROOT" ;;
  esac
  exit "$code"
}
trap cleanup EXIT

fail() {
  printf 'install test failed: %s\n' "$*" >&2
  exit 1
}

file_mode() {
  local path=$1
  if stat -c '%a' "$path" >/dev/null 2>&1; then
    stat -c '%a' "$path"
  else
    stat -f '%Lp' "$path"
  fi
}

host_sha256() {
  local path=$1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
  else
    shasum -a 256 "$path" | awk '{print $1}'
  fi
}

link_command() {
  local destination=$1
  local name=$2
  local source
  source=$(PATH=$ORIGINAL_PATH command -v "$name" || true)
  [[ -n $source ]] || return 0
  ln -s "$source" "$destination/$name"
}

make_test_path() {
  local directory=$1
  local include_curl=$2
  local include_cursor=$3
  mkdir -p "$directory"
  local command_name
  for command_name in awk basename chmod cp dirname grep install mkdir mktemp mv rm sed shasum sha256sum stat tr; do
    link_command "$directory" "$command_name"
  done
  cat >"$directory/uname" <<'EOF'
#!/bin/sh
case "$1" in
  -s) printf 'Linux\n' ;;
  -m) printf 'x86_64\n' ;;
  *) exit 1 ;;
esac
EOF
  chmod 0755 "$directory/uname"
  cat >"$directory/fake-cursor-shell" <<'EOF'
#!/bin/sh
printf 'shebang\n' >"$HOME/cursor-installer-shebang-ran"
exec /bin/sh "$@"
EOF
  chmod 0755 "$directory/fake-cursor-shell"
  if [[ $include_curl == 1 ]]; then
    cat >"$directory/curl" <<'EOF'
#!/bin/sh
output=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o|--output)
      shift
      [ "$#" -gt 0 ] || exit 2
      output=$1
      ;;
    -*) ;;
    *) url=$1 ;;
  esac
  shift
done
[ -n "$output" ] && [ -n "$url" ] || exit 2
printf '%s\n' "$url" >>"$FAKE_CURL_LOG"
case "$url" in
  https://cursor.com/install) source_file=$FAKE_CURL_ASSET_DIR/cursor-install.sh ;;
  *) source_file=$FAKE_CURL_ASSET_DIR/${url##*/} ;;
esac
[ -f "$source_file" ] || exit 22
cp "$source_file" "$output"
EOF
    chmod 0755 "$directory/curl"
  fi
  if [[ $include_cursor == 1 ]]; then
    cat >"$directory/cursor-agent" <<'EOF'
#!/bin/sh
printf '2026.07.08-test\n'
EOF
    chmod 0755 "$directory/cursor-agent"
  fi
}

make_assets() {
  local directory=$1
  local contents=$2
  local checksum_mode=${3:-valid}
  mkdir -p "$directory"
  printf '%s' "$contents" >"$directory/cursor-cli-byok-linux-amd64"
  chmod 0755 "$directory/cursor-cli-byok-linux-amd64"
  local checksum
  checksum=$(host_sha256 "$directory/cursor-cli-byok-linux-amd64")
  if [[ $checksum_mode == invalid ]]; then
    checksum=0000000000000000000000000000000000000000000000000000000000000000
  fi
  printf '%s  cursor-cli-byok-linux-amd64\n' "$checksum" >"$directory/checksums.txt"
  cat >"$directory/cursor-install.sh" <<'EOF'
#!/usr/bin/env fake-cursor-shell
set -eu
mkdir -p "$HOME/.local/bin"
printf '#!/bin/sh\nprintf "2026.07.08-installed\\n"\n' >"$HOME/.local/bin/cursor-agent"
chmod 0755 "$HOME/.local/bin/cursor-agent"
printf 'delegated\n' >"$HOME/cursor-installer-ran"
EOF
}

run_installer() {
  local home=$1
  local path=$2
  local assets=$3
  local log=$4
  shift 4
  env \
    HOME="$home" \
    PATH="$path" \
    FAKE_CURL_ASSET_DIR="$assets" \
    FAKE_CURL_LOG="$log" \
    CURSOR_CLI_BYOK_RELEASE_BASE_URL=https://releases.example.test/v0.1.0 \
    /bin/sh "$INSTALLER" "$@"
}

test_first_install() {
  local root=$TEST_ROOT/first
  local home=$root/home
  local path=$root/path
  local assets=$root/assets
  local install_dir=$home/.local/bin
  mkdir -p "$home"
  make_test_path "$path" 1 1
  make_assets "$assets" 'binary-v1'
  run_installer "$home" "$path" "$assets" "$root/curl.log" --install-dir "$install_dir" --skip-cursor-install >"$root/output"
  [[ $(cat "$install_dir/cursor-cli-byok") == binary-v1 ]] || fail 'first install contents differ'
  [[ $(file_mode "$install_dir/cursor-cli-byok") == 755 ]] || fail 'first install mode is not 0755'
  grep -Fq 'checksums.txt' "$root/curl.log" || fail 'checksums were not downloaded'
  grep -Fxq 'Next: export OPENAI_API_KEY, then run cursor-cli-byok config init' "$root/output" || fail 'first install did not print the configuration next step'
  if grep -Eq 'sk-[[:alnum:]_-]{16,}' "$root/output"; then
    fail 'first install output contained an API-key-like value'
  fi
}

test_upgrade_replaces_existing_binary() {
  local root=$TEST_ROOT/upgrade
  local home=$root/home
  local path=$root/path
  local assets=$root/assets
  local install_dir=$home/.local/bin
  mkdir -p "$install_dir"
  printf 'binary-old' >"$install_dir/cursor-cli-byok"
  make_test_path "$path" 1 1
  make_assets "$assets" 'binary-v2'
  run_installer "$home" "$path" "$assets" "$root/curl.log" --install-dir "$install_dir" --skip-cursor-install >/dev/null
  [[ $(cat "$install_dir/cursor-cli-byok") == binary-v2 ]] || fail 'upgrade did not replace the existing binary'
}

test_checksum_failure_preserves_existing_binary() {
  local root=$TEST_ROOT/checksum
  local home=$root/home
  local path=$root/path
  local assets=$root/assets
  local install_dir=$home/.local/bin
  mkdir -p "$install_dir"
  printf 'known-good' >"$install_dir/cursor-cli-byok"
  make_test_path "$path" 1 1
  make_assets "$assets" 'corrupt-download' invalid
  if run_installer "$home" "$path" "$assets" "$root/curl.log" --install-dir "$install_dir" --skip-cursor-install >"$root/output" 2>&1; then
    fail 'checksum failure returned success'
  fi
  [[ $(cat "$install_dir/cursor-cli-byok") == known-good ]] || fail 'checksum failure replaced the existing binary'
  grep -Fqi 'checksum' "$root/output" || fail 'checksum failure was not reported'
}

test_missing_curl_fails_before_install() {
  local root=$TEST_ROOT/missing-curl
  local home=$root/home
  local path=$root/path
  local assets=$root/assets
  mkdir -p "$home"
  make_test_path "$path" 0 1
  make_assets "$assets" 'binary-v1'
  if run_installer "$home" "$path" "$assets" "$root/curl.log" --install-dir "$home/.local/bin" --skip-cursor-install >"$root/output" 2>&1; then
    fail 'missing curl returned success'
  fi
  grep -Fqi 'curl' "$root/output" || fail 'missing curl was not reported'
  [[ ! -e $home/.local/bin/cursor-cli-byok ]] || fail 'missing curl created an installation'
}

test_missing_cursor_cli_delegates_to_official_installer() {
  local root=$TEST_ROOT/cursor-delegation
  local home=$root/home
  local path=$root/path
  local assets=$root/assets
  mkdir -p "$home"
  make_test_path "$path" 1 0
  make_assets "$assets" 'binary-v1'
  run_installer "$home" "$path" "$assets" "$root/curl.log" --install-dir "$home/.local/bin" >/dev/null
  [[ -x $home/.local/bin/cursor-agent ]] || fail 'official Cursor installer did not create cursor-agent'
  [[ $(cat "$home/cursor-installer-ran") == delegated ]] || fail 'official Cursor installer was not invoked'
  [[ $(cat "$home/cursor-installer-shebang-ran") == shebang ]] || fail 'official Cursor installer shebang was bypassed'
  grep -Fxq 'https://cursor.com/install' "$root/curl.log" || fail 'official Cursor installer URL was not requested'
}

for test_name in \
  test_first_install \
  test_upgrade_replaces_existing_binary \
  test_checksum_failure_preserves_existing_binary \
  test_missing_curl_fails_before_install \
  test_missing_cursor_cli_delegates_to_official_installer; do
  "$test_name"
  TESTS_RUN=$((TESTS_RUN + 1))
  printf 'ok - %s\n' "$test_name"
done

printf 'PASS: %d installer tests\n' "$TESTS_RUN"
