$ErrorActionPreference = "Stop"

$projectRoot = Split-Path -Parent $PSScriptRoot

$helpOutput = & (Join-Path $projectRoot "afterburn.ps1") help | Out-String
if ($helpOutput -notmatch "afterburn extension install") {
    throw "Standalone Afterburner command help is incomplete."
}

$compatibilityTest = @'
import { createServer } from "node:http";
import { startRequestCompatibilityProxy } from "./extensions/BYOModels/extensions/BYOModels/request-compatibility.mjs";

let received;
let receivedAuthorization;
let tokenRequestCount = 0;
const upstream = createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    received = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    receivedAuthorization = request.headers.authorization;
    response.writeHead(200, { "content-type": "application/json" });
    response.end('{"ok":true}');
});
await new Promise((resolve) => upstream.listen(0, "127.0.0.1", resolve));
const upstreamAddress = upstream.address();
const proxy = await startRequestCompatibilityProxy({
    name: "test",
    baseUrl: `http://127.0.0.1:${upstreamAddress.port}`,
    requestCompatibility: { maxInputItemIdLength: 64 }
}, {
    getBearerToken: async () => {
        tokenRequestCount++;
        return `fresh-token-${tokenRequestCount}`;
    }
});
const oversizedId = "x".repeat(496);
const sendRequest = () => fetch(`${proxy.baseUrl}/responses`, {
    method: "POST",
    headers: {
        "content-type": "application/json",
        authorization: "Bearer stale-runtime-token"
    },
    body: JSON.stringify({
        input: [
            { type: "tool_search_call", id: oversizedId },
            { type: "reasoning", id: oversizedId },
            { type: "message", id: "valid-id" }
        ]
    })
});
await sendRequest();
if (received.input[0].id !== received.input[1].id ||
    received.input[0].id.length > 64 ||
    received.input[2].id !== "valid-id") {
    throw new Error("Compatibility proxy did not safely normalize oversized input item IDs.");
}
if (receivedAuthorization !== "Bearer fresh-token-1") {
    throw new Error(`Compatibility proxy did not inject the expected bearer token: ${receivedAuthorization}`);
}
await sendRequest();
if (receivedAuthorization !== "Bearer fresh-token-2" || tokenRequestCount !== 2) {
    throw new Error("Compatibility proxy did not reacquire authentication for the next request.");
}
await proxy.close();
await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
'@
$compatibilityTest | node --input-type=module -
if ($LASTEXITCODE -ne 0) {
    throw "BYOK request compatibility proxy test failed."
}

& (Join-Path $projectRoot "scripts\install.ps1") -InstallRuntime:$false

$directVersion = copilot version 2>&1 | Out-String
if ($directVersion -match "runtime-extension-host") {
    throw "Normal copilot invocation unexpectedly loaded Afterburner."
}

$testHome = Join-Path ([System.IO.Path]::GetTempPath()) ("afterburner-test-" + [guid]::NewGuid().ToString("N"))
$testConfigDirectory = Join-Path $testHome "config"
New-Item -ItemType Directory -Force $testConfigDirectory | Out-Null
Copy-Item (Join-Path $projectRoot "extensions\BYOModels\extensions\BYOModels\models.example.json") `
    (Join-Path $testConfigDirectory "byomodels.json")
$previousAfterburnerHome = $env:AFTERBURNER_HOME
$env:AFTERBURNER_HOME = $testHome
$env:COPILOT_RUNTIME_EXTENSION_SELF_TEST = "1"
try {
    & node (Join-Path $projectRoot "src\extension-manager.mjs") install `
        (Join-Path $projectRoot "extensions\BYOModels") | Out-Null
    & node (Join-Path $projectRoot "src\extension-manager.mjs") enable byomodels | Out-Null
    $result = & (Join-Path $projectRoot "afterburn.ps1") --version |
        Out-String |
        ConvertFrom-Json
} finally {
    Remove-Item Env:COPILOT_RUNTIME_EXTENSION_SELF_TEST -ErrorAction SilentlyContinue
    if ($null -eq $previousAfterburnerHome) { Remove-Item Env:AFTERBURNER_HOME -ErrorAction SilentlyContinue }
    else { $env:AFTERBURNER_HOME = $previousAfterburnerHome }
    Remove-Item -LiteralPath $testHome -Recurse -Force -ErrorAction SilentlyContinue
}

if (-not $result.projection.hasReasoningColumn) {
    throw "Expected a reasoning column from the installed BYOK adapter."
}
if (-not $result.projection.hasContextColumn) {
    throw "Expected a context column from the installed BYOK adapter."
}
foreach ($task in @($result.externalTasks)) {
    if ($task.nativeId.Length -gt 63 -or $task.nativeId -notmatch '^ext_[A-Za-z0-9_-]+$') {
        throw "External task provider emitted an invalid native task ID."
    }
    if (@($result.externalTasks).Count -gt 0 -and -not $result.externalTaskRead) {
        throw "External task read routing did not return a result."
    }
    if ($result.externalTaskRevision -lt 0) {
        throw "External task subscription revision is invalid."
    }
}

$expectedContexts = @("256K", "512K", "768K", "1.05M")
foreach ($entry in $result.contextCycles.PSObject.Properties) {
    $actual = @($entry.Value | ForEach-Object { $_.contextCellText })
    if (($actual -join ",") -ne ($expectedContexts -join ",")) {
        throw "Unexpected context options for $($entry.Name): $($actual -join ', ')"
    }
}

$nativeDefault, $nativeLong = @($result.nativeContextNavigation)
if ($nativeDefault.contextCanLower -or
    -not $nativeDefault.contextCanRaise -or
    $nativeDefault.nextContextTier -ne "long_context") {
    throw "Native default context tier does not expose a proper right-arrow target."
}
if (-not $nativeLong.contextCanLower -or
    $nativeLong.contextCanRaise -or
    $nativeLong.previousContextTier -ne "default") {
    throw "Native long context tier does not expose a proper left-arrow target."
}

$versionOutput = copilot version 2>&1 | Out-String
if ($LASTEXITCODE -ne 0 -or $versionOutput -notmatch "GitHub Copilot CLI") {
    throw "Normal Copilot failed to start after Afterburner."
}

$copilotHome = Join-Path $HOME ".afterburner\copilot-home"
$transformedApp = Get-ChildItem (Join-Path $copilotHome "pkg\win32-*\*\.afterburner-app.mjs") -File |
    Sort-Object LastWriteTime -Descending |
    Select-Object -First 1
if (-not $transformedApp -or -not (
    (Select-String -Path $transformedApp -SimpleMatch '[j,se,Ge,q,T,P,N,R,D,$e,Ne,a,Qe])' -Quiet) -or
    (Select-String -Path $transformedApp -SimpleMatch '[j,se,$e,Q,T,P,N,R,O,Le,Ne,a,Ke])' -Quiet)
)) {
    throw "Expected the picker row renderer to depend on the active context focus state."
}

$normalConfig = Get-Content (Join-Path $HOME ".copilot\config.json") -Raw
if ($normalConfig -match "afterburner-byomodels|steward-burn") {
    throw "Afterburner-managed companions leaked into normal Copilot configuration."
}
if (Test-Path (Join-Path $HOME ".copilot\afterburner")) {
    throw "Afterburner registry leaked into normal Copilot home."
}
if (Test-Path (Join-Path $HOME ".copilot\pkg\win32-x64\9999.0.0-afterburner")) {
    throw "Afterburner runtime leaked into normal Copilot package cache."
}

$managedConfig = Get-Content (Join-Path $HOME ".afterburner\copilot-home\config.json") -Raw | ConvertFrom-Json
$managedByoModels = @($managedConfig.installedPlugins | Where-Object { $_.name -eq "afterburner-byomodels" })
if ($managedByoModels.Count -ne 1 -or
    $managedByoModels[0].source.source -ne "afterburner" -or
    $managedByoModels[0].cache_path -ne "$HOME\.afterburner\copilot-home\extensions\afterburner\byomodels") {
    throw "BYOModels session entrypoint is not registered from the identity-addressed Afterburner package."
}

$upgradeHome = Join-Path ([System.IO.Path]::GetTempPath()) ("afterburner-upgrade-" + [guid]::NewGuid().ToString("N"))
$source = Join-Path $upgradeHome "source"
$userConfig = Join-Path $upgradeHome "config\byomodels.json"
try {
    New-Item -ItemType Directory -Force $source, (Split-Path $userConfig -Parent) | Out-Null
    Copy-Item (Join-Path $projectRoot "extensions\BYOModels\*") $source -Recurse
    Set-Content $userConfig '{"userOwned":true}' -Encoding utf8
    $env:AFTERBURNER_HOME = $upgradeHome
    & node (Join-Path $projectRoot "src\extension-manager.mjs") install $source | Out-Null
    & node (Join-Path $projectRoot "src\extension-manager.mjs") enable byomodels | Out-Null
    Add-Content (Join-Path $source "package.json") " "
    & node (Join-Path $projectRoot "src\extension-manager.mjs") update byomodels | Out-Null
    $upgradeRegistry = Get-Content (Join-Path $upgradeHome "registry.json") -Raw | ConvertFrom-Json
    if (!$upgradeRegistry.extensions.byomodels.enabled -or
        !$upgradeRegistry.extensions.byomodels.previousActivePath) {
        throw "Extension update did not preserve enablement and rollback state."
    }
    if ((Get-Content $userConfig -Raw).Trim() -ne '{"userOwned":true}') {
        throw "Extension update overwrote user configuration."
    }
    $updatedPath = $upgradeRegistry.extensions.byomodels.activePath
    & node (Join-Path $projectRoot "src\extension-manager.mjs") rollback byomodels | Out-Null
    $rolledBack = Get-Content (Join-Path $upgradeHome "registry.json") -Raw | ConvertFrom-Json
    if ($rolledBack.extensions.byomodels.previousActivePath -ne $updatedPath) {
        throw "Extension rollback did not preserve the updated package."
    }
} finally {
    Remove-Item Env:AFTERBURNER_HOME -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $upgradeHome -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Output "Afterburner tests passed."
