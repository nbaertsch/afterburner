$ErrorActionPreference = "Stop"

$projectRoot = Split-Path -Parent $PSScriptRoot
$installRoot = if ($env:AFTERBURNER_HOME) {
    $env:AFTERBURNER_HOME
} else {
    Join-Path $HOME ".afterburner"
}
$bin = Join-Path $installRoot "bin"
$app = Join-Path $installRoot "app"
$config = Join-Path $installRoot "config"

$staging = Join-Path $installRoot ("app-staging-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force $bin, $config, $staging | Out-Null
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

# Existing terminal processes do not receive user PATH broadcasts. PowerShell
# profiles make the command available immediately in newly opened tabs hosted
# by an already-running terminal application.
$profilePaths = @(
    (Join-Path ([Environment]::GetFolderPath("MyDocuments")) "PowerShell\profile.ps1"),
    (Join-Path ([Environment]::GetFolderPath("MyDocuments")) "WindowsPowerShell\profile.ps1")
)
$pathBlock = @"
# BEGIN AFTERBURN PATH
`$afterburnBin = '$bin'
if ((Test-Path `$afterburnBin) -and (`$env:Path -split ';') -notcontains `$afterburnBin) {
    `$env:Path = "`$afterburnBin;`$env:Path"
}
# END AFTERBURN PATH
"@
foreach ($profilePath in $profilePaths) {
    New-Item -ItemType Directory -Force (Split-Path $profilePath -Parent) | Out-Null
    $content = if (Test-Path $profilePath) { Get-Content $profilePath -Raw } else { "" }
    $content = [regex]::Replace($content,
        "(?ms)^# BEGIN AFTERBURN PATH\r?\n.*?^# END AFTERBURN PATH\r?\n?", "")
    $updated = ($content.TrimEnd() + "`r`n`r`n" + $pathBlock.Trim() + "`r`n").TrimStart()
    Set-Content $profilePath $updated -Encoding utf8
}

Write-Output "Installed the afterburn CLI at $bin\afterburn.cmd"
Write-Output "Run 'afterburn help' from a new PowerShell session."
