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
    $commit = (& git rev-parse HEAD 2>$null)
    if ([string]::IsNullOrWhiteSpace($commit)) {
        $commit = "unknown"
    } else {
        $commit = $commit.Trim()
    }
    $buildDate = "2099-01-01T00:00:00Z"
    $ldflags = "-s -w -X github.com/rainoffallingstar/rs-reborn/internal/cli.cliVersion=$Tag -X github.com/rainoffallingstar/rs-reborn/internal/cli.cliCommit=$commit -X github.com/rainoffallingstar/rs-reborn/internal/cli.cliBuildDate=$buildDate"
    & go build -trimpath "-ldflags=$ldflags" -o (Join-Path $StagingDir "rs.exe") ./cmd/rs
    Compress-Archive -Path (Join-Path $StagingDir "rs.exe") -DestinationPath (Join-Path $DistDir $Asset) -Force
    $hash = (Get-FileHash -LiteralPath (Join-Path $DistDir $Asset) -Algorithm SHA256).Hash.ToLowerInvariant()
    Set-Content -LiteralPath (Join-Path $DistDir "SHA256SUMS") -Value "$hash  $Asset"

    Write-Host "==> installing rs from the locally built release artifact"
    $env:RS_INSTALL_TAG = $Tag
    $env:RS_INSTALL_BASE_URL = $DistDir
    $env:RS_INSTALL_DIR = $InstallDir
    $installText = (& (Join-Path $RootDir "install.ps1") *>&1) | Out-String
    $installText | Tee-Object -FilePath (Join-Path $TmpDir "install.txt") | Out-Null
    if ($installText -notmatch "verified sha256") {
        throw "expected checksum verification output"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $InstallDir "rs.exe"))) {
        throw "expected installed rs.exe"
    }

    Write-Host "==> running the installed binary"
    $helpText = (& (Join-Path $InstallDir "rs.exe") --help) | Out-String
    $helpText | Tee-Object -FilePath (Join-Path $TmpDir "help.txt") | Out-Null
    if ($helpText -notmatch "Usage:") {
        throw "expected help output"
    }
    $versionText = (& (Join-Path $InstallDir "rs.exe") version) | Out-String
    if ($versionText -notmatch "rs $Tag") {
        throw "expected version output to include release tag"
    }

    Write-Host "release install smoke E2E passed"
    $global:LASTEXITCODE = 0
} finally {
    Remove-Item Env:RS_INSTALL_TAG, Env:RS_INSTALL_BASE_URL, Env:RS_INSTALL_DIR -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $TmpDir) {
        Remove-Item -LiteralPath $TmpDir -Recurse -Force
    }
}
