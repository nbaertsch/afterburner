#!/usr/bin/env pwsh
param(
    [Parameter(Position = 0)]
    [string]$Command,

    [Parameter(Position = 1, ValueFromRemainingArguments)]
    [string[]]$Arguments
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$managementCommands = @("install", "extension", "version", "help", "doctor")

function Show-Help {
@"
Afterburn - GitHub Copilot CLI with trusted Afterburner extensions

Usage:
  afterburn                         Start an Afterburner-managed Copilot session
  afterburn [copilot arguments]     Pass arguments directly to Copilot
  afterburn install                 Install the built-in companion components
  afterburn extension install <path|git-url|owner/repo@ref>
  afterburn extension update <id>
  afterburn extension update --all
  afterburn extension rollback <id>
  afterburn extension inspect <id>
  afterburn extension enable <id>
  afterburn extension disable <id>
  afterburn extension list
  afterburn doctor                  Check the installation and configured extensions
  afterburn version
  afterburn help

Normal 'copilot' invocations do not load Afterburner.
"@
}

function Initialize-ManagedHome {
    param([string]$ManagedHome)

    New-Item -ItemType Directory -Force $ManagedHome | Out-Null
    $normalHome = Join-Path $env:USERPROFILE ".copilot"
    foreach ($name in @(
        "session-state",
        "settings.json",
        "permissions-config.json",
        "mcp-config.json"
    )) {
        $source = Join-Path $normalHome $name
        $target = Join-Path $ManagedHome $name
        if ((Test-Path $source) -and !(Test-Path $target)) {
            if ((Get-Item $source).PSIsContainer) {
                New-Item -ItemType Junction -Path $target -Target $source | Out-Null
            } else {
                Copy-Item $source $target
            }
        }
    }
}

function Register-ManagedSessionExtensions {
    $registryPath = Join-Path ($env:AFTERBURNER_HOME ?? (Join-Path $env:USERPROFILE ".afterburner")) "registry.json"
    if (!(Test-Path $registryPath)) { return }
    $registry = Get-Content $registryPath -Raw | ConvertFrom-Json
    foreach ($extension in $registry.extensions.PSObject.Properties.Value) {
        if (!$extension.enabled -or !$extension.manifest.sessionExtension) { continue }
        $configPath = Join-Path $env:COPILOT_HOME "config.json"
        $config = if (Test-Path $configPath) {
            Get-Content $configPath -Raw | ConvertFrom-Json
        } else {
            [pscustomobject]@{}
        }
        $name = "afterburner-$($extension.manifest.id)"
        $sessionRoot = $extension.activePath
        $shimRoot = Join-Path $env:COPILOT_HOME "extensions\afterburner\$($extension.manifest.id)"
        New-Item -ItemType Directory -Force $shimRoot | Out-Null
        $entrypoint = Join-Path $extension.activePath $extension.manifest.sessionExtension.entrypoint
        $entrypointUrl = "file:///$($entrypoint.Replace('\', '/'))"
        @"
import '$entrypointUrl';
"@ | Set-Content (Join-Path $shimRoot "extension.mjs") -Encoding utf8
        $installed = @($config.installedPlugins | Where-Object { $_.name -ne $name })
        $installed += [pscustomobject]@{
            name = $name
            marketplace = "afterburner"
            version = "managed"
            installed_at = [DateTime]::UtcNow.ToString("O")
            cache_path = $shimRoot
            enabled = $true
            source = [pscustomobject]@{
                source = "afterburner"
                extension = $extension.manifest.id
            }
        }
        $config | Add-Member -NotePropertyName installedPlugins -NotePropertyValue $installed -Force
        $config | ConvertTo-Json -Depth 100 | Set-Content $configPath -Encoding utf8
    }
}

if (!$Command) {
    $Command = "run"
} elseif ($Command.StartsWith("-") -or $managementCommands -notcontains $Command) {
    $Arguments = @($Command) + $Arguments
    $Command = "run"
}

switch ($Command) {
    "run" {
        $managedHome = Join-Path $env:USERPROFILE ".afterburner\copilot-home"
        Initialize-ManagedHome $managedHome
        if (!$env:AFTERBURNER_BYOMODELS_CONFIG) {
            $defaultModelsConfig = Join-Path $env:USERPROFILE ".afterburner\config\byomodels.json"
            if (Test-Path $defaultModelsConfig) {
                $env:AFTERBURNER_BYOMODELS_CONFIG = $defaultModelsConfig
            }
        }
        $previousCopilotHome = $env:COPILOT_HOME
        $env:COPILOT_HOME = $managedHome
        $exitCode = 1
        try {
            Register-ManagedSessionExtensions
            & (Join-Path $root "scripts\prepare-runtime.ps1")
            & copilot --prefer-version 9999.0.0-afterburner @Arguments
            $exitCode = $LASTEXITCODE
        } finally {
            if ($null -eq $previousCopilotHome) { Remove-Item Env:COPILOT_HOME -ErrorAction SilentlyContinue }
            else { $env:COPILOT_HOME = $previousCopilotHome }
        }
        exit $exitCode
    }
    "install" {
        & (Join-Path $root "scripts\install.ps1") -InstallRuntime:$false
        exit $LASTEXITCODE
    }
    "extension" {
        & node (Join-Path $root "src\extension-manager.mjs") @Arguments
        exit $LASTEXITCODE
    }
    "doctor" {
        $failed = $false
        if (!(Get-Command copilot -ErrorAction SilentlyContinue)) {
            Write-Error "Copilot CLI is not available on PATH."
            $failed = $true
        } else {
            Write-Output "OK  Copilot CLI found."
        }
        if (!(Get-Command node -ErrorAction SilentlyContinue)) {
            Write-Error "Node.js is required for extension management."
            $failed = $true
        } else {
            Write-Output "OK  Node.js found."
        }
        $config = if ($env:AFTERBURNER_BYOMODELS_CONFIG) {
            $env:AFTERBURNER_BYOMODELS_CONFIG
        } else {
            Join-Path $env:USERPROFILE ".afterburner\config\byomodels.json"
        }
        if (Test-Path $config) { Write-Output "OK  BYOModels config: $config" }
        else {
            Write-Error "BYOModels config is missing: $config"
            $failed = $true
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
