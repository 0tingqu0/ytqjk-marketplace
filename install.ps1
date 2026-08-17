param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
$ErrorActionPreference = 'Stop'

function Test-PythonRuntime {
  param([string]$Executable, [string[]]$Prefix = @())
  if (-not $Executable -or -not (Test-Path -LiteralPath $Executable)) {
    return $false
  }
  & $Executable @Prefix -c 'import sys; raise SystemExit(0 if sys.version_info >= (3, 10) else 1)' 2>$null
  return $LASTEXITCODE -eq 0
}

function Find-PythonRuntime {
  $candidates = @()
  $pythonCommand = Get-Command python -ErrorAction SilentlyContinue
  if ($pythonCommand) {
    $candidates += [pscustomobject]@{
      Executable = $pythonCommand.Source
      Prefix = @()
    }
  }
  $pyCommand = Get-Command py -ErrorAction SilentlyContinue
  if ($pyCommand) {
    $candidates += [pscustomobject]@{
      Executable = $pyCommand.Source
      Prefix = @('-3')
    }
  }
  $candidates += [pscustomobject]@{
    Executable = Join-Path $env:LOCALAPPDATA 'Programs\Python\Python312\python.exe'
    Prefix = @()
  }
  foreach ($candidate in $candidates) {
    if (Test-PythonRuntime $candidate.Executable $candidate.Prefix) {
      return $candidate
    }
  }
  return $null
}

$python = Find-PythonRuntime
if ($null -eq $python) {
  $winget = Get-Command winget -ErrorAction SilentlyContinue
  if ($null -eq $winget) {
    Write-Error 'Python 3.10+ is unavailable and winget was not found.'
    exit 127
  }
  Write-Host 'YTQJK: 未检测到 Python 3.10+，正在安装用户级 Python 3.12。'
  & $winget.Source install --id Python.Python.3.12 --exact --source winget `
    --scope user --silent --accept-package-agreements `
    --accept-source-agreements --disable-interactivity
  if ($LASTEXITCODE -ne 0) {
    Write-Error 'Python 3.12 installation failed.'
    exit 127
  }
  $python = Find-PythonRuntime
  if ($null -eq $python) {
    Write-Error 'Python 3.12 was installed but could not be located.'
    exit 127
  }
}
if ($Arguments.Count -eq 0) {
  Write-Host 'YTQJK: 开始完整安装，首次下载依赖可能需要几分钟。'
  $Arguments = @(
    '--mode', 'all',
    '--target-root', $PSScriptRoot,
    '--project-root', $PSScriptRoot,
    '--apply', '--yes'
  )
} elseif ($Arguments -contains '--uninstall') {
  if ($Arguments -notcontains '--mode') {
    $Arguments += @('--mode', 'all')
  }
  if ($Arguments -notcontains '--target-root') {
    $Arguments += @('--target-root', $PSScriptRoot)
  }
  if ($Arguments -notcontains '--apply') {
    $Arguments += '--apply'
  }
  if ($Arguments -notcontains '--yes') {
    $Arguments += '--yes'
  }
}
& $python.Executable @($python.Prefix) "$PSScriptRoot/setup.py" @Arguments
exit $LASTEXITCODE
