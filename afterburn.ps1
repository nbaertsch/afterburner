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
        $previousCopilotHome = $env:COPILOT_HOME
        $env:COPILOT_HOME = $managedHome
        $exitCode = 1
        try {
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
        $config = if ($env:AFTERBURNER_BYOMODELS_CONFIG) { $env:AFTERBURNER_BYOMODELS_CONFIG } else { "<not configured>" }
        Write-Output "INFO BYOModels config: $config"
        & node (Join-Path $root "src\extension-manager.mjs") list
        if ($failed) { exit 1 }
    }
    "version" {
        $package = Get-Content (Join-Path $root "package.json") -Raw | ConvertFrom-Json
        Write-Output "Afterburn $($package.version)"
    }
    "help" { Show-Help }
}
