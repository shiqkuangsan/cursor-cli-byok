# Cursor CLI Protocol Boundary

This document records the minimal protocol surface implemented by this project.
It is an interoperability contract, not a copy of Cursor's complete schemas.

## Verified Client

The primary compatibility surface was exercised on 2026-07-11 with official
`cursor-agent` version `2026.07.09-a3815c0` in non-root Linux arm64 and x86_64
containers. Each client ran with a fresh HOME/XDG tree, the file credential
store, no Cursor IDE process, and no Cursor login. `cursor-cli-byok
--list-models` returned the configured alias and default. Real `--print` and
interactive PTY runs completed through both `/v1/responses` and
`/v1/chat/completions`; the Linux runs also covered Read, Write, streaming
Shell, dynamic stdio MCP, concurrent conversations, cancellation, reconnect
de-duplication, and fail-closed provider errors.

The prior Darwin arm64 baseline, official `cursor-agent`
`2026.07.08-0c04a8a`, additionally exercised Edit, Delete, List, Glob, and Grep
through the Cursor client. Every tool result was returned to provider
continuation. Delete and Shell acceptance used the official CLI's explicit
`--force` permission mode.

## Transport

- Loopback-only TLS on a random port, with HTTP/1.1 and HTTP/2 enabled.
- Connect unary requests use `POST` with `application/proto`; JSON is also
  accepted for diagnostics and tests.
- The official CLI sends native bidirectional Connect envelopes to
  `/agent.v1.AgentService/Run`. The first envelope is handled as soon as it is
  complete; the server does not wait for the request body to close.
- After the response end-stream envelope is flushed, the direct handler waits
  briefly for the client upload half-close. This prevents Go HTTP/2 from
  canceling a still-open Shell/MCP upload stream and causing a full Run retry.
- Direct retries are correlated by the client-provided conversation ID and
  user-message ID. A reconnect subscribes to and replays the existing session
  instead of running provider or tool side effects again. Clients without a
  user-message ID retain random per-request sessions for legacy compatibility.
- Terminal replay sessions are retained for five minutes and capped at 1024;
  active sessions are never evicted. Provider conversation history is bounded
  to 50 turns per conversation. Its LRU registry targets 256 entries, permits
  temporary overflow only while excess entries are active, and never evicts an
  active or queued conversation.
- Legacy `/aiserver.v1.BidiService/BidiAppend` plus
  `/agent.v1.AgentService/RunSSE` remains supported for compatibility. Current
  clients may use binary field 4 or legacy hex field 1 in `BidiAppendRequest`.
- `/healthz` is unauthenticated and returns only status, instance ID, and daemon
  version. Every other path requires the per-instance Bearer token from the
  mode-0600 daemon state file.
- `/cursor-cli-byok.v1.Control/ProviderEnvironment` accepts a bounded JSON POST
  from the wrapper over the same CA-pinned loopback TLS connection. It accepts
  only `api_key_env` names present in the current config, updates them in daemon
  memory, returns an empty `204` response, and never serves their values back.
- `/cursor-cli-byok.v1.Control/Shutdown` accepts an empty authenticated POST and
  gracefully cancels the owning daemon. Wrappers use it to replace a healthy
  daemon whose build version differs from the current executable.
- Unary request bodies are bounded to 4 MiB. Unknown request fields are ignored.
- Unknown procedures return HTTP 404; the server never reports an invented
  successful Agent turn.

## Model Messages

Only fields consumed by the verified CLI are emitted:

| Message | Fields |
| --- | --- |
| `AvailableModelsResponse` | `model_names` (1), `models` (2), feature model configs (4-10) |
| `AvailableModel` | name (1), default (2), agent support (5), display/server names (17/18), non-max (19), plan (22), user-added (23), short name (24), sandbox (25) |
| `GetUsableModelsResponse` | repeated `ModelDetails` (1) |
| `GetDefaultModelForCliResponse` | `ModelDetails` (1) |
| Agent `ModelDetails` | model ID (1), display ID/name/short name (3/4/5), alias (6) |

Agent run decoding accepts the current `requested_model` field (9, nested model
ID 1) and falls back to legacy `model_details` (3, nested model ID 1).

Provider URLs, upstream model names, and API keys are never placed in these
Cursor-facing model messages. The visible model ID is always the configured
local alias.

## Compatibility Procedures

Implemented structured responses:

- `/aiserver.v1.AiService/AvailableModels`
- `/aiserver.v1.AiService/GetUsableModels`
- `/aiserver.v1.AiService/GetDefaultModelForCli`
- `/aiserver.v1.DashboardService/GetMe`
- `/aiserver.v1.DashboardService/GetUserPrivacyMode`
- `/aiserver.v1.AiService/GetServerConfig`
- `/aiserver.v1.ServerConfigService/GetServerConfig`

Observed startup, plugin, marketplace, usage, logging, Statsig, and trace
procedures with empty response messages are enumerated in
`internal/server/compat.go`. Empty responses are used only where every response
field is optional/repeated and the verified CLI accepts absence.

## Agent Run And Provider Status

Implemented and verified:

- native `/agent.v1.AgentService/Run` Connect BiDi transport;
- text and thinking deltas, heartbeat, token usage, turn completion,
  cancellation, malformed-frame rejection, and bounded messages;
- OpenAI-compatible Responses and Chat Completions streaming;
- per-conversation serialized history with isolation across conversations;
- Responses/Chat fragmented function-call accumulation and bounded multi-pass
  continuation;
- Cursor client-side `Read` dispatch/result/completion with each call ID
  delivered at most once;
- client-side Write, Delete, List, Glob-via-Grep, Grep, and streamed Shell
  execution, with bounded Shell output and terminal exit/rejection handling;
- Edit as one visible provider call backed by an internal, fail-before-write
  Read -> exact replacement -> Write transaction;
- terminal sessions reject late direct or detached tool results, clear pending
  tool/MCP state, and cannot turn a canceled Edit Read result into a hidden
  Write dispatch;
- MCP definitions from Run metadata or an on-demand `McpStateExecArgs` query,
  preserving the dynamic provider-facing name, description, and JSON Schema;
- a ten-second bound on on-demand MCP discovery, after which the provider is not
  started and late MCP state results are rejected;
- MCP JSON argument <-> protobuf Value conversion and client-side stdio MCP
  result continuation.

Wrapped provider failures preserve safe standard Connect codes such as
`unauthenticated`, `resource_exhausted`, and `unavailable`, while response
details and unknown codes are reduced to a generic sanitized failure. Real-CLI
acceptance verifies that reconnects after a post-Shell provider failure replay
the terminal session: the Shell side effect occurs once and the provider turn
is not dispatched again.

The first release does not automatically redispatch a provider request after a
transport or HTTP failure. Even errors classified as retryable are returned to
the Cursor-facing session, and Cursor reconnects replay that terminal result.
This stricter at-most-once policy avoids duplicating a provider turn or local
side effect when upstream request commitment is unknowable.

The daemon starts with configured provider key environment variables. Before
each Cursor launch, the wrapper synchronizes only the selected model's resolved
environment key through the authenticated control procedure. Model resolution
reads the current in-memory override on every Run, so key rotation works when a
daemon is reused. Overrides are neither persisted nor included in formatted
diagnostics. The wrapper then removes every configured `api_key_env` name before
launching the official Cursor process. The internal
`CURSOR_CLI_BYOK_LOCK_FD` name is rejected as a provider key source.

Each model alias may also define static provider compatibility headers. The
daemon copies them into only that alias's outbound inference requests, and
`doctor` applies the same set to its inference-free provider probe. Header
values never enter Cursor-facing model messages, the official CLI environment,
or formatted/JSON diagnostics. Authentication, representation, target-host,
and hop-by-hop headers are reserved; the adapter remains authoritative for its
Bearer token, SSE `Accept`, JSON `Content-Type`, and connection behavior.

The daemon advertises the eight implemented built-ins: `Read`, `Write`,
`Edit`, `Delete`, `List`, `Glob`, `Grep`, and `Shell`. MCP tools are appended per
Run only after the Cursor client reports them; the internal `CallMcpTool`
transport name is never offered to the provider.

Linux `cursor-agent 2026.07.08-0c04a8a` can move a foreground Shell command to
background tracking after its block interval, then send a metadata-only Run
whose conversation action is `BackgroundTaskCompletionAction`. This is not a
new user turn and must not invoke the provider or repeat the tool. The direct
Run facade validates the completion task ID and returns a zero-usage
`turnEnded` frame followed by the Connect terminal frame. Returning HTTP 400 or
an empty HTTP 200 makes that CLI report a retriable error even when the Shell
and provider calls already succeeded.

## Evidence And Provenance

Field numbers were cross-checked against the public protocol research recorded
in `docs/upstream-reference.md` and sanitized real-CLI field-shape captures.
Encoding and handlers in this repository were implemented independently with
`protowire`; neither reference repository is a source, build, Git, or runtime
dependency.

Executable acceptance scenarios and the exact verified Cursor version are
recorded in `docs/compatibility.md`.
