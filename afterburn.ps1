#!/usr/bin/env pwsh
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
& (Join-Path $root "afterburner.ps1") run @args
exit $LASTEXITCODE
