$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $true

$RootDir = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("rs-e2e-smoke-" + [System.Guid]::NewGuid().ToString("N"))
$RSBin = Join-Path $TmpDir "rs.exe"
$ProjectDir = Join-Path $TmpDir "project"
$ScriptPath = Join-Path $ProjectDir "analysis.R"
$RscriptPath = (Get-Command Rscript.exe).Source

New-Item -ItemType Directory -Force -Path $TmpDir, $ProjectDir | Out-Null

try {
    $env:GOCACHE = Join-Path $TmpDir "go-build"
    $env:GOMODCACHE = Join-Path $TmpDir "gomodcache"
    $env:RS_INSTALL_BACKEND = "native"

    Set-Location $RootDir

    Write-Host "==> building rs"
    go build -o $RSBin ./cmd/rs

    Write-Host "==> non-mutating example coverage"
    $scanText = (& $RSBin scan (Join-Path $RootDir "examples\cran-basic\analysis.R")) | Out-String
    if ($scanText -notmatch "jsonlite") {
        throw "expected scan output to include jsonlite"
    }
    $listExample = (& $RSBin list --json (Join-Path $RootDir "examples\multi-script\scripts\report.R")) | Out-String
    if ($listExample -notmatch '"script_profile": "scripts/report.R"') {
        throw "expected list example JSON profile"
    }

@'
args <- commandArgs(trailingOnly = TRUE)
cat(jsonlite::toJSON(list(args = args, lib = .libPaths()[1]), auto_unbox = TRUE), "\n")
'@ | Set-Content -LiteralPath $ScriptPath -Encoding ascii

    & $RSBin init --rscript $RscriptPath --from $ScriptPath $ProjectDir
    $projectConfig = Get-Content -LiteralPath (Join-Path $ProjectDir "rs.toml") -Raw
    if ($projectConfig -notmatch 'rscript = ') {
        throw "expected init to write rscript"
    }

    $listJson = (& $RSBin list --json $ScriptPath) | Out-String
    if ($listJson -notmatch '"rscript_path":' -or $listJson -notmatch '"cran_packages":') {
        throw "expected list JSON output"
    }
    $doctorJson = (& $RSBin doctor --json $ScriptPath) | Out-String
    if ($doctorJson -notmatch '"rscript_path":') {
        throw "expected doctor JSON output"
    }

    & $RSBin lock $ScriptPath
    if (-not (Test-Path -LiteralPath (Join-Path $ProjectDir "rs.lock.json"))) {
        throw "expected lockfile to exist"
    }
    & $RSBin check $ScriptPath

    $execText = (& $RSBin exec --frozen -e 'cat(jsonlite::toJSON(list(exec=TRUE), auto_unbox=TRUE), "\n")' $ScriptPath) | Out-String
    if ($execText -notmatch '{"exec":true}') {
        throw "expected exec output"
    }

    $shellInput = 'cat(jsonlite::toJSON(list(shell=TRUE), auto_unbox=TRUE), "\n"); q("no")'
    $shellText = ($shellInput | & $RSBin shell --frozen $ScriptPath) | Out-String
    if ($shellText -notmatch '{"shell":true}') {
        throw "expected shell output"
    }

    $runText = (& $RSBin run --locked $ScriptPath alpha beta) | Out-String
    if ($runText -notmatch '"args":\["alpha","beta"\]') {
        throw "expected run output"
    }

    $cacheDir = (& $RSBin cache dir $ScriptPath) | Out-String
    if ([string]::IsNullOrWhiteSpace($cacheDir)) {
        throw "expected cache dir output"
    }
    $cacheList = (& $RSBin cache ls --json $ScriptPath) | Out-String
    if ($cacheList -notmatch '"active": true') {
        throw "expected cache listing output"
    }

    $rWhich = (& $RSBin r which $ProjectDir) | Out-String
    if ($rWhich -notmatch [Regex]::Escape($RscriptPath)) {
        throw "expected rs r which output"
    }

    Write-Host "CLI smoke E2E passed"
    $global:LASTEXITCODE = 0
} finally {
    if (Test-Path -LiteralPath $TmpDir) {
        Remove-Item -LiteralPath $TmpDir -Recurse -Force
    }
}
