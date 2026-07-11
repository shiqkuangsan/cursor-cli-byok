# Upstream Reference Ledger

This ledger tracks public technical references without making them project
dependencies. A reviewed revision is a research baseline, not code to merge.

## Current Baselines

| Reference | License | Last reviewed commit | Reference areas | Planned local area | Follow-up status |
| --- | --- | --- | --- | --- | --- |
| [`shiqkuangsan/cursor-agent-byok`](https://github.com/shiqkuangsan/cursor-agent-byok) | MIT | [`8606538b2569335b92e16d20151870cf07fa03ad`](https://github.com/shiqkuangsan/cursor-agent-byok/commit/8606538b2569335b92e16d20151870cf07fa03ad) | CLI endpoint override, TLS facade, mock auth, model discovery, Run stream observations | Cursor CLI facade and wrapper | Wrapper/TLS/model discovery independently implemented; no code imported |
| [`leookun/cursor-byok`](https://github.com/leookun/cursor-byok) | MIT | [`f93b18740df9d7034be689e7bb1af6c9da0388de`](https://github.com/leookun/cursor-byok/commit/f93b18740df9d7034be689e7bb1af6c9da0388de) | Protocol schemas, agent/tool lifecycle, provider adapters, stream behavior | Protocol bridge and provider adapters | Model, tool oneof, exec, Shell stream, and MCP state fields cross-checked; no code imported |

## Review Procedure

1. Resolve the latest upstream commit without adding a remote, submodule, or
   dependency to this repository.
2. Diff from the recorded commit and inspect only the reference areas above.
3. Decide whether each relevant change is protocol evidence, an idea to
   reimplement, or code proposed for intentional porting.
4. Update the baseline and append the review history before local implementation.
5. For ported code, record exact source files and license-notice handling.

## Review History

| Date | Reference | Reviewed through | Finding | Local action |
| --- | --- | --- | --- | --- |
| 2026-07-10 | `shiqkuangsan/cursor-agent-byok` | `8606538b2569335b92e16d20151870cf07fa03ad` | Initial prior-art baseline | Track CLI facade techniques; independently implement |
| 2026-07-10 | `leookun/cursor-byok` | `f93b18740df9d7034be689e7bb1af6c9da0388de` | Initial prior-art baseline | Track protocol and provider behavior; independently implement |
| 2026-07-11 | Both baselines | Commits above | Cross-checked endpoint override, model discovery, and minimal model field numbers during compatibility work | Independently implemented wrapper, TLS daemon, and `protowire` responses; no code imported |
| 2026-07-11 | Both baselines | Commits above | Cross-checked native `/Run` to legacy Bidi/RunSSE behavior, current `requested_model` field, interaction events, and Read exec field numbers against sanitized real CLI captures | Independently implemented direct BiDi, both OpenAI adapters, conversation runner, and Read continuation; no code imported |
| 2026-07-11 | `leookun/cursor-byok` | `f93b18740df9d7034be689e7bb1af6c9da0388de` | Cross-checked Write/Delete/Ls/Grep/Shell/MCP exec oneofs, visible ToolCall variants, Shell stream states, protobuf Value schemas, and MCP state messages; real CLI behavior remained authoritative | Independently implemented all tool encoders/decoders, pending state machines, MCP discovery, and bounded result conversion with local tests; no code imported |
| 2026-07-11 | `leookun/cursor-byok` plus real CLI capture | Commit above; Cursor CLI `2026.07.08-0c04a8a` | Confirmed the user-message ID inside Run action metadata is stable protocol identity for direct reconnects | Independently implemented stable direct-session replay, provider error classification, post-tool retry de-duplication, and real-CLI E2E coverage; no code imported |
| 2026-07-11 | Official Cursor CLI executable acceptance | Cursor CLI `2026.07.09-a3815c0` on Linux arm64/x86_64 | The existing compatibility surface passed without protocol changes; the official installer relies on its Bash shebang; final acceptance exposed local terminal-result and daemon-stop lifecycle races | Added the exact version manifest and CI gate, recorded both Linux E2E runs, preserved the official script's interpreter, rejected terminal late tool results, made stale cleanup lock-guarded and identity-aware, and gated final tagged artifacts; no reference-project or Cursor source code imported |

## Ported Code Record

None.
