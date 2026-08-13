param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
$ErrorActionPreference = 'Stop'
$python = Get-Command python -ErrorAction SilentlyContinue
if ($null -eq $python) {
  $python = Get-Command py -ErrorAction SilentlyContinue
}
if ($null -eq $python) {
  Write-Error 'Python 3.10+ is required.'
  exit 127
}
& $python.Source "$PSScriptRoot/setup.py" @Arguments
exit $LASTEXITCODE
