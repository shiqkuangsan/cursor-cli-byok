#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
BYOK=${BYOK_BINARY:-}

fail() {
  printf 'Linux lifecycle smoke failed: %s\n' "$*" >&2
  exit 1
}

file_mode() {
  stat -c '%a' "$1"
}

[[ $(uname -s) == Linux ]] || fail 'Linux host is required'
[[ -n $BYOK && $BYOK = /* && -x $BYOK ]] || fail 'BYOK_BINARY must be an absolute executable path'

SMOKE_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/cursor-cli-byok-linux-smoke.XXXXXX")
cleanup() {
  local code=$?
  trap - EXIT INT TERM
  "$BYOK" stop >/dev/null 2>&1 || true
  case $SMOKE_ROOT in
    "${TMPDIR:-/tmp}"/cursor-cli-byok-linux-smoke.*) rm -r "$SMOKE_ROOT" ;;
    *) printf 'Refusing to remove unexpected smoke path: %s\n' "$SMOKE_ROOT" >&2 ;;
  esac
  exit "$code"
}
trap cleanup EXIT INT TERM

mkdir -p "$SMOKE_ROOT/home" "$SMOKE_ROOT/bin" "$SMOKE_ROOT/workspace"
cp "$ROOT_DIR/test/linux-smoke/fake_cursor_agent.sh" "$SMOKE_ROOT/bin/cursor-agent"
chmod 0755 "$SMOKE_ROOT/bin/cursor-agent"

export HOME=$SMOKE_ROOT/home
export XDG_CONFIG_HOME=$SMOKE_ROOT/xdg/config
export XDG_DATA_HOME=$SMOKE_ROOT/xdg/data
export XDG_STATE_HOME=$SMOKE_ROOT/xdg/state
export PATH=$SMOKE_ROOT/bin:/usr/local/bin:/usr/bin:/bin
export LINUX_SMOKE_PROVIDER_KEY=linux-smoke-secret
export LINUX_SMOKE_MARKER=$SMOKE_ROOT/cursor-args.txt

"$BYOK" config init --non-interactive \
  --name linux-smoke \
  --base-url http://127.0.0.1:9 \
  --endpoint /v1/responses \
  --upstream-model smoke-model \
  --api-key-env LINUX_SMOKE_PROVIDER_KEY >/dev/null

set +e
"$BYOK" --model linux-smoke --print smoke-prompt >"$SMOKE_ROOT/wrapper.out" 2>&1
wrapper_status=$?
set -e
[[ $wrapper_status -eq 23 ]] || fail "wrapper exit code was $wrapper_status, want 23"
[[ -f $LINUX_SMOKE_MARKER ]] || fail 'fake Cursor child did not run'
grep -Fxq -- '-e' "$LINUX_SMOKE_MARKER" || fail 'local endpoint argument was not forwarded'
grep -Fxq -- '--model' "$LINUX_SMOKE_MARKER" || fail 'model argument was not forwarded'
grep -Fxq -- 'linux-smoke' "$LINUX_SMOKE_MARKER" || fail 'selected model was not forwarded'
grep -Fxq -- '--print' "$LINUX_SMOKE_MARKER" || fail 'user arguments were not forwarded'
grep -Fxq -- 'smoke-prompt' "$LINUX_SMOKE_MARKER" || fail 'prompt was not forwarded'
if grep -Fq -- "$LINUX_SMOKE_PROVIDER_KEY" "$LINUX_SMOKE_MARKER" "$SMOKE_ROOT/wrapper.out"; then
  fail 'provider key leaked to child arguments or output'
fi

status_output=$("$BYOK" status 2>&1)
[[ $status_output == *'daemon: running'* ]] || fail 'daemon did not remain available after child exit'
STATE_FILE=$XDG_STATE_HOME/cursor-cli-byok/daemon.json
CONFIG_FILE=$XDG_CONFIG_HOME/cursor-cli-byok/config.yaml
[[ $(file_mode "$STATE_FILE") == 600 ]] || fail 'daemon state mode is not 0600'
[[ $(file_mode "$CONFIG_FILE") == 600 ]] || fail 'config mode is not 0600'
[[ $(file_mode "$(dirname "$STATE_FILE")") == 700 ]] || fail 'state directory mode is not 0700'

"$BYOK" stop >/dev/null
stopped_output=$("$BYOK" status 2>&1)
[[ $stopped_output == *'daemon: stopped'* ]] || fail 'daemon did not stop cleanly'

printf 'PASS: Linux lifecycle smoke on %s as uid %s\n' "$(uname -m)" "$(id -u)"
