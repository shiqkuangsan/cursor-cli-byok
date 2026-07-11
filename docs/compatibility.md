# Compatibility And Acceptance

`cursor-cli-byok` targets the official headless Cursor CLI. Cursor's private
protocol can change without notice, so compatibility is recorded by exact CLI
version and executable acceptance evidence rather than by a broad version
claim.

## Current Matrix

| Environment | Cursor CLI | Evidence | Status |
| --- | --- | --- | --- |
| Darwin arm64 | `2026.07.08-0c04a8a` | Full `test/e2e/run.sh` against the real CLI with a fresh HOME/XDG tree | Passed 2026-07-11 |
| Linux x86_64 | `2026.07.09-a3815c0` | Full `test/e2e/run.sh` against the official CLI as a non-root user in a `linux/amd64` Debian container | Passed 2026-07-11 |
| Linux arm64 | `2026.07.09-a3815c0` | Full `test/e2e/run.sh` against the official CLI as a non-root user in a native Debian arm64 container | Passed 2026-07-11 |
| Linux arm64 container | fake child, not Cursor | Non-root release-ELF wrapper/daemon lifecycle via `test/linux-smoke/run.sh` | Passed 2026-07-11; not protocol evidence |

Docker on the local arm64 development machine supplied both acceptance hosts.
The arm64 client ran natively; the x86_64 client ran through Docker's
`linux/amd64` platform support. Both containers used Cursor's official
installer, contained no Cursor IDE, and ran the complete gate as UID 1000.
CI and release workflows independently install the official Linux CLI and run
the same hard `make linux-e2e` gate before accepting or publishing artifacts.

The separate Linux lifecycle smoke has run in a cached Debian arm64 container
as UID 1000. It verifies native ELF startup, daemon locking and TLS state,
selected provider-key isolation from the child, argument and exit-code
preservation, and `status`/`stop`; its fake child deliberately proves nothing
about Cursor's private protocol.

## Acceptance Coverage

The E2E runner creates an isolated HOME, XDG configuration, workspace, daemon,
provider, and MCP server. It does not use an existing Cursor login or Cursor
IDE state. It verifies:

- `doctor`, `status`, `stop`, and real `--list-models` discovery;
- a real PTY interactive run, including fresh-workspace trust and clean
  double-Ctrl+C exit, in addition to non-interactive `--print`;
- Responses and Chat Completions streaming;
- real Cursor-side Read, Write, Shell, and dynamic stdio MCP execution;
- multi-pass tool-result continuation and concurrent conversation isolation;
- signal cancellation reaching the provider request;
- terminal-session rejection of late tool results, including an Edit Read
  result that must not dispatch its hidden Write after cancellation;
- Shell side effects executing once when a post-tool provider failure causes
  the official CLI to reconnect;
- loopback-only daemon/provider endpoints and mode-0600 state/config files;
- provider API keys remaining available to the daemon while absent from the
  official Cursor process, Shell tool environment, logs, config values, and
  process arguments;
- provider failure returning nonzero without falling back to Cursor inference.

The Go integration suite additionally verifies authenticated, CA-pinned key
rotation against a reused daemon: one provider turn uses the original process
environment, the wrapper updates the selected key in memory, and the next
conversation uses the rotated value without restarting the service.

Stop lifecycle coverage also models a daemon that has removed its ownership
state before the operating system reaps its PID. The command treats that
daemon-owned state transition as completion instead of reporting a false
timeout. Stale cleanup acquires the daemon startup lock and rechecks instance
ownership before removing state, preserving replacements published before or
during cleanup. The final Linux x86_64 E2E rerun exercised the PID-reaping
behavior in a Docker container without an init shim.

The interactive gate uses the host's `script` implementation: BSD `script` on
Darwin and util-linux `script` on Linux. Both official Linux client runs used
the util-linux implementation and completed a real interactive PTY session.

Run the local compatibility suite:

```sh
make e2e
```

Require an actual Linux host:

```sh
make linux-e2e
```

To test already-built artifacts instead of rebuilding inside the runner, set
both absolute executable paths:

```sh
E2E_BYOK_BINARY=/path/to/cursor-cli-byok-linux-amd64 \
E2E_HELPER_BINARY=/path/to/cursor-cli-byok-e2e-linux-amd64 \
E2E_REQUIRE_LINUX=1 ./test/e2e/run.sh
```

Set `E2E_KEEP_TMP=1` to retain sanitized failure artifacts. Provider audit logs
contain scenario names, endpoint type, request sequence, and status only; they
do not contain request bodies, tool outputs, or authorization values.

## Goal Completion Audit

Last audited: 2026-07-11. A `Proved` row has direct current-worktree evidence;
`Partial` or `Missing` rows cannot support a completion claim.

| Requirement | Authoritative evidence | State |
| --- | --- | --- |
| Independent project with tracked prior art | `AGENTS.md`, `README.md`, and `docs/upstream-reference.md`; module and filesystem inspection show no source/build/runtime dependency on either reference project | Proved |
| One pure-Go release binary | `Makefile` builds one `cmd/cursor-cli-byok`; current amd64 and arm64 artifacts are statically linked ELF files and their SHA-256 manifest verifies | Proved |
| Explicit wrapper plus shared on-demand daemon | Wrapper, lock/state manager, authenticated loopback TLS service, idle shutdown, version-aware replacement, and real-CLI `status`/`stop` E2E | Proved |
| No Cursor IDE or Cursor login | Official Linux arm64/x86_64 runs used isolated HOME/XDG trees, the file credential store, and containers containing only the CLI | Proved |
| Custom Responses and Chat Completions streaming | Fake-provider contracts plus official Linux Cursor print and PTY runs through both configured endpoint types | Proved |
| Multi-pass built-in and MCP tools | Unit/race coverage for all eight built-ins and terminal late-result rejection; real Darwin all-tool coverage; official Linux Read/Write/Shell/dynamic-MCP continuation, duplicate side-effect checks, and bounded MCP discovery | Proved |
| Secure config and secret handling | Atomic mode-0600 config/state, mode-0700 directories, remote-HTTP rejection, CA-pinned controls, in-memory key rotation, child-env stripping, and E2E log/argv/process checks | Proved |
| `doctor`, `status`, and `stop` | Command tests plus isolated real-CLI E2E, including version reporting, authenticated daemon lifecycle, state-before-PID-reaping completion, and lock-guarded replacement-state preservation across both cleanup race windows | Proved |
| Upstream maintenance documentation | Independence policy, reviewed commit ledger, protocol boundary, and compatibility/version policy are present and current | Proved |
| One-command non-root install and release gates | Five installer scenarios, checksum-preserving atomic replacement, workflow structure tests, tagged amd64/arm64 lifecycle smokes, and tagged amd64 official Linux E2E before upload | Proved |
| Official Cursor CLI on Linux VPS-equivalent host | Complete `E2E_REQUIRE_LINUX=1` runs on arm64 and x86_64 with fresh HOME/XDG, no IDE/login, both endpoints, PTY/print/tools/MCP/concurrency/cancellation/fail-closed assertions | Proved |

The official runs ended with `E2E: PASS on Linux/aarch64 with cursor-agent
2026.07.09-a3815c0` and `E2E: PASS on Linux/x86_64 with cursor-agent
2026.07.09-a3815c0`. The committed acceptance runner contains the assertions;
temporary provider audits retain scenario metadata only and are not source
fixtures.

## Version Policy

An unlisted Cursor CLI version is unverified, not automatically incompatible.
Run the E2E gate before adding it to the matrix. A protocol mismatch must fail
closed and record the affected procedure and exact Cursor version. Updating the
matrix requires updating `docs/upstream-reference.md` when public prior-art
schemas or behavior were consulted. The ordered
`internal/cursorcli/verified_versions.txt` manifest must exactly match the Go
verified-version set. CI and release gates compare the official installer's
actual version with the manifest's latest entry before running acceptance; a
new upstream version therefore requires an explicit compatibility review.
