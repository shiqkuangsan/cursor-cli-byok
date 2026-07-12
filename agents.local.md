# Local Agent Context

## Upstream reference clones

This project is independently implemented. The repositories below are public
technical references, not project dependencies, vendored code, Git submodules,
or additional remotes of this repository.

| Previously referenced fork | Canonical upstream | Ignored local clone |
| --- | --- | --- |
| `shiqkuangsan/cursor-agent-byok` | `sherkevin/cursor-agent-byok` | `.labs/cursor-agent-byok` |
| `shiqkuangsan/cursor-byok` | `leookun/cursor-byok` | `.labs/cursor-byok` |

`docs/upstream-reference.md` is the source of truth for reviewed baseline
commits, relevant areas, findings, and follow-up work. A clone's current `HEAD`
is not automatically a reviewed baseline.

Before adopting any upstream behavior:

- fetch the canonical upstream in `.labs`;
- compare the recorded baseline with `origin/main` before advancing the clone;
- review commit history and actual diffs only in relevant reference areas;
- preserve this project's standalone pure-Go architecture and test contracts;
- update the ledger only after the new revision has been reviewed;
- never add `.labs/` contents to Git.

Local commands and clone recovery instructions live in `.labs/README.md`.
