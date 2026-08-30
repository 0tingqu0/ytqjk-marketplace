# YTQJK Marketplace

[English](README.md)

YTQJK 是一个本地优先的 Codex 编排与知识库插件市场。自 0.6.10 起，安装器、
插件钩子、本地 RAG、会话锚点、SQLite v4 知识服务、Dashboard API、编排身份账本
和经复审的 Git handoff 全部由同一个跨平台 Go 二进制提供。Dashboard 前端保留为
普通的 HTML、CSS 和 JavaScript 静态资源。

## 环境要求

- Windows 10/11、Linux、macOS 或 WSL2
- Git（项目身份与 handoff 工作流需要）
- 开发时使用 Go 1.25 或更高版本

安装脚本会优先使用现有 Go；未找到兼容版本时，会下载 Go 1.27.0 官方归档并校验
SHA-256 后再构建 YTQJK。项目不再使用 Python 运行时、虚拟环境或 Python 依赖。

## 安装

Windows：

```powershell
.\install.ps1
```

Linux 或 macOS：

```sh
sh ./install.sh
```

无参数安装会构建 `ytqjk`、安装两个插件、为当前项目建立索引、导入通过安全扫描的
Codex 知识候选，并启动本地 Dashboard。只查看计划而不修改系统：

```powershell
.\install.ps1 --mode all --target-root . --json
```

真正修改必须同时提供 `--target-root`、`--apply` 与 `--yes`。卸载会保留知识库：

```powershell
.\install.ps1 --uninstall
```

## 统一命令行

```text
ytqjk install [选项]
ytqjk uninstall [选项]
ytqjk rag <init|index|index-global|bootstrap|query> [选项]
ytqjk session <query|anchor|checkpoint|inspect|prepare-archive|finalize-archive> [选项]
ytqjk knowledge <create-project|create-candidate|edit|delete|state|snapshot|feedback|search|intake|workbench> [选项]
ytqjk dashboard <serve|start|stop|status|restart> [选项]
ytqjk orchestration <start-run|show-run|transition|grant|attest|verify> [选项]
ytqjk handoff <export|apply> [选项]
```

示例：

```powershell
ytqjk rag bootstrap --project-root . --vector-mode auto
ytqjk session query --project-root . --session-id $env:CODEX_THREAD_ID '安装器架构'
ytqjk dashboard start
```

当前 Go 检索实现为词法检索。`--vector-mode` 作为兼容的规划输入保留，在真实向量
后端接入前，回执会明确标记 `LEXICAL_ONLY`，不会虚报向量证据。

## 项目结构

```text
cmd/ytqjk                 统一可执行文件
internal/cli              命令边界
internal/install          事务式安装与插件物化
internal/knowledge        SQLite v4 与治理写入服务
internal/rag              项目身份、索引、查询、会话锚点
internal/dashboard        仅回环地址的 Dashboard 与 Workbench
internal/orchestration    签名且绑定会话的运行/角色租约
internal/handoff          带哈希与路径白名单的 Git 交接包
plugins/                  Codex 清单、技能、钩子与静态资源
```

知识根目录按 `--knowledge-root`、`YTQJK_KNOWLEDGE_ROOT`、平台数据目录的顺序解析。
Windows、Linux 与 WSL 应使用各自的 SQLite 文件，不要跨宿主文件系统并发打开同一个
数据库。

## 安全边界

- HTTP 只监听 `127.0.0.1`；写请求校验本地 Host、Origin 与 JSON 请求体。
- 索引拒绝符号链接、二进制、依赖/生成目录、凭据路径、私钥、令牌、会话材料与
  超大文件。
- 新建和导入内容始终为候选，只有显式治理操作才能提升状态。
- Git handoff 必须位于仓库外，并用 SHA-256 绑定基础 HEAD、路径白名单、补丁、
  载荷和最终暂存快照。
- 安装替换只操作受管目标，并使用局部快照回滚，不删除无关用户文件。

## 开发与验证

```powershell
go test ./...
go vet ./...
go build ./cmd/ytqjk
```

CI 在 Windows 与 Linux 上测试 Go 1.25/1.27，运行竞态检测、格式检查、Go-only
迁移守卫，并交叉编译 Linux、Windows 与 macOS 发布二进制。

## 许可证

MIT。详见 [LICENSE](LICENSE) 与各插件的第三方声明。
