# Project Instructions

## Product Boundary

- Build a standalone, pure-Go `cursor-cli-byok` executable for headless Linux.
- Keep BYOK explicit; do not replace or modify the official `cursor-agent`.
- Do not require Cursor IDE, Cursor authentication, a GUI, MITM, or system proxy
  changes.
- Fail closed: never silently route requests to Cursor-hosted inference.

## Independence And Provenance

- This repository is not a fork of `cursor-agent-byok` or `cursor-byok`.
- Do not add either project as a module, submodule, Git remote, vendored tree,
  build input, runtime service, or filesystem dependency.
- Independently implement behavior from observed protocols and public ideas by
  default. Do not merge or copy upstream changes wholesale.
- Before intentionally porting substantial MIT-licensed code, record the source
  repository, commit, files, and required license notice in
  `docs/upstream-reference.md`.

## Upstream Review Policy

- Treat `docs/upstream-reference.md` as the source of truth for reviewed
  revisions and follow-up work.
- Review upstream selectively when protocol compatibility work starts, a Cursor
  CLI change breaks behavior, or a maintainer explicitly requests an update.
- Compare each recorded commit with the new upstream revision in the ignored
  `.labs/` reference clones. Inspect only the tracked reference areas.
- Record the review date, new commit, relevant findings, local impact, and
  follow-up status before implementing any adopted behavior.
- Preserve local architecture and tests. An upstream implementation is evidence,
  not an automatic design decision.

## Engineering Workflow

- Use strict test-driven development for behavior changes: RED, GREEN, then
  refactor.
- Keep code and comments in English. Run `gofmt -w .` and `go test ./...` before
  reporting completion.
- Keep the default build free of CGO so Linux targets remain cross-compilable.
- Never log API keys, authorization headers, or unredacted sensitive payloads.
- Do not commit, push, create pull requests, or publish releases without explicit
  user authorization.
