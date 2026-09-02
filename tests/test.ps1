$ErrorActionPreference = "Stop"

$projectRoot = Split-Path -Parent $PSScriptRoot
& (Join-Path $projectRoot "scripts\install.ps1")

$env:COPILOT_RUNTIME_EXTENSION_SELF_TEST = "1"
try {
    $result = copilot --prefer-version 9999.0.0-afterburner version |
        Out-String |
        ConvertFrom-Json
} finally {
    Remove-Item Env:COPILOT_RUNTIME_EXTENSION_SELF_TEST -ErrorAction SilentlyContinue
}

if (-not $result.projection.hasReasoningColumn) {
    throw "Expected a reasoning column from the installed BYOK adapter."
}
if (-not $result.projection.hasContextColumn) {
    throw "Expected a context column from the installed BYOK adapter."
}

$expectedContexts = @("256K", "512K", "768K", "1.05M")
foreach ($entry in $result.contextCycles.PSObject.Properties) {
    $actual = @($entry.Value | ForEach-Object { $_.contextCellText })
    if (($actual -join ",") -ne ($expectedContexts -join ",")) {
        throw "Unexpected context options for $($entry.Name): $($actual -join ', ')"
    }
}

$versionOutput = copilot version | Out-String
if ($LASTEXITCODE -ne 0 -or $versionOutput -notmatch "GitHub Copilot CLI") {
    throw "Copilot failed to start through Afterburner."
}

Write-Output "Afterburner tests passed."
