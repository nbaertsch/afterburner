#!/usr/bin/env pwsh
param(
    [Parameter(Position = 0)]
    [ValidateSet("install", "extension", "version", "run", "help")]
    [string]$Command = "help",

    [Parameter(Position = 1, ValueFromRemainingArguments)]
    [string[]]$Arguments
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path

switch ($Command) {
    "install" {
        & (Join-Path $root "scripts\install.ps1") -InstallRuntime:$false
        exit $LASTEXITCODE
    }
    "run" {
        & (Join-Path $root "scripts\prepare-runtime.ps1")
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
        & copilot --prefer-version 9999.0.0-afterburner @Arguments
        exit $LASTEXITCODE
    }
    "extension" {
        & node (Join-Path $root "src\extension-manager.mjs") @Arguments
        exit $LASTEXITCODE
    }
    "version" {
        $package = Get-Content (Join-Path $root "package.json") -Raw | ConvertFrom-Json
        Write-Output "Afterburner $($package.version)"
    }
    default {
        @"
Afterburner - trusted runtime extensions for GitHub Copilot CLI

Usage:
  afterburner install
  afterburner run [copilot arguments]
  afterburner extension install <path|git-url|owner/repo@ref>
  afterburner extension inspect <id>
  afterburner extension enable <id>
  afterburner extension disable <id>
  afterburner extension list
  afterburner version
"@
    }
}
