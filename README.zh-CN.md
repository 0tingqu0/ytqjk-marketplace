# YTQJK Marketplace

[English](README.md)

YTQJK 是一个本地优先的 Codex 编排与知识库插件市场。当前版本 0.7.2 的安装器、
插件钩子、本地 RAG、会话锚点、SQLite v4 知识服务、Dashboard API、编排身份账本
和经复审的 Git handoff 全部由同一个跨平台 Go 二进制提供。Dashboard 前端保留为
普通的 HTML、CSS 和 JavaScript 静态资源。

## 环境要求

- Windows 10/11 x64、Linux x64 或 WSL2 x64
- Git（项目身份与 handoff 工作流需要）
- 开发时使用 Go 1.25 或更高版本

安装脚本会优先使用现有 Go；未找到兼容版本时，会下载 Go 1.27.0 官方归档并校验
SHA-256 后再构建 YTQJK。项目不再使用 Python 运行时、虚拟环境或 Python 依赖。

## 安装

Windows：

```powershell
.\install.ps1
```

Linux 或 WSL2：

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
ytqjk upgrade <check|apply|status|rollback> [选项]
```

示例：

```powershell
ytqjk rag bootstrap --project-root . --vector-mode auto
ytqjk session query --project-root . --session-id $env:CODEX_THREAD_ID '安装器架构'
ytqjk dashboard start
ytqjk upgrade check
```

当前 Go 检索实现会持久化无外部服务依赖的哈希字符/词 n-gram 稀疏向量，并将向量
相似度与词法得分混合。`--vector-mode off|auto|on` 分别用于禁用、自动启用或强制
启用该后端。全局库回退命中会写入受 1 GiB 上限约束的项目 SQLite 预取缓存，并在
全局索引指纹变化时按代失效；Dashboard 的文档/实体图谱、语义检索、相关推荐与最短
路径探索也全部由 Go 运行时生成。

## 事务式快照升级

本项目没有强行套用 Android 式完整 Virtual A/B。Codex 插件必须位于固定稳定路径，
Windows 也不适合依赖符号链接切槽；因此采用跨平台的 A/B-lite 事务式快照：

```powershell
ytqjk upgrade check
ytqjk upgrade apply --yes
ytqjk upgrade status
ytqjk upgrade rollback --yes
```

升级会验证固定 GitHub 仓库的正式 SemVer 发布、平台二进制、`SHA256SUMS`、安全压缩
路径和两个插件的版本清单。B 槽准备完成后，由独立 Go helper 等待旧进程退出，再替换
运行时和受管插件；新 Dashboard 必须报告预期版本才算激活。失败会自动恢复 A 槽。

系统只保留“当前版本 + 上一版本”。SQLite 知识库不作为代码槽直接切换：激活前使用
SQLite 一致性备份供故障自动回滚；人工回滚默认保留现有数据，并在旧版无法读取当前
schema 时拒绝执行。Dashboard 顶部版本按钮使用同一升级状态机。

## 项目结构

```text
cmd/ytqjk                 统一可执行文件
internal/cli              命令边界
internal/install          事务式安装与插件物化
internal/knowledge        SQLite v4 与治理写入服务
internal/rag              项目身份、索引、查询、预取缓存、会话锚点
internal/dashboard        仅回环 API、语义图谱、Dashboard 与 Workbench
internal/document         文档解析、OCR 边界与持久化入库任务
internal/tree             带预览/CAS 的知识库树
internal/peer             HMAC 认证的私有 peer 查询协议
internal/security         凭据路径与高置信度敏感内容扫描
internal/upgrade          事务式快照升级、健康检查与回滚
internal/orchestration    签名且绑定会话的运行/角色租约
internal/handoff          带哈希与路径白名单的 Git 交接包
plugins/                  Codex 清单、技能、钩子与静态资源
```

知识根目录按 `--knowledge-root`、`YTQJK_KNOWLEDGE_ROOT`、平台数据目录的顺序解析。
Windows、Linux 与 WSL 应使用各自的 SQLite 文件，不要跨宿主文件系统并发打开同一个
数据库。

## 安全边界

- Dashboard 与 Workbench 只监听 `127.0.0.1`，写请求校验本地 Host、Origin 与 JSON
  请求体。Peer 服务默认仅回环；局域网监听必须显式启用，并仍要求 HMAC 签名与重放保护。
- 索引拒绝符号链接、二进制、依赖/生成目录、凭据路径、私钥、令牌、会话材料与
  超大文件；全局索引只读取受治理的 `global/`、已批准或已验证目录，分组索引只读取
  已批准或已验证目录，两者都不包含候选。
- 新建和导入内容始终为候选，只有显式治理操作才能提升状态。Dashboard 候选编辑使用
  内容版本 CAS，审批会稳定读取、原子发布并刷新全局索引。
- 项目预取行只有在路径、行范围、内容与摘要都匹配当前受治理全局索引时才可返回；
  全局索引换代会清空旧缓存。
- Git handoff 必须位于仓库外，并用 SHA-256 绑定基础 HEAD、路径白名单、补丁、
  载荷和最终暂存快照。
- 安装和升级只操作受管目标；升级保留上一代快照，健康检查失败时自动回滚，不删除
  无关用户文件。

## 开发与验证

```powershell
go test ./...
go vet ./...
go build ./cmd/ytqjk
```

CI 在 Windows x64 与 Linux x64 上测试 Go 1.25/1.27，运行竞态检测、格式检查、
Go-only 迁移守卫，并构建当前支持的 Linux x64、Windows x64 发布二进制。

## 许可证

MIT。详见 [LICENSE](LICENSE) 与各插件的第三方声明。
