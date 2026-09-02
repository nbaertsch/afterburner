$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$rawArguments = @($args)
$managementCommands = @("install", "enable", "disable", "uninstall", "extension", "version", "help", "doctor")
$command = if ($rawArguments.Count -eq 0) { "run" } else { $rawArguments[0] }
$arguments = if ($rawArguments.Count -gt 1) { @($rawArguments[1..($rawArguments.Count - 1)]) } else { @() }

function Show-Help {
@"
Afterburn - GitHub Copilot CLI with trusted Afterburner extensions

Usage:
  afterburn
  afterburn [copilot arguments]
  afterburn install [id...]
  afterburn enable <id...>
  afterburn disable <id...>
  afterburn uninstall <id...>
  afterburn extension install <source>
  afterburn extension update <id>
  afterburn extension update --all
  afterburn extension rollback <id>
  afterburn extension inspect|enable|disable <id>
  afterburn extension list
  afterburn doctor
  afterburn version
"@
}

function Initialize-ManagedHome([string]$managedHome) {
    New-Item -ItemType Directory -Force $managedHome | Out-Null
    $normalHome = Join-Path $env:USERPROFILE ".copilot"
    foreach ($name in @("session-state", "settings.json", "permissions-config.json", "mcp-config.json")) {
        $source = Join-Path $normalHome $name
        $target = Join-Path $managedHome $name
        if ((Test-Path $source) -and !(Test-Path $target)) {
            if ((Get-Item $source).PSIsContainer) {
                New-Item -ItemType Junction -Path $target -Target $source | Out-Null
            } else { Copy-Item $source $target }
        }
    }
}

function Register-ManagedSessionExtensions {
    $managedConfigPath = Join-Path $env:COPILOT_HOME "config.json"
    & node (Join-Path $root "src\extension-manager.mjs") reconcile-session $managedConfigPath
    if ($LASTEXITCODE -ne 0) { throw "Failed to reconcile Afterburner-managed session extensions." }
}

function Invoke-BuiltInLifecycle([string]$action, [string[]]$ids) {
    $managerArguments = @(
        (Join-Path $root "src\extension-manager.mjs"),
        "$action-builtins",
        (Join-Path $root "extensions")
    ) + $ids
    & node @managerArguments
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

if ($command.StartsWith("-") -or $managementCommands -notcontains $command) {
    $arguments = @($command) + $arguments
    $command = "run"
}

switch ($command) {
    "run" {
        $afterburnerHome = if ($env:AFTERBURNER_HOME) { $env:AFTERBURNER_HOME } else { Join-Path $env:USERPROFILE ".afterburner" }
        $managedHome = Join-Path $afterburnerHome "copilot-home"
        Initialize-ManagedHome $managedHome
        if (!$env:AFTERBURNER_BYOMODELS_CONFIG) {
            $defaultModelsConfig = Join-Path $afterburnerHome "config\byomodels.json"
            if (Test-Path $defaultModelsConfig) { $env:AFTERBURNER_BYOMODELS_CONFIG = $defaultModelsConfig }
        }
        $previousCopilotHome = $env:COPILOT_HOME
        $env:COPILOT_HOME = $managedHome
        try {
            Register-ManagedSessionExtensions
            & (Join-Path $root "scripts\prepare-runtime.ps1")
            & copilot --prefer-version 9999.0.0-afterburner @arguments
            $exitCode = $LASTEXITCODE
        } finally {
            if ($null -eq $previousCopilotHome) { Remove-Item Env:COPILOT_HOME -ErrorAction SilentlyContinue }
            else { $env:COPILOT_HOME = $previousCopilotHome }
        }
        exit $exitCode
    }
    "install" {
        & (Join-Path $root "scripts\install.ps1") -InstallRuntime:$false -BuiltInIds $arguments
        exit $LASTEXITCODE
    }
    "enable" { Invoke-BuiltInLifecycle "enable" $arguments; exit 0 }
    "disable" { Invoke-BuiltInLifecycle "disable" $arguments; exit 0 }
    "uninstall" { Invoke-BuiltInLifecycle "uninstall" $arguments; exit 0 }
    "extension" { & node (Join-Path $root "src\extension-manager.mjs") $arguments; exit $LASTEXITCODE }
    "doctor" {
        $failed = $false
        foreach ($tool in @("copilot", "node")) {
            if (Get-Command $tool -ErrorAction SilentlyContinue) { Write-Output "OK  $tool found." }
            else { Write-Error "$tool is unavailable."; $failed = $true }
        }
        $afterburnerHome = if ($env:AFTERBURNER_HOME) { $env:AFTERBURNER_HOME } else { Join-Path $env:USERPROFILE ".afterburner" }
        $registryPath = Join-Path $afterburnerHome "registry.json"
        $byoModelsInstalled = $false
        if (Test-Path $registryPath) {
            $registry = Get-Content $registryPath -Raw | ConvertFrom-Json
            $byoModelsInstalled = $null -ne $registry.extensions.PSObject.Properties["byo-models"] -or
                $null -ne $registry.extensions.PSObject.Properties["byomodels"]
        }
        if ($byoModelsInstalled) {
            $config = if ($env:AFTERBURNER_BYOMODELS_CONFIG) { $env:AFTERBURNER_BYOMODELS_CONFIG } else { Join-Path $afterburnerHome "config\byomodels.json" }
            if (Test-Path $config) { Write-Output "OK  BYOModels config: $config" }
            else { Write-Error "BYOModels config is missing: $config"; $failed = $true }
        }
        & node (Join-Path $root "src\extension-manager.mjs") list
        if ($failed) { exit 1 }
    }
    "version" {
        $package = Get-Content (Join-Path $root "package.json") -Raw | ConvertFrom-Json
        Write-Output "Afterburn $($package.version)"
    }
    "help" { Show-Help }
}
