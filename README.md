# cursor-cli-byok

**English** | [简体中文](README.zh-CN.md)

`cursor-cli-byok` is an independent, explicit wrapper that runs the official
Cursor CLI against user-supplied OpenAI-compatible endpoints. It is designed
for headless Linux servers and does not require Cursor IDE, a Cursor login, a
desktop session, MITM, or system proxy changes.

The official `cursor-agent` remains installed and untouched. BYOK is used only
when you invoke the separate `cursor-cli-byok` command.

> **Release:** The installation commands below target `v0.1.0`. Check
> [GitHub Releases](https://github.com/shiqkuangsan/cursor-cli-byok/releases)
> for availability and newer versions.

## Quickstart

### Install

For a Linux host that already has `cursor-agent`, download the installer for
inspection and skip Cursor's installer:

```sh
curl --proto '=https' --tlsv1.2 -fsSL \
  https://raw.githubusercontent.com/shiqkuangsan/cursor-cli-byok/v0.1.0/scripts/install.sh \
  -o /tmp/install-cursor-cli-byok.sh
sh /tmp/install-cursor-cli-byok.sh --version v0.1.0 --skip-cursor-install
```

Omit `--skip-cursor-install` when the official Cursor CLI is absent. Keep the
installer tag and `--version` value identical when selecting another release. The
installer then delegates that one step to Cursor's official installer. It runs
without root, verifies the selected amd64/arm64 release checksum, and installs
the wrapper to `~/.local/bin/cursor-cli-byok` by default.

### Configure

Load the provider key into `OPENAI_API_KEY`. For an interactive Bash session,
this avoids placing the value in shell history:

```sh
read -r -s -p 'Provider API key: ' OPENAI_API_KEY
printf '\n'
export OPENAI_API_KEY
```

Create the first provider alias:

```sh
cursor-cli-byok config init
```

Enter the provider Base URL and upstream model. The initial setup defaults to
`/v1/responses`, uses the upstream model as the local alias, and records
`api_key_env: OPENAI_API_KEY` when that environment variable is available.

The equivalent minimal non-interactive setup is:

```sh
cursor-cli-byok config init --non-interactive \
  --base-url https://relay.example.com \
  --upstream-model gpt-5.4
```

### Run

Use the wrapper explicitly. This read-only headless call trusts the selected
workspace and prints the final answer to stdout:

```sh
cursor-cli-byok --workspace "$PWD" --trust -p --mode ask \
  'Summarize this repository.'
```

The API key environment variable must be present for every invocation. Existing
configuration is never silently rewritten.

## Capabilities

| Surface | Current support |
| --- | --- |
| Host | Headless Linux amd64 and arm64; non-root install |
| Provider APIs | OpenAI Responses and Chat Completions streaming |
| Cursor use | Interactive CLI plus `text`, `json`, and `stream-json` headless output |
| Agent tools | Read, Write, Edit, Delete, List, Glob, Grep, Shell, and dynamic stdio MCP |
| Operations | `doctor`, `status`, `stop`, secure XDG config, shared on-demand daemon |
| Security | Loopback TLS facade, mode-0600 state/config, provider key removed from Cursor's environment |

Compatibility is executable evidence, not a broad promise about Cursor's
private protocol. The exact official Cursor CLI versions and tested platforms
are recorded in [docs/compatibility.md](docs/compatibility.md).

## Documentation

- [Installation and first use](docs/getting-started.md)
- [Provider and model configuration](docs/configuration.md)
- [Headless Shell, Node.js, and CI use](docs/headless.md)
- [Compatibility and acceptance evidence](docs/compatibility.md)
- [Cursor CLI protocol boundary](docs/protocol.md)
- [Upstream reference and independence ledger](docs/upstream-reference.md)
- [Changelog](CHANGELOG.md)

## Source Build

Source builds require Go 1.24 or later:

```sh
make verify
make build
install -m 0755 dist/cursor-cli-byok ~/.local/bin/cursor-cli-byok
```

`make verify` runs formatting checks, unit and race tests, `go vet`, installer
and shell tests, and static Linux amd64/arm64 builds. Real official-Cursor
acceptance is separate:

```sh
make e2e
make linux-e2e
```

No Git commit, tag, release, or upload is created by these commands.

## Security Boundary

Remote provider URLs must use HTTPS. Plain HTTP is accepted only for literal
loopback addresses and `localhost`. The wrapper resolves only the selected
alias's key, synchronizes it to the authenticated local daemon in memory, and
removes all configured provider-key variables before starting `cursor-agent`.
Keys are not written to daemon state, command output, or logs.

`--trust`, execution mode, `--force`, and `--approve-mcps` remain official
Cursor permissions. The wrapper never adds them silently. See
[docs/headless.md](docs/headless.md) before enabling write, Shell, or MCP access
in automation.

## Independence

This repository is not a fork and has no source, build, Git, or runtime
dependency on `shiqkuangsan/cursor-agent-byok` or `leookun/cursor-byok`. Their
public implementations are prior-art references for protocol research; this
project owns its architecture and implementation. Reviewed revisions and
maintenance rules are recorded in
[docs/upstream-reference.md](docs/upstream-reference.md).

This project is not affiliated with Cursor or either reference project.

## License

Licensed under the [MIT License](LICENSE).
