# Sanitized Model Discovery Evidence

Verified clients on 2026-07-11: official `cursor-agent
2026.07.09-a3815c0` on Linux arm64/x86_64 and the prior
`2026.07.08-0c04a8a` baseline on Darwin arm64.

Observed unary procedure order includes:

1. `DashboardService/GetUserPrivacyMode`
2. `AiService/AvailableModels`
3. `AiService/GetUsableModels`
4. `AiService/GetDefaultModelForCli`

The compatibility implementation intentionally does not decode these request
bodies. Unknown protobuf fields are accepted and ignored, so machine IDs, OS
statistics, user paths, authorization values, and other client metadata are not
retained as fixtures. Golden response wire shapes are asserted in
`internal/protocol/wire_test.go`; handler behavior and config reload are asserted
in `internal/server/compat_test.go`.

Real-client acceptance evidence is the headless `--list-models` run documented
in `docs/protocol.md`. No credentials or user-specific paths are stored here.
