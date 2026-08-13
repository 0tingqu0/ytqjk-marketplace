# YTQJK Agentic Orchestrator

面向复杂项目的 Codex 多任务总控。它负责十段式计划、并行 Worker、独立监督与复审、唯一 Git 提交者、单独进度报告者，以及本机 agentic RAG 知识缓存；总控本身不处理实现。

知识库脚本支持 Windows、Linux 和 WSL2。VS Code、Cursor、Windsurf 中的 Codex IDE
extension 可加载独立 skills，但不加载 plugin；完整多会话编排还取决于宿主是否提供可见会话 API。

## Codex 桌面版与 CLI

安装 plugin：

```powershell
codex plugin marketplace add 0tingqu0/ytqjk-marketplace
codex plugin add ytqjk-agentic-orchestrator@ytqjk
```

重启 Codex、新建任务，然后输入 `$ytqjk`；也可以通过 `/skills` 选择 `ytqjk`。仅当当前宿主
明确提供 `/ytqjk` 快捷命令时才使用该写法，不把它作为可移植入口。目标明确并由你显式确认前，它会留在当前激活任务，
每次只问一个带推荐答案的目标问题，不调用工具、不创建任何总控或其他角色。确认后的首个工具调用
才读取协议并创建总控；目标确认不等于计划批准，后续仍执行 `grill-me`、监督和计划批准门。

### 宿主能力边界

| 使用方式 | 可加载内容 | 完整多会话编排 |
| --- | --- | --- |
| Codex 桌面版 | plugin 与 bundled skills | 宿主提供会话创建、列出、读取、等待、消息和标题 API 时可用 |
| Codex CLI | marketplace plugin 与 bundled skills | 需要同一组完整可见会话 API；能力检测不通过时返回 `BLOCKED` |
| Codex IDE extension | 项目级 standalone skills；不加载 plugin | 需要同一组完整可见会话 API；能力检测不通过时返回 `BLOCKED`，改用具备这些 API 的桌面版或 CLI 宿主 |

安装成功只表示 skill 可被发现，不代表宿主具备完整多会话控制能力。置顶和归档属于可选增强；
缺失时进度会话保持可见，完成会话标记为 `DONE`。YTQJK 不会用隐藏智能体或当前会话内角色扮演
绕过核心能力检查。

## VS Code、Cursor 与 Windsurf

Codex IDE extension 当前不加载 plugins，但支持 standalone skills。请在目标项目的终端执行以下项目级安装；使用 Remote SSH、Dev Container 或 WSL 时，要在远端/容器/WSL 工作区终端执行：

```bash
npx skills@latest add https://github.com/0tingqu0/ytqjk-marketplace/tree/main/plugins/ytqjk-agentic-orchestrator/skills --agent codex --skill ytqjk --skill caveman --copy
```

重载 IDE 或新建聊天，然后输入 `$ytqjk`，也可以通过 `/skills` 选择 `ytqjk`。IDE 中不要使用 `/ytqjk`。

`skills@latest` 是 Vercel 维护的第三方 CLI，会下载并执行最新版代码并写入项目 skill 目录；安装前应检查来源。项目级安装只影响当前仓库。

## 更新与回滚

Codex 桌面版在 Plugins 页面卸载后重新安装。Codex CLI 输入 `/plugins` 打开插件浏览器，
在 `ytqjk` marketplace 中卸载后重新安装；也可从已配置的 marketplace 重新执行安装命令：

```bash
codex plugin add ytqjk-agentic-orchestrator@ytqjk
```

安装完成后必须新建任务，已有任务不会重新载入 bundled skills。正式发布版本使用纯
SemVer；`0.2.0` 发布后不覆盖，同一发布线的后续修复使用 `0.2.1`、`0.2.2`。`+codex.*`
仅供本地开发缓存刷新，不进入正式发布清单。

IDE 项目级 skills 更新后重载 IDE 或新建聊天：

```bash
npx skills@latest update ytqjk caveman -p
```

若更新命令无法识别旧安装记录，重新执行上面的项目级 `skills add` 命令。

已有对应发布 tag 时，可将 marketplace 固定回指定版本；以下以 `v0.2.0` 为例：

```bash
codex plugin marketplace remove ytqjk
codex plugin marketplace add 0tingqu0/ytqjk-marketplace --ref v0.2.0
codex plugin add ytqjk-agentic-orchestrator@ytqjk
```

IDE 回滚使用相同 tag 的 skills 路径重新安装：

```bash
npx skills@latest add https://github.com/0tingqu0/ytqjk-marketplace/tree/v0.2.0/plugins/ytqjk-agentic-orchestrator/skills --agent codex --skill ytqjk --skill caveman --copy
```

## 环境要求

- Git、Python 3.10 或更高版本。
- Node.js/npm 必须满足 `npm view skills@latest engines`；发布时 `skills@1.5.22` 要求 Node.js 22.20.0 或更高版本。
- Linux 需要可用的 Python `venv` 模块；Ubuntu/Debian 通常由 `python3-venv` 提供。
- Windows 优先使用 `D:\knowledge`。没有 D 盘时使用 `%LOCALAPPDATA%\YTQJK\knowledge`。
- Linux/WSL2 使用 `${XDG_DATA_HOME:-$HOME/.local/share}/ytqjk`。
- `YTQJK_KNOWLEDGE_ROOT` 可显式覆盖任一平台默认值。WSL 不会自动复用 Windows 缓存；不要让 Windows 与 WSL 同时打开同一 SQLite/LanceDB 缓存。
- RAG 首次查询前会刷新缺失、过期或安全版本不兼容的项目与全局索引。`auto` 仅在文本达到
  10 MiB 或 2,000 个分块时启用向量；小知识库不会因连续空查询而自动下载模型。
- 所有会话检索都先查当前项目子库；命中即结束，未命中才回源总库。总库命中会写入当前
  项目子库，总库仍未命中则返回 `KNOWLEDGE_MISS`，由当前会话外部检索并通过候选接口提交。
  会话不能切换项目或读取其他项目子库。每个项目子库总容量为 1 GiB，按 LFU+LRU 淘汰，
  优先保留多次命中的知识。
- 只有当前用户配置未发现 `grill-me` 时，总控才会在确认后执行
  `npx skills@latest add mattpocock/skills`；暖启动不做 npm 或网络检查。启用向量检索时，会在确认相关信息后安装隔离依赖并下载本地模型。

## 知识库控制台

在本机管理控制台查看知识根、项目索引、已验证/已批准经验和候选经验，并投递、编辑、删除或
人工批准候选资料。

Windows PowerShell：

```powershell
python plugins\ytqjk-agentic-orchestrator\skills\ytqjk\dashboard\knowledge_dashboard.py
```

Linux 或 WSL2：

```bash
python3 plugins/ytqjk-agentic-orchestrator/skills/ytqjk/dashboard/knowledge_dashboard.py
```

打开 `http://127.0.0.1:8765`。服务只绑定本机回环地址；可以拖入、选择或粘贴文本、
Word（`.docx`）、PowerPoint（`.pptx`）、Excel（`.xlsx`/`.csv`）、常见图片和音频资料，最大
10 MiB。Office 正文和表格会被提取分析，图片记录格式、尺寸和文件大小；WAV 音频记录声道、
采样率和时长，其他音频只记录格式。音频不做语音识别或转写。原文件随候选
分析记录保存在 `imports/originals`。候选资料不会自动批准、不会进入 `verified`、不会
自动重新索引。敏感文件名和提取文本中的高置信凭据会被拒绝。
此外支持常见 UTF-8 源码、配置和数据文本（如 `.py`、`.ts`、`.java`、`.go`、`.sql`、
`.xml`、`.toml`、`.ini`、`.sh`、`.ps1`、`.diff`、`.jsonl`、`.svg`）。旧版二进制 Office
格式（`.doc`、`.ppt`、`.xls`）需要先转换为现代格式后投递。
候选资料可在控制台中编辑或删除，已验证和已批准知识不提供此入口；删除投递资料时会一并
删除其关联原件。
每次投递后会自动评估是否具备批准条件，并在候选资料中记录结论和原因：需有可解析内容、
至少 200 个有效字符，以及来源、证据或验证线索。评估通过仅表示“可提交批准审阅”，不会
自动进入 `approved` 或参与全局索引。
外部资料会作为候选资料包保存：原件保留、总览记录分析结果，正文会优先按标题和段落拆为
约 1,800 字符以内的知识片段。片段都含来源文件、片段序号和父资料 ID；删除总览会一并
删除关联片段与原件。

## 会话锚定

YTQJK 创建的总控、监督、复审、Git、进度、RAG 和 Worker 会话会在知识根建立匿名锚点。
压缩恢复时取回该会话的脱敏任务摘要；归档时将可复用经验写入候选知识区。锚点不保存原始
会话 ID 或完整对话。同一会话每次调用知识库只刷新同一个匿名锚点，不会重复建立或重复导出
未变化的经验。支持定时任务的宿主可运行以下命令，回收 30 天未活动且有摘要的会话：

Windows PowerShell：

```powershell
python plugins\ytqjk-agentic-orchestrator\skills\ytqjk\scripts\session_memory.py sweep --days 30
```

Linux 或 WSL2：

```bash
python3 plugins/ytqjk-agentic-orchestrator/skills/ytqjk/scripts/session_memory.py sweep --days 30
```

Codex 未向插件开放全局新会话、自动压缩和闲置事件订阅时，插件无法自行监听所有会话；此时
仅对 YTQJK 创建并由其生命周期流程管理的会话执行锚定与回收。

启用 `$ytqjk` 并确认目标后，会优先在 Codex 中复用当前项目已有、未归档的
`[YTQJK][项目ID]` 职责会话，并恢复其锚点摘要；只有找不到合适会话、旧会话已归档或职责
冲突时才创建新会话。它使用 Codex 会话协作，不把当前会话替换为智能体。

## 本地数据与安全

- 插件在目标确认后自动构建当前会话工作目录的完整知识库：建立独立项目子库、索引项目安全文本，并刷新只包含已验证/已批准内容的总库索引。该流程不要求目录是 Git 仓库，且不会自动批准候选资料。Git 项目仅索引已跟踪的文本文件；普通目录会安全扫描其中的常规文本文件。两种模式都会在分块前排除常见敏感路径和高置信秘密内容；这不能替代项目自己的秘密扫描。
- 知识根保存源码检索缓存、项目绝对路径、脱敏后的网络 remote 或本地 remote 指纹，以及模型和隔离运行时。
- 项目知识子库为可重建缓存，达到 1 GiB 硬上限时会按 LFU+LRU 自动淘汰回源知识，必要时
  丢弃可重建向量/词法索引。安全规则升级产生的旧缓存不因升级自动删除，任意手工删除仍需明确批准。
- 知识根、模型、SQLite/vector 数据库、handoff 和任何本机凭据均不在本仓库中。

安装前请检查 [插件清单](plugins/ytqjk-agentic-orchestrator/.codex-plugin/plugin.json)、[总控协议](plugins/ytqjk-agentic-orchestrator/skills/ytqjk/references/protocol.md) 和 [知识库说明](plugins/ytqjk-agentic-orchestrator/skills/ytqjk/references/knowledge-store.md)。

## 引用与致谢

- 计划拷问使用 Matt Pocock 的 [`grill-me`](https://github.com/mattpocock/skills)，由用户指定的 `npx skills@latest add mattpocock/skills` 在缺失时安装；版权和许可证归原作者。
- 精简输出使用 Matt Pocock 历史版 `caveman` 的 MIT 授权快照。当前上游已移除此 skill，因此本插件随包分发审计版本；完整来源、改动说明与许可证见 [第三方声明](plugins/ytqjk-agentic-orchestrator/THIRD_PARTY_NOTICES.md)。
- skill 安装命令使用 Vercel Labs 的开源 [`skills` CLI](https://github.com/vercel-labs/skills)。插件和 skill 结构遵循 [OpenAI Codex Plugins](https://developers.openai.com/codex/plugins) 与 [Skills](https://developers.openai.com/codex/skills) 规范。
- OpenAI 的 `plugin-creator` 仅用于本项目的脚手架、cachebuster 和校验，不是运行时依赖。除以上明确列出的 skill 外，本插件没有内嵌其他人的插件代码。

## 许可证

[MIT](LICENSE)，Copyright (c) 2026 一听曲就困。
