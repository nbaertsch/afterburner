param(
    [switch]$InstallRuntime = $true,
    [string[]]$BuiltInIds = @()
)
$ErrorActionPreference = "Stop"

$projectRoot = Split-Path -Parent $PSScriptRoot

if ($InstallRuntime) {
    & (Join-Path $PSScriptRoot "prepare-runtime.ps1")
}

$managerArguments = @(
    (Join-Path $projectRoot "src\extension-manager.mjs"),
    "install-builtins",
    (Join-Path $projectRoot "extensions")
) + $BuiltInIds
& node @managerArguments
if ($LASTEXITCODE -ne 0) { throw "Failed to install the requested built-in Afterburner extensions." }

$installByoModels = $BuiltInIds.Count -eq 0 -or @($BuiltInIds | Where-Object { $_ -in @("byo-models", "byomodels") }).Count -gt 0
$afterburnerHome = if ($env:AFTERBURNER_HOME) { $env:AFTERBURNER_HOME } else { Join-Path $HOME ".afterburner" }
$configDirectory = Join-Path $afterburnerHome "config"
New-Item -ItemType Directory -Force $configDirectory | Out-Null
if ($installByoModels) {
    $configPath = Join-Path $configDirectory "byomodels.json"
    if (!(Test-Path $configPath)) {
        Copy-Item (Join-Path $projectRoot "extensions\BYOModels\extensions\BYOModels\models.example.json") $configPath
        Write-Warning "Created example BYOModels configuration at $configPath. Customize it before launching Afterburn."
    }
}
$installBlackBox = $BuiltInIds.Count -eq 0 -or @($BuiltInIds | Where-Object { $_ -eq "black-box" }).Count -gt 0
if ($installBlackBox) {
    $blackBoxConfigPath = Join-Path $configDirectory "black-box.json"
    if (!(Test-Path $blackBoxConfigPath)) {
        Copy-Item (Join-Path $projectRoot "extensions\BlackBox\config.example.json") $blackBoxConfigPath
        Write-Output "Created Black Box configuration at $blackBoxConfigPath."
    }
}

Write-Output "Run 'afterburn' to start an Afterburner-managed Copilot session."
