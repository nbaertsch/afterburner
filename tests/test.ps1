$ErrorActionPreference = "Stop"

$projectRoot = Split-Path -Parent $PSScriptRoot

$compatibilityTest = @'
import { createServer } from "node:http";
import { startRequestCompatibilityProxy } from "./extensions/byok-models/extensions/byok-models/request-compatibility.mjs";

let received;
const upstream = createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    received = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    response.writeHead(200, { "content-type": "application/json" });
    response.end('{"ok":true}');
});
await new Promise((resolve) => upstream.listen(0, "127.0.0.1", resolve));
const upstreamAddress = upstream.address();
const proxy = await startRequestCompatibilityProxy({
    name: "test",
    baseUrl: `http://127.0.0.1:${upstreamAddress.port}`,
    requestCompatibility: { maxInputItemIdLength: 64 }
});
const oversizedId = "x".repeat(496);
await fetch(`${proxy.baseUrl}/responses`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
        input: [
            { type: "tool_search_call", id: oversizedId },
            { type: "reasoning", id: oversizedId },
            { type: "message", id: "valid-id" }
        ]
    })
});
if (received.input[0].id !== received.input[1].id ||
    received.input[0].id.length > 64 ||
    received.input[2].id !== "valid-id") {
    throw new Error("Compatibility proxy did not safely normalize oversized input item IDs.");
}
await proxy.close();
await new Promise((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()));
'@
$compatibilityTest | node --input-type=module -
if ($LASTEXITCODE -ne 0) {
    throw "BYOK request compatibility proxy test failed."
}

& (Join-Path $projectRoot "scripts\install.ps1")

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

Write-Output "Afterburner tests passed."
