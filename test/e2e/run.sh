#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
source "$ROOT_DIR/test/e2e/version.sh"
ORIGINAL_PATH=$PATH
CURSOR_AGENT_PATH=${CURSOR_AGENT:-}
KEEP_TMP=${E2E_KEEP_TMP:-0}
PREBUILT_BYOK=${E2E_BYOK_BINARY:-}
PREBUILT_HELPER=${E2E_HELPER_BINARY:-}

fail() {
  printf 'E2E FAIL: %s\n' "$*" >&2
  exit 1
}

note() {
  printf 'E2E: %s\n' "$*"
}

assert_contains() {
  local value=$1
  local fragment=$2
  local label=$3
  if [[ $value != *"$fragment"* ]]; then
    printf '%s\n' "$value" >&2
    fail "$label did not contain $fragment"
  fi
}

assert_file_contains() {
  local path=$1
  local fragment=$2
  local label=$3
  if [[ ! -f $path ]] || ! grep -Fq -- "$fragment" "$path"; then
    [[ -f $path ]] && sed -n '1,200p' "$path" >&2
    fail "$label did not contain $fragment"
  fi
}

wait_for_file() {
  local path=$1
  local attempts=${2:-100}
  local index
  for ((index = 0; index < attempts; index++)); do
    if [[ -s $path ]]; then
      return 0
    fi
    sleep 0.1
  done
  fail "timed out waiting for $path"
}

wait_for_pattern() {
  local path=$1
  local fragment=$2
  local attempts=${3:-100}
  local index
  for ((index = 0; index < attempts; index++)); do
    if [[ -f $path ]] && grep -Fq -- "$fragment" "$path"; then
      return 0
    fi
    sleep 0.1
  done
  fail "timed out waiting for $fragment in $path"
}

wait_for_process_exit() {
  local pid=$1
  local attempts=${2:-100}
  local index
  for ((index = 0; index < attempts; index++)); do
    if ! kill -0 "$pid" 2>/dev/null; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

terminate_and_reap() {
  local pid=$1
  kill "$pid" 2>/dev/null || true
  if ! wait_for_process_exit "$pid" 50; then
    kill -KILL "$pid" 2>/dev/null || true
    wait_for_process_exit "$pid" 20 || true
  fi
  wait "$pid" 2>/dev/null || true
}

file_mode() {
  local path=$1
  if stat -c '%a' "$path" >/dev/null 2>&1; then
    stat -c '%a' "$path"
  else
    stat -f '%Lp' "$path"
  fi
}

provider_request_count() {
  local scenario=$1
  if [[ ! -f $PROVIDER_LOG ]]; then
    printf '0\n'
    return
  fi
  grep -F '"event":"request"' "$PROVIDER_LOG" | grep -F "\"scenario\":\"$scenario\"" | wc -l | tr -d ' '
}

assert_no_secret_in_process_arguments() {
  command -v ps >/dev/null 2>&1 || fail 'ps is required for process-argument inspection'
  local process_snapshot
  process_snapshot=$(ps -axo command= 2>/dev/null) || fail 'could not inspect process arguments'
  [[ $process_snapshot != *"$CURSOR_CLI_BYOK_E2E_PROVIDER_KEY"* ]] || fail 'provider API key leaked to process arguments'
}

assert_no_secret_in_artifacts() {
  local artifact
  while IFS= read -r -d '' artifact; do
    if grep -Fq -- "$CURSOR_CLI_BYOK_E2E_PROVIDER_KEY" "$artifact"; then
      fail "provider API key leaked to E2E artifact: ${artifact#"$E2E_ROOT"/}"
    fi
  done < <(find "$E2E_ROOT" -path "$E2E_ROOT/go-build-cache" -prune -o -type f -print0)
}

run_interactive_smoke() {
  local transcript=$E2E_ROOT/interactive.out
  local interactive_status
  command -v script >/dev/null 2>&1 || fail 'script is required for the interactive PTY smoke'

  set +e
  if [[ $(uname -s) == Darwin ]]; then
    { sleep 5; printf 'a'; sleep 10; printf '\003'; sleep 1; printf '\003'; } |
      script -q "$transcript" "$BYOK" --workspace "$WORKSPACE" --model responses-e2e E2E_TEXT >/dev/null 2>&1
    interactive_status=$?
  else
    local interactive_command
    printf -v interactive_command '%q ' "$BYOK" --workspace "$WORKSPACE" --model responses-e2e E2E_TEXT
    { sleep 5; printf 'a'; sleep 10; printf '\003'; sleep 1; printf '\003'; } |
      script -qefc "$interactive_command" "$transcript" >/dev/null 2>&1
    interactive_status=$?
  fi
  set -e

  if [[ ! -f $transcript ]] || ! grep -aFq -- 'E2E_RESPONSES_OK' "$transcript"; then
    [[ -f $transcript ]] && tail -c 12000 "$transcript" >&2
    fail "interactive PTY smoke failed with status $interactive_status"
  fi
  case $interactive_status in
    0|130) ;;
    *) fail "interactive PTY smoke exited with unexpected status $interactive_status" ;;
  esac
}

if [[ -z $CURSOR_AGENT_PATH ]]; then
  CURSOR_AGENT_PATH=$(command -v cursor-agent || true)
fi
[[ -n $CURSOR_AGENT_PATH ]] || fail 'official cursor-agent is not installed; set CURSOR_AGENT=/absolute/path'
[[ $CURSOR_AGENT_PATH = /* ]] || fail 'CURSOR_AGENT must resolve to an absolute path'
[[ -x $CURSOR_AGENT_PATH ]] || fail "cursor-agent is not executable: $CURSOR_AGENT_PATH"
if ! CURSOR_AGENT_VERSION=$("$CURSOR_AGENT_PATH" --version 2>/dev/null); then
  fail 'could not read the official cursor-agent version'
fi
if ! version_error=$(check_expected_cursor_version "$CURSOR_AGENT_VERSION" "${E2E_EXPECT_CURSOR_VERSION:-}" 2>&1); then
  fail "$version_error"
fi

if [[ ${E2E_REQUIRE_LINUX:-0} == 1 ]] && [[ $(uname -s) != Linux ]]; then
  fail 'E2E_REQUIRE_LINUX=1 but the host is not Linux'
fi

TMP_PARENT=${TMPDIR:-/tmp}
E2E_ROOT=$(mktemp -d "$TMP_PARENT/cursor-cli-byok-e2e.XXXXXX")
HOME_DIR=$E2E_ROOT/home
BIN_DIR=$E2E_ROOT/bin
WORKSPACE=$E2E_ROOT/workspace
PROVIDER_READY=$E2E_ROOT/provider.url
PROVIDER_LOG=$E2E_ROOT/provider.jsonl
PROVIDER_OUTPUT=$E2E_ROOT/provider.out
BYOK=$BIN_DIR/cursor-cli-byok
HELPER=$BIN_DIR/cursor-cli-byok-e2e
PROVIDER_PID=
ACTIVE_PIDS=

cleanup() {
  local code=$?
  trap - EXIT INT TERM
  for pid in $ACTIVE_PIDS; do
    terminate_and_reap "$pid"
  done
  if [[ -x $BYOK ]]; then
    "$BYOK" stop >/dev/null 2>&1 || true
  fi
  if [[ -n $PROVIDER_PID ]]; then
    terminate_and_reap "$PROVIDER_PID"
  fi
  if [[ $KEEP_TMP == 1 || $code -ne 0 ]]; then
    printf 'E2E artifacts: %s\n' "$E2E_ROOT" >&2
  else
    case $E2E_ROOT in
      "$TMP_PARENT"/cursor-cli-byok-e2e.*) rm -r "$E2E_ROOT" ;;
      *) printf 'Refusing to remove unexpected E2E path: %s\n' "$E2E_ROOT" >&2 ;;
    esac
  fi
  exit "$code"
}
trap cleanup EXIT INT TERM

mkdir -p "$HOME_DIR" "$BIN_DIR" "$WORKSPACE"
cp "$ROOT_DIR/test/e2e/fixtures/read.txt" "$WORKSPACE/read.txt"
ln -s "$CURSOR_AGENT_PATH" "$BIN_DIR/cursor-agent"

if [[ -n $PREBUILT_BYOK || -n $PREBUILT_HELPER ]]; then
  [[ -n $PREBUILT_BYOK && -n $PREBUILT_HELPER ]] || fail 'E2E_BYOK_BINARY and E2E_HELPER_BINARY must be set together'
  [[ $PREBUILT_BYOK = /* && -x $PREBUILT_BYOK ]] || fail 'E2E_BYOK_BINARY must be an absolute executable path'
  [[ $PREBUILT_HELPER = /* && -x $PREBUILT_HELPER ]] || fail 'E2E_HELPER_BINARY must be an absolute executable path'
  note 'copying prebuilt acceptance binaries'
  cp "$PREBUILT_BYOK" "$BYOK"
  cp "$PREBUILT_HELPER" "$HELPER"
  chmod 0755 "$BYOK" "$HELPER"
else
  note 'building isolated binaries'
  GO_MODULE_CACHE=$(env GOTOOLCHAIN=local GOENV=off GOWORK=off GOFLAGS= go env GOMODCACHE)
  [[ $GO_MODULE_CACHE = /* ]] || fail 'Go module cache must resolve to an absolute path'
  (
    cd "$ROOT_DIR"
    GOTOOLCHAIN=local GOENV=off GOWORK=off GOFLAGS= GOPROXY=off CGO_ENABLED=0 \
      GOMODCACHE="$GO_MODULE_CACHE" GOCACHE="$E2E_ROOT/go-build-cache" go build -trimpath \
      -ldflags '-s -w -X github.com/shiqkuangsan/cursor-cli-byok/internal/buildinfo.Version=e2e' \
      -o "$BYOK" ./cmd/cursor-cli-byok
    GOTOOLCHAIN=local GOENV=off GOWORK=off GOFLAGS= GOPROXY=off CGO_ENABLED=0 \
      GOMODCACHE="$GO_MODULE_CACHE" GOCACHE="$E2E_ROOT/go-build-cache" go build -trimpath -o "$HELPER" ./test/e2e
  )
fi

export HOME=$HOME_DIR
export XDG_CONFIG_HOME=$E2E_ROOT/xdg/config
export XDG_DATA_HOME=$E2E_ROOT/xdg/data
export XDG_STATE_HOME=$E2E_ROOT/xdg/state
export PATH=$BIN_DIR:$ORIGINAL_PATH
export CURSOR_CLI_BYOK_E2E_PROVIDER_KEY='local-e2e-provider-key'
export NO_OPEN_BROWSER=1
export npm_config_cache=$E2E_ROOT/npm-cache
export npm_config_offline=true
export npm_config_update_notifier=false

"$HELPER" provider \
  --api-key-env CURSOR_CLI_BYOK_E2E_PROVIDER_KEY \
  --workspace "$WORKSPACE" \
  --log-file "$PROVIDER_LOG" \
  --ready-file "$PROVIDER_READY" \
  >"$PROVIDER_OUTPUT" 2>&1 &
PROVIDER_PID=$!
wait_for_file "$PROVIDER_READY"
PROVIDER_URL=$(tr -d '\r\n' <"$PROVIDER_READY")
[[ $PROVIDER_URL == http://127.0.0.1:* ]] || fail "provider is not loopback-only: $PROVIDER_URL"

note 'creating isolated BYOK configuration'
"$BYOK" config init --non-interactive \
  --name responses-e2e \
  --base-url "$PROVIDER_URL" \
  --endpoint /v1/responses \
  --upstream-model responses-upstream \
  --api-key-env CURSOR_CLI_BYOK_E2E_PROVIDER_KEY >/dev/null
"$BYOK" config add --non-interactive \
  --name chat-e2e \
  --base-url "$PROVIDER_URL" \
  --endpoint /v1/chat/completions \
  --upstream-model chat-upstream \
  --api-key-env CURSOR_CLI_BYOK_E2E_PROVIDER_KEY >/dev/null

mkdir -p "$HOME/.cursor"
escaped_helper=$(printf '%s' "$HELPER" | sed 's/\\/\\\\/g; s/"/\\"/g')
cat >"$HOME/.cursor/mcp.json" <<EOF
{
  "mcpServers": {
    "weather-server": {
      "command": "$escaped_helper",
      "args": ["mcp"]
    }
  }
}
EOF
chmod 600 "$HOME/.cursor/mcp.json"

run_byok() {
	cd "$WORKSPACE"
	exec "$BYOK" --workspace "$WORKSPACE" "$@"
}

note 'checking doctor and model discovery without Cursor login or IDE state'
doctor_output=$("$BYOK" doctor 2>&1)
assert_contains "$doctor_output" 'doctor: ok' 'doctor output'
models_output=$(run_byok --list-models 2>&1)
assert_contains "$models_output" 'responses-e2e' 'model list'
assert_contains "$models_output" 'chat-e2e' 'model list'

note 'checking real interactive PTY transport'
run_interactive_smoke

status_output=$("$BYOK" status 2>&1)
assert_contains "$status_output" 'daemon: running' 'daemon status'
assert_contains "$status_output" 'endpoint: https://127.0.0.1:' 'daemon status'
STATE_FILE=$XDG_STATE_HOME/cursor-cli-byok/daemon.json
CONFIG_FILE=$XDG_CONFIG_HOME/cursor-cli-byok/config.yaml
[[ $(file_mode "$STATE_FILE") == 600 ]] || fail 'daemon state mode is not 0600'
[[ $(file_mode "$CONFIG_FILE") == 600 ]] || fail 'configuration mode is not 0600'
[[ $(file_mode "$(dirname "$STATE_FILE")") == 700 ]] || fail 'daemon state directory mode is not 0700'

note 'checking headless text, JSON, and stream JSON contracts'
if ! (run_byok --model responses-e2e --trust -p E2E_TEXT) >"$E2E_ROOT/headless-text.out" 2>"$E2E_ROOT/headless-text.err"; then
  sed -n '1,200p' "$E2E_ROOT/headless-text.err" >&2
  fail 'Responses text output failed'
fi
assert_file_contains "$E2E_ROOT/headless-text.out" 'E2E_RESPONSES_OK' 'Responses text stdout'

if ! (run_byok --model responses-e2e --trust -p --output-format json E2E_JSON) >"$E2E_ROOT/headless-json.out" 2>"$E2E_ROOT/headless-json.err"; then
  sed -n '1,200p' "$E2E_ROOT/headless-json.err" >&2
  fail 'Responses JSON output failed'
fi
if ! "$HELPER" output-check --format json --expected E2E_JSON_OK <"$E2E_ROOT/headless-json.out"; then
  sed -n '1,200p' "$E2E_ROOT/headless-json.err" >&2
  fail 'Responses JSON stdout failed validation'
fi
[[ $(provider_request_count E2E_JSON) == 1 ]] || fail 'Responses JSON request was not dispatched exactly once'

if ! (run_byok --model responses-e2e --trust -p --output-format stream-json --stream-partial-output E2E_TEXT) >"$E2E_ROOT/headless-stream.out" 2>"$E2E_ROOT/headless-stream.err"; then
  sed -n '1,200p' "$E2E_ROOT/headless-stream.err" >&2
  fail 'Responses stream JSON output failed'
fi
if ! "$HELPER" output-check --format stream-json --expected E2E_RESPONSES_OK <"$E2E_ROOT/headless-stream.out"; then
  sed -n '1,200p' "$E2E_ROOT/headless-stream.err" >&2
  fail 'Responses stream JSON stdout failed validation'
fi

chat_output=$(run_byok --model chat-e2e --trust --print E2E_TEXT 2>&1)
assert_contains "$chat_output" 'E2E_CHAT_OK' 'Chat stream'

note 'checking real Read and Write continuation'
read_output=$(run_byok --model responses-e2e --force --trust --print E2E_READ 2>&1)
assert_contains "$read_output" 'E2E_READ_OK' 'Read continuation'
write_output=$(run_byok --model responses-e2e --force --trust --print E2E_WRITE 2>&1)
assert_contains "$write_output" 'E2E_WRITE_OK' 'Write continuation'
cmp -s "$WORKSPACE/written.txt" "$ROOT_DIR/test/e2e/fixtures/write.txt" || fail 'Write tool did not create exact fixture contents'

note 'checking streamed Shell continuation and side-effect de-duplication'
shell_output=$(run_byok --model chat-e2e --force --trust --print E2E_SHELL 2>&1)
assert_contains "$shell_output" 'E2E_SHELL_OK' 'Shell continuation'
[[ -f $WORKSPACE/shell-count.txt ]] || fail 'Shell side-effect file is missing'
[[ $(wc -l <"$WORKSPACE/shell-count.txt" | tr -d ' ') == 1 ]] || fail 'Shell command executed more than once'
[[ $(cat "$WORKSPACE/shell-count.txt") == once ]] || fail 'Shell side-effect content is incorrect'

note 'checking dynamic stdio MCP discovery and continuation'
mcp_list=$(run_byok mcp list 2>&1)
assert_contains "$mcp_list" 'weather-server' 'MCP list'
mcp_output=$(run_byok --model responses-e2e --approve-mcps --force --trust --print E2E_MCP 2>&1)
assert_contains "$mcp_output" 'E2E_MCP_OK' 'MCP continuation'

note 'checking concurrent conversation isolation'
run_byok --model responses-e2e --trust --print E2E_CONCURRENT_A >"$E2E_ROOT/concurrent-a.out" 2>&1 &
pid_a=$!
run_byok --model chat-e2e --trust --print E2E_CONCURRENT_B >"$E2E_ROOT/concurrent-b.out" 2>&1 &
pid_b=$!
ACTIVE_PIDS="$pid_a $pid_b"
wait "$pid_a"
wait "$pid_b"
ACTIVE_PIDS=
assert_file_contains "$E2E_ROOT/concurrent-a.out" 'E2E_CONCURRENT_A_OK' 'concurrent Responses output'
assert_file_contains "$E2E_ROOT/concurrent-b.out" 'E2E_CONCURRENT_B_OK' 'concurrent Chat output'

note 'checking cancellation propagation'
run_byok --model responses-e2e --trust --print E2E_CANCEL >"$E2E_ROOT/cancel.out" 2>&1 &
cancel_pid=$!
ACTIVE_PIDS=$cancel_pid
wait_for_pattern "$PROVIDER_LOG" '"scenario":"E2E_CANCEL"'
assert_no_secret_in_process_arguments
kill -INT "$cancel_pid"
if ! wait_for_process_exit "$cancel_pid" 100; then
  kill -KILL "$cancel_pid" 2>/dev/null || true
  fail 'canceled wrapper did not exit'
fi
set +e
wait "$cancel_pid"
cancel_code=$?
set -e
ACTIVE_PIDS=
[[ $cancel_code -ne 0 ]] || fail 'canceled wrapper exited successfully'
wait_for_pattern "$PROVIDER_LOG" '"event":"canceled"'

note 'checking provider failure remains fail-closed'
run_byok --model responses-e2e --trust -p --output-format stream-json E2E_FAIL >"$E2E_ROOT/fail.out" 2>"$E2E_ROOT/fail.err" &
fail_pid=$!
ACTIVE_PIDS=$fail_pid
wait_for_pattern "$PROVIDER_LOG" '"scenario":"E2E_FAIL"'
if ! wait_for_process_exit "$fail_pid" 300; then
  kill -INT "$fail_pid" 2>/dev/null || true
  fail 'provider failure did not terminate the wrapper within 30 seconds'
fi
set +e
wait "$fail_pid"
fail_code=$?
set -e
ACTIVE_PIDS=
fail_output=$(cat "$E2E_ROOT/fail.out")
[[ $fail_code -ne 0 ]] || fail 'provider failure returned a successful wrapper exit code'
[[ $fail_output != *E2E_RESPONSES_OK* ]] || fail 'provider failure produced a success marker'
if ! "$HELPER" output-check --format stream-json --reject-success <"$E2E_ROOT/fail.out"; then
  sed -n '1,200p' "$E2E_ROOT/fail.err" >&2
  fail 'provider failure machine stdout failed validation'
fi
[[ $(provider_request_count E2E_FAIL) == 1 ]] || fail 'pre-tool provider failure was re-dispatched after Cursor reconnect'

note 'checking post-tool failure never repeats a side effect'
run_byok --model responses-e2e --force --trust --print E2E_SHELL_FAIL >"$E2E_ROOT/shell-fail.out" 2>&1 &
shell_fail_pid=$!
ACTIVE_PIDS=$shell_fail_pid
wait_for_pattern "$PROVIDER_LOG" '"scenario":"E2E_SHELL_FAIL"'
if ! wait_for_process_exit "$shell_fail_pid" 300; then
  kill -INT "$shell_fail_pid" 2>/dev/null || true
  fail 'post-tool provider failure did not terminate the wrapper within 30 seconds'
fi
set +e
wait "$shell_fail_pid"
shell_fail_code=$?
set -e
ACTIVE_PIDS=
[[ $shell_fail_code -ne 0 ]] || fail 'post-tool provider failure returned success'
[[ -f $WORKSPACE/shell-fail-count.txt ]] || fail 'post-tool Shell side-effect file is missing'
[[ $(wc -l <"$WORKSPACE/shell-fail-count.txt" | tr -d ' ') == 1 ]] || fail 'post-tool Shell command executed more than once'
[[ $(provider_request_count E2E_SHELL_FAIL) == 2 ]] || fail 'post-tool provider turn was re-dispatched after Cursor reconnect'

note 'checking secret and process-argument hygiene'
assert_no_secret_in_artifacts
if command -v ps >/dev/null 2>&1; then
  provider_command=$(ps -o command= -p "$PROVIDER_PID" 2>/dev/null || true)
elif [[ -r /proc/$PROVIDER_PID/cmdline ]]; then
  provider_command=$(tr '\000' ' ' <"/proc/$PROVIDER_PID/cmdline")
else
  fail 'cannot inspect provider process arguments on this platform'
fi
[[ $provider_command != *"$CURSOR_CLI_BYOK_E2E_PROVIDER_KEY"* ]] || fail 'provider API key leaked to process arguments'
assert_contains "$provider_command" '--api-key-env CURSOR_CLI_BYOK_E2E_PROVIDER_KEY' 'provider process arguments'

"$BYOK" stop >/dev/null
stopped_output=$("$BYOK" status 2>&1)
assert_contains "$stopped_output" 'daemon: stopped' 'stopped daemon status'

note "PASS on $(uname -s)/$(uname -m) with cursor-agent $CURSOR_AGENT_VERSION"
