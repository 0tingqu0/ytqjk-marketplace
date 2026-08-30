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

$go = Resolve-GoRuntime
$localData = $env:LOCALAPPDATA
if (-not $localData) {
  $localData = Join-Path $env:USERPROFILE 'AppData\Local'
}
$runtimeBin = Join-Path $localData 'YTQJK\runtime\bin'
New-Item -ItemType Directory -Force -Path $runtimeBin | Out-Null
$binary = Join-Path $runtimeBin 'ytqjk.exe'
$temporaryBinary = "$binary.partial"

Write-Host 'YTQJK: building the Go runtime.'
& $go -C $PSScriptRoot build -trimpath -ldflags '-s -w' -o $temporaryBinary '.\cmd\ytqjk'
if ($LASTEXITCODE -ne 0) {
  throw 'YTQJK Go build failed.'
}
Move-Item -LiteralPath $temporaryBinary -Destination $binary -Force

if ($Arguments.Count -eq 0) {
  Write-Host 'YTQJK: starting full installation.'
  $Arguments = @(
    '--mode', 'all',
    '--target-root', $PSScriptRoot,
    '--project-root', $PSScriptRoot,
    '--source-root', $PSScriptRoot,
    '--apply', '--yes'
  )
} elseif ($Arguments -contains '--uninstall') {
  if ($Arguments -notcontains '--mode') { $Arguments += @('--mode', 'all') }
  if ($Arguments -notcontains '--target-root') { $Arguments += @('--target-root', $PSScriptRoot) }
  if ($Arguments -notcontains '--source-root') { $Arguments += @('--source-root', $PSScriptRoot) }
  if ($Arguments -notcontains '--apply') { $Arguments += '--apply' }
  if ($Arguments -notcontains '--yes') { $Arguments += '--yes' }
} elseif ($Arguments -notcontains '--source-root') {
  $Arguments += @('--source-root', $PSScriptRoot)
}

& $binary @Arguments
exit $LASTEXITCODE
