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

& (Join-Path $projectRoot "scripts\install.ps1") -InstallRuntime

$directVersion = copilot version | Out-String
if ($directVersion -match "runtime-extension-host") {
    throw "Normal copilot invocation unexpectedly loaded Afterburner."
}

$env:COPILOT_RUNTIME_EXTENSION_SELF_TEST = "1"
try {
    $result = copilot --prefer-version 9999.0.0-afterburner version |
        Out-String |
        ConvertFrom-Json
} finally {
    Remove-Item Env:COPILOT_RUNTIME_EXTENSION_SELF_TEST -ErrorAction SilentlyContinue
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

$versionOutput = copilot version | Out-String
if ($LASTEXITCODE -ne 0 -or $versionOutput -notmatch "GitHub Copilot CLI") {
    throw "Copilot failed to start through Afterburner."
}

$copilotHome = if ($env:COPILOT_HOME) { $env:COPILOT_HOME } else { Join-Path $HOME ".copilot" }
$transformedApp = Get-ChildItem (Join-Path $copilotHome "pkg\win32-*\*\.afterburner-app.mjs") -File |
    Sort-Object LastWriteTime -Descending |
    Select-Object -First 1
if (-not $transformedApp -or -not (
    (Select-String -Path $transformedApp -SimpleMatch '[j,se,Ge,q,T,P,N,R,D,$e,Ne,a,Qe])' -Quiet) -or
    (Select-String -Path $transformedApp -SimpleMatch '[j,se,$e,Q,T,P,N,R,O,Le,Ne,a,Ke])' -Quiet)
)) {
    throw "Expected the picker row renderer to depend on the active context focus state."
}

Write-Output "Afterburner tests passed."
