#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
source "$ROOT_DIR/test/e2e/version.sh"

fail() {
	printf 'version check test failed: %s\n' "$*" >&2
	exit 1
}

check_expected_cursor_version '2026.07.09-a3815c0' '' || fail 'empty expectation was rejected'
check_expected_cursor_version '2026.07.09-a3815c0' '2026.07.09-a3815c0' || fail 'matching version was rejected'

set +e
mismatch_output=$(check_expected_cursor_version '2026.07.10-next' '2026.07.09-a3815c0' 2>&1)
mismatch_status=$?
empty_output=$(check_expected_cursor_version '' '2026.07.09-a3815c0' 2>&1)
empty_status=$?
set -e

[[ $mismatch_status -ne 0 ]] || fail 'mismatched version was accepted'
[[ $mismatch_output == *'expected 2026.07.09-a3815c0, got 2026.07.10-next'* ]] || fail 'mismatch diagnostic is incomplete'
[[ $empty_status -ne 0 ]] || fail 'empty actual version was accepted'
[[ $empty_output == *'version output is empty'* ]] || fail 'empty-version diagnostic is incomplete'

printf 'PASS: Cursor version gate checks\n'
