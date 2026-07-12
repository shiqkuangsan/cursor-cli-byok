# Provider And Model Configuration

`cursor-cli-byok` stores explicit local aliases. Each alias maps the model name
visible to Cursor to one OpenAI-compatible endpoint, upstream model, and key
source. Existing configuration is never changed implicitly by an agent run.

## Initial Defaults

Defaults apply only while creating an alias with `config init` or `config add`:

| Missing value | Applied default |
| --- | --- |
| `--endpoint` | `/v1/responses` |
| `--name` | The value of `--upstream-model` |
| Key source | `api_key_env: OPENAI_API_KEY` when that variable is non-empty |

Explicit flags always win. Existing version 1 YAML remains compatible and is
not rewritten just because new defaults exist.

Minimal non-interactive setup:

```sh
export OPENAI_API_KEY

cursor-cli-byok config init --non-interactive \
  --base-url https://relay.example.com \
  --upstream-model gpt-5.4
```

If neither `--api-key-env`, `--api-key`, nor a non-empty `OPENAI_API_KEY` is
available, non-interactive setup fails without creating a configuration.

## Endpoint Types

Responses is the default:

```sh
cursor-cli-byok config init --non-interactive \
  --name relay-responses \
  --base-url https://relay.example.com \
  --endpoint /v1/responses \
  --upstream-model gpt-5.4 \
  --api-key-env RELAY_API_KEY
```

Chat Completions is explicit:

```sh
cursor-cli-byok config add --non-interactive \
  --name relay-chat \
  --base-url https://relay.example.com \
  --endpoint /v1/chat/completions \
  --upstream-model gpt-5-mini \
  --api-key-env RELAY_API_KEY
```

The Base URL is combined with the selected endpoint. A remote provider URL
must use HTTPS because each request carries a Bearer key. Plain HTTP is allowed
only for `localhost`, `127.0.0.0/8`, or `::1`, which supports a local relay
without allowing credentials over a remote cleartext connection.

Base URLs may not include user information, a query string, or a fragment. Put
authentication in the configured key source rather than in the URL.

## Key Sources

An environment variable is the preferred key source:

```sh
export RELAY_API_KEY

cursor-cli-byok config add --non-interactive \
  --name private-relay \
  --base-url https://relay.example.com \
  --upstream-model gpt-5.4 \
  --api-key-env RELAY_API_KEY
```

Only the variable name is stored. The selected alias's value is resolved on
every wrapper invocation and synchronized to the local daemon in memory. Every
configured key variable is removed before `cursor-agent` starts, so Cursor
tools and Shell commands cannot inherit it.

The command also accepts `--api-key`, which stores the value inline in the
mode-0600 config file. This is less suitable for automation because the value
can enter shell history and remains on disk. Environment-backed secrets are
recommended.

The project does not automatically load `.env` files. Export variables in the
calling shell, inject them from the service manager, or use the CI platform's
secret environment support.

### Shared daemon credential scope

Environment overrides belong to the shared daemon identified by the current
XDG roots, not to an individual shell process. Distinct `api_key_env` names are
independent. If concurrent wrappers share the same XDG roots and synchronize
different values for the same environment-variable name, the last successful
sync is used by subsequent provider turns, including turns from an already-open
Cursor session.

Use a distinct key environment name for each stable account or provider alias.
For parallel jobs that may use different values for the same name, isolate
`HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, and `XDG_STATE_HOME` per job. To
revoke a daemon-held override deterministically, run `cursor-cli-byok stop`
before unsetting or replacing the key.

## Multiple Aliases

List the aliases and whether each environment key is currently set:

```sh
cursor-cli-byok config list
```

Choose the default alias:

```sh
cursor-cli-byok config use relay-chat
```

Choose an alias for one invocation without changing the default:

```sh
cursor-cli-byok --model relay-responses --trust -p --mode ask \
  'Explain the current changes.'
```

Remove a non-default alias:

```sh
cursor-cli-byok config remove old-relay
```

To remove the default, select another alias first. `config init` refuses to
replace an existing config unless `--force` is supplied deliberately.

## Provider Compatibility Headers

Some relays require a non-secret client identity or routing header. Attach it
only to the affected alias with repeatable `--header` flags:

```sh
cursor-cli-byok config add --non-interactive \
  --name routed-model \
  --base-url https://relay.example.com \
  --upstream-model provider-model \
  --api-key-env RELAY_API_KEY \
  --header 'User-Agent: approved-client-identity' \
  --header 'X-Provider-Route: primary'
```

Configured headers are sent by inference requests and the inference-free
`doctor` probe for that alias. They are not injected into `cursor-agent`.
Header values are redacted from formatted and JSON diagnostics.

Use the key source for authentication. The wrapper rejects headers that would
override authentication, representation, target host, content length, proxy,
or hop-by-hop transport behavior, including `Authorization`, `Accept`,
`Content-Type`, and `Host`.

## Storage

The configuration path is:

```text
$XDG_CONFIG_HOME/cursor-cli-byok/config.yaml
```

When `XDG_CONFIG_HOME` is unset, it is:

```text
~/.config/cursor-cli-byok/config.yaml
```

The directory and file modes are enforced as `0700` and `0600`. Saving is
atomic, unknown YAML fields are rejected, symlink config files are rejected,
and command diagnostics redact inline keys and header values.

## Diagnostics

Run:

```sh
cursor-cli-byok doctor
```

`doctor` validates the default alias and key availability, checks the official
Cursor CLI version, probes the provider route without requesting inference,
and inspects daemon health. It first sends a body-free `HEAD`. When a compatible
relay returns 404, it may send one empty JSON POST and accepts only an
invalid-request response as route evidence.

The configured provider URL and key are not printed in diagnostics. An
unlisted but otherwise valid Cursor CLI version produces a warning and remains
runnable so compatibility can be evaluated with the E2E gate.

Show every configuration command and flag:

```sh
cursor-cli-byok config --help
```
