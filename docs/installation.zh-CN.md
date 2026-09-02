# YTQJK v0.7.0 安装与部署

[English](installation.md)

本文只适用于正式版 v0.7.0。默认分支可能包含未发布代码，不应直接作为生产安装来源。

## 1. 选择安装来源

推荐正式发布包。发布包包含已经构建的 Go 二进制、两个插件、安装入口和内部
release-manifest.json，不需要本机安装 Go。

只有开发、审查或自行构建时才使用源码安装。源码路径需要 Go 1.25 或更高版本；
安装脚本未发现兼容 Go 时会下载并校验 Go 1.27.0。

两种方式都要求：

- Windows 10/11 x64、Linux x64 或 WSL2 x64。
- Git 和 Codex CLI。
- all、codex-only 模式需要可执行的 codex plugin 命令。
- all、ide-only 在缺少 grill-me 时需要 Node.js、npm 和 npx。
- Linux 下载或源码自举需要 curl 或 wget、sha256sum 或 shasum、tar。

## 2. Windows 正式发布包

以下命令把下载内容放在 D:\Download，校验压缩包后再解压。先把终端切换到需要
建立知识索引的项目目录，再执行安装。

~~~powershell
$ErrorActionPreference = 'Stop'
$version = '0.7.0'
$downloadRoot = "D:\Download\ytqjk-v$version"
$bundle = Join-Path $downloadRoot 'bundle'
$archive = Join-Path $downloadRoot 'ytqjk-windows-amd64.zip'
$sums = Join-Path $downloadRoot 'SHA256SUMS'
$baseUrl = "https://github.com/0tingqu0/ytqjk-marketplace/releases/download/v$version"

New-Item -ItemType Directory -Force -Path $downloadRoot | Out-Null
Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/ytqjk-windows-amd64.zip" -OutFile $archive
Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/SHA256SUMS" -OutFile $sums

$sumLine = Get-Content -LiteralPath $sums -Encoding UTF8 |
  Where-Object { $_ -match '\s+ytqjk-windows-amd64\.zip$' } |
  Select-Object -First 1
if (-not $sumLine) {
  throw 'SHA256SUMS does not contain the Windows archive.'
}
$expected = ($sumLine -split '\s+')[0].ToLowerInvariant()
$actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash.ToLowerInvariant()
if ($actual -ne $expected) {
  throw 'YTQJK archive checksum verification failed.'
}

if (Test-Path -LiteralPath $bundle) {
  throw 'Bundle directory already exists; choose a new empty destination.'
}
New-Item -ItemType Directory -Path $bundle | Out-Null
Expand-Archive -LiteralPath $archive -DestinationPath $bundle
~~~

在目标项目目录中先预览：

~~~powershell
$ErrorActionPreference = 'Stop'
$bundle = 'D:\Download\ytqjk-v0.7.0\bundle'
$projectRoot = (Get-Location).Path
$targetRoot = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { "$HOME\.codex" }

& "$bundle\install.ps1" --mode all --target-root $targetRoot --project-root $projectRoot --json
if ($LASTEXITCODE -ne 0) {
  throw 'YTQJK install preview failed.'
}
~~~

确认预览目标正确后执行：

~~~powershell
$ErrorActionPreference = 'Stop'
& "$bundle\install.ps1" --mode all --target-root $targetRoot --project-root $projectRoot --apply --yes --json
if ($LASTEXITCODE -ne 0) {
  throw 'YTQJK installation failed.'
}
~~~

不要无参数运行 v0.7.0 发布包。它的历史默认值会把目标根和项目根都绑定到发布包目录，
必须使用上面的显式命令。默认分支已为后续版本修正：目标使用 CODEX_HOME（未配置时为
~/.codex），项目使用调用安装器时的当前目录。

## 3. Linux 或 WSL2 正式发布包

~~~sh
set -eu

version=0.7.0
download_root="$HOME/Downloads/ytqjk-v$version"
bundle="$download_root/bundle"
archive="$download_root/ytqjk-linux-amd64.tar.gz"
base_url="https://github.com/0tingqu0/ytqjk-marketplace/releases/download/v$version"

mkdir -p "$download_root"
if [ -e "$bundle" ]; then
  printf '%s\n' 'Bundle directory already exists; choose a new empty destination.' >&2
  exit 1
fi
mkdir "$bundle"
curl --fail --location "$base_url/ytqjk-linux-amd64.tar.gz" -o "$archive"
curl --fail --location "$base_url/SHA256SUMS" -o "$download_root/SHA256SUMS"
(
  cd "$download_root"
  grep 'ytqjk-linux-amd64.tar.gz$' SHA256SUMS | sha256sum -c -
)
tar -xzf "$archive" -C "$bundle"

project_root=$(pwd -P)
target_root="$HOME/.codex"
sh "$bundle/install.sh" --mode all --target-root "$target_root" \
  --project-root "$project_root" --json
sh "$bundle/install.sh" --mode all --target-root "$target_root" \
  --project-root "$project_root" --apply --yes --json
~~~

配置了 CODEX_HOME 时，把 target_root 改为该目录。Windows 与 WSL2 必须使用不同的
知识根，不能跨宿主并发打开同一个 SQLite 文件。

## 4. 源码安装

源码安装用于开发，不是正式部署的首选。固定到 v0.7.0 tag，不要从默认分支构建生产版本。

~~~powershell
$ErrorActionPreference = 'Stop'
$sourceRoot = 'D:\code\ytqjk-marketplace-v0.7.0'
$projectRoot = (Get-Location).Path
$targetRoot = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { "$HOME\.codex" }

git clone --branch v0.7.0 --depth 1 https://github.com/0tingqu0/ytqjk-marketplace.git $sourceRoot
if ($LASTEXITCODE -ne 0) {
  throw 'YTQJK source clone failed.'
}
& "$sourceRoot\install.ps1" --mode all --target-root $targetRoot --project-root $projectRoot --apply --yes --json
if ($LASTEXITCODE -ne 0) {
  throw 'YTQJK source installation failed.'
}
~~~

发布包会直接使用 bin 目录中的二进制；源码目录才会寻找或下载 Go 并执行构建。

## 5. 安装位置与命令入口

安装器不会修改用户 PATH。

| 内容 | Windows 默认位置 | Linux/WSL2 默认位置 |
| --- | --- | --- |
| Go 运行时入口 | %LOCALAPPDATA%\YTQJK\runtime\bin\ytqjk.exe | XDG_DATA_HOME/ytqjk/runtime/bin/ytqjk 或 $HOME/.local/share/ytqjk/runtime/bin/ytqjk |
| Codex 根 | %CODEX_HOME% 或 %USERPROFILE%\.codex | $CODEX_HOME 或 $HOME/.codex |
| 知识根 | %YTQJK_KNOWLEDGE_ROOT% 或 %LOCALAPPDATA%\YTQJK\knowledge | $YTQJK_KNOWLEDGE_ROOT 或 XDG_DATA_HOME/ytqjk |
| Dashboard | http://127.0.0.1:8765/ | http://127.0.0.1:8765/ |

Windows 后续命令：

~~~powershell
$ErrorActionPreference = 'Stop'
$ytqjk = Join-Path $env:LOCALAPPDATA 'YTQJK\runtime\bin\ytqjk.exe'
& $ytqjk version
& $ytqjk dashboard status
~~~

Linux 或 WSL2 后续命令：

~~~sh
set -eu
ytqjk_bin="$HOME/.local/share/ytqjk/runtime/bin/ytqjk"
"$ytqjk_bin" version
"$ytqjk_bin" dashboard status
~~~

## 6. 安装验收

安装进程必须返回 0，JSON 回执中不得出现 FAILED 或 UNKNOWN。至少检查：

- runtime.status 为 ACTIVE。
- apply.status 为 APPLIED。
- project_bootstrap、codex_import、dashboard_service 有明确终态。
- 两个插件清单存在。
- 运行时 version 输出为 0.7.0。
- dashboard status 返回 RUNNING，并能打开 http://127.0.0.1:8765/。

Windows：

~~~powershell
$ErrorActionPreference = 'Stop'
$codexRoot = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { "$HOME\.codex" }
$ytqjk = Join-Path $env:LOCALAPPDATA 'YTQJK\runtime\bin\ytqjk.exe'

if ((& $ytqjk version) -ne '0.7.0') {
  throw 'Unexpected YTQJK version.'
}
& $ytqjk dashboard status
if ($LASTEXITCODE -ne 0) {
  throw 'YTQJK dashboard status failed.'
}
Test-Path -LiteralPath "$codexRoot\plugins\ytqjk-agentic-orchestrator\.codex-plugin\plugin.json"
Test-Path -LiteralPath "$codexRoot\plugins\ytqjk-knowledge\.codex-plugin\plugin.json"
~~~

安装完成后重启 Codex 或新建任务，使新插件和技能进入新会话。

## 7. 升级与回滚

升级只接受固定仓库中的正式 SemVer Release，校验平台制品、SHA256SUMS、发布清单、
插件版本和安全压缩路径。失败的健康激活会自动恢复上一代。

~~~powershell
$ErrorActionPreference = 'Stop'
& $ytqjk upgrade check
& $ytqjk upgrade apply --yes
& $ytqjk upgrade status
& $ytqjk upgrade rollback --yes
~~~

升级前保留安装回执。SQLite 数据不作为代码槽切换；激活前会创建一致性备份，人工回滚
在旧二进制无法读取当前 schema 时会拒绝执行。

## 8. 卸载

必须从保留的、已经校验的外部发布包运行卸载。不要从活动运行时直接执行 ytqjk uninstall；
活动运行时会拒绝删除自身。

~~~powershell
$ErrorActionPreference = 'Stop'
$bundle = 'D:\Download\ytqjk-v0.7.0\bundle'
$targetRoot = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { "$HOME\.codex" }
& "$bundle\install.ps1" --uninstall --target-root $targetRoot --json
if ($LASTEXITCODE -ne 0) {
  throw 'YTQJK uninstall failed.'
}
~~~

卸载会移除受管插件、技能、Dashboard 服务和活动运行时，但保留知识数据库。删除知识根是
独立的破坏性操作，不属于普通卸载。

## 9. 常见故障

| 现象 | 原因与处理 |
| --- | --- |
| 找不到 codex | 安装 Codex CLI，确认 codex plugin list 可执行后重试。 |
| 找不到 npx | 安装 Node.js/npm，或使用不需要 IDE skill 的 codex-only 模式。 |
| 找不到 ytqjk | 安装器不修改 PATH；使用第 5 节的运行时完整路径。 |
| runtime update requires the authenticated upgrade workflow | 已存在运行时不能通过安装入口覆盖；使用 upgrade apply。 |
| runtime uninstall must run from a verified installer bootstrap | 从保留的 v0.7.0 发布包执行卸载。 |
| Dashboard 不是 RUNNING | 先执行 dashboard status，再检查安装回执中的 dashboard_service 与 maintenance。 |
| Linux 下载失败 | 确认 curl 或 wget、sha256sum 或 shasum、tar 可用。 |
| WSL2 数据库锁定 | Windows 与 WSL2 使用了同一知识根；关闭句柄并改为各自独立目录。 |

任何安装失败都应保留完整 JSON 回执。不要通过手工复制插件目录绕过 FAILED 或 UNKNOWN，
也不要删除知识根来掩盖安装问题。
