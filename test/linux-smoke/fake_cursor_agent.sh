#!/bin/sh
set -eu

if [ "${1:-}" = "--version" ]; then
  printf '%s\n' '2026.07.08-0c04a8a'
  exit 0
fi

[ -z "${LINUX_SMOKE_PROVIDER_KEY+x}" ] || exit 90
[ -n "${CURSOR_AUTH_TOKEN:-}" ] || exit 91
case ${CURSOR_API_ENDPOINT:-} in
  https://127.0.0.1:*) ;;
  *) exit 92 ;;
esac
[ "${CURSOR_API_ENDPOINT:-}" = "${CURSOR_API_BASE_URL:-}" ] || exit 93
[ -n "${NODE_EXTRA_CA_CERTS:-}" ] && [ -r "$NODE_EXTRA_CA_CERTS" ] || exit 94
[ -n "${LINUX_SMOKE_MARKER:-}" ] || exit 95

printf '%s\n' "$@" >"$LINUX_SMOKE_MARKER"
exit 23
