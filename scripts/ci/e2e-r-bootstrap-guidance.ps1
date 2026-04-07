$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $true

$RootDir = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("rs-e2e-bootstrap-" + [System.Guid]::NewGuid().ToString("N"))
$RSBin = Join-Path $TmpDir "rvx.exe"
$ProjectDir = Join-Path $TmpDir "project"
$ScriptPath = Join-Path $ProjectDir "analysis.R"
$SanitizedPath = $env:SystemRoot + "\System32"

New-Item -ItemType Directory -Force -Path $TmpDir, $ProjectDir | Out-Null

try {
    $env:GOCACHE = Join-Path $TmpDir "go-build"
    $env:GOMODCACHE = Join-Path $TmpDir "gomodcache"

    Set-Location $RootDir

    Write-Host "==> building rvx"
    go build -o $RSBin ./cmd/rvx

@'
cat("native-r-guidance\n")
'@ | Set-Content -LiteralPath $ScriptPath -Encoding ascii

    Write-Host "==> run should explain how to install R when Rscript is unavailable"
    $runOutput = Join-Path $TmpDir "run.txt"
    $runSucceeded = $true
    $oldPath = $env:PATH
    $oldRSHome = $env:RS_HOME
    try {
        $env:PATH = $SanitizedPath
        $env:RS_HOME = Join-Path $TmpDir "rvx-home"
        & $RSBin run $ScriptPath *> $runOutput
    } catch {
        $runSucceeded = $false
    } finally {
        $env:PATH = $oldPath
        $env:RS_HOME = $oldRSHome
    }
    if ($runSucceeded) {
        throw "expected rvx run to fail without a managed or external Rscript"
    }
    $runText = Get-Content -LiteralPath $runOutput -Raw
    if ($runText -notmatch "next step:" -or $runText -notmatch "RS_AUTO_INSTALL_R=1") {
        throw "missing bootstrap guidance in run output:`n$runText"
    }
    if ($runText -notmatch "rvx r install 4.4") {
        throw "missing Windows native manager next step in run output:`n$runText"
    }

    Write-Host "==> doctor should surface the same next steps"
    $doctorOutput = Join-Path $TmpDir "doctor.txt"
    $doctorSucceeded = $true
    try {
        $env:PATH = $SanitizedPath
        $env:RS_HOME = Join-Path $TmpDir "rvx-home"
        & $RSBin doctor $ScriptPath *> $doctorOutput
    } catch {
        $doctorSucceeded = $false
    } finally {
        $env:PATH = $oldPath
        $env:RS_HOME = $oldRSHome
    }
    if ($doctorSucceeded) {
        throw "expected rvx doctor to report blocking setup issues"
    }
    $doctorText = Get-Content -LiteralPath $doctorOutput -Raw
    if ($doctorText -notmatch "RS_AUTO_INSTALL_R=1 rvx run" -or $doctorText -notmatch "rvx r install 4.4") {
        throw "missing doctor bootstrap guidance:`n$doctorText"
    }

    Write-Host "native R bootstrap guidance E2E passed"
    $global:LASTEXITCODE = 0
} finally {
    if (Test-Path -LiteralPath $TmpDir) {
        Remove-Item -LiteralPath $TmpDir -Recurse -Force
    }
}
