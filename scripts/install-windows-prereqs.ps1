# scripts/install-windows-prereqs.ps1
#
# Install the Windows build prerequisites for the desktop client, skipping
# anything already satisfied:
#
#   Go >= 1.26            (shell builds; `make build-all`, `make build-windows`)
#   .NET SDK 8 + WiX v5   (`packaging/windows/build-msi.ps1` installs the wix
#                          CLI itself via `dotnet tool` when missing)
#   MinGW gcc/g++         (web child + cgo builds: `make build-web-windows`,
#                          `make build-windows-cgo`; mirrors CI, which uses
#                          `choco install mingw`)
#
# Installers run through winget when present, else choco (MinGW always uses
# choco, like CI). Needs at least one of the two to be installed.
# If you only build the MSI from CI-downloaded archives you can skip MinGW:
#
#   install-windows-prereqs -SkipMinGW
#
# From Git Bash: `make install-windows-prereqs`, or directly:
#   powershell -ExecutionPolicy Bypass -File scripts/install-windows-prereqs.ps1
#
# PATH changes apply to new shells: restart Git Bash afterwards.
[CmdletBinding()]
param(
  [switch]$SkipMinGW
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ([Environment]::OSVersion.Platform -ne "Win32NT") {
  Write-Error "This script installs Windows prerequisites; run it on Windows."
  exit 1
}

function Refresh-Path {
  # Pick up Machine/User PATH changes made by installers, plus the dotnet
  # global-tools dir (where `wix` lands) for this session.
  $machine = [Environment]::GetEnvironmentVariable("PATH", "Machine")
  $user = [Environment]::GetEnvironmentVariable("PATH", "User")
  $tools = Join-Path $env:USERPROFILE ".dotnet\tools"
  $env:PATH = "$machine;$user;$tools"
}

function Get-GoVersion {
  if (-not (Get-Command go -ErrorAction SilentlyContinue)) { return $null }
  $out = & go version 2>$null
  if ($out -match 'go(\d+)\.(\d+)') {
    return @{ Major = [int]$Matches[1]; Minor = [int]$Matches[2] }
  }
  return $null
}

function Has-DotNetSdk8 {
  if (-not (Get-Command dotnet -ErrorAction SilentlyContinue)) { return $false }
  $sdks = & dotnet --list-sdks 2>$null
  foreach ($line in $sdks) {
    if ($line -match '^(\d+)\.' -and [int]$Matches[1] -ge 8) { return $true }
  }
  return $false
}

function Has-Wix {
  if (-not (Get-Command wix -ErrorAction SilentlyContinue)) { return $false }
  & wix --version 2>$null | Out-Null
  return $LASTEXITCODE -eq 0
}

function Has-MinGW {
  return (Get-Command gcc -ErrorAction SilentlyContinue) -and `
         (Get-Command g++ -ErrorAction SilentlyContinue)
}

function Test-GoFloor {
  # True when a Go >= 1.26 toolchain (go.mod floor) is on PATH.
  $v = Get-GoVersion
  return ($v -and ($v.Major -gt 1 -or ($v.Major -eq 1 -and $v.Minor -ge 26)))
}

function Test-Elevated {
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  return ([Security.Principal.WindowsPrincipal]$id).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)
}

$hasWinget = [bool](Get-Command winget -ErrorAction SilentlyContinue)
$hasChoco = [bool](Get-Command choco -ErrorAction SilentlyContinue)
if (-not $hasWinget -and -not $hasChoco) {
  Write-Error "Neither winget nor choco found. Install 'App Installer' from the Microsoft Store or Chocolatey (https://chocolatey.org/install), then re-run."
  exit 1
}

# Package-manager installs (Go, .NET SDK, MinGW) write to machine locations
# and fail without elevation: fail fast with the fix instead of dying
# mid-install. The WiX CLI installs per-user via `dotnet tool` and is exempt.
$needsMachineInstall = -not (Test-GoFloor) -or -not (Has-DotNetSdk8) -or `
  ((-not $SkipMinGW) -and -not (Has-MinGW))
if ($needsMachineInstall -and -not (Test-Elevated)) {
  Write-Error "Not running elevated: package installs need admin rights. Close Git Bash, re-open it via right-click -> 'Run as administrator', then re-run."
  exit 1
}

# Go >= 1.26 (go.mod floor). Prefer an existing toolchain; otherwise install
# the latest via winget/choco and verify it meets the floor.
$go = Get-GoVersion
if (Test-GoFloor) {
  Write-Host ("Go present: " + (& go version))
} else {
  if ($hasWinget) {
    Write-Host "Installing Go via winget..."
    & winget install -e --id GoLang.Go --accept-source-agreements --accept-package-agreements
    if ($LASTEXITCODE -ne 0) { Write-Error "winget Go install failed (exit $LASTEXITCODE)."; exit 1 }
  } else {
    Write-Host "Installing Go via choco..."
    & choco install golang -y --no-progress
    if ($LASTEXITCODE -ne 0) { Write-Error "choco golang install failed (exit $LASTEXITCODE)."; exit 1 }
  }
  Refresh-Path
  $go = Get-GoVersion
  if (-not (Test-GoFloor)) {
    Write-Error "Go >= 1.26 still not on PATH after install; restart the shell and re-run."
    exit 1
  }
  Write-Host ("Go installed: " + (& go version))
}

# .NET SDK 8 (LTS runtime the WiX v5 CLI runs on). A newer SDK is fine too.
if (Has-DotNetSdk8) {
  Write-Host "dotnet SDK present:"
  & dotnet --list-sdks 2>$null | Write-Host
} else {
  if ($hasWinget) {
    Write-Host "Installing .NET SDK 8 via winget..."
    & winget install -e --id Microsoft.DotNet.SDK.8 --accept-source-agreements --accept-package-agreements
    if ($LASTEXITCODE -ne 0) { Write-Error "winget .NET SDK install failed (exit $LASTEXITCODE)."; exit 1 }
  } else {
    Write-Host "Installing .NET SDK via choco..."
    & choco install dotnet-sdk -y --no-progress
    if ($LASTEXITCODE -ne 0) { Write-Error "choco dotnet-sdk install failed (exit $LASTEXITCODE)."; exit 1 }
  }
  Refresh-Path
  if (-not (Has-DotNetSdk8)) {
    Write-Error ".NET SDK 8 still not on PATH after install; restart the shell and re-run."
    exit 1
  }
  Write-Host ".NET SDK installed."
}

# WiX CLI (same exact version as build-msi.ps1 and CI).
if (Has-Wix) {
  Write-Host ("WiX present: " + (& wix --version))
} else {
  Write-Host "Installing WiX v5 CLI via dotnet tool..."
  & dotnet tool install --global wix --version "5.0.2"
  if ($LASTEXITCODE -ne 0) { Write-Error "dotnet wix install failed (exit $LASTEXITCODE)."; exit 1 }
  Refresh-Path
  if (-not (Has-Wix)) {
    Write-Error "wix still not on PATH after install; restart the shell and re-run."
    exit 1
  }
  Write-Host ("WiX installed: " + (& wix --version))
}

# MinGW (CI parity: choco mingw). Needed only for cgo builds (web child).
if ($SkipMinGW) {
  Write-Host "Skipping MinGW (-SkipMinGW)."
} elseif (Has-MinGW) {
  Write-Host ("MinGW present: " + (& gcc --version | Select-Object -First 1))
} elseif (Get-Command choco -ErrorAction SilentlyContinue) {
  Write-Host "Installing MinGW via choco (same as CI)..."
  & choco install mingw -y --no-progress
  if ($LASTEXITCODE -ne 0) { Write-Error "choco mingw install failed (exit $LASTEXITCODE)."; exit 1 }
  Refresh-Path
  if (-not (Has-MinGW)) {
    Write-Error "gcc/g++ still not on PATH after install; restart the shell and re-run."
    exit 1
  }
  Write-Host ("MinGW installed: " + (& gcc --version | Select-Object -First 1))
} else {
  Write-Error "gcc/g++ not found and choco is unavailable. Install Chocolatey (https://chocolatey.org/install), then re-run -- or re-run with -SkipMinGW if you only build the MSI from CI archives (no cgo needed)."
  exit 1
}

Write-Host ""
Write-Host "Prerequisites OK. Restart Git Bash so PATH changes apply to new shells, then verify:"
Write-Host "  go version; dotnet --list-sdks; wix --version"
if (-not $SkipMinGW) { Write-Host "  gcc --version" }
