[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]*$')]
    [string]$Version,

    [string]$OutputDir = (Join-Path (Get-Location) "dist\windows"),
    [string]$TargetTriple = "x86_64-pc-windows-msvc",
    [string]$ApiVersion = "1",
    [ValidateSet("desktop-managed", "external")]
    [string]$Ownership = "desktop-managed",
    [string]$DesktopExecutable = "",
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
$repositoryRoot = Split-Path -Parent $PSScriptRoot
if ([IO.Path]::IsPathRooted($OutputDir)) {
    $outputPath = [IO.Path]::GetFullPath($OutputDir)
}
else {
    $outputPath = [IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputDir))
}
$archiveArch = "amd64"
if ($TargetTriple -like "aarch64-*") {
    $archiveArch = "arm64"
}
elseif ($TargetTriple -notlike "x86_64-*") {
    throw "unsupported Windows target triple: $TargetTriple"
}
$gatewayPath = Join-Path $outputPath "Free-Model-Router.exe"
$cliPath = Join-Path $outputPath "fmr.exe"
$metadataPath = Join-Path $outputPath "Free-Model-Router.exe.metadata.json"
$manifestPath = Join-Path $outputPath "manifest.json"

Push-Location $repositoryRoot
try {
    New-Item -ItemType Directory -Force -Path $outputPath | Out-Null

    if (-not $SkipBuild) {
        $oldGOOS = $env:GOOS
        $oldGOARCH = $env:GOARCH
        try {
            $env:GOOS = "windows"
            $env:GOARCH = if ($archiveArch -eq "arm64") { "arm64" } else { "amd64" }
            $ldflags = "-s -w -X github.com/konor123/Free-Model-Router/internal/app.BuildVersion=$Version"
            & go build -trimpath -ldflags $ldflags -o $gatewayPath ./cmd/Free-Model-Router
            if ($LASTEXITCODE -ne 0) {
                throw "gateway build failed with exit code $LASTEXITCODE"
            }
            & go build -trimpath -ldflags $ldflags -o $cliPath ./cmd/fmr
            if ($LASTEXITCODE -ne 0) {
                throw "control CLI build failed with exit code $LASTEXITCODE"
            }
        }
        finally {
            if ($null -eq $oldGOOS) { Remove-Item Env:GOOS -ErrorAction SilentlyContinue } else { $env:GOOS = $oldGOOS }
            if ($null -eq $oldGOARCH) { Remove-Item Env:GOARCH -ErrorAction SilentlyContinue } else { $env:GOARCH = $oldGOARCH }
        }
    }

    if (-not (Test-Path -LiteralPath $gatewayPath -PathType Leaf)) {
        throw "gateway sidecar not found: $gatewayPath"
    }
    if (-not (Test-Path -LiteralPath $cliPath -PathType Leaf)) {
        throw "control CLI not found: $cliPath"
    }

    $metadataArgs = @(
        "run", "./cmd/fmr-package",
        "-binary", $gatewayPath,
        "-metadata", $metadataPath,
        "-target", $TargetTriple,
        "-version", $Version,
        "-api-version", $ApiVersion,
        "-ownership", $Ownership,
        "-manifest", $manifestPath
    )
    if ([string]::IsNullOrWhiteSpace($DesktopExecutable)) {
        $desktopName = "Free-Model-Router-desktop.exe"
    }
    else {
        $desktopName = [IO.Path]::GetFileName($DesktopExecutable)
        if (-not (Test-Path -LiteralPath $DesktopExecutable -PathType Leaf)) {
            throw "desktop executable not found: $DesktopExecutable"
        }
        Copy-Item -Force -LiteralPath $DesktopExecutable -Destination (Join-Path $outputPath $desktopName)
    }
    $metadataArgs += @("-desktop-executable", $desktopName)
    if (-not [string]::IsNullOrWhiteSpace($DesktopExecutable)) {
        $metadataArgs += "-desktop-included"
    }
    & go @metadataArgs | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "sidecar metadata generation failed with exit code $LASTEXITCODE"
    }

    Copy-Item -Force -LiteralPath (Join-Path $repositoryRoot "packaging\windows\manifest.schema.json") -Destination (Join-Path $outputPath "manifest.schema.json")
    $archivePath = Join-Path (Split-Path -Parent $outputPath) ("Free-Model-Router-{0}-windows-{1}.zip" -f $Version, $archiveArch)
    if (Test-Path -LiteralPath $archivePath) {
        Remove-Item -Force -LiteralPath $archivePath
    }
    Compress-Archive -Force -Path (Join-Path $outputPath "*") -DestinationPath $archivePath
    Write-Output ("PACKAGE_OK {0}" -f $archivePath)
}
finally {
    Pop-Location
}
