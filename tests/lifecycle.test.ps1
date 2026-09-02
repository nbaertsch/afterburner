$ErrorActionPreference = "Stop"

$projectRoot = Split-Path -Parent $PSScriptRoot
$manager = Join-Path $projectRoot "src\extension-manager.mjs"
$sandbox = Join-Path $projectRoot (".lifecycle-tests-" + [guid]::NewGuid().ToString("N"))
$previousAfterburnerHome = $env:AFTERBURNER_HOME

function Invoke-Manager([string[]]$ManagerArguments) {
    & node $manager @ManagerArguments
    if ($LASTEXITCODE -ne 0) { throw "Extension manager failed: $($ManagerArguments -join ' ')" }
}

function Write-TestExtension([string]$Path, [string]$Id, [string]$Visibility = "builtin") {
    New-Item -ItemType Directory -Force (Join-Path $Path "runtime") | Out-Null
    [ordered]@{
        schemaVersion = 1
        id = $Id
        displayName = $Id
        visibility = $Visibility
        runtime = [ordered]@{ entrypoint = "runtime/extension.mjs"; execution = "in-process" }
    } | ConvertTo-Json -Depth 10 | Set-Content (Join-Path $Path "afterburner.json") -Encoding utf8
    Set-Content (Join-Path $Path "runtime\extension.mjs") "export default {};" -Encoding utf8
}

try {
    $catalogRoot = Join-Path $sandbox "app\extensions"
    New-Item -ItemType Directory -Force $catalogRoot | Out-Null
    Copy-Item (Join-Path $projectRoot "extensions\BYOModels") (Join-Path $catalogRoot "LegacyByo") -Recurse
    Write-TestExtension (Join-Path $catalogRoot "BlackBox") "black-box"
    Write-TestExtension (Join-Path $catalogRoot "Private") "private-test" "private"

    $catalog = @(& node $manager catalog $catalogRoot)
    if ($LASTEXITCODE -ne 0 -or ($catalog | Sort-Object) -join "," -ne "black-box,byo-models") {
        throw "Built-in catalog did not discover only the canonical public built-in IDs."
    }

    $lifecycleHome = Join-Path $sandbox "home"
    $env:AFTERBURNER_HOME = $lifecycleHome
    $configPath = Join-Path $lifecycleHome "config\byomodels.json"
    $dataPath = Join-Path $lifecycleHome "extension-data\byo-models\state.json"
    New-Item -ItemType Directory -Force (Split-Path $configPath -Parent), (Split-Path $dataPath -Parent) | Out-Null
    Set-Content $configPath '{"userOwned":true}' -Encoding utf8
    Set-Content $dataPath '{"durable":true}' -Encoding utf8

    Invoke-Manager @("install-builtins", $catalogRoot)
    $registry = Get-Content (Join-Path $lifecycleHome "registry.json") -Raw | ConvertFrom-Json
    if (!$registry.extensions.'byo-models'.enabled -or !$registry.extensions.'black-box'.enabled -or
        $registry.extensions.PSObject.Properties["byomodels"]) {
        throw "Installing all built-ins did not install and enable the canonical catalog IDs."
    }

    Invoke-Manager @("disable-builtins", $catalogRoot, "byo-models", "black-box")
    $registry = Get-Content (Join-Path $lifecycleHome "registry.json") -Raw | ConvertFrom-Json
    if ($registry.extensions.'byo-models'.enabled -or $registry.extensions.'black-box'.enabled) {
        throw "Disabling multiple built-ins did not persist."
    }
    Invoke-Manager @("enable-builtins", $catalogRoot, "byo-models")

    $managedConfigPath = Join-Path $lifecycleHome "copilot-home\config.json"
    New-Item -ItemType Directory -Force (Split-Path $managedConfigPath -Parent) | Out-Null
    [ordered]@{
        installedPlugins = @(
            [ordered]@{ name = "ordinary-plugin"; cache_path = "C:\ordinary\plugin"; enabled = $true },
            [ordered]@{
                name = "stale-afterburner-plugin"
                cache_path = (Join-Path $lifecycleHome "extensions\removed\old")
                source = [ordered]@{ source = "local"; path = (Join-Path $lifecycleHome "extensions\removed\old") }
                enabled = $true
            }
        )
    } | ConvertTo-Json -Depth 10 | Set-Content $managedConfigPath -Encoding utf8
    Invoke-Manager @("reconcile-session", $managedConfigPath)
    $managedConfig = Get-Content $managedConfigPath -Raw | ConvertFrom-Json
    if (@($managedConfig.installedPlugins | Where-Object name -eq "ordinary-plugin").Count -ne 1 -or
        @($managedConfig.installedPlugins | Where-Object name -eq "stale-afterburner-plugin").Count -ne 0 -or
        @($managedConfig.installedPlugins | Where-Object name -eq "afterburner-byomodels").Count -ne 1) {
        throw "Managed session extension reconciliation did not replace stale registrations."
    }

    Invoke-Manager @("uninstall-builtins", $catalogRoot, "byo-models")
    if (Test-Path (Join-Path $lifecycleHome "extensions\byo-models")) {
        throw "Built-in uninstall retained the package cache."
    }
    if (!(Test-Path $configPath) -or !(Test-Path $dataPath)) {
        throw "Built-in uninstall removed user configuration or extension data."
    }
    Invoke-Manager @("reconcile-session", $managedConfigPath)
    $managedConfig = Get-Content $managedConfigPath -Raw | ConvertFrom-Json
    if (@($managedConfig.installedPlugins | Where-Object name -eq "afterburner-byomodels").Count -ne 0 -or
        @($managedConfig.installedPlugins | Where-Object name -eq "ordinary-plugin").Count -ne 1) {
        throw "Uninstalled built-in left a stale managed session registration after reconciliation."
    }

    $legacyHome = Join-Path $sandbox "legacy-home"
    $env:AFTERBURNER_HOME = $legacyHome
    $legacyPackageRoot = Join-Path $legacyHome "extensions\byomodels"
    foreach ($version in @("v1", "v2")) {
        Copy-Item (Join-Path $projectRoot "extensions\BYOModels") (Join-Path $legacyPackageRoot $version) -Recurse
    }
    $legacyConfigPath = Join-Path $legacyHome "config\byomodels.json"
    New-Item -ItemType Directory -Force (Split-Path $legacyConfigPath -Parent) | Out-Null
    Set-Content $legacyConfigPath '{"legacyConfig":true}' -Encoding utf8
    $legacyRegistry = [ordered]@{
        schemaVersion = 1
        extensions = [ordered]@{
            byomodels = [ordered]@{
                enabled = $true
                activePath = (Join-Path $legacyPackageRoot "v2")
                previousActivePath = (Join-Path $legacyPackageRoot "v1")
                manifest = Get-Content (Join-Path $projectRoot "extensions\BYOModels\afterburner.json") -Raw | ConvertFrom-Json
                source = [ordered]@{ type = "git"; value = "example/source.git"; ref = "main"; commit = "abc123" }
                update = [ordered]@{ channel = "stable" }
                updatedAt = "2026-01-02T03:04:05.000Z"
            }
        }
    }
    New-Item -ItemType Directory -Force $legacyHome | Out-Null
    $legacyRegistry | ConvertTo-Json -Depth 20 | Set-Content (Join-Path $legacyHome "registry.json") -Encoding utf8

    Invoke-Manager @("list") | Out-Null
    $firstMigration = Get-Content (Join-Path $legacyHome "registry.json") -Raw
    $migrated = $firstMigration | ConvertFrom-Json
    $migrationChecks = [ordered]@{
        legacyRemoved = !$migrated.extensions.PSObject.Properties["byomodels"]
        enabled = $migrated.extensions.'byo-models'.enabled
        activePath = $migrated.extensions.'byo-models'.activePath -eq (Join-Path $legacyHome "extensions\byo-models\v2")
        previousActivePath = $migrated.extensions.'byo-models'.previousActivePath -eq (Join-Path $legacyHome "extensions\byo-models\v1")
        source = $migrated.extensions.'byo-models'.source.commit -eq "abc123"
        update = $migrated.extensions.'byo-models'.update.channel -eq "stable"
        updatedAt = [DateTimeOffset]::Parse($migrated.extensions.'byo-models'.updatedAt).ToUniversalTime().ToString("o") -eq "2026-01-02T03:04:05.0000000+00:00"
    }
    if (@($migrationChecks.GetEnumerator() | Where-Object { !$_.Value }).Count -gt 0) {
        throw "Legacy registry migration checks failed: $($migrationChecks | ConvertTo-Json -Compress)"
    }
    if ((Get-Content $legacyConfigPath -Raw).Trim() -ne '{"legacyConfig":true}' -or
        (Test-Path (Join-Path $legacyHome "extensions\byomodels")) -or
        !(Test-Path (Join-Path $legacyHome "extensions\byo-models\v1")) -or
        !(Test-Path (Join-Path $legacyHome "extensions\byo-models\v2"))) {
        throw "Legacy package migration did not preserve versions and BYOModels configuration."
    }
    foreach ($version in @("v1", "v2")) {
        $cachedManifest = Get-Content (Join-Path $legacyHome "extensions\byo-models\$version\afterburner.json") -Raw | ConvertFrom-Json
        if ($cachedManifest.id -ne "byo-models") { throw "Migrated package manifest retained the legacy ID." }
    }
    Invoke-Manager @("list") | Out-Null
    if ((Get-Content (Join-Path $legacyHome "registry.json") -Raw) -ne $firstMigration) {
        throw "Legacy registry migration is not idempotent."
    }

    $commandHome = Join-Path $sandbox "command-home"
    $env:AFTERBURNER_HOME = $commandHome
    & (Join-Path $projectRoot "afterburn.ps1") install byo-models black-box | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "afterburn install <id...> failed." }
    if (!(Test-Path (Join-Path $commandHome "config\black-box.json"))) {
        throw "Selective Black Box installation did not create its missing configuration."
    }
    & (Join-Path $projectRoot "afterburn.ps1") disable black-box | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "afterburn disable black-box failed." }
    & (Join-Path $projectRoot "afterburn.ps1") enable black-box | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "afterburn enable black-box failed." }
    & (Join-Path $projectRoot "afterburn.ps1") disable byo-models | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "afterburn disable <id> failed." }
    & (Join-Path $projectRoot "afterburn.ps1") enable byo-models | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "afterburn enable <id> failed." }
    & (Join-Path $projectRoot "afterburn.ps1") uninstall byo-models | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "afterburn uninstall <id> failed." }
    if (!(Test-Path (Join-Path $commandHome "config\byomodels.json"))) {
        throw "Direct lifecycle commands did not preserve BYOModels configuration."
    }
    $blackBoxData = Join-Path $commandHome "extension-data\black-box\state.json"
    New-Item -ItemType Directory -Force (Split-Path $blackBoxData -Parent) | Out-Null
    Set-Content $blackBoxData '{"durable":true}' -Encoding utf8
    & (Join-Path $projectRoot "afterburn.ps1") uninstall black-box | Out-Null
    if ($LASTEXITCODE -ne 0 -or !(Test-Path (Join-Path $commandHome "config\black-box.json")) -or
        !(Test-Path $blackBoxData)) {
        throw "afterburn uninstall black-box did not preserve configuration and durable data."
    }

    Write-Output "Selective built-in lifecycle tests passed."
} finally {
    if ($null -eq $previousAfterburnerHome) { Remove-Item Env:AFTERBURNER_HOME -ErrorAction SilentlyContinue }
    else { $env:AFTERBURNER_HOME = $previousAfterburnerHome }
    Remove-Item -LiteralPath $sandbox -Recurse -Force -ErrorAction SilentlyContinue
}

