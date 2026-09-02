param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$GoVersion = '1.27.0'
$GoSHA256 = 'f0c0a0d33ba94f4d2c5dbc887334ce678b21813504ddb3aafcb06e60a5a667c4'

function Test-GoRuntime {
  param([string]$Executable)
  if (-not $Executable -or -not (Test-Path -LiteralPath $Executable -PathType Leaf)) {
    return $false
  }
  $versionText = & $Executable version 2>$null
  if ($LASTEXITCODE -ne 0 -or $versionText -notmatch 'go([0-9]+)\.([0-9]+)') {
    return $false
  }
  return ([int]$Matches[1] -gt 1) -or
    ([int]$Matches[1] -eq 1 -and [int]$Matches[2] -ge 25)
}

function Resolve-GoRuntime {
  $command = Get-Command go -ErrorAction SilentlyContinue
  if ($command -and (Test-GoRuntime $command.Source)) {
    return $command.Source
  }
  $localData = $env:LOCALAPPDATA
  if (-not $localData) {
    $localData = Join-Path $env:USERPROFILE 'AppData\Local'
  }
  $toolchainRoot = Join-Path $localData "YTQJK\runtime\toolchains\go$GoVersion"
  $executable = Join-Path $toolchainRoot 'go\bin\go.exe'
  if (Test-GoRuntime $executable) {
    return $executable
  }
  $downloadRoot = Join-Path $localData 'YTQJK\runtime\downloads'
  New-Item -ItemType Directory -Force -Path $downloadRoot | Out-Null
  $archive = Join-Path $downloadRoot "go$GoVersion.windows-amd64.zip"
  $temporaryArchive = "$archive.partial"
  Write-Host "YTQJK: downloading Go $GoVersion for Windows amd64."
  Invoke-WebRequest -UseBasicParsing `
    -Uri "https://go.dev/dl/go$GoVersion.windows-amd64.zip" `
    -OutFile $temporaryArchive
  $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $temporaryArchive).Hash.ToLowerInvariant()
  if ($actual -ne $GoSHA256) {
    Remove-Item -LiteralPath $temporaryArchive -Force
    throw 'Go toolchain checksum verification failed.'
  }
  Move-Item -LiteralPath $temporaryArchive -Destination $archive -Force
  $stage = Join-Path $downloadRoot ("go-stage-" + [guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $stage | Out-Null
  try {
    Expand-Archive -LiteralPath $archive -DestinationPath $stage
    if (Test-Path -LiteralPath $toolchainRoot) {
      Remove-Item -LiteralPath $toolchainRoot -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $toolchainRoot) | Out-Null
    Move-Item -LiteralPath $stage -Destination $toolchainRoot
  } finally {
    if (Test-Path -LiteralPath $stage) {
      Remove-Item -LiteralPath $stage -Recurse -Force
    }
  }
  if (-not (Test-GoRuntime $executable)) {
    throw 'Go toolchain installation did not produce a usable executable.'
  }
  return $executable
}

function Resolve-DefaultCodexRoot {
  if ($env:CODEX_HOME) {
    return [IO.Path]::GetFullPath($env:CODEX_HOME)
  }
  return Join-Path $env:USERPROFILE '.codex'
}

function Resolve-DefaultProjectRoot {
  $location = Get-Location
  if ($location.Provider.Name -ne 'FileSystem') {
    throw 'YTQJK installation must run from a file-system project directory.'
  }
  return [IO.Path]::GetFullPath($location.ProviderPath)
}

$bundleBinary = Join-Path $PSScriptRoot 'bin\ytqjk.exe'
$bundleManifest = Join-Path $PSScriptRoot 'release-manifest.json'
$bundleCommand = Join-Path $PSScriptRoot 'install.cmd'
$bootstrapBinary = $null
$bundleDetected = (Test-Path -LiteralPath $bundleBinary) -or
  (Test-Path -LiteralPath $bundleManifest)

if ($bundleDetected) {
  $requiredFiles = @(
    $bundleBinary,
    $bundleManifest,
    $bundleCommand,
    (Join-Path $PSScriptRoot 'plugins\ytqjk-agentic-orchestrator\.codex-plugin\plugin.json'),
    (Join-Path $PSScriptRoot 'plugins\ytqjk-knowledge\.codex-plugin\plugin.json')
  )
  $requiredDirectories = @(
    (Join-Path $PSScriptRoot 'plugins\ytqjk-agentic-orchestrator'),
    (Join-Path $PSScriptRoot 'plugins\ytqjk-knowledge')
  )
  foreach ($path in $requiredFiles) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
      throw 'YTQJK release bundle is incomplete or unsafe.'
    }
    $item = Get-Item -LiteralPath $path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
      throw 'YTQJK release bundle is incomplete or unsafe.'
    }
  }
  foreach ($path in $requiredDirectories) {
    if (-not (Test-Path -LiteralPath $path -PathType Container)) {
      throw 'YTQJK release bundle is incomplete or unsafe.'
    }
    $item = Get-Item -LiteralPath $path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
      throw 'YTQJK release bundle is incomplete or unsafe.'
    }
  }
  $utf8 = [Text.UTF8Encoding]::new($false, $true)
  $manifestDocument = [IO.File]::ReadAllText($bundleManifest, $utf8)
  if ($manifestDocument.Length -lt 2 -or -not $manifestDocument.EndsWith("`n")) {
    throw 'YTQJK release manifest is invalid.'
  }
  $manifestText = $manifestDocument.Substring(0, $manifestDocument.Length - 1)
  $manifestPattern = '^\{"schema":"ytqjk-release-bundle/v1","version":"(?<version>(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*))","os":"windows","arch":"amd64","binary_sha256":"(?<sha>[0-9a-f]{64})"\}$'
  if ($manifestText -notmatch $manifestPattern) {
    throw 'YTQJK release manifest is invalid.'
  }
  $manifestVersion = $Matches.version
  $manifestSHA = $Matches.sha
  $actualSHA = (Get-FileHash -Algorithm SHA256 -LiteralPath $bundleBinary).Hash.ToLowerInvariant()
  if ($actualSHA -ne $manifestSHA) {
    throw 'YTQJK release bundle verification failed.'
  }
  $binaryVersion = & $bundleBinary version
  if ($LASTEXITCODE -ne 0 -or $binaryVersion -ne $manifestVersion) {
    throw 'YTQJK release bundle verification failed.'
  }
  $binary = $bundleBinary
} else {
  $go = Resolve-GoRuntime
  $bootstrapBinary = Join-Path `
    ([IO.Path]::GetTempPath()) `
    ("ytqjk-bootstrap-" + [guid]::NewGuid().ToString('N') + '.exe')
  Write-Host 'YTQJK: building the Go runtime.'
  try {
    & $go -C $PSScriptRoot build -trimpath -ldflags '-s -w' -o $bootstrapBinary '.\cmd\ytqjk'
    if ($LASTEXITCODE -ne 0) {
      throw 'YTQJK Go build failed.'
    }
  } catch {
    if (Test-Path -LiteralPath $bootstrapBinary -PathType Leaf) {
      Remove-Item -LiteralPath $bootstrapBinary -Force
    }
    throw
  }
  $binary = $bootstrapBinary
}

if ($Arguments.Count -eq 0) {
  Write-Host 'YTQJK: starting full installation.'
  $defaultTargetRoot = Resolve-DefaultCodexRoot
  $defaultProjectRoot = Resolve-DefaultProjectRoot
  $Arguments = @(
    '--mode', 'all',
    '--target-root', $defaultTargetRoot,
    '--project-root', $defaultProjectRoot,
    '--source-root', $PSScriptRoot,
    '--apply', '--yes'
  )
} elseif ($Arguments -contains '--uninstall') {
  if ($Arguments -notcontains '--mode') { $Arguments += @('--mode', 'all') }
  if ($Arguments -notcontains '--target-root') {
    $Arguments += @('--target-root', (Resolve-DefaultCodexRoot))
  }
  if ($Arguments -notcontains '--source-root') { $Arguments += @('--source-root', $PSScriptRoot) }
  if ($Arguments -notcontains '--apply') { $Arguments += '--apply' }
  if ($Arguments -notcontains '--yes') { $Arguments += '--yes' }
} elseif ($Arguments -notcontains '--source-root') {
  $Arguments += @('--source-root', $PSScriptRoot)
}

try {
  & $binary @Arguments
  $exitCode = $LASTEXITCODE
} finally {
  if ($bootstrapBinary -and (Test-Path -LiteralPath $bootstrapBinary -PathType Leaf)) {
    Remove-Item -LiteralPath $bootstrapBinary -Force
  }
}
exit $exitCode
