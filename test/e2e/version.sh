#!/usr/bin/env bash

check_expected_cursor_version() {
	local actual=${1:-}
	local expected=${2:-}
	if [[ -z $actual ]]; then
		printf 'Cursor CLI version output is empty\n' >&2
		return 1
	fi
	if [[ -z $expected ]]; then
		return 0
	fi
	if [[ $actual != "$expected" ]]; then
		printf 'Cursor CLI version mismatch: expected %s, got %s\n' "$expected" "$actual" >&2
		return 1
	fi
}
