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

Invoke-Checked -FilePath npm -Arguments @("run", "check:runtime")
Invoke-Checked -FilePath go -Arguments @("test", "./...")
Invoke-Checked -FilePath go -Arguments @("vet", "-unsafeptr=false", "./...")
Invoke-Checked -FilePath npm -Arguments @("test")

New-Item artifacts -ItemType Directory -Force | Out-Null
$previousAfterburnerHome = $env:AFTERBURNER_HOME
$packagingHome = Join-Path ([IO.Path]::GetTempPath()) ("afterburn-package-" + [Guid]::NewGuid().ToString("N"))
try {
    $env:AFTERBURNER_HOME = $packagingHome
    Invoke-Checked -FilePath go -Arguments @("run", "./cmd/afterburn", "extension", "pack", "extensions/BlackBox", "artifacts/black-box.zip")
    Invoke-Checked -FilePath go -Arguments @("run", "./cmd/afterburn", "extension", "pack", "extensions/BYOModels", "artifacts/byo-models.zip")
    Invoke-Checked -FilePath go -Arguments @("run", "./cmd/afterburn", "extension", "pack", "extensions/OpenAIServer", "artifacts/openai-server.zip")
}
finally {
    if ($null -eq $previousAfterburnerHome) {
        Remove-Item Env:AFTERBURNER_HOME -ErrorAction SilentlyContinue
    }
    else {
        $env:AFTERBURNER_HOME = $previousAfterburnerHome
    }
    if (Test-Path $packagingHome) {
        Remove-Item -LiteralPath $packagingHome -Recurse -Force
    }
}
Invoke-Checked -FilePath go -Arguments @("build", "-trimpath", "-o", "artifacts/afterburn.exe", "./cmd/afterburn")
Invoke-Checked -FilePath go -Arguments @("build", "-trimpath", "-o", "artifacts/fakecopilot.exe", "./internal/testutil/fakecopilot")
Invoke-Checked -FilePath node -Arguments @("tests/native-signal-conpty.mjs", "artifacts/afterburn.exe", "artifacts/fakecopilot.exe")
Invoke-Checked -FilePath node -Arguments @("tests/native-modal-conpty.mjs", "artifacts/afterburn.exe", "artifacts/fakecopilot.exe")
Invoke-Checked -FilePath node -Arguments @("tests/real-blackbox-modal-tui.mjs", "artifacts/afterburn.exe")
Invoke-Checked -FilePath npm -Arguments @("run", "test:real-tui:openai-server")
Invoke-Checked -FilePath git -Arguments @("diff", "--check")
