# Installation And First Use

This guide installs `cursor-cli-byok` for one Linux user and connects the
official Cursor CLI to an OpenAI-compatible provider. Cursor IDE and Cursor
login are not required.

> `v0.1.0` is not published yet. The release commands below describe the stable
> post-release path. Use [Build From Source](#build-from-source) during release
> candidate testing.

## Requirements

- Linux amd64 or arm64;
- a non-root user with a writable home directory;
- `curl` and either `sha256sum` or `shasum`;
- an OpenAI-compatible provider Base URL, model name, and API key;
- the official `cursor-agent`, either already installed or installed through
  Cursor's official installer.

The wrapper finds `cursor-agent` on `PATH` or at
`~/.local/bin/cursor-agent`. It does not use or install Cursor IDE.

## Install A Release

Download the project installer before executing it:

```sh
curl --proto '=https' --tlsv1.2 -fsSL \
  https://raw.githubusercontent.com/shiqkuangsan/cursor-cli-byok/v0.1.0/scripts/install.sh \
  -o /tmp/install-cursor-cli-byok.sh
```

When the official Cursor CLI is already installed:

```sh
sh /tmp/install-cursor-cli-byok.sh --version v0.1.0 --skip-cursor-install
```

When it is absent, omit that option. The project installer downloads Cursor's
official installer and delegates only the Cursor CLI installation step:

```sh
sh /tmp/install-cursor-cli-byok.sh --version v0.1.0
```

The installer selects the Linux architecture, verifies the release checksum,
and atomically installs `cursor-cli-byok` to `~/.local/bin`. It never replaces
an existing wrapper when download or checksum verification fails.
Keep the installer URL tag and `--version` value identical when selecting a
different release.

Select an exact release or an alternative absolute installation directory:

```sh
sh /tmp/install-cursor-cli-byok.sh \
  --version v0.1.0 \
  --install-dir "$HOME/bin" \
  --skip-cursor-install
```

Make sure the selected directory is on `PATH` before continuing.

## Load The Provider Key

Environment-backed keys are recommended. In an interactive Bash session, read
the key without putting its value in shell history:

```sh
read -r -s -p 'Provider API key: ' OPENAI_API_KEY
printf '\n'
export OPENAI_API_KEY
```

For services and CI, inject `OPENAI_API_KEY` through the service manager or
secret store. This project deliberately does not load project `.env` files.
The variable must be available to each `cursor-cli-byok` invocation.

## Create The First Alias

Interactive setup asks for the provider Base URL and upstream model:

```sh
cursor-cli-byok config init
```

Press Enter at the model alias prompt to use the upstream model name. With
`OPENAI_API_KEY` loaded, the generated configuration uses these defaults:

| Field | Default |
| --- | --- |
| Endpoint | `/v1/responses` |
| Local alias | Upstream model name |
| Key source | `api_key_env: OPENAI_API_KEY` |

For an unattended VPS setup, provide only the values without safe defaults:

```sh
cursor-cli-byok config init --non-interactive \
  --base-url https://relay.example.com \
  --upstream-model gpt-5.4
```

Use `--endpoint /v1/chat/completions` for a provider that exposes Chat
Completions instead of Responses. See [configuration.md](configuration.md) for
multiple aliases, custom key names, provider headers, and security rules.

## Verify And Run

Check configuration, provider reachability, and the official Cursor CLI without
starting an inference turn:

```sh
cursor-cli-byok doctor
```

Run a read-only headless question in the current workspace:

```sh
cursor-cli-byok --workspace "$PWD" --trust -p --mode ask \
  'Summarize this repository.'
```

The wrapper starts or reuses its own loopback daemon, launches the official
Cursor CLI, and preserves Cursor's stdout, stderr, signals, and exit code. BYOK
is active only for this explicit wrapper command; invoking `cursor-agent`
directly keeps its normal behavior.

Inspect or stop the shared daemon:

```sh
cursor-cli-byok status
cursor-cli-byok stop
```

The daemon also shuts down after its idle period. A newer wrapper binary
replaces an older healthy daemon through an authenticated control request.

## Build From Source

Source builds require Go 1.24 or later:

```sh
make verify
make build
install -m 0755 dist/cursor-cli-byok ~/.local/bin/cursor-cli-byok
```

To build versioned static Linux artifacts without publishing anything:

```sh
make release VERSION=v0.1.0-rc.1
```

Artifacts and `checksums.txt` are written under ignored `dist/`. No Git commit,
tag, release, or upload is created.

## Files And Permissions

Default XDG locations are:

| Purpose | Path | Mode |
| --- | --- | --- |
| Configuration | `~/.config/cursor-cli-byok/config.yaml` | `0600` |
| Configuration directory | `~/.config/cursor-cli-byok/` | `0700` |
| Daemon state | `~/.local/state/cursor-cli-byok/daemon.json` | `0600` |
| Local certificates | `~/.local/share/cursor-cli-byok/` | private to the user |

`XDG_CONFIG_HOME`, `XDG_STATE_HOME`, and `XDG_DATA_HOME` override these roots.
The provider API key is not written to daemon state and is removed from the
official Cursor process environment.

## Next Steps

- [Configure providers and model aliases](configuration.md)
- [Automate text, JSON, and stream JSON runs](headless.md)
- [Review tested Cursor versions and platforms](compatibility.md)
