# cursor-cli-byok

[English](README.md) | **简体中文**

`cursor-cli-byok` 是一个独立、显式调用的包装器，可让官方 Cursor CLI 使用用户提供的
OpenAI 兼容端点。它面向 headless Linux 服务器，无需安装 Cursor IDE、登录 Cursor
账号、运行桌面会话、配置中间人代理（MITM）或修改系统代理。

官方 `cursor-agent` 保持原样安装，不会被替换或修改。只有显式调用独立的
`cursor-cli-byok` 命令时才会启用 BYOK。

> **版本：** 以下安装命令以 `v0.1.0` 为目标版本。请通过
> [GitHub Releases](https://github.com/shiqkuangsan/cursor-cli-byok/releases)
> 确认版本是否可用并查看后续版本。

## 快速开始

### 安装

如果 Linux 主机已经安装 `cursor-agent`，可以先下载并检查安装脚本，然后跳过 Cursor
CLI 安装步骤：

```sh
curl --proto '=https' --tlsv1.2 -fsSL \
  https://raw.githubusercontent.com/shiqkuangsan/cursor-cli-byok/v0.1.0/scripts/install.sh \
  -o /tmp/install-cursor-cli-byok.sh
sh /tmp/install-cursor-cli-byok.sh --version v0.1.0 --skip-cursor-install
```

如果尚未安装官方 Cursor CLI，请去掉 `--skip-cursor-install`。选择其他版本时，安装脚本
URL 中的 tag 必须和 `--version` 参数保持一致。安装脚本只会把 Cursor CLI 的安装步骤
委托给 Cursor 官方脚本。整个过程无需 root 权限，会验证所选 amd64/arm64 Release
产物的 checksum，并默认把包装器安装到 `~/.local/bin/cursor-cli-byok`。

### 配置

把 Provider API Key 加载到 `OPENAI_API_KEY`。在交互式 Bash 会话中，可以使用以下方式
避免把 key 值写入 shell history：

```sh
read -r -s -p 'Provider API key: ' OPENAI_API_KEY
printf '\n'
export OPENAI_API_KEY
```

创建第一个 Provider alias：

```sh
cursor-cli-byok config init
```

依次输入 Provider Base URL 和上游模型。首次配置默认使用 `/v1/responses`，把上游模型
名称同时作为本地 alias；如果当前环境中存在 `OPENAI_API_KEY`，配置会记录
`api_key_env: OPENAI_API_KEY`。

等效的最小非交互式配置如下：

```sh
cursor-cli-byok config init --non-interactive \
  --base-url https://relay.example.com \
  --upstream-model gpt-5.4
```

### 运行

显式调用包装器。下面的只读 headless 命令会信任所选 workspace，并把最终回答输出到
stdout：

```sh
cursor-cli-byok --workspace "$PWD" --trust -p --mode ask \
  'Summarize this repository.'
```

每次调用时都必须提供配置所引用的 API Key 环境变量。已有配置不会被静默改写。

## 能力范围

| 范围 | 当前支持情况 |
| --- | --- |
| 主机 | Headless Linux amd64 和 arm64；非 root 安装 |
| Provider API | OpenAI Responses 和 Chat Completions streaming |
| Cursor 使用方式 | 交互式 CLI，以及 `text`、`json`、`stream-json` headless 输出 |
| Agent tools | Read、Write、Edit、Delete、List、Glob、Grep、Shell 和动态 stdio MCP |
| 运维命令 | `doctor`、`status`、`stop`，安全 XDG 配置，共享按需 daemon |
| 安全 | loopback TLS facade、mode-0600 状态/配置文件、从 Cursor 环境中移除 Provider key |

兼容性结论来自可执行验证，并不代表对 Cursor 私有协议的宽泛承诺。经过验证的官方
Cursor CLI 版本和平台记录在
[docs/compatibility.md](docs/compatibility.md)（英文）中。

## 文档

- [安装与首次使用](docs/getting-started.md)（英文）
- [Provider 与模型配置](docs/configuration.md)（英文）
- [Headless Shell、Node.js 与 CI 用法](docs/headless.md)（英文）
- [兼容性与验收证据](docs/compatibility.md)（英文）
- [Cursor CLI 协议边界](docs/protocol.md)（英文）
- [上游参考与独立实现记录](docs/upstream-reference.md)（英文）
- [变更记录](CHANGELOG.md)（英文）

## 从源码构建

从源码构建需要 Go 1.24 或更高版本：

```sh
make verify
make build
install -m 0755 dist/cursor-cli-byok ~/.local/bin/cursor-cli-byok
```

`make verify` 会执行格式检查、单元测试、race tests、`go vet`、installer/shell tests，
并构建静态 Linux amd64/arm64 产物。使用真实官方 Cursor CLI 的验收需要单独执行：

```sh
make e2e
make linux-e2e
```

这些命令不会创建 Git commit、tag、Release，也不会上传文件。

## 安全边界

远程 Provider URL 必须使用 HTTPS。只有字面意义上的 loopback 地址和 `localhost` 可以
使用明文 HTTP。包装器只解析当前 alias 的 key，把它同步到经过认证的本地 daemon
内存中，并在启动 `cursor-agent` 前移除所有已配置的 Provider key 环境变量。Key 不会
写入 daemon state、命令输出或日志。

`--trust`、执行模式、`--force` 和 `--approve-mcps` 仍然属于官方 Cursor 权限控制。
包装器不会静默添加这些参数。在自动化场景中允许写文件、Shell 或 MCP 访问前，请先
阅读 [docs/headless.md](docs/headless.md)（英文）。

## 独立性

本仓库不是 fork，在源码、构建、Git 和运行时层面都不依赖
canonical reference `sherkevin/cursor-agent-byok` 或 `leookun/cursor-byok`。项目初期曾参考
`shiqkuangsan/cursor-agent-byok` 和 `shiqkuangsan/cursor-byok` 这两个 fork；后续审查直接
对比它们的 canonical upstream。这些公开实现仅作为协议研究的 prior-art 参考；本项目
独立拥有自己的架构和实现。精确的已审阅 commit、被忽略的 `.labs/` clone 布局和维护
规则记录在 [docs/upstream-reference.md](docs/upstream-reference.md)（英文）中。

本项目与 Cursor 及上述两个参考项目均无关联。

## License

本项目采用 [MIT License](LICENSE)。
