$ErrorActionPreference = 'Stop'
$binary = Join-Path $env:PLUGIN_ROOT 'bin\ytqjk.exe'
if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) {
  $local = Join-Path $env:LOCALAPPDATA 'YTQJK\runtime\bin\ytqjk.exe'
  if (Test-Path -LiteralPath $local -PathType Leaf) {
    $binary = $local
  } else {
    $command = Get-Command ytqjk -ErrorAction SilentlyContinue
    if ($command) { $binary = $command.Source }
  }
}
if (-not $binary -or -not (Test-Path -LiteralPath $binary -PathType Leaf)) {
  Write-Error 'YTQJK Go runtime is unavailable.'
  exit 1
}
& $binary hook session-start
exit $LASTEXITCODE
