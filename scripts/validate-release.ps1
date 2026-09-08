$ErrorActionPreference = "Stop"

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)]
        [string] $FilePath,
        [Parameter(ValueFromRemainingArguments = $true)]
        [string[]] $Arguments
    )

    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath exited with code $LASTEXITCODE"
    }
}

function Get-SHA256 {
    param([Parameter(Mandatory = $true)][string] $Path)
    $stream = [IO.File]::OpenRead($Path)
    try {
        $sha = [Security.Cryptography.SHA256]::Create()
        try {
            return ([BitConverter]::ToString($sha.ComputeHash($stream))).Replace("-", "")
        }
        finally {
            $sha.Dispose()
        }
    }
    finally {
        $stream.Dispose()
    }
}

Invoke-Checked -FilePath npm -Arguments @("run", "check:runtime")
$generatedArchives = @(
    "internal/assets/generated/black-box.zip",
    "internal/assets/generated/byo-models.zip",
    "internal/assets/generated/openai-server.zip"
)
$generatedHashes = @{}
foreach ($archive in $generatedArchives) {
    if (-not (Test-Path $archive)) {
        throw "Missing generated archive: $archive"
    }
    $generatedHashes[$archive] = Get-SHA256 $archive
}
Invoke-Checked -FilePath go -Arguments @("generate", "./internal/assets")
foreach ($archive in $generatedArchives) {
    if ($generatedHashes[$archive] -ne (Get-SHA256 $archive)) {
        throw "Generated archive is stale: $archive"
    }
}
Invoke-Checked -FilePath go -Arguments @("test", "./...")
Invoke-Checked -FilePath go -Arguments @("vet", "-unsafeptr=false", "./...")
Invoke-Checked -FilePath npm -Arguments @("test")

New-Item artifacts -ItemType Directory -Force | Out-Null
Invoke-Checked -FilePath go -Arguments @("build", "-trimpath", "-o", "artifacts/afterburn.exe", "./cmd/afterburn")
Invoke-Checked -FilePath go -Arguments @("build", "-trimpath", "-o", "artifacts/fakecopilot.exe", "./internal/testutil/fakecopilot")
Invoke-Checked -FilePath node -Arguments @("tests/native-signal-conpty.mjs", "artifacts/afterburn.exe", "artifacts/fakecopilot.exe")
Invoke-Checked -FilePath node -Arguments @("tests/native-modal-conpty.mjs", "artifacts/afterburn.exe", "artifacts/fakecopilot.exe")
Invoke-Checked -FilePath node -Arguments @("tests/real-blackbox-modal-tui.mjs", "artifacts/afterburn.exe")
Invoke-Checked -FilePath npm -Arguments @("run", "test:real-tui:openai-server")
Invoke-Checked -FilePath git -Arguments @("diff", "--check")
