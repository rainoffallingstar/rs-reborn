param(
    [string]$Tag = $env:RS_INSTALL_TAG
)

$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $true

$RepoOwner = "rainoffallingstar"
$RepoName = "rs-reborn"
$BinDir = if ($env:RS_INSTALL_DIR) { $env:RS_INSTALL_DIR } else { Join-Path $HOME ".cargo\bin" }
$BinName = "rvx.exe"
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

function Copy-OrDownload {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination
    )

    if ($Source.StartsWith("file://")) {
        Copy-Item -LiteralPath ([System.Uri]$Source).LocalPath -Destination $Destination -Force
        return
    }
    if (Test-Path -LiteralPath $Source) {
        Copy-Item -LiteralPath $Source -Destination $Destination -Force
        return
    }
    Invoke-WebRequest -Uri $Source -Headers (Get-GitHubHeaders) -OutFile $Destination
}

function Get-ExpectedHash {
    param(
        [Parameter(Mandatory = $true)][string]$ChecksumFile,
        [Parameter(Mandatory = $true)][string]$Asset
    )

    foreach ($line in Get-Content -LiteralPath $ChecksumFile) {
        if ($line -match '^(?<hash>[0-9A-Fa-f]{64})[ *]+(?<name>.+)$') {
            if ($Matches["name"] -eq $Asset) {
                return $Matches["hash"].ToLowerInvariant()
            }
        }
    }
    throw "could not find checksum for $Asset in SHA256SUMS"
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
$Asset = "rvx_${Tag}_windows_${Arch}.zip"
if (-not [string]::IsNullOrWhiteSpace($BaseUrl)) {
    $Url = ($BaseUrl.TrimEnd('/')) + "/" + $Asset
} else {
    $Url = "https://github.com/$RepoOwner/$RepoName/releases/download/$Tag/$Asset"
}

$TempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("rvx-install-" + [System.Guid]::NewGuid().ToString("N"))
$ArchivePath = Join-Path $TempDir $Asset
$ChecksumPath = Join-Path $TempDir "SHA256SUMS"
$ExtractDir = Join-Path $TempDir "extract"

New-Item -ItemType Directory -Force -Path $TempDir | Out-Null
New-Item -ItemType Directory -Force -Path $ExtractDir | Out-Null

try {
    Write-Host "==> downloading $Url"
    if (-not [string]::IsNullOrWhiteSpace($BaseUrl)) {
        Copy-OrDownload -Source (($BaseUrl.TrimEnd('/')) + "/" + $Asset) -Destination $ArchivePath
        Copy-OrDownload -Source (($BaseUrl.TrimEnd('/')) + "/SHA256SUMS") -Destination $ChecksumPath
    } else {
        Copy-OrDownload -Source $Url -Destination $ArchivePath
        Copy-OrDownload -Source "https://github.com/$RepoOwner/$RepoName/releases/download/$Tag/SHA256SUMS" -Destination $ChecksumPath
    }

    $expectedHash = Get-ExpectedHash -ChecksumFile $ChecksumPath -Asset $Asset
    $actualHash = (Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($expectedHash -ne $actualHash) {
        throw "checksum mismatch for $Asset`nexpected: $expectedHash`nactual:   $actualHash"
    }
    Write-Host "==> verified sha256 for $Asset"

    Write-Host "==> extracting $Asset"
    Expand-Archive -Path $ArchivePath -DestinationPath $ExtractDir -Force

    $BinaryPath = Join-Path $ExtractDir $BinName
    if (-not (Test-Path -LiteralPath $BinaryPath)) {
        throw "downloaded archive did not contain $BinName"
    }

    New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
    Copy-Item -LiteralPath $BinaryPath -Destination (Join-Path $BinDir $BinName) -Force

    Write-Host "installed rvx $Tag to $(Join-Path $BinDir $BinName)"
    $pathEntries = ($env:PATH -split ';') | Where-Object { $_ -ne "" }
    if ($pathEntries -notcontains $BinDir) {
        Write-Warning "$BinDir is not currently on PATH"
    }
} finally {
    if (Test-Path -LiteralPath $TempDir) {
        Remove-Item -LiteralPath $TempDir -Recurse -Force
    }
}
