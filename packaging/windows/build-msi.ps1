#Requires -Version 5.1
<#
.SYNOPSIS
  Build the unsigned Kube Workspaces Windows MSI from a staged tag archive.

.DESCRIPTION
  Harvests kube-workspaces.exe (required), kube-workspaces-web.exe (optional:
  absent on windows/arm64) and Tier 1 codec DLLs (optional, future) from a
  directory holding the extracted tag-archive contents, generates the WiX
  payload fragment (Files.wxs), and runs `wix build` (WiX v4/v5).

  The MSI is UNSIGNED: install hygiene only (Add/Remove, Start Menu shortcut,
  upgrade/uninstall), not a trust fix. SmartScreen warns the same as the zip.

.PARAMETER Version
  Release tag, e.g. v0.1.1. Sanitized to an MSI ProductVersion (X.Y.Z);
  dev/dirty strings fall back to 0.0.0 so main-branch validation builds work.

.PARAMETER Arch
  amd64 or arm64 (mapped to wix -arch x64 / arm64).

.PARAMETER StagingDir
  Directory with the extracted archive files (kube-workspaces.exe, ...).

.PARAMETER OutFile
  Output .msi path.

.PARAMETER WxsDir
  Directory holding kube-workspaces.wxs (defaults to this script's dir).

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File packaging/windows/build-msi.ps1 `
    -Version v0.1.1 -Arch amd64 `
    -StagingDir C:\staging\kube-workspaces-windows-amd64 `
    -OutFile dist\kube-workspaces-v0.1.1-windows-amd64.msi
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$Version,
  [Parameter(Mandatory = $true)][ValidateSet("amd64", "arm64")][string]$Arch,
  [Parameter(Mandatory = $true)][string]$StagingDir,
  [Parameter(Mandatory = $true)][string]$OutFile,
  [string]$WxsDir = $PSScriptRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-MsiProductVersion([string]$tag) {
  # Windows Installer compares three fields, with limits 255.255.65535.
  # Never silently truncate an unrepresentable release version.
  if ($tag -notmatch '^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
    return "0.0.0"
  }
  $nums = @([long]$Matches[1], [long]$Matches[2], [long]$Matches[3])
  if ($nums[0] -gt 255 -or $nums[1] -gt 255 -or $nums[2] -gt 65535) {
    throw "Release version '$tag' exceeds MSI limits (255.255.65535)."
  }
  return ($nums -join '.')
}

function Get-SafeId([string]$name) {
  # WiX identifiers: letters, digits, underscore; must not start with a digit.
  $id = $name -replace '[^A-Za-z0-9_]', '_'
  if ($id -match '^[0-9]') { $id = '_' + $id }
  return $id
}

# $PSScriptRoot comes back empty when the script is invoked via a relative
# -File path from a UNC working directory (Git Bash on a \\wsl$ mapping:
# powershell.exe cannot hold a UNC current directory, so the relative script
# path never resolves). Fall back to the invocation path, and fail with the
# fix instead of a cryptic Join-Path bind error downstream.
if ([string]::IsNullOrEmpty($WxsDir)) {
  $WxsDir = Split-Path -Parent $MyInvocation.MyCommand.Path
}
if ([string]::IsNullOrEmpty($WxsDir)) {
  Write-Error "Cannot resolve the packaging directory. Re-run with an absolute script path, e.g. -File 'Z:\...\packaging\windows\build-msi.ps1'."
  exit 1
}

if (-not (Test-Path $StagingDir -PathType Container)) {
  Write-Error "StagingDir not found: $StagingDir"
  exit 1
}
$staging = (Resolve-Path $StagingDir).Path
$OutFile = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutFile)

$shell = Join-Path $staging "kube-workspaces.exe"
if (-not (Test-Path $shell -PathType Leaf)) {
  Write-Error "Required shell binary missing: $shell"
  exit 1
}

# Payload contract: shell (required) + web child (absent on arm64) + Tier 1
# codec DLLs (future slots; installer accepts whatever of these is staged).
$payload = @("kube-workspaces.exe")
if ($Arch -eq "amd64" -and -not (Test-Path (Join-Path $staging "kube-workspaces-web.exe") -PathType Leaf)) {
  throw "Windows amd64 requires the sibling kube-workspaces-web.exe."
}
foreach ($opt in @("kube-workspaces-web.exe", "avcodec-59.dll", "avutil-57.dll", "opus.dll", "libopus-0.dll")) {
  if (Test-Path (Join-Path $staging $opt) -PathType Leaf) { $payload += $opt }
}
$stagedDlls = Get-ChildItem $staging -Filter "*.dll" -File | ForEach-Object { $_.Name } | Where-Object { $payload -notcontains $_ }
foreach ($extra in $stagedDlls) {
  Write-Warning "Staging holds unexpected DLL '$extra'; including it in the MSI."
  $payload += $extra
}

$productVersion = Get-MsiProductVersion $Version
Write-Host "ProductVersion: $productVersion (from $Version)"

$wixArch = @{ amd64 = "x64"; arm64 = "arm64" }[$Arch]
$skeleton = Join-Path $WxsDir "kube-workspaces.wxs"
if (-not (Test-Path $skeleton -PathType Leaf)) {
  Write-Error "WiX skeleton missing: $skeleton"
  exit 1
}
$icon = Join-Path $WxsDir "..\..\assets\icon.ico"
if (-not (Test-Path $icon -PathType Leaf)) {
  Write-Error "Icon missing: $icon"
  exit 1
}
$iconPath = (Resolve-Path $icon).Path

# Generated fragment: one 64-bit component per payload file. The shell's File
# Id is the fixed contract ShellExe (the skeleton's shortcut targets it).
$fragment = Join-Path ([System.IO.Path]::GetTempPath()) ("kw-files-" + [System.Guid]::NewGuid().ToString("N") + ".wxs")
$lines = @(
  '<?xml version="1.0" encoding="UTF-8"?>',
  '<!-- Generated by build-msi.ps1; do not edit. Lists the staged payload. -->',
  '<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">',
  '  <Fragment>',
  '    <ComponentGroup Id="PayloadFiles">'
)
foreach ($file in $payload) {
  $fileId = "ShellExe"
  if ($file -ne "kube-workspaces.exe") { $fileId = "File_" + (Get-SafeId $file) }
  $compId = "Comp_" + (Get-SafeId $file)
  $src = [System.Security.SecurityElement]::Escape((Join-Path $staging $file))
  $lines += "      <Component Id=`"$compId`" Directory=`"INSTALLDIR`" Bitness=`"always64`" Guid=`"*`">"
  $lines += "        <File Id=`"$fileId`" Source=`"$src`" KeyPath=`"yes`" />"
  $lines += "      </Component>"
}
$lines += @(
  '    </ComponentGroup>',
  '  </Fragment>',
  '</Wix>'
)
[System.IO.File]::WriteAllLines($fragment, ([string[]]$lines), [System.Text.Encoding]::UTF8)
Write-Host "Payload: $($payload -join ', ')"

try {
  $wix = Get-Command wix -ErrorAction SilentlyContinue
  if (-not $wix) {
    Write-Host "WiX CLI not found; installing via dotnet tool..."
    & dotnet tool install --global wix --version 5.0.2 | Write-Host
    if ($LASTEXITCODE -ne 0) { throw "WiX tool installation failed ($LASTEXITCODE)." }
    $wix = Get-Command wix -ErrorAction SilentlyContinue
  }
  if (-not $wix) {
    Write-Error "WiX CLI (wix) still unavailable after install; need dotnet SDK + WiX v4/v5."
    exit 1
  }
  & wix --version | Write-Host

  $outDir = Split-Path $OutFile -Parent
  if ($outDir -and -not (Test-Path $outDir)) { New-Item -ItemType Directory -Path $outDir -Force | Out-Null }

  & wix build $skeleton $fragment `
    -arch $wixArch `
    -d ProductVersion=$productVersion `
    -d IconPath="$iconPath" `
    -o $OutFile
  if ($LASTEXITCODE -ne 0) {
    Write-Error "wix build failed (exit $LASTEXITCODE)."
    exit 1
  }

  # Verify the authored database read-only; never mutate it after validation.
  & (Join-Path $WxsDir "verify-msi.ps1") -Path $OutFile -Arch $Arch -Version $productVersion -StagingDir $staging
  Write-Host "Built $OutFile"
}
finally {
  Remove-Item $fragment -ErrorAction SilentlyContinue
}
