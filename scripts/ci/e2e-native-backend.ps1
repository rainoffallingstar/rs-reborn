$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $true

$RootDir = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("rs-e2e-native-backend-" + [System.Guid]::NewGuid().ToString("N"))
$RSBin = Join-Path $TmpDir "rs.exe"
$ProjectDir = Join-Path $TmpDir "project"
$ScriptPath = Join-Path $ProjectDir "analysis.R"
$RscriptPath = (Get-Command Rscript.exe).Source

New-Item -ItemType Directory -Force -Path $TmpDir, $ProjectDir | Out-Null

try {
    $env:GOCACHE = Join-Path $TmpDir "go-build"
    $env:GOMODCACHE = Join-Path $TmpDir "gomodcache"
    $env:RS_INSTALL_BACKEND = "auto"

    Set-Location $RootDir

    Write-Host "==> building rs"
    go build -o $RSBin ./cmd/rs

@'
cat(jsonlite::toJSON(list(value = "native-backend", bioc = as.character(packageVersion("BiocGenerics"))), auto_unbox = TRUE), "\n")
'@ | Set-Content -LiteralPath $ScriptPath -Encoding ascii

    Write-Host "==> initialize project and verify auto backend resolves through native installer"
    & $RSBin init --rscript $RscriptPath $ProjectDir
    & $RSBin add --project-dir $ProjectDir jsonlite
    & $RSBin add --project-dir $ProjectDir --bioc BiocGenerics

    Write-Host "==> lock through auto/native backend"
    $lockOutputPath = Join-Path $TmpDir "lock.txt"
    $lockSucceeded = $true
    try {
        $lockText = (& $RSBin lock $ScriptPath *>&1 | Tee-Object -FilePath $lockOutputPath) | Out-String
    } catch {
        $lockSucceeded = $false
        if (Test-Path -LiteralPath $lockOutputPath) {
            $lockText = Get-Content -LiteralPath $lockOutputPath -Raw
        } else {
            $lockText = ""
        }
    }
    if (-not $lockSucceeded) {
        throw "rs lock failed:`n$lockText"
    }
    if ($lockText -notmatch "native backend") {
        throw "expected native backend install output:`n$lockText"
    }
    if ($lockText -match "falling back to legacy") {
        throw "unexpected legacy fallback while RS_INSTALL_BACKEND=auto"
    }

    Write-Host "==> run through auto/native backend"
    $runText = (& $RSBin run --locked $ScriptPath 2>&1 | Tee-Object -FilePath (Join-Path $TmpDir "run.txt")) | Out-String
    if ($runText -notmatch '"value":"native-backend"' -or $runText -notmatch '"bioc":"') {
        throw "expected JSON output from native backend run:`n$runText"
    }
    if ($runText -match "falling back to legacy") {
        throw "unexpected legacy fallback while RS_INSTALL_BACKEND=auto"
    }

    Write-Host "Auto/native backend E2E passed"
    $global:LASTEXITCODE = 0
} finally {
    if (Test-Path -LiteralPath $TmpDir) {
        Remove-Item -LiteralPath $TmpDir -Recurse -Force
    }
}
