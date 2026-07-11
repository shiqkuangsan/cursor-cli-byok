# Cursor CLI BYOK Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a standalone Go CLI and on-demand daemon that lets the official Cursor CLI use custom OpenAI Responses and Chat Completions endpoints on a headless Linux VPS without Cursor IDE or Cursor authentication.

**Architecture:** One `cursor-cli-byok` executable owns wrapper, configuration, diagnostics, and `serve` modes. The daemon exposes a loopback TLS HTTP/2 Cursor-compatible facade, translates Agent streams and tool results to a canonical conversation model, and routes that model through independently implemented OpenAI-compatible adapters.

**Tech Stack:** Go, standard library HTTP/TLS/process APIs, Connect-RPC, Protocol Buffers, YAML v3, Linux file locking, table-driven Go tests, fake SSE provider servers, shell installer.

**Implemented deviations:** The compatibility layer uses independently written
`protowire` encoders/decoders instead of checked-in generated schemas. Provider
errors are classified but never automatically retried; real Cursor reconnects
replay terminal sessions to preserve at-most-once provider/tool dispatch. The
release matrix includes both Linux amd64 and arm64.

---

### Task 1: Project foundation and provenance

**Files:**
- Create: `go.mod`
- Create: `cmd/cursor-cli-byok/main.go`
- Create: `internal/buildinfo/buildinfo.go`
- Create: `README.md`
- Create: `AGENTS.md`
- Create: `docs/upstream-reference.md`
- Create: `.gitignore`

**Steps:**
1. Write a smoke test that invokes the command dispatcher with `--version` and expects a stable development version string.
2. Run `go test ./...` and verify it fails because the dispatcher is absent.
3. Add the minimal module, command dispatcher, and build information package.
4. Add README independence/prior-art wording and user-facing project objective.
5. Add AGENTS maintenance rules and the upstream review ledger with the currently reviewed commits.
6. Run `gofmt -w .` and `go test ./...`; expect PASS.
7. Do not commit until the user explicitly authorizes Git operations.

### Task 2: XDG paths and secure configuration

**Files:**
- Create: `internal/paths/paths.go`
- Create: `internal/paths/paths_test.go`
- Create: `internal/config/types.go`
- Create: `internal/config/store.go`
- Create: `internal/config/store_test.go`

**Steps:**
1. Write failing table tests for XDG defaults, environment overrides, model-name uniqueness, supported endpoints, default-model validation, and API-key resolution.
2. Add permission tests requiring directory mode `0700` and config mode `0600`.
3. Run the focused tests and confirm the expected failures.
4. Implement paths, YAML loading/saving, normalization, environment-key lookup, and atomic writes.
5. Run focused tests and `go test ./...`; expect PASS.
6. Keep secrets out of validation error strings.

### Task 3: Config command UX

**Files:**
- Create: `internal/command/config.go`
- Create: `internal/command/config_test.go`
- Modify: `cmd/cursor-cli-byok/main.go`

**Steps:**
1. Write failing command tests for `config init`, `add`, `list`, `use`, and `remove` using a temporary XDG home.
2. Verify non-interactive flags work before adding interactive prompts.
3. Implement deterministic text prompts over injected stdin/stdout interfaces.
4. Ensure listings redact inline keys and show unresolved environment variables without their values.
5. Run focused and full tests; expect PASS.

### Task 4: Cursor CLI discovery and wrapper argument handling

**Files:**
- Create: `internal/cursorcli/discovery.go`
- Create: `internal/cursorcli/discovery_test.go`
- Create: `internal/cursorcli/launch.go`
- Create: `internal/cursorcli/launch_test.go`
- Create: `internal/command/root.go`

**Steps:**
1. Write failing tests for `$PATH` discovery, `~/.local/bin/cursor-agent`, version parsing, missing-binary diagnostics, argument pass-through, signal forwarding, and exit-code preservation.
2. Implement discovery without invoking a shell.
3. Implement a launch specification that adds only the local `-e`, model, mock-auth, and CA settings while preserving user arguments.
4. Use a fake child executable in tests; do not contact Cursor services.
5. Run focused and full tests; expect PASS.

### Task 5: Daemon state, locking, and lifecycle

**Files:**
- Create: `internal/daemon/state.go`
- Create: `internal/daemon/state_test.go`
- Create: `internal/daemon/lock_linux.go`
- Create: `internal/daemon/lock_test.go`
- Create: `internal/daemon/manager.go`
- Create: `internal/daemon/manager_test.go`

**Steps:**
1. Write failing tests for mode-0600 atomic state files, stale PID detection, exclusive startup, health polling, concurrent wrapper startup, and idle shutdown.
2. Implement Linux `flock`-based ownership and a versioned daemon state schema.
3. Start the daemon as the same executable with `serve --background-child` and sanitized environment.
4. Add bounded startup and shutdown timeouts.
5. Run race-enabled focused tests: `go test -race ./internal/daemon`.
6. Run the full suite; expect PASS.

### Task 6: TLS and loopback-only HTTP/2 server

**Files:**
- Create: `internal/certs/manager.go`
- Create: `internal/certs/manager_test.go`
- Create: `internal/server/server.go`
- Create: `internal/server/server_test.go`
- Create: `internal/server/health.go`

**Steps:**
1. Write failing tests for certificate generation/reuse, SANs for localhost/127.0.0.1, key-file mode `0600`, random loopback binding, HTTP/2 negotiation, and health responses.
2. Implement a local CA and server certificate using standard-library crypto APIs.
3. Reject non-loopback configured listen addresses.
4. Start and gracefully stop the server with context cancellation.
5. Run focused tests, including an HTTP/2 TLS client; expect PASS.

### Task 7: Compatibility endpoints and model discovery

**Files:**
- Create: `internal/protocol/wire.go`
- Create: `internal/protocol/wire_test.go`
- Create: `internal/server/compat.go`
- Create: `internal/server/compat_test.go`
- Create: `testdata/protocol/model_requests/README.md`

**Steps:**
1. Capture and sanitize current Cursor CLI model-discovery requests into golden fixtures without credentials or user paths.
2. Write failing tests for privacy/account mocks, `AvailableModels`, `GetUsableModels`, `GetDefaultModelForCli`, telemetry-safe responses, and unknown procedure behavior.
3. Implement only the required protobuf wire shapes, preserving unknown-field compatibility.
4. Verify `cursor-agent --list-models` against an in-process server returns configured aliases without Cursor IDE.
5. Run focused, full, and race tests; expect PASS.

### Task 8: Minimal local protocol schemas and Agent Run transport

**Files:**
- Create: `proto/agent/v1/agent.proto`
- Create: `proto/aiserver/v1/compat.proto`
- Create: `internal/gen/agentv1/*.go` (generated and committed)
- Create: `internal/gen/aiserverv1/*.go` (generated and committed)
- Create: `internal/server/agent.go`
- Create: `internal/server/agent_test.go`
- Create: `docs/protocol.md`

**Steps:**
1. Derive the smallest required schemas from sanitized wire captures and publicly documented prior art; record every supported procedure and message field in `docs/protocol.md`.
2. Generate Go types with pinned generator versions and add a reproducible `go generate` command.
3. Write failing server-stream tests for `/agent.v1.AgentService/Run`, cancellation, heartbeat, malformed frames, and Connect end-stream errors.
4. Implement the Connect server-stream transport independently.
5. Confirm unknown fields do not break decoding.
6. Run focused and full tests; expect PASS.

### Task 9: Canonical conversation and provider interfaces

**Files:**
- Create: `internal/agent/types.go`
- Create: `internal/agent/conversation.go`
- Create: `internal/agent/conversation_test.go`
- Create: `internal/provider/provider.go`
- Create: `internal/provider/events.go`
- Create: `internal/provider/fake_test.go`

**Steps:**
1. Write failing tests for user/assistant/tool history, request IDs, model mapping, concurrent conversation isolation, and cancellation.
2. Define provider-neutral messages, tools, streamed events, usage, and terminal errors.
3. Implement an in-memory conversation registry keyed by conversation ID and guarded for concurrent access.
4. Add bounded history persistence only where Cursor resume behavior requires it; avoid speculative databases.
5. Run race-enabled focused tests and the full suite; expect PASS.

### Task 10: OpenAI Responses adapter

**Files:**
- Create: `internal/provider/openai/client.go`
- Create: `internal/provider/openai/responses.go`
- Create: `internal/provider/openai/responses_test.go`
- Create: `internal/provider/openai/sse.go`
- Create: `internal/provider/openai/sse_test.go`

**Steps:**
1. Write fake-server contract tests for request URL, Bearer auth, model alias mapping, message conversion, response text deltas, reasoning deltas, function-call deltas, usage, 401/404/429/5xx, malformed SSE, and cancellation.
2. Verify every test fails before implementation.
3. Implement a bounded SSE parser and Responses request/stream conversion.
4. Implement redacted typed provider errors and pre-side-effect retry classification.
5. Run focused, full, and race tests; expect PASS.

### Task 11: OpenAI Chat Completions adapter

**Files:**
- Create: `internal/provider/openai/chat.go`
- Create: `internal/provider/openai/chat_test.go`
- Modify: `internal/provider/openai/client.go`

**Steps:**
1. Write fake-server contract tests for chat messages, streamed content, compatible reasoning fields, fragmented tool calls, finish reasons, usage, and provider errors.
2. Verify the tests fail.
3. Implement Chat Completions conversion using the shared HTTP/SSE/error infrastructure.
4. Run both OpenAI adapter suites and the full suite; expect PASS.

### Task 12: Built-in and MCP tool catalog

**Files:**
- Create: `internal/tools/catalog.go`
- Create: `internal/tools/catalog_test.go`
- Create: `internal/tools/mcp.go`
- Create: `internal/tools/mcp_test.go`
- Create: `internal/tools/schemas/*.json`

**Steps:**
1. Write failing tests for read, write, edit, delete, list, glob, grep, shell, and MCP schemas.
2. Add tests that convert Cursor MCP definitions to provider function tools without changing names or JSON Schema.
3. Implement the minimum catalog required by the Cursor CLI client execution surface.
4. Reject unsupported tools with a structured terminal error before provider dispatch.
5. Run focused and full tests; expect PASS.

### Task 13: Tool-call execution bridge and multi-turn loop

**Files:**
- Create: `internal/agent/runner.go`
- Create: `internal/agent/runner_test.go`
- Create: `internal/protocol/tools.go`
- Create: `internal/protocol/tools_test.go`
- Create: `internal/protocol/events.go`
- Create: `internal/protocol/events_test.go`

**Steps:**
1. Write failing golden tests that map canonical tool calls to Cursor interaction and exec server messages.
2. Write failing tests that map Cursor exec results back to canonical tool messages.
3. Add red-green tests proving one tool-call ID is delivered at most once across reconnects and retries.
4. Implement the provider -> Cursor tool dispatch -> Cursor result -> provider continuation loop.
5. Add cancellation, heartbeat, terminal usage, and turn-ended events.
6. Run focused tests under `-race`, then the full suite; expect PASS.

### Task 14: Operational commands

**Files:**
- Create: `internal/command/doctor.go`
- Create: `internal/command/doctor_test.go`
- Create: `internal/command/status.go`
- Create: `internal/command/status_test.go`
- Create: `internal/command/stop.go`
- Modify: `internal/command/root.go`

**Steps:**
1. Write failing tests for healthy, missing CLI, invalid config, missing environment key, stale daemon, untested CLI version, TLS failure, and provider connectivity states.
2. Implement stable human-readable output and non-zero diagnostic exit codes.
3. Ensure diagnostics never print secrets.
4. Run focused and full tests; expect PASS.

### Task 15: Installer and release artifacts

**Files:**
- Create: `scripts/install.sh`
- Create: `scripts/install_test.sh`
- Create: `Makefile`
- Create: `.github/workflows/ci.yml`
- Create: `.github/workflows/release.yml`
- Modify: `README.md`

**Steps:**
1. Write shell tests using temporary HOME/PATH directories for first install, upgrade, checksum failure, missing curl, and official Cursor CLI installation delegation.
2. Implement a non-root installer that downloads a checksummed `linux/amd64` binary to `~/.local/bin` and invokes Cursor's official installer only when needed.
3. Add reproducible build metadata and release checksums.
4. Add CI for unit, race, shell, and cross-build checks.
5. Run installer tests and `make verify`; expect PASS.

### Task 16: Linux end-to-end acceptance

**Files:**
- Create: `test/e2e/fake_openai.go`
- Create: `test/e2e/run.sh`
- Create: `test/e2e/fixtures/`
- Create: `docs/compatibility.md`

**Steps:**
1. Build an isolated fake provider supporting Responses and Chat streaming plus deterministic tool calls.
2. Run the real current Cursor CLI against `cursor-cli-byok` without Cursor IDE and verify `--list-models`, interactive transport smoke, and `-p` output.
3. Verify read, write, shell, MCP, multi-turn tool continuation, cancellation, and two concurrent conversations.
4. Assert the daemon is loopback-only, secrets are absent from logs/process arguments, and provider failure never reaches Cursor inference.
5. Record the tested Cursor CLI version and protocol fixture revision in `docs/compatibility.md` and `docs/upstream-reference.md`.
6. Run `go test -race ./...`, shell tests, `go vet ./...`, clean Linux build, and E2E; all must pass before declaring the MVP complete.
