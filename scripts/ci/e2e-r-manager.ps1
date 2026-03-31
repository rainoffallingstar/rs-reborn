$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $true

$RootDir = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("rs-e2e-r-manager-" + [System.Guid]::NewGuid().ToString("N"))
$RSBin = Join-Path $TmpDir "rs.exe"
$ProjectDir = Join-Path $TmpDir "project"
$ScriptPath = Join-Path $ProjectDir "analysis.R"
$MismatchProjectDir = Join-Path $TmpDir "mismatch-project"
$MismatchScriptPath = Join-Path $MismatchProjectDir "analysis.R"

New-Item -ItemType Directory -Force -Path $TmpDir, $ProjectDir, $MismatchProjectDir | Out-Null

try {
    $env:GOCACHE = Join-Path $TmpDir "go-build"
    $env:GOMODCACHE = Join-Path $TmpDir "gomodcache"

    Set-Location $RootDir

    Write-Host "==> building rs"
    go build -o $RSBin ./cmd/rs

    Write-Host "==> installing R via the native rs manager"
    & $RSBin r install 4.4
    $rList = (& $RSBin r list) | Out-String
    $rList | Tee-Object -FilePath (Join-Path $TmpDir "r-list.txt") | Out-Null
    if ($rList -notmatch '(?m)^\* managed\s+4\.4') {
        throw "expected managed R listing to include 4.4"
    }

@'
cat("native-r-e2e\n")
'@ | Set-Content -LiteralPath $ScriptPath -Encoding ascii

    & $RSBin init $ProjectDir
    & $RSBin r use --project-dir $ProjectDir 4.4
    $rWhich = (& $RSBin r which $ProjectDir) | Out-String
    $rWhich | Tee-Object -FilePath (Join-Path $TmpDir "r-which.txt") | Out-Null
    if ($rWhich -notmatch "Rscript") {
        throw "expected rs r which to resolve a managed Rscript"
    }
    $projectConfig = Get-Content -LiteralPath (Join-Path $ProjectDir "rs.toml") -Raw
    if ($projectConfig -notmatch 'r_version = "4.4"') {
        throw "expected rs r use 4.4 to write r_version"
    }
    if ($projectConfig -match '(?m)^rscript = ') {
        throw "expected rs r use 4.4 to write r_version instead of rscript"
    }

    $runOutput = (& $RSBin run $ScriptPath) | Out-String
    $runOutput | Tee-Object -FilePath (Join-Path $TmpDir "run.txt") | Out-Null
    if ($runOutput -notmatch "native-r-e2e") {
        throw "expected managed run output"
    }

    Write-Host "==> mismatched r_version and rscript should fail clearly"
@'
cat("native-r-mismatch\n")
'@ | Set-Content -LiteralPath $MismatchScriptPath -Encoding ascii

    @"
repo = "https://cloud.r-project.org"
cache_dir = ".rs-cache"
lockfile = "rs.lock.json"
rscript = "$rWhich"
r_version = "9.9"
"@ | Set-Content -LiteralPath (Join-Path $MismatchProjectDir "rs.toml") -Encoding ascii

    $mismatchOutput = Join-Path $TmpDir "mismatch-list.txt"
    $mismatchSucceeded = $true
    try {
        & $RSBin list $MismatchScriptPath *> $mismatchOutput
    } catch {
        $mismatchSucceeded = $false
    }
    if ($mismatchSucceeded) {
        throw "expected mismatched r_version/rscript configuration to fail"
    }
    $mismatchText = Get-Content -LiteralPath $mismatchOutput -Raw
    if ($mismatchText -notmatch 'configured r_version "9.9" does not match selected interpreter runtime') {
        throw "expected clear mismatch error, got:`n$mismatchText"
    }

    Write-Host "native R manager integration E2E passed"
    $global:LASTEXITCODE = 0
} finally {
    if (Test-Path -LiteralPath $TmpDir) {
        Remove-Item -LiteralPath $TmpDir -Recurse -Force
    }
}
