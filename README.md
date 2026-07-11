# cursor-cli-byok

`cursor-cli-byok` is an independent, pure-Go wrapper that runs the official
Cursor CLI with user-supplied OpenAI-compatible endpoints on headless Linux. It
does not require Cursor IDE, a Cursor login, a graphical environment, MITM, or
system proxy changes.

It supports both OpenAI Responses (`/v1/responses`) and Chat Completions
(`/v1/chat/completions`) with a custom base URL, API key, and model. BYOK use
remains explicit through the separate
`cursor-cli-byok` command; the official `cursor-agent` installation will stay
untouched.

## Status

The runtime MVP is operational: explicit wrapper, shared on-demand daemon,
loopback TLS/HTTP2 facade, secure XDG configuration, model discovery, both
OpenAI-compatible endpoints, all eight built-in tools, streamed Shell, dynamic
MCP, and `doctor`, `status`, and `stop`. Official `cursor-agent`
`2026.07.09-a3815c0` passed the complete isolated print and interactive PTY E2E
suite as a non-root user on Linux arm64 and x86_64, without Cursor IDE or
login. A separate headless Linux arm64 VPS run with `2026.07.08-0c04a8a` also
passed against a real loopback OpenAI-compatible relay, including Responses,
Chat Completions, Read, Write, foreground-to-background Shell completion, and
an interactive PTY. The same VPS then passed three independent official-Cursor
QA runs through Sub2API `v0.1.150` and `gpt-5.6-luna`, using an explicit
alias-scoped compatibility User-Agent. The prior Darwin arm64 baseline remains
`2026.07.08-0c04a8a`. Static Linux amd64/arm64 release builds, a checksummed
installer, CI, and release automation are present. Release gates execute the
final tagged amd64 artifact against the official CLI and run both tagged
architectures through the Linux lifecycle smoke before upload; see
[docs/compatibility.md](docs/compatibility.md).

## Installation

Build and install from this source tree without modifying `cursor-agent`:

```sh
make build
install -m 0755 dist/cursor-cli-byok ~/.local/bin/cursor-cli-byok
```

After a GitHub release is published, the non-root installer downloads the
matching Linux amd64/arm64 binary, verifies it against `checksums.txt`, and
atomically installs it to `~/.local/bin`:

```sh
curl -fsSL \
  https://raw.githubusercontent.com/shiqkuangsan/cursor-cli-byok/main/scripts/install.sh \
  -o /tmp/install-cursor-cli-byok.sh
sh /tmp/install-cursor-cli-byok.sh
```

If `cursor-agent` is absent, the installer delegates to Cursor's official
installer. Suppress that step when the CLI will be managed separately:

```sh
sh /tmp/install-cursor-cli-byok.sh --skip-cursor-install
```

Select a release or installation directory explicitly with `--version v0.1.0`
and `--install-dir /absolute/path`. The installer supports Linux amd64 and
arm64, requires no root privileges, and never replaces an existing binary when
checksum verification fails.

Run the development command directly:

```sh
go run ./cmd/cursor-cli-byok --version
go run ./cmd/cursor-cli-byok -v
```

Build and invoke BYOK explicitly while leaving the official CLI untouched:

```sh
go build -o cursor-cli-byok ./cmd/cursor-cli-byok

./cursor-cli-byok --list-models
./cursor-cli-byok --trust --print 'Summarize this repository.'
```

Use the official CLI's explicit approval flags when an automated run is
expected to perform high-risk local actions:

```sh
./cursor-cli-byok --force --trust --print 'Run the requested shell command.'
./cursor-cli-byok --approve-mcps --force --trust --print 'Use the configured MCP tool.'
```

`--trust` trusts the workspace. `--force` (official alias: `--yolo`) approves
commands and destructive file operations unless explicitly denied. MCP servers
remain configured and executed by the official Cursor CLI through
`~/.cursor/mcp.json`; this project discovers their original names and JSON
Schemas at run time and does not proxy them through a separate MCP service.

`cursor-agent` must be available on `PATH` or at `~/.local/bin/cursor-agent`.
The wrapper starts or reuses its own daemon, injects only the loopback endpoint,
local authentication token, and CA, then preserves the official CLI arguments,
stdio, signals, and exit code.

The wrapper records the tested Cursor CLI version. A different valid version is
allowed to run with an explicit warning so an upstream release does not become
an artificial hard failure. When the `cursor-cli-byok` binary is upgraded, a
healthy older daemon is stopped through its authenticated control endpoint and
replaced under the same startup lock.

Executable acceptance versions are listed in
`internal/cursorcli/verified_versions.txt`. CI and release acceptance require
the version installed by Cursor's official installer to equal the latest entry;
local E2E can omit that expectation when evaluating a new upstream version.

At each explicit invocation, the wrapper resolves only the selected model's
`api_key_env` value and sends it to the reused daemon through an authenticated,
CA-pinned loopback TLS control request. The daemon keeps the override in memory,
so rotating the environment value does not require a daemon restart. The
control response is empty, and the key is never written to daemon state or
logs. Every configured provider key environment variable is removed from the
official Cursor CLI environment before launch, so Cursor tools and Shell
commands cannot inherit it.

Check the headless setup without starting the daemon, inspect a running daemon,
or stop it explicitly:

```sh
go run ./cmd/cursor-cli-byok doctor
go run ./cmd/cursor-cli-byok status
go run ./cmd/cursor-cli-byok stop
```

`doctor` validates the default model configuration and API key availability,
checks the official `cursor-agent` version, probes the configured provider
endpoint, and inspects daemon health. The probe starts with a body-free `HEAD`.
If an otherwise compatible relay returns 404 for `HEAD`, `doctor` sends one
empty JSON POST with no model or input and accepts only an invalid-request
response as route evidence; it cannot trigger inference. Diagnostics remain
sanitized and never print the provider URL or API key.

## Configuration

Interactive setup creates the first model and makes it the default:

```sh
go run ./cmd/cursor-cli-byok config init
```

For VPS automation, prefer an environment variable for the API key:

```sh
export RELAY_API_KEY='replace-me'

go run ./cmd/cursor-cli-byok config init --non-interactive \
  --name relay-gpt \
  --base-url https://relay.example.com \
  --endpoint /v1/responses \
  --upstream-model gpt-5.4 \
  --api-key-env RELAY_API_KEY
```

Chat Completions uses `--endpoint /v1/chat/completions`. Inline
`--api-key` storage is supported, but `--api-key-env` avoids putting a secret
in shell history and is the recommended mode.

Some relays use the HTTP client identity when selecting upstream model access.
Attach non-secret compatibility headers to only the affected alias with a
repeatable `--header` flag:

```sh
export SUB2_API_KEY='replace-me'

go run ./cmd/cursor-cli-byok config add --non-interactive \
  --name sub2-luna \
  --base-url http://127.0.0.1:8100 \
  --endpoint /v1/responses \
  --upstream-model gpt-5.6-luna \
  --api-key-env SUB2_API_KEY \
  --header 'User-Agent: codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color'
```

Configured headers are sent by both inference requests and the `doctor`
provider probe; they are not injected into `cursor-agent`. Header values are
stored only in the mode-0600 config and are redacted from formatted and JSON
diagnostics. Use `api_key_env` for authentication rather than a custom header.
`Authorization`, `Accept`, `Content-Type`, `Host`, and hop-by-hop headers are
reserved so an alias cannot override protocol or redirect-sensitive behavior.
The Sub2API version-specific reason for the example is recorded in
[docs/compatibility.md](docs/compatibility.md) and
[docs/upstream-reference.md](docs/upstream-reference.md).

The configured `base_url` is combined with the selected endpoint. For example,
`https://relay.example.com` and `/v1/responses` produce
`https://relay.example.com/v1/responses`.

Remote provider URLs must use HTTPS because the request carries a Bearer key.
Plain HTTP is accepted only for literal loopback addresses or `localhost`,
which keeps local relay and acceptance-test setups available without allowing
remote cleartext credentials.

Manage model aliases explicitly:

```sh
go run ./cmd/cursor-cli-byok config add
go run ./cmd/cursor-cli-byok config list
go run ./cmd/cursor-cli-byok config use relay-gpt
go run ./cmd/cursor-cli-byok config remove old-model
go run ./cmd/cursor-cli-byok config --help
```

The config file is stored at
`$XDG_CONFIG_HOME/cursor-cli-byok/config.yaml`, or
`~/.config/cursor-cli-byok/config.yaml` when `XDG_CONFIG_HOME` is unset. Its
directory and file modes are enforced as `0700` and `0600`.

Release builds can override the development version:

```sh
go build \
  -ldflags "-X github.com/shiqkuangsan/cursor-cli-byok/internal/buildinfo.Version=v0.1.0" \
  -o cursor-cli-byok ./cmd/cursor-cli-byok
```

Build and verify both Linux release artifacts:

```sh
make verify
make release VERSION=v0.1.0
```

`make verify` runs formatting checks, unit tests, race tests, `go vet`, offline
installer tests, and CGO-free Linux amd64/arm64 builds. Real-CLI acceptance is
separate because it requires the official Cursor installation:

```sh
make e2e
make linux-e2e
```

On a Linux host, a release artifact can also run the narrower lifecycle smoke
with a deterministic fake child. This verifies the Linux wrapper/daemon process
path but is not a substitute for `make linux-e2e`:

```sh
BYOK_BINARY="$PWD/dist/cursor-cli-byok-linux-amd64" \
  ./test/linux-smoke/run.sh
```

No Git tag, commit, release, or upload is created by these local commands.

## Prior Art And Independence

This project is not a fork and has no source, build, Git, or runtime dependency
on either reference below. Their public implementations inform protocol
research; this project owns its architecture and implementation.

- [`shiqkuangsan/cursor-agent-byok`](https://github.com/shiqkuangsan/cursor-agent-byok)
  at [`8606538b2569335b92e16d20151870cf07fa03ad`](https://github.com/shiqkuangsan/cursor-agent-byok/commit/8606538b2569335b92e16d20151870cf07fa03ad):
  Cursor CLI endpoint override, TLS facade, mock authentication, and model
  discovery.
- [`leookun/cursor-byok`](https://github.com/leookun/cursor-byok)
  at [`f93b18740df9d7034be689e7bb1af6c9da0388de`](https://github.com/leookun/cursor-byok/commit/f93b18740df9d7034be689e7bb1af6c9da0388de):
  protocol schemas, agent and tool lifecycle, provider adapters, and streaming
  behavior.

Both references use the MIT License at the reviewed revisions. Substantial code
is not copied by default. Any intentional port must record its exact source and
carry all required license notices before it is accepted.

See [docs/upstream-reference.md](docs/upstream-reference.md) for the maintenance
ledger. This project is not affiliated with Cursor or either reference project.
