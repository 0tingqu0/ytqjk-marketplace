# YTQJK Agentic Orchestrator

面向复杂项目的 Codex 多任务总控。它负责按自然任务粒度加权的计划、并行 Worker、独立监督与复审、唯一 Git 提交者、单独进度报告者，以及本机 agentic RAG 知识缓存；总控本身不处理实现。

知识库脚本支持 Windows、Linux 和 WSL2。VS Code、Cursor、Windsurf 中的 Codex IDE
extension 可加载独立 skills，但不加载 plugin；完整多会话编排还取决于宿主是否提供可见会话 API。

## 推荐：Git clone 后一键部署

推荐使用仓库根目录的无参数安装入口。它会把当前克隆目录同时作为安装目标和知识索引项目，
安装两个 Codex 插件、复制项目级 skills、导入当前用户允许导入的 Codex 候选资料，并
自动建立项目知识索引。安装只写入当前用户目录和本机知识根，不需要管理员或 root 权限，也不会下载
发布者的私人知识内容。

### 1. 检查运行环境

Windows 只需预先安装 Git 和 Python 3.10+。缺少 Node.js、npm、`npx` 或 Codex CLI 时，
`install.ps1` 会自动在 `%LOCALAPPDATA%\YTQJK\runtime` 准备便携 Node.js 24.15.0，校验
Node.js 官方 `SHASUMS256.txt`，再安装固定版 `@openai/codex@0.147.0`。该运行时不需要管理员
权限、不修改系统 PATH，只在本次安装进程中使用；重复运行会验证并复用有效运行时。

先确认 Git 和 Python：

```powershell
git --version
python --version
```

Linux、macOS 或 WSL 将 `python` 改为 `python3`，并仍需预先提供 Node.js/npm、`npx` 和
Codex CLI。使用 Remote SSH、Dev Container 或 WSL 时，必须在远端、容器或 WSL 终端内
执行检查和安装，不能在 Windows 宿主安装后直接当作远端结果。

### 2. 克隆并安装

Windows PowerShell：

```powershell
git clone https://github.com/0tingqu0/ytqjk-marketplace.git
Set-Location .\ytqjk-marketplace
.\install.ps1
```

Linux、macOS 或 WSL：

```bash
git clone https://github.com/0tingqu0/ytqjk-marketplace.git
cd ytqjk-marketplace
sh ./install.sh
```

脚本会立即打印启动提示，并实时转发依赖下载和 Codex 插件安装输出。Windows 首次运行需要
访问 `nodejs.org`、npm registry、GitHub 和 Codex 插件源，会下载并执行 Node.js、固定版本
Codex CLI、Codex 插件及第三方 `skills` CLI；请在受信任网络中执行。首次运行可能需要几分钟，
不要在尚有输出时关闭终端。安装成功后进程返回 `0`，终端最后输出 JSON 回执。

### 3. 验证安装结果

首次完整部署的回执应满足：

- `apply.status` 为 `APPLIED`。
- `cli_runtime.status` 为 `SYSTEM`、`BOOTSTRAPPED` 或 `REUSED`；后两者表示使用用户目录中的
  便携运行时。
- `knowledge_bootstrap.status` 为 `SUCCEEDED`。
- `knowledge_import.status` 为 `SUCCEEDED`；重复安装可能为 `SKIPPED_MARKER`。如果当前用户的
  历史资料中有个别文件无法解析，则为 `SUCCEEDED_WITH_WARNINGS`，可用资料仍会导入，且不影响
  插件和项目知识库部署。
- `knowledge_import.discovered_count` 为 `0` 表示当前用户没有可导入资料，不是安装失败。
- `apply.codex_plugins.stable_paths` 包含两个稳定插件目录。

Windows PowerShell 可进一步检查稳定插件和知识库布局：

```powershell
$knowledgeRoot = if (Test-Path 'D:\') {
  'D:\knowledge'
} else {
  Join-Path $env:LOCALAPPDATA 'YTQJK\knowledge'
}

Test-Path "$HOME\.codex\plugins\ytqjk-agentic-orchestrator\.codex-plugin\plugin.json"
Test-Path "$HOME\.codex\plugins\ytqjk-knowledge\.codex-plugin\plugin.json"
Test-Path "$knowledgeRoot\config.json"
Test-Path "$knowledgeRoot\catalog.json"
Get-ChildItem "$knowledgeRoot\projects" -Directory
```

Linux、macOS 或 WSL：

```bash
knowledge_root="${YTQJK_KNOWLEDGE_ROOT:-${XDG_DATA_HOME:-$HOME/.local/share}/ytqjk}"

test -f "$HOME/.codex/plugins/ytqjk-agentic-orchestrator/.codex-plugin/plugin.json"
test -f "$HOME/.codex/plugins/ytqjk-knowledge/.codex-plugin/plugin.json"
test -f "$knowledge_root/config.json"
test -f "$knowledge_root/catalog.json"
find "$knowledge_root/projects" -mindepth 1 -maxdepth 1 -type d -print
```

随后重启 Codex 或新建任务，输入 `$ytqjk`。网页端不会作为后台服务自动启动；需要时按
[知识库控制台](#知识库控制台)中的命令启动，它只绑定 `127.0.0.1`。

### 4. 自定义安装

需要预览或分别指定安装目标和知识索引项目时，传入参数会保留 dry-run 行为：

```bash
python3 setup.py --mode all --target-root /path/to/install \
  --project-root /path/to/project --json
```

确认后显式应用：

```powershell
.\install.ps1 --mode all --target-root C:\path\to\install `
  --project-root C:\path\to\project --apply --yes --json
```

`--target-root` 只决定安装位置，永远不会隐式成为知识索引项目。项目索引只读取显式
`--project-root`；未配置时回执为 `NOT_CONFIGURED`。`--project-bootstrap off` 可跳过项目索引
初始化。正常应用成功返回 `0`；默认自动导入中的单文件解析告警也返回 `0`，不可恢复的候选
资料导入失败或 `--codex-import force` 解析失败返回 `3`，项目索引初始化失败返回 `4`，安装或
参数错误返回 `2`。JSON 回执不包含项目绝对路径或知识内容。

## 卸载历史版本

已安装过的 YTQJK 版本可通过当前安装器统一卸载。默认仅输出将移除的插件和技能目录；确认后
再执行。卸载只处理 YTQJK 自身的 Codex 插件、marketplace 和技能目录，不会删除第三方
`grill-me`、知识库数据或 `%LOCALAPPDATA%\YTQJK\runtime` 便携运行时。该运行时需要清理时，
可在确认没有安装进程运行后手工删除这个精确目录。

```powershell
.\install.ps1 --uninstall --mode all --apply --yes --target-root C:\path\to\project
```

可将 `all` 改为 `codex-only`、`ide-only` 或 `knowledge-only` 以缩小范围。

`all` 和 `knowledge-only` 首次成功应用后默认执行 Codex 资料候选导入。来源根按
`--codex-root`、`CODEX_HOME`、`~/.codex` 的顺序解析，不从 `--target-root` 推导；仅处理
`mem.md` 以及 `memories/`、`knowledge/`、`attachments/` 中受支持且通过安全检查的文件。
`memories/` 中无扩展名的普通文件仅按严格 UTF-8 文本处理；未配置解析器的 Office、图片和
音频只计入 `not_configured_count`，不会伪报为已导入。凭据、token、secret、auth、config、
session、日志、缓存、plugin、skill、worktree 和 archive 命名族永久排除，不会打开读取。
所有新来源都以独立 `CANDIDATE` 证明入库；内容即使与 approved/verified 文档重复，也不会
继承批准状态或写入已批准版本的来源列表。
目标按 `--knowledge-root`、`YTQJK_KNOWLEDGE_ROOT` 和平台默认目录的顺序解析，固定写入
`<knowledge-root>/service/knowledge.sqlite3` 的 `global-candidates` 范围，不与检索缓存数据库
混用。使用 `--codex-import off` 禁用，或使用 `--codex-import force` 重试已标记的导入。
Dry-run 不读取 Codex 资料。默认 `auto` 模式会隔离无法解析的单个历史文件，继续导入其余安全
资料；回执为 `SUCCEEDED_WITH_WARNINGS`，并保留 `parse_failed_count`、`failure_stage=PARSING`
和 `failure_code=PARSE_FAILED`，进程返回码为 `0`。使用 `--codex-import force` 时仍严格失败，
便于人工修复后重试。不可恢复的导入失败不会回滚已成功安装的文件，回执中 `apply.status` 仍为
`APPLIED`、`knowledge_import.status` 为 `FAILED`，进程返回码为 `3`。

## Codex 桌面版与 CLI

仅安装 plugin（不初始化指定项目的索引）：

```powershell
codex plugin marketplace add 0tingqu0/ytqjk-marketplace
codex plugin add ytqjk-agentic-orchestrator@ytqjk
codex plugin add ytqjk-knowledge@ytqjk
```

这会同时安装总控和本地知识库插件。重启 Codex、新建任务，然后输入 `$ytqjk`；也可以通过 `/skills` 选择 `ytqjk`。仅当当前宿主
明确提供 `/ytqjk` 快捷命令时才使用该写法，不把它作为可移植入口。裸调用尚未给出明确目标时，它会留在当前激活任务，
每次只问一个带推荐答案的目标问题，不调用工具、不创建任何总控或其他角色；调用中已包含可执行目标，或你随后给出明确目标时，不再重复要求确认。目标确认后的首个工具调用
才读取协议并创建总控；目标确认不等于计划批准，后续仍执行 `grill-me`、监督和计划批准门。

### 宿主能力边界

| 使用方式 | 可加载内容 | 完整多会话编排 |
| --- | --- | --- |
| Codex 桌面版 | plugin 与 bundled skills | 宿主提供会话创建、列出、读取、等待和消息 API 时可用；标题缺失只影响跨次复用 |
| Codex CLI | marketplace plugin 与 bundled skills | 需要同一组核心可见会话 API；能力检测不通过时返回 `BLOCKED` |
| Codex IDE extension | 项目级 standalone skills；不加载 plugin | 需要同一组核心可见会话 API；能力检测不通过时返回 `BLOCKED`，改用具备这些 API 的桌面版或 CLI 宿主 |

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

通过 Git clone 一键部署的用户，在原克隆目录中只做快进更新，然后重新运行安装入口。安装器会
更新本项目 manifest 管理的插件目录并复用已有知识库，不会删除候选资料或已批准资料：

```powershell
git pull --ff-only
.\install.ps1
```

Linux、macOS 或 WSL：

```bash
git pull --ff-only
sh ./install.sh
```

Codex 桌面版在 Plugins 页面卸载后重新安装。Codex CLI 输入 `/plugins` 打开插件浏览器，
在 `ytqjk` marketplace 中卸载后重新安装；也可从已配置的 marketplace 重新执行安装命令：

```bash
codex plugin marketplace upgrade ytqjk
codex plugin add ytqjk-agentic-orchestrator@ytqjk
codex plugin add ytqjk-knowledge@ytqjk
```

安装完成后必须新建任务，已有任务不会重新载入 bundled skills。当前正式发布版本为纯
SemVer `0.4.2`；`+codex.*` 仅供本地开发临时缓存刷新，不提交、不进入正式发布清单。

IDE 项目级 skills 更新后重载 IDE 或新建聊天：

```bash
npx skills@latest update ytqjk caveman -p
```

若更新命令无法识别旧安装记录，重新执行上面的项目级 `skills add` 命令。

已有对应发布 tag 时，可将 marketplace 固定回该 tag；发布者未创建 tag 时不能使用此回滚流程：

```bash
codex plugin marketplace remove ytqjk
codex plugin marketplace add 0tingqu0/ytqjk-marketplace --ref <release-tag>
codex plugin add ytqjk-agentic-orchestrator@ytqjk
codex plugin add ytqjk-knowledge@ytqjk
```

IDE 回滚使用相同 tag 的 skills 路径重新安装：

```bash
npx skills@latest add https://github.com/0tingqu0/ytqjk-marketplace/tree/<release-tag>/plugins/ytqjk-agentic-orchestrator/skills --agent codex --skill ytqjk --skill caveman --copy
```

## 环境要求

- Git、Python 3.10 或更高版本。
- Windows 缺少 Node.js/npm、`npx` 或 Codex CLI 时会自动使用用户级便携运行时；Linux、macOS
  和 WSL 仍需自行安装这些命令。
- Node.js/npm 必须满足 `npm view skills@latest engines`；当前 Windows 自举版本为 Node.js
  24.15.0，Codex CLI 固定为 0.147.0。发布时 `skills@1.5.22` 要求 Node.js 22.20.0 或更高版本。
- Linux 需要可用的 Python `venv` 模块；Ubuntu/Debian 通常由 `python3-venv` 提供。
- Windows 优先使用 `D:\knowledge`。没有 D 盘时使用 `%LOCALAPPDATA%\YTQJK\knowledge`。
- Linux/WSL2 使用 `${XDG_DATA_HOME:-$HOME/.local/share}/ytqjk`。
- `YTQJK_KNOWLEDGE_ROOT` 可显式覆盖任一平台默认值。WSL 不会自动复用 Windows 缓存；不要让 Windows 与 WSL 同时打开同一 SQLite/LanceDB 缓存。
- RAG 首次查询前会刷新缺失、过期或安全版本不兼容的项目与全局索引。`auto` 仅在文本达到
  10 MiB 或 2,000 个分块时启用向量；小知识库不会因连续空查询而自动下载模型。
- 常规查询不重新扫描整个总库，只校验实际命中的来源文件；单次查询最多等待 60 秒，超时
  返回可重试错误，避免会话长期卡住。
- 所有会话检索都先查当前项目子库；命中即结束，未命中才回源总库。总库命中会写入当前
  项目子库，总库仍未命中则返回 `KNOWLEDGE_MISS`，由当前会话外部检索并通过候选接口提交。
  会话不能切换项目或读取其他项目子库。每个项目子库总容量为 1 GiB，按 LFU+LRU 淘汰，
  优先保留多次命中的知识。
- 只有当前用户配置未发现 `grill-me` 时，总控才会在确认后执行
  `npx skills@latest add mattpocock/skills --agent codex --skill grill-me --yes --copy`；暖启动不做 npm 或网络检查。启用向量检索时，会在确认相关信息后安装隔离依赖并下载本地模型。

## 知识库控制台

在本机管理控制台查看知识根、项目索引、已验证/已批准经验和候选经验，并投递、编辑、删除或
人工批准候选资料。

Windows PowerShell：

```powershell
python "$HOME\.codex\plugins\ytqjk-agentic-orchestrator\skills\ytqjk\dashboard\knowledge_dashboard.py"
```

Linux 或 WSL2：

```bash
python3 ~/.codex/plugins/ytqjk-agentic-orchestrator/skills/ytqjk/dashboard/knowledge_dashboard.py
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

控制台启动后会检查 `0tingqu0/ytqjk-marketplace` 的最新正式 GitHub Release。发现更高版本时，
页面顶部显示版本和“更新”按钮；确认后会下载固定仓库的 Release 包，校验安全路径及两个插件
manifest 的版本一致性，再调用原子安装器更新 `~/.codex/plugins` 下的稳定插件目录。失败会保留
当前版本，知识库数据不会删除；成功后重启 Codex 即可加载新版本。草稿、预发布版本、非纯
SemVer tag 和其他下载地址不会进入自动更新。`0.3.2` 及更早版本尚无网页更新入口，需要先按
[更新与回滚](#更新与回滚)执行一次 `git pull --ff-only` 和安装脚本，升级到 `0.4.0` 或更高
版本后才会收到后续网页更新提醒。

每次投递后会自动评估是否具备批准条件，并在候选资料中记录结论和原因：需有可解析内容、
至少 200 个有效字符，以及来源、证据或验证线索。评估通过仅表示“可提交批准审阅”，不会
自动进入 `approved` 或参与全局索引。
外部资料会作为候选资料包保存：原件保留、总览记录分析结果，正文会优先按标题和段落拆为
约 1,800 字符以内的知识片段。片段都含来源文件、片段序号和父资料 ID；删除总览会一并
删除关联片段与原件。

一键安装成功后，安装器从当前克隆的发布包复制并以 manifest 管理两个稳定用户目录：
`~/.codex/plugins/ytqjk-agentic-orchestrator` 和
`~/.codex/plugins/ytqjk-knowledge`（Windows 为 `$HOME\.codex\plugins\...`）。这些目录不依赖
Codex marketplace 的版本化 cache；重复安装可安全更新本项目受管目录，卸载只删除清单明确
拥有的上述两个目录。

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
`[YTQJK][项目ID]` 职责会话，并恢复其活动锚点摘要；只有找不到合适会话、旧会话或锚点已归档、
会话持续失联或职责冲突时才创建新会话。它使用 Codex 会话协作，不把当前会话替换为智能体。

## 本地数据与安全

- 插件在目标确认后保持轻量；首次出现可由项目回答的问题或首次索引需求时，才自动构建当前会话工作目录的完整知识库：建立独立项目子库、索引项目安全文本，并刷新只包含已验证/已批准内容的总库索引。该流程不要求目录是 Git 仓库，且不会自动批准候选资料。Git 项目仅索引已跟踪的文本文件；普通目录会安全扫描其中的常规文本文件。两种模式都会在分块前排除常见敏感路径和高置信秘密内容；这不能替代项目自己的秘密扫描。
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
