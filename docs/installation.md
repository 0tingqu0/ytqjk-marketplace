# YTQJK v0.7.0 Installation and Deployment

[简体中文](installation.zh-CN.md)

This guide applies to the stable v0.7.0 release. The default branch may contain
unreleased code and is not a production installation source.

## 1. Choose an installation source

Use the release bundle for normal deployment. It contains the built Go binary, both
plugins, the installer, and an internal release-manifest.json, so it does not need a
local Go toolchain.

Use a source checkout only for development, review, or a local build. Source
installation needs Go 1.25 or newer; the installer can download and verify Go 1.27.0
when no compatible toolchain exists.

Both paths require:

- Windows 10/11 x64, Linux x64, or WSL2 x64.
- Git and the Codex CLI.
- A working codex plugin command for all and codex-only modes.
- Node.js, npm, and npx only when all or ide-only must install a missing grill-me skill.
- On Linux, curl or wget, sha256sum or shasum, and tar for downloads or source bootstrap.

## 2. Windows release bundle

The commands below download to D:\Download, verify the archive, and extract it.
Open the terminal in the project that should be indexed before running the installer.

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
  throw 'The bundle directory already exists; choose a new empty destination.'
}
New-Item -ItemType Directory -Path $bundle | Out-Null
Expand-Archive -LiteralPath $archive -DestinationPath $bundle
~~~

Preview from the target project directory:

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

Apply only after confirming the previewed targets:

~~~powershell
$ErrorActionPreference = 'Stop'
& "$bundle\install.ps1" --mode all --target-root $targetRoot --project-root $projectRoot --apply --yes --json
if ($LASTEXITCODE -ne 0) {
  throw 'YTQJK installation failed.'
}
~~~

Do not use the v0.7.0 bundle without arguments. Its historical default binds the
target and project roots to the bundle directory. Always use the explicit commands
above. The default branch fixes this behavior for a future release by using
CODEX_HOME (or ~/.codex) as target and the caller's current directory as project.

## 3. Linux or WSL2 release bundle

~~~sh
set -eu

version=0.7.0
download_root="$HOME/Downloads/ytqjk-v$version"
bundle="$download_root/bundle"
archive="$download_root/ytqjk-linux-amd64.tar.gz"
base_url="https://github.com/0tingqu0/ytqjk-marketplace/releases/download/v$version"

mkdir -p "$download_root"
if [ -e "$bundle" ]; then
  printf '%s\n' 'The bundle directory already exists; choose a new empty destination.' >&2
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

Use CODEX_HOME as target_root when configured. Windows and WSL2 must use separate
knowledge roots and must not open the same SQLite file across host filesystems.

## 4. Source installation

Source installation is for development. Pin the v0.7.0 tag instead of building the
default branch for production.

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

A release bundle uses its bin directory directly. Only a source directory discovers
or downloads Go and builds a bootstrap executable.

## 5. Installed paths and command entry

The installer does not modify the user's PATH.

| Content | Windows default | Linux/WSL2 default |
| --- | --- | --- |
| Go runtime entry | %LOCALAPPDATA%\YTQJK\runtime\bin\ytqjk.exe | XDG_DATA_HOME/ytqjk/runtime/bin/ytqjk or $HOME/.local/share/ytqjk/runtime/bin/ytqjk |
| Codex root | %CODEX_HOME% or %USERPROFILE%\.codex | $CODEX_HOME or $HOME/.codex |
| Knowledge root | %YTQJK_KNOWLEDGE_ROOT% or %LOCALAPPDATA%\YTQJK\knowledge | $YTQJK_KNOWLEDGE_ROOT or XDG_DATA_HOME/ytqjk |
| Dashboard | http://127.0.0.1:8765/ | http://127.0.0.1:8765/ |

Windows:

~~~powershell
$ErrorActionPreference = 'Stop'
$ytqjk = Join-Path $env:LOCALAPPDATA 'YTQJK\runtime\bin\ytqjk.exe'
& $ytqjk version
& $ytqjk dashboard status
~~~

Linux or WSL2:

~~~sh
set -eu
ytqjk_bin="$HOME/.local/share/ytqjk/runtime/bin/ytqjk"
"$ytqjk_bin" version
"$ytqjk_bin" dashboard status
~~~

## 6. Acceptance checks

The installer must exit with code 0 and its JSON receipt must not contain FAILED or
UNKNOWN. Verify at least:

- runtime.status is ACTIVE.
- apply.status is APPLIED.
- project_bootstrap, codex_import, and dashboard_service have explicit terminal states.
- Both plugin manifests exist.
- version prints 0.7.0.
- dashboard status reports RUNNING and http://127.0.0.1:8765/ opens locally.

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

Restart Codex or create a new task after installation so the new plugins and skills
are loaded into a fresh session.

## 7. Upgrade and rollback

Upgrade accepts only stable SemVer releases from the fixed repository and verifies
the platform assets, SHA256SUMS, release manifest, plugin versions, and archive paths.
Failed health activation restores the previous generation automatically.

~~~powershell
$ErrorActionPreference = 'Stop'
& $ytqjk upgrade check
& $ytqjk upgrade apply --yes
& $ytqjk upgrade status
& $ytqjk upgrade rollback --yes
~~~

Keep the installation receipt. SQLite data is not a code slot: activation takes a
consistent backup, and manual rollback is refused when the previous binary cannot
read the current schema.

## 8. Uninstall

Run uninstall from the retained, verified external bundle. Do not run ytqjk uninstall
from the active runtime; the active runtime deliberately refuses to delete itself.

~~~powershell
$ErrorActionPreference = 'Stop'
$bundle = 'D:\Download\ytqjk-v0.7.0\bundle'
$targetRoot = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { "$HOME\.codex" }
& "$bundle\install.ps1" --uninstall --target-root $targetRoot --json
if ($LASTEXITCODE -ne 0) {
  throw 'YTQJK uninstall failed.'
}
~~~

Uninstall removes managed plugins, skills, the dashboard service, and the active
runtime, but preserves the knowledge database. Removing the knowledge root is a
separate destructive operation.

## 9. Troubleshooting

| Symptom | Cause and action |
| --- | --- |
| codex is not found | Install Codex CLI and confirm codex plugin list works. |
| npx is not found | Install Node.js/npm, or use codex-only when IDE skills are not needed. |
| ytqjk is not found | PATH is unchanged; use the full runtime path from section 5. |
| runtime update requires the authenticated upgrade workflow | Use upgrade apply instead of overwriting an existing runtime. |
| runtime uninstall must run from a verified installer bootstrap | Run uninstall from the retained v0.7.0 bundle. |
| Dashboard is not RUNNING | Run dashboard status and inspect dashboard_service and maintenance in the install receipt. |
| Linux download fails | Confirm curl or wget, sha256sum or shasum, and tar are installed. |
| WSL2 database is locked | Stop all handles and give Windows and WSL2 separate knowledge roots. |

Keep the complete JSON receipt for every failure. Do not copy plugin directories by
hand to bypass FAILED or UNKNOWN, and do not delete the knowledge root to hide an
installation problem.
