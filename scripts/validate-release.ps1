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
Invoke-Checked -FilePath go -Arguments @("generate", "./internal/assets")
Invoke-Checked -FilePath git -Arguments @("diff", "--exit-code", "--", "internal/assets/generated", "src", "internal/runtimepkg/app.js", "internal/runtimepkg/runtime", "sdk/ui/dist")
Invoke-Checked -FilePath go -Arguments @("test", "./...")
Invoke-Checked -FilePath go -Arguments @("vet", "-unsafeptr=false", "./...")
Invoke-Checked -FilePath npm -Arguments @("run", "test:sdk-ui")
Invoke-Checked -FilePath npm -Arguments @("test")

New-Item artifacts -ItemType Directory -Force | Out-Null
Invoke-Checked -FilePath go -Arguments @("build", "-trimpath", "-o", "artifacts/afterburn.exe", "./cmd/afterburn")
Invoke-Checked -FilePath go -Arguments @("build", "-trimpath", "-o", "artifacts/fakecopilot.exe", "./internal/testutil/fakecopilot")
Invoke-Checked -FilePath node -Arguments @("tests/native-signal-conpty.mjs", "artifacts/afterburn.exe", "artifacts/fakecopilot.exe")
Invoke-Checked -FilePath node -Arguments @("tests/native-modal-conpty.mjs", "artifacts/afterburn.exe", "artifacts/fakecopilot.exe")
Invoke-Checked -FilePath npm -Arguments @("run", "test:real-tui:black-box")
Invoke-Checked -FilePath npm -Arguments @("run", "test:real-tui:openai-server")
Invoke-Checked -FilePath git -Arguments @("diff", "--check")
