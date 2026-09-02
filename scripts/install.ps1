param([switch]$InstallRuntime = $true)
$ErrorActionPreference = "Stop"

$projectRoot = Split-Path -Parent $PSScriptRoot
$platform = "win32-$([System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString().ToLowerInvariant())"
if ($platform -eq "win32-x64") {
    $platform = "win32-x64"
} elseif ($platform -eq "win32-arm64") {
    $platform = "win32-arm64"
} else {
    throw "Afterburner currently supports Windows x64 and arm64."
}

$copilotHome = if ($env:COPILOT_HOME) { $env:COPILOT_HOME } else { Join-Path $HOME ".copilot" }
$packageRoot = Join-Path $copilotHome "pkg\$platform"
$target = Join-Path $packageRoot "9999.0.0-afterburner"

if ($InstallRuntime) {
    & (Join-Path $PSScriptRoot "prepare-runtime.ps1")
}

$trackedByokPlugin = Join-Path $projectRoot "extensions\BYOModels"
& copilot plugin install $trackedByokPlugin
if ($LASTEXITCODE -ne 0) {
    throw "Failed to install the built-in BYOModels companion plugin."
}

Write-Output "Installed built-in extension companion: BYOModels"
Write-Output "Run 'afterburn' to start an Afterburner-managed Copilot session."
