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
$legacy = Join-Path $packageRoot "9999.0.0-colosseum"

New-Item -ItemType Directory -Force $target | Out-Null
Copy-Item (Join-Path $projectRoot "src\app.js") (Join-Path $target "app.js") -Force
@'
{
  "name": "copilot-afterburner-runtime-host",
  "version": "9999.0.0-afterburner",
  "private": true,
  "type": "module"
}
'@ | Set-Content -Encoding UTF8 (Join-Path $target "package.json")

if (Test-Path $legacy) {
    Remove-Item -Recurse -Force $legacy
}

$trackedByokPlugin = Join-Path $projectRoot "extensions\byok-models"
$installedPlugins = copilot plugin list | Out-String
if ($installedPlugins -match "(?m)^\s*•\s+colosseum-foundry-models\b") {
    & copilot plugin uninstall colosseum-foundry-models
}
if ($installedPlugins -match "(?m)^\s*•\s+afterburner-byok-models\b") {
    & copilot plugin uninstall afterburner-byok-models
}
& copilot plugin install $trackedByokPlugin
if ($LASTEXITCODE -ne 0) {
    throw "Failed to install the tracked Afterburner BYOK plugin."
}

Write-Output "Installed Afterburner at $target"
Write-Output "Installed tracked plugin: afterburner-byok-models"
Write-Output "Restart Copilot so its loader selects the Afterburner package."
