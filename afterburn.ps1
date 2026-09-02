$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$rawArguments = @($args)
$managementCommands = @("install", "extension", "version", "help", "doctor")
$command = if ($rawArguments.Count -eq 0) { "run" } else { $rawArguments[0] }
$arguments = if ($rawArguments.Count -gt 1) { @($rawArguments[1..($rawArguments.Count - 1)]) } else { @() }

function Show-Help {
@"
Afterburn - GitHub Copilot CLI with trusted Afterburner extensions

Usage:
  afterburn
  afterburn [copilot arguments]
  afterburn install
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
    $registryPath = Join-Path ($env:AFTERBURNER_HOME ?? (Join-Path $env:USERPROFILE ".afterburner")) "registry.json"
    if (!(Test-Path $registryPath)) { return }
    $registry = Get-Content $registryPath -Raw | ConvertFrom-Json
    foreach ($extension in $registry.extensions.PSObject.Properties.Value) {
        if (!$extension.enabled -or !$extension.manifest.sessionExtension) { continue }
        $pluginManifestPath = Join-Path $extension.activePath "plugin.json"
        if (!(Test-Path $pluginManifestPath)) {
            throw "Session component for '$($extension.manifest.id)' is missing plugin.json."
        }
        $pluginManifest = Get-Content $pluginManifestPath -Raw | ConvertFrom-Json
        $managedConfigPath = Join-Path $env:COPILOT_HOME "config.json"
        $managedConfig = if (Test-Path $managedConfigPath) {
            Get-Content $managedConfigPath -Raw | ConvertFrom-Json
        } else {
            [pscustomobject]@{}
        }
        if (!$managedConfig.PSObject.Properties["installedPlugins"]) {
            $managedConfig | Add-Member -NotePropertyName installedPlugins -NotePropertyValue @()
        }
        $managedConfig.installedPlugins = @(
            $managedConfig.installedPlugins | Where-Object { $_.name -ne $pluginManifest.name }
        ) + [pscustomobject]@{
            name = $pluginManifest.name
            marketplace = ""
            version = $pluginManifest.version
            installed_at = (Get-Date).ToUniversalTime().ToString("o")
            cache_path = $extension.activePath
            enabled = $true
            source = [pscustomobject]@{
                source = "local"
                path = $extension.activePath
            }
        }
        $managedConfig | ConvertTo-Json -Depth 20 | Set-Content $managedConfigPath
    }
}

if ($command.StartsWith("-") -or $managementCommands -notcontains $command) {
    $arguments = @($command) + $arguments
    $command = "run"
}

switch ($command) {
    "run" {
        $afterburnerHome = $env:AFTERBURNER_HOME ?? (Join-Path $env:USERPROFILE ".afterburner")
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
    "install" { & (Join-Path $root "scripts\install.ps1") -InstallRuntime:$false; exit $LASTEXITCODE }
    "extension" { & node (Join-Path $root "src\extension-manager.mjs") @arguments; exit $LASTEXITCODE }
    "doctor" {
        $failed = $false
        foreach ($tool in @("copilot", "node")) {
            if (Get-Command $tool -ErrorAction SilentlyContinue) { Write-Output "OK  $tool found." }
            else { Write-Error "$tool is unavailable."; $failed = $true }
        }
        $config = $env:AFTERBURNER_BYOMODELS_CONFIG ?? (Join-Path $env:USERPROFILE ".afterburner\config\byomodels.json")
        if (Test-Path $config) { Write-Output "OK  BYOModels config: $config" }
        else { Write-Error "BYOModels config is missing: $config"; $failed = $true }
        & node (Join-Path $root "src\extension-manager.mjs") list
        if ($failed) { exit 1 }
    }
    "version" {
        $package = Get-Content (Join-Path $root "package.json") -Raw | ConvertFrom-Json
        Write-Output "Afterburn $($package.version)"
    }
    "help" { Show-Help }
}
