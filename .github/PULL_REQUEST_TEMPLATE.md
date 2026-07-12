# Pull Request Notice / 提交说明

> [!IMPORTANT]
>
> Keep the summary concise and personally reviewed. Explain why the change
> works and include reproducible verification evidence.
>
> 请提交经过本人审阅的简洁说明，解释变更为何有效，并提供可复现的验证证据。

## Summary / 变更概述

<!-- What changed, why it is needed, and how the implementation works. -->
<!-- 说明改了什么、为什么需要修改，以及实现为何能够生效。 -->

## Type of Change / 变更类型

- [ ] Bug fix / Bug 修复
- [ ] New feature / 新功能
- [ ] Refactor or performance improvement / 重构或性能优化
- [ ] Documentation / 文档更新
- [ ] Test or build infrastructure / 测试或构建基础设施

## Related Issue / 关联任务

- Closes #
- Context or incident / 相关背景或事件：

## Verification / 验证证据

- Tests / 自动化测试：
  - `...`
- Manual verification / 手动验证：
  - `...`
- Not run / 未执行项：
  - `...`（Explain why / 请说明原因）

## Checklist / 提交前检查

- [ ] I understand and can explain the implementation and its impact.
      我已理解并能够解释实现方式及其影响。
- [ ] The change is focused and contains no unrelated modifications.
      本 PR 范围聚焦，不包含无关修改。
- [ ] Relevant tests pass, or every omitted check is explained above.
      相关测试已经通过，或已在上方说明未执行项及原因。
- [ ] No API keys, authorization headers, credentials, or sensitive payloads
      are included in code, configuration, logs, fixtures, or screenshots.
      代码、配置、日志、fixture 和截图中不包含 API Key、Authorization header、凭据或
      敏感 payload。
- [ ] User-facing behavior, compatibility evidence, and documentation are
      updated when applicable.
      如涉及用户行为或兼容性，相关证据与文档已经同步更新。
- [ ] BYOK remains explicit and fail-closed; the change never silently falls
      back to Cursor-hosted inference.
      BYOK 仍为显式调用并保持 fail-closed，不会静默回退到 Cursor 托管推理。
