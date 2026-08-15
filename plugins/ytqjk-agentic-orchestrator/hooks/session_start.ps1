$ErrorActionPreference = 'Stop'
$candidates = @(
  (Join-Path $env:LOCALAPPDATA 'YTQJK\runtime\python\python.exe'),
  (Join-Path $env:LOCALAPPDATA 'Programs\Python\Python312\python.exe')
)
$python = $candidates | Where-Object { Test-Path -LiteralPath $_ } |
  Select-Object -First 1
if (-not $python) {
  $command = Get-Command python -ErrorAction SilentlyContinue
  if ($command) { $python = $command.Source }
}
if (-not $python) {
  Write-Error 'YTQJK Python runtime is unavailable.'
  exit 1
}
& $python (Join-Path $env:PLUGIN_ROOT 'hooks\session_start.py')
exit $LASTEXITCODE
