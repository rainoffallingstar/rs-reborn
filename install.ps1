param(
    [string]$Tag = $env:RS_INSTALL_TAG
)

$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $true

$RepoOwner = "rainoffallingstar"
$RepoName = "rs-reborn"
$BinDir = if ($env:RS_INSTALL_DIR) { $env:RS_INSTALL_DIR } else { Join-Path $HOME ".cargo\bin" }
$BinName = "rs.exe"
$BaseUrl = $env:RS_INSTALL_BASE_URL

function Get-GitHubHeaders {
    foreach ($name in @("GITHUB_TOKEN", "GH_TOKEN", "GITHUB_PAT")) {
        $value = [Environment]::GetEnvironmentVariable($name)
        if (-not [string]::IsNullOrWhiteSpace($value)) {
            return @{
                Authorization = "Bearer $value"
                Accept = "application/vnd.github+json"
            }
        }
    }
    return @{
        Accept = "application/vnd.github+json"
    }
}

function Get-LatestTag {
    $headers = Get-GitHubHeaders
    $url = "https://api.github.com/repos/$RepoOwner/$RepoName/releases/latest"
    $release = Invoke-RestMethod -Uri $url -Headers $headers
    return [string]$release.tag_name
}

function Get-Arch {
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
    switch ($arch) {
        "x64" { return "amd64" }
        "arm64" { return "arm64" }
        default { throw "unsupported Windows architecture: $arch" }
    }
}

if ([string]::IsNullOrWhiteSpace($Tag)) {
    $Tag = Get-LatestTag
}
if ([string]::IsNullOrWhiteSpace($Tag)) {
    throw "failed to determine latest release tag"
}

$Arch = Get-Arch
$Asset = "rs_${Tag}_windows_${Arch}.zip"
if (-not [string]::IsNullOrWhiteSpace($BaseUrl)) {
    $Url = ($BaseUrl.TrimEnd('/')) + "/" + $Asset
} else {
    $Url = "https://github.com/$RepoOwner/$RepoName/releases/download/$Tag/$Asset"
}

$TempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("rs-install-" + [System.Guid]::NewGuid().ToString("N"))
$ArchivePath = Join-Path $TempDir $Asset
$ExtractDir = Join-Path $TempDir "extract"

New-Item -ItemType Directory -Force -Path $TempDir | Out-Null
New-Item -ItemType Directory -Force -Path $ExtractDir | Out-Null

try {
    Write-Host "==> downloading $Url"
    if (-not [string]::IsNullOrWhiteSpace($BaseUrl) -and ($BaseUrl.StartsWith("file://") -or (Test-Path -LiteralPath $BaseUrl))) {
        $sourceDir = if ($BaseUrl.StartsWith("file://")) {
            ([System.Uri]$BaseUrl).LocalPath
        } else {
            $BaseUrl
        }
        Copy-Item -LiteralPath (Join-Path $sourceDir $Asset) -Destination $ArchivePath -Force
    } else {
        Invoke-WebRequest -Uri $Url -Headers (Get-GitHubHeaders) -OutFile $ArchivePath
    }

    Write-Host "==> extracting $Asset"
    Expand-Archive -Path $ArchivePath -DestinationPath $ExtractDir -Force

    $BinaryPath = Join-Path $ExtractDir $BinName
    if (-not (Test-Path -LiteralPath $BinaryPath)) {
        throw "downloaded archive did not contain $BinName"
    }

    New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
    Copy-Item -LiteralPath $BinaryPath -Destination (Join-Path $BinDir $BinName) -Force

    Write-Host "installed rs $Tag to $(Join-Path $BinDir $BinName)"
    $pathEntries = ($env:PATH -split ';') | Where-Object { $_ -ne "" }
    if ($pathEntries -notcontains $BinDir) {
        Write-Warning "$BinDir is not currently on PATH"
    }
} finally {
    if (Test-Path -LiteralPath $TempDir) {
        Remove-Item -LiteralPath $TempDir -Recurse -Force
    }
}
