# Upstream Reference Ledger

This ledger tracks public technical references without making them project
dependencies. A reviewed revision is a research baseline, not code to merge.

## Current Baselines

| Reference | License | Last reviewed commit | Reference areas | Planned local area | Follow-up status |
| --- | --- | --- | --- | --- | --- |
| [`sherkevin/cursor-agent-byok`](https://github.com/sherkevin/cursor-agent-byok) | MIT | [`8606538b2569335b92e16d20151870cf07fa03ad`](https://github.com/sherkevin/cursor-agent-byok/commit/8606538b2569335b92e16d20151870cf07fa03ad) | CLI endpoint override, TLS facade, mock auth, model discovery, Run stream observations | Cursor CLI facade and wrapper | Canonical upstream of the previously referenced `shiqkuangsan/cursor-agent-byok` fork; wrapper/TLS/model discovery independently implemented; no code imported |
| [`leookun/cursor-byok`](https://github.com/leookun/cursor-byok) | MIT | [`799dbda7e0ca30ab5d0bfe965fd1ab3c5da5c588`](https://github.com/leookun/cursor-byok/commit/799dbda7e0ca30ab5d0bfe965fd1ab3c5da5c588) | Protocol schemas, agent/tool lifecycle, provider adapters, stream behavior | Protocol bridge and provider adapters | Reviewed provider-specific request normalization and WebSearch backend changes; no protocol, auth, response-streaming, or tool-lifecycle impact; no code imported |

## Local Reference Layout

Canonical upstream repositories are cloned under the Git-ignored `.labs/`
directory. They remain local research material and are not modules, submodules,
vendored trees, build inputs, runtime dependencies, or remotes of this project.

| Local path | Canonical upstream | Previously referenced fork |
| --- | --- | --- |
| `.labs/cursor-agent-byok` | `sherkevin/cursor-agent-byok` | `shiqkuangsan/cursor-agent-byok` |
| `.labs/cursor-byok` | `leookun/cursor-byok` | `shiqkuangsan/cursor-byok` |

If a clone is missing, recreate it from the project root:

```sh
git clone https://github.com/sherkevin/cursor-agent-byok.git .labs/cursor-agent-byok
git clone https://github.com/leookun/cursor-byok.git .labs/cursor-byok
```

## Runtime Interoperability References

These are deployed providers or gateways inspected to explain observed
behavior. They are not prior-art baselines and are not project dependencies.

| Reference | Observed version | Inspected behavior | Local action |
| --- | --- | --- | --- |
| [`Wei-Shaw/sub2api`](https://github.com/Wei-Shaw/sub2api) | `v0.1.150`, [`0dec1ad2922ff8c9d27b67f8a31dfb35bce1902b`](https://github.com/Wei-Shaw/sub2api/commit/0dec1ad2922ff8c9d27b67f8a31dfb35bce1902b) | Compared `account_test_service.go` with the public `/v1/responses` gateway path. Account testing always sends a Codex CLI User-Agent; the gateway derives upstream client identity from the inbound headers. A real `gpt-5.6-luna` request returned 404 with the default Go client identity and HTTP 200 when only the tested Codex CLI User-Agent was added. | Added generic alias-scoped provider headers with reserved-header validation and redacted diagnostics; no Sub2API code imported |

## Review Procedure

1. Fetch the canonical upstream in its ignored `.labs/` reference clone without
   adding a remote, submodule, vendored tree, or dependency to this repository.
2. Compare the recorded commit with `origin/main` using both `git log` and
   `git diff`; inspect only the reference areas above.
3. Decide whether each relevant change is protocol evidence, an idea to
   reimplement, or code proposed for intentional porting.
4. Update the baseline and append the review history only after the intervening
   changes have been reviewed; fetching or advancing a clone does not advance a
   reviewed baseline.
5. For ported code, record exact source files and license-notice handling.

## Review History

| Date | Reference | Reviewed through | Finding | Local action |
| --- | --- | --- | --- | --- |
| 2026-07-10 | `sherkevin/cursor-agent-byok` (originally reviewed through the `shiqkuangsan` fork) | `8606538b2569335b92e16d20151870cf07fa03ad` | Initial prior-art baseline | Track CLI facade techniques; independently implement |
| 2026-07-10 | `leookun/cursor-byok` | `f93b18740df9d7034be689e7bb1af6c9da0388de` | Initial prior-art baseline | Track protocol and provider behavior; independently implement |
| 2026-07-11 | Both baselines | Commits above | Cross-checked endpoint override, model discovery, and minimal model field numbers during compatibility work | Independently implemented wrapper, TLS daemon, and `protowire` responses; no code imported |
| 2026-07-11 | Both baselines | Commits above | Cross-checked native `/Run` to legacy Bidi/RunSSE behavior, current `requested_model` field, interaction events, and Read exec field numbers against sanitized real CLI captures | Independently implemented direct BiDi, both OpenAI adapters, conversation runner, and Read continuation; no code imported |
| 2026-07-11 | `leookun/cursor-byok` | `f93b18740df9d7034be689e7bb1af6c9da0388de` | Cross-checked Write/Delete/Ls/Grep/Shell/MCP exec oneofs, visible ToolCall variants, Shell stream states, protobuf Value schemas, and MCP state messages; real CLI behavior remained authoritative | Independently implemented all tool encoders/decoders, pending state machines, MCP discovery, and bounded result conversion with local tests; no code imported |
| 2026-07-11 | `leookun/cursor-byok` plus real CLI capture | Commit above; Cursor CLI `2026.07.08-0c04a8a` | Confirmed the user-message ID inside Run action metadata is stable protocol identity for direct reconnects | Independently implemented stable direct-session replay, provider error classification, post-tool retry de-duplication, and real-CLI E2E coverage; no code imported |
| 2026-07-11 | `leookun/cursor-byok` plus real Linux CLI capture | Commit above; Cursor CLI `2026.07.08-0c04a8a` on Linux arm64 | A foreground Shell that crossed its block interval produced Run action field 12; the public schema confirmed `BackgroundTaskCompletionAction` and metadata-only semantics | Independently decoded the validated completion shape and emitted a zero-usage direct-Run terminal without provider or tool redispatch; local RED/GREEN tests cover the captured shape; no code imported |
| 2026-07-11 | Official Cursor CLI executable acceptance | Cursor CLI `2026.07.09-a3815c0` on Linux arm64/x86_64 | The existing compatibility surface passed without protocol changes; the official installer relies on its Bash shebang; final acceptance exposed local terminal-result and daemon-stop lifecycle races | Added the exact version manifest and CI gate, recorded both Linux E2E runs, preserved the official script's interpreter, rejected terminal late tool results, made stale cleanup lock-guarded and identity-aware, and gated final tagged artifacts; no reference-project or Cursor source code imported |
| 2026-07-12 | `Wei-Shaw/sub2api` runtime inspection | `v0.1.150`, `0dec1ad2922ff8c9d27b67f8a31dfb35bce1902b` | A real account test and public gateway request used different upstream client identities; `gpt-5.6-luna` availability depended on the Codex CLI User-Agent | Implemented a generic, explicit alias header rather than a Sub2-specific default or global gateway mutation, then verified three official Cursor Agent QA turns; no code imported |
| 2026-07-16 | `leookun/cursor-byok` | `799dbda7e0ca30ab5d0bfe965fd1ab3c5da5c588` | Reviewed 12 commits covering MiMo thinking disable, Anthropic thinking consistency and relay image relocation, a Baidu-to-DuckDuckGo WebSearch backend, and desktop/build changes; no protocol schema, Connect-RPC, auth, provider response-streaming, or tool-lifecycle change | No local code change; retained provider-specific ideas as future evidence only; no code imported |

## Ported Code Record

None.
