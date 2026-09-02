$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$platform = "win32-$([System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString().ToLowerInvariant())"
if ($platform -notin @("win32-x64", "win32-arm64")) {
    throw "Afterburner currently supports Windows x64 and arm64."
}
$copilotHome = if ($env:COPILOT_HOME) { $env:COPILOT_HOME } else { Join-Path $HOME ".copilot" }
$target = Join-Path $copilotHome "pkg\$platform\9999.0.0-afterburner"
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
