$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $true

$RootDir = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("rs-e2e-pak-" + [System.Guid]::NewGuid().ToString("N"))
$RSBin = Join-Path $TmpDir "rs.exe"
$ProjectDir = Join-Path $TmpDir "project"
$ScriptPath = Join-Path $ProjectDir "analysis.R"
$RscriptPath = (Get-Command Rscript.exe).Source

New-Item -ItemType Directory -Force -Path $TmpDir, $ProjectDir | Out-Null

try {
    $env:GOCACHE = Join-Path $TmpDir "go-build"
    $env:GOMODCACHE = Join-Path $TmpDir "gomodcache"
    $env:R_LIBS_USER = Join-Path $TmpDir "r-user-lib"
    $env:RS_INSTALL_BACKEND = "pak"

    Set-Location $RootDir

    Write-Host "==> building rs"
    go build -o $RSBin ./cmd/rs

    Write-Host "==> installing pak into isolated user library"
    New-Item -ItemType Directory -Force -Path $env:R_LIBS_USER | Out-Null
    & Rscript.exe -e 'options(repos = c(CRAN = "https://cloud.r-project.org"), timeout = max(300, getOption("timeout"))); install.packages("pak", lib = Sys.getenv("R_LIBS_USER"))'
    $pakCheck = & Rscript.exe -e 'cat("pak=", requireNamespace("pak", quietly = TRUE), "\n", sep = "")'
    $pakCheck | Tee-Object -FilePath (Join-Path $TmpDir "pak-check.txt")
    if ($pakCheck -notmatch 'pak=TRUE') {
        throw "pak was not installed successfully"
    }

@'
cat(jsonlite::toJSON(list(value = "pak-backend"), auto_unbox = TRUE), "\n")
'@ | Set-Content -LiteralPath $ScriptPath -Encoding ascii

    Write-Host "==> initialize project and force pak backend"
    & $RSBin init --rscript $RscriptPath $ProjectDir
    & $RSBin add --project-dir $ProjectDir jsonlite

    Write-Host "==> run through pak backend"
    $lockText = (& $RSBin lock $ScriptPath 2>&1 | Tee-Object -FilePath (Join-Path $TmpDir "lock.txt")) | Out-String
    if ($lockText -notmatch 'installing via pak:|installing packages via pak backend') {
        throw "expected pak backend install output:`n$lockText"
    }
    if ($lockText -match 'falling back to legacy') {
        throw "unexpected legacy fallback while RS_INSTALL_BACKEND=pak"
    }

    & $RSBin check $ScriptPath
    $runText = (& $RSBin run --locked $ScriptPath 2>&1 | Tee-Object -FilePath (Join-Path $TmpDir "run.txt")) | Out-String
    if ($runText -notmatch '"value":"pak-backend"') {
        throw "expected pak backend run output:`n$runText"
    }
    if ($runText -match 'falling back to legacy') {
        throw "unexpected legacy fallback while running with RS_INSTALL_BACKEND=pak"
    }

    Write-Host "Pak backend E2E passed"
} finally {
    if (Test-Path -LiteralPath $TmpDir) {
        Remove-Item -LiteralPath $TmpDir -Recurse -Force
    }
}
