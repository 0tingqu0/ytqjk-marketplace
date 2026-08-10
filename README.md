# YTQJK Agentic Orchestrator

Codex 多任务编排插件。通过 `/ytqjk` 启动总控、监督、独立复审、唯一 Git 提交者、进度报告和本地 agentic RAG 工作流。

## 安装

```powershell
codex plugin marketplace add 0tingqu0/ytqjk-marketplace
codex plugin add ytqjk-agentic-orchestrator@ytqjk
```

重启 Codex 并新建任务，然后输入：

```text
/ytqjk
```

## 环境要求

- Windows、Git、Python 3、Node.js/npm。
- 可写的 `D:\knowledge`；个人知识、模型和 Python 运行时仅保存在本机，不随插件分发。
- 首次使用会在确认后执行 `npx skills@latest add mattpocock/skills`。该命令运行第三方最新代码并修改全局 skills。
- 启用向量检索时，会在确认相关信息后通过 pip 安装隔离依赖，并下载本地模型。

## 本地数据与安全

- RAG 仅索引 Git 已跟踪的文本文件，并在分块前排除常见敏感路径和高置信秘密内容；这不能替代项目自己的秘密扫描。
- `D:\knowledge` 会保存源码检索缓存、项目绝对路径、脱敏后的网络 remote 或本地 remote 指纹，以及模型和隔离运行时。
- 缓存不会自动删除。安全规则升级后必须重新索引；旧缓存可能仍含旧内容，只有用户明确批准后才删除。
- `D:\knowledge`、模型、SQLite/vector 数据库、handoff 和任何本机凭据均不在本仓库中。

安装前请检查 [插件清单](plugins/ytqjk-agentic-orchestrator/.codex-plugin/plugin.json) 和 [总控协议](plugins/ytqjk-agentic-orchestrator/skills/ytqjk/references/protocol.md)。

## 许可证

[MIT](LICENSE)，Copyright (c) 2026 一听曲就困。
