$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $true

$RootDir = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("rs-e2e-release-install-" + [System.Guid]::NewGuid().ToString("N"))
$DistDir = Join-Path $TmpDir "dist"
$InstallDir = Join-Path $TmpDir "bin"
$Tag = "2099-01-01"
$Asset = "rs_${Tag}_windows_amd64.zip"
$StagingDir = Join-Path $TmpDir "stage"

New-Item -ItemType Directory -Force -Path $TmpDir, $DistDir, $InstallDir, $StagingDir | Out-Null

try {
    $env:GOCACHE = Join-Path $TmpDir "go-build"
    $env:GOMODCACHE = Join-Path $TmpDir "gomodcache"

    Set-Location $RootDir

    Write-Host "==> building host release artifact"
    go build -trimpath -ldflags="-s -w" -o (Join-Path $StagingDir "rs.exe") ./cmd/rs
    Compress-Archive -Path (Join-Path $StagingDir "rs.exe") -DestinationPath (Join-Path $DistDir $Asset) -Force

    Write-Host "==> installing rs from the locally built release artifact"
    $env:RS_INSTALL_TAG = $Tag
    $env:RS_INSTALL_BASE_URL = $DistDir
    $env:RS_INSTALL_DIR = $InstallDir
    $installText = (& (Join-Path $RootDir "install.ps1")) | Out-String
    $installText | Tee-Object -FilePath (Join-Path $TmpDir "install.txt") | Out-Null
    if (-not (Test-Path -LiteralPath (Join-Path $InstallDir "rs.exe"))) {
        throw "expected installed rs.exe"
    }
    if ($installText -notmatch "installed rs $Tag") {
        throw "unexpected install output:`n$installText"
    }

    Write-Host "==> running the installed binary"
    $helpText = (& (Join-Path $InstallDir "rs.exe") --help) | Out-String
    $helpText | Tee-Object -FilePath (Join-Path $TmpDir "help.txt") | Out-Null
    if ($helpText -notmatch "Usage:") {
        throw "expected help output"
    }

    Write-Host "release install smoke E2E passed"
    $global:LASTEXITCODE = 0
} finally {
    Remove-Item Env:RS_INSTALL_TAG, Env:RS_INSTALL_BASE_URL, Env:RS_INSTALL_DIR -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $TmpDir) {
        Remove-Item -LiteralPath $TmpDir -Recurse -Force
    }
}
