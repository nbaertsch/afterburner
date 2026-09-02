$ErrorActionPreference = "Stop"

$projectRoot = Split-Path -Parent $PSScriptRoot
$installRoot = if ($env:AFTERBURNER_HOME) {
    $env:AFTERBURNER_HOME
} else {
    Join-Path $HOME ".afterburner"
}
$bin = Join-Path $installRoot "bin"
$app = Join-Path $installRoot "app"

$staging = Join-Path $installRoot ("app-staging-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force $bin, $staging | Out-Null
if (Test-Path $app) { Remove-Item -LiteralPath $app -Recurse -Force }
Move-Item $staging $app
Copy-Item (Join-Path $projectRoot "afterburn.ps1") $app -Force
Copy-Item (Join-Path $projectRoot "afterburn.cmd") $app -Force
Copy-Item (Join-Path $projectRoot "package.json") $app -Force
Copy-Item (Join-Path $projectRoot "src") $app -Recurse -Force
Copy-Item (Join-Path $projectRoot "scripts") $app -Recurse -Force
Copy-Item (Join-Path $projectRoot "extensions") $app -Recurse -Force
Copy-Item (Join-Path $projectRoot "schemas") $app -Recurse -Force

@"
@echo off
pwsh -NoProfile -ExecutionPolicy Bypass -File "$app\afterburn.ps1" %*
"@ | Set-Content (Join-Path $bin "afterburn.cmd") -Encoding ASCII
Remove-Item (Join-Path $bin "afterburner.cmd") -Force -ErrorAction SilentlyContinue

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$parts = @($userPath -split ";" | Where-Object { $_ })
if ($parts -notcontains $bin) {
    [Environment]::SetEnvironmentVariable("Path", (($parts + $bin) -join ";"), "User")
}
$env:Path = "$bin;$env:Path"

Write-Output "Installed the afterburn CLI at $bin\afterburn.cmd"
Write-Output "Open a new terminal and run: afterburn help"
