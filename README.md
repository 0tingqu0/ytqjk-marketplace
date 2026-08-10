# YTQJK Agentic Orchestrator

面向复杂项目的 Codex 多任务总控。它负责十段式计划、并行 Worker、独立监督与复审、唯一 Git 提交者、单独进度报告者，以及本机 agentic RAG 知识缓存；总控本身不处理实现。

支持 Windows、Linux、WSL2，以及 VS Code、Cursor、Windsurf 中的 Codex IDE extension。

## Codex 桌面版与 CLI

安装 plugin：

```powershell
codex plugin marketplace add 0tingqu0/ytqjk-marketplace
codex plugin add ytqjk-agentic-orchestrator@ytqjk
```

重启 Codex、新建任务，然后输入 `/ytqjk`。新激活的首条回复不调用工具：它会立即确认已启用、
说明尚未执行操作，并提出一个含推荐答案的问题；你回答后才延迟初始化协议、角色与 RAG。

## VS Code、Cursor 与 Windsurf

Codex IDE extension 当前不加载 plugins，但支持 standalone skills。请在目标项目的终端执行以下项目级安装；使用 Remote SSH、Dev Container 或 WSL 时，要在远端/容器/WSL 工作区终端执行：

```bash
npx skills@latest add https://github.com/0tingqu0/ytqjk-marketplace/tree/main/plugins/ytqjk-agentic-orchestrator/skills --agent codex --skill ytqjk --skill caveman --copy
```

重载 IDE 或新建聊天，然后输入 `$ytqjk`，也可以通过 `/skills` 选择 `ytqjk`。IDE 中不要使用 `/ytqjk`。

`skills@latest` 是 Vercel 维护的第三方 CLI，会下载并执行最新版代码并写入项目 skill 目录；安装前应检查来源。项目级安装只影响当前仓库。

## 环境要求

- Git、Python 3.10 或更高版本。
- Node.js/npm 必须满足 `npm view skills@latest engines`；发布时 `skills@1.5.22` 要求 Node.js 22.20.0 或更高版本。
- Linux 需要可用的 Python `venv` 模块；Ubuntu/Debian 通常由 `python3-venv` 提供。
- Windows 优先使用 `D:\knowledge`。没有 D 盘时使用 `%LOCALAPPDATA%\YTQJK\knowledge`。
- Linux/WSL2 使用 `${XDG_DATA_HOME:-$HOME/.local/share}/ytqjk`。
- `YTQJK_KNOWLEDGE_ROOT` 可显式覆盖任一平台默认值。WSL 不会自动复用 Windows 缓存；不要让 Windows 与 WSL 同时打开同一 SQLite/LanceDB 缓存。
- 只有当前用户配置未发现 `grill-me` 时，总控才会在确认后执行
  `npx skills@latest add mattpocock/skills`；暖启动不做 npm 或网络检查。启用向量检索时，会在确认相关信息后安装隔离依赖并下载本地模型。

## 本地数据与安全

- RAG 仅索引 Git 已跟踪的文本文件，并在分块前排除常见敏感路径和高置信秘密内容；这不能替代项目自己的秘密扫描。
- 知识根保存源码检索缓存、项目绝对路径、脱敏后的网络 remote 或本地 remote 指纹，以及模型和隔离运行时。
- 缓存不会自动删除。安全规则升级后必须重新索引；旧缓存可能仍含旧内容，只有用户明确批准后才删除。
- 知识根、模型、SQLite/vector 数据库、handoff 和任何本机凭据均不在本仓库中。

安装前请检查 [插件清单](plugins/ytqjk-agentic-orchestrator/.codex-plugin/plugin.json)、[总控协议](plugins/ytqjk-agentic-orchestrator/skills/ytqjk/references/protocol.md) 和 [知识库说明](plugins/ytqjk-agentic-orchestrator/skills/ytqjk/references/knowledge-store.md)。

## 引用与致谢

- 计划拷问使用 Matt Pocock 的 [`grill-me`](https://github.com/mattpocock/skills)，由用户指定的 `npx skills@latest add mattpocock/skills` 在缺失时安装；版权和许可证归原作者。
- 精简输出使用 Matt Pocock 历史版 `caveman` 的 MIT 授权快照。当前上游已移除此 skill，因此本插件随包分发审计版本；完整来源、改动说明与许可证见 [第三方声明](plugins/ytqjk-agentic-orchestrator/THIRD_PARTY_NOTICES.md)。
- skill 安装命令使用 Vercel Labs 的开源 [`skills` CLI](https://github.com/vercel-labs/skills)。插件和 skill 结构遵循 [OpenAI Codex Plugins](https://developers.openai.com/codex/plugins) 与 [Skills](https://developers.openai.com/codex/skills) 规范。
- OpenAI 的 `plugin-creator` 仅用于本项目的脚手架、cachebuster 和校验，不是运行时依赖。除以上明确列出的 skill 外，本插件没有内嵌其他人的插件代码。

## 许可证

[MIT](LICENSE)，Copyright (c) 2026 一听曲就困。
