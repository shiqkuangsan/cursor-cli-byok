# Cursor CLI BYOK Design

## Objective

Build an independent Linux tool named `cursor-cli-byok` that runs the official
Cursor CLI against user-supplied OpenAI-compatible APIs. It must not require
Cursor IDE, a Cursor login, a graphical environment, MITM, or system proxy
changes.

The first release supports both OpenAI Responses (`/v1/responses`) and Chat
Completions (`/v1/chat/completions`). The project is not a fork of either prior
art repository and has no source, build, Git, or runtime dependency on them.

## Confirmed Feasibility

Cursor CLI `2026.07.08-0c04a8a` was run without a Cursor IDE process and pointed
at an isolated loopback endpoint with its hidden `-e` option. It sent requests
for privacy settings, available models, usable models, and the default CLI
model to that endpoint. This proves the CLI needs a compatible Cursor protocol
server, not Cursor IDE itself.

## Architecture

Ship one pure-Go executable with two cooperating modes:

```text
cursor-cli-byok (wrapper)
  -> starts or reuses cursor-cli-byok serve
  -> injects local endpoint and mock auth
  -> launches official cursor-agent with unchanged TTY and arguments

cursor-cli-byok serve (on-demand daemon)
  -> loopback TLS HTTP/2 + Connect-RPC facade
  -> Cursor auth/profile/model compatibility endpoints
  -> Cursor Agent request and tool-result bridge
  -> OpenAI Responses or Chat Completions streaming adapter
  -> configured custom URL and API key
```

The daemon starts on demand, supports concurrent isolated conversations, and
exits after ten idle minutes. It binds only to a random loopback port. A file
lock prevents duplicate startup and a mode-0600 state file publishes the PID,
port, certificate path, and daemon version to wrappers.

## Commands

The official `cursor-agent` remains untouched. BYOK is always explicit:

```text
cursor-cli-byok [cursor-agent arguments...]
cursor-cli-byok serve
cursor-cli-byok config init|add|list|use|remove
cursor-cli-byok doctor
cursor-cli-byok status
cursor-cli-byok stop
```

Unknown/default arguments pass through to `cursor-agent`. The wrapper preserves
stdin, stdout, stderr, TTY behavior, signals, and the child exit code.

## Configuration

Use XDG locations:

```text
~/.config/cursor-cli-byok/config.yaml
~/.local/share/cursor-cli-byok/
~/.local/state/cursor-cli-byok/
```

Configuration is model-channel based:

```yaml
version: 1
default_model: relay-gpt
models:
  - name: relay-gpt
    protocol: openai
    base_url: https://example.com
    endpoint: /v1/responses
    api_key_env: RELAY_API_KEY
    upstream_model: gpt-5.4
```

`api_key_env` is preferred. Inline `api_key` is allowed only in a mode-0600
configuration file under a mode-0700 directory. Configuration reloads before
each new agent turn.

## Cursor Protocol Boundary

Implement only the protocol surface exercised by Cursor CLI. The first
compatibility set includes:

- auth, privacy, account, and telemetry-safe mock endpoints;
- `AvailableModels`, `GetUsableModels`, and `GetDefaultModelForCli`;
- `/agent.v1.AgentService/Run` server streaming;
- user messages, resume/cancel actions, model selection, and checkpoints;
- text/reasoning deltas and turn completion;
- client-side read, write, edit, delete, list, glob, grep, shell, and MCP tools;
- tool results, multi-turn continuation, cancellation, and heartbeats.

Minimal protobuf schemas are maintained locally and generated into this
repository. Public schemas and behavior in the prior art repositories may be
used as protocol references, but runtime code is independently implemented.
Unknown protobuf fields remain forward-compatible.

## Provider Boundary

Both adapters expose one canonical internal event stream:

```text
text delta | reasoning delta | tool-call delta | usage | terminal error
```

The Responses adapter handles response text, reasoning summaries, function
calls, tool outputs, and usage events. The Chat Completions adapter handles
assistant deltas, reasoning-compatible fields, streamed tool calls, finish
reasons, and usage. Provider model names are mapped from local model aliases.

Provider requests are not automatically redispatched after a transport or HTTP
failure. Cursor reconnects replay the same terminal session instead of invoking
the provider again. A tool call ID is delivered at most once to avoid duplicate
shell commands or file writes.

## Security And Failure Behavior

The product fails closed. It never silently falls back to Cursor-hosted model
inference. Provider authentication, request bodies, and tool outputs are
redacted from normal logs.

- Missing Cursor CLI: show the official installation command.
- Invalid configuration: fail before daemon startup with a field-level error.
- Provider 401/403: classify as authentication/permission failure; no retry.
- Provider 404: distinguish URL, endpoint, and model errors when possible.
- Provider 429: classify as resource exhaustion and fail closed without an
  automatic redispatch.
- Provider 5xx or stream interruption: fail closed and replay the terminal
  result to Cursor reconnects without repeating provider or tool work.
- Protocol mismatch: report CLI version and failing procedure; never fake
  successful agent completion.

Cursor CLI versions are observed at startup. Untested newer versions warn and
remain usable until a concrete protocol mismatch is detected.

## Prior Art And Independence

Publicly acknowledge and track these technical references:

- `shiqkuangsan/cursor-agent-byok`: CLI endpoint override, TLS facade, mock
  authentication, model discovery, and observed Run-to-stream behavior.
- `leookun/cursor-byok`: extracted protocol schemas, agent/tool lifecycle,
  provider adapters, and stream compatibility behavior.

`README.md` states project independence and attribution. `AGENTS.md` defines
the maintenance policy. `docs/upstream-reference.md` records the last reviewed
commit, reference area, local equivalent, and follow-up status. Upstream
changes are reviewed selectively and never merged wholesale.

Both reference repositories currently use the MIT License. If any substantial
code is ever intentionally ported rather than independently implemented, its
source and required license notice must be recorded before merging.

## Verification

Testing has four layers:

- unit tests for configuration, paths, permissions, SSE parsing, and dedup;
- golden protocol tests using sanitized Cursor CLI protobuf fixtures;
- provider contract tests against local fake Responses and Chat servers;
- Linux end-to-end tests using the real Cursor CLI without Cursor IDE.

The MVP is accepted only when interactive and print modes work, core file and
shell tools complete multi-turn calls, MCP calls work, concurrent conversations
do not cross state, secrets remain protected, the daemon stays loopback-only,
and provider failure never falls back to Cursor inference.

The first release target is `linux/amd64`, with a pure-Go design that allows a
later `linux/arm64` build without architectural changes.
