# Changelog

All notable project changes are recorded here. The project intends to follow
Semantic Versioning after the first public release.

## Unreleased

No unreleased changes.

## 0.1.0 - 2026-07-12

### Added

- Independent pure-Go `cursor-cli-byok` wrapper with an authenticated,
  CA-pinned loopback daemon for the official Cursor CLI.
- Headless Linux operation without Cursor IDE, Cursor login, or a graphical
  session.
- OpenAI-compatible Responses and Chat Completions streaming with custom Base
  URLs, API keys, upstream models, and alias-scoped compatibility headers.
- Explicit model alias management through `config init`, `add`, `list`, `use`,
  and `remove`.
- Built-in Read, Write, Edit, Delete, List, Glob, Grep, and streamed Shell tool
  continuation, plus dynamic stdio MCP discovery and execution.
- Text, JSON, and partial stream JSON headless contracts, validated against the
  official Cursor CLI with stdout separated from stderr.
- `doctor`, `status`, and `stop` lifecycle commands.
- Secure XDG configuration and daemon state with atomic writes, strict file
  modes, remote cleartext-provider rejection, in-memory key rotation, and
  provider-secret removal from the Cursor process environment.
- Non-root checksummed installer with optional delegation to Cursor's official
  CLI installer.
- Static Linux amd64 and arm64 artifacts, lifecycle smoke tests, CI gates, and
  tag-triggered release automation.
- Compatibility evidence for official `cursor-agent`
  `2026.07.08-0c04a8a` and `2026.07.09-a3815c0` across Darwin arm64, Linux
  arm64, and Linux x86_64 acceptance hosts.
- Project-level MIT License.

### Changed

- Initial model setup now defaults to `/v1/responses`, the upstream model as
  its local alias, and `OPENAI_API_KEY` as the key environment name when that
  variable is available.
- Missing configuration and successful installation now print an actionable
  `cursor-cli-byok config init` next step without printing a key value.

### Security

- Provider failures remain fail-closed and are not automatically redispatched
  when request commitment is unknown.
- Tool side effects are de-duplicated across official Cursor reconnects.
- Provider keys are excluded from Cursor child environments, Shell tool
  environments, state files, logs, diagnostics, and process arguments.
