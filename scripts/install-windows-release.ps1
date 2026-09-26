# scripts/install-windows-release.ps1
#
# Install the latest Windows release of Kube Workspaces.
#
# Default is a per-user install into %LocalAppData%\Programs\Kube Workspaces:
# writable without UAC, so the built-in updater works unelevated. Pass
# -PerMachine for C:\Program Files\Kube Workspaces instead (needs elevation,
# and every later update needs it too). Program Files stays reserved for the
# future signed per-machine installer.
#
# Both .exes (+ future codec DLLs) stay together in the install dir: the shell
# finds its web child by sibling path and codec DLLs load from the app dir
# only. Moving the install dir later is safe (tokens: Credential Manager,
# profiles: %AppData%\kube-workspaces).
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts/install-windows-release.ps1
#   powershell -ExecutionPolicy Bypass -File scripts/install-windows-release.ps1 -PerMachine
[CmdletBinding()]
param(
  [switch]$PerMachine,
  [ValidateSet("auto", "amd64", "arm64")][string]$Arch = "auto"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ($Arch -eq "auto") {
  $Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
}

# Get latest tag
$latestRelease = Invoke-RestMethod -Uri "https://api.github.com/repos/kube-workspaces/desktop-client/releases/latest"
$tag = $latestRelease.tag_name
if (-not $tag) {
    Write-Error "Could not find latest release tag"
    exit 1
}
Write-Host "Installing latest release: $tag ($Arch)"

$url = "https://github.com/kube-workspaces/desktop-client/releases/download/$tag/kube-workspaces-$tag-windows-$Arch.zip"
$zipPath = Join-Path $env:TEMP "kube-workspaces-$tag-windows-$Arch.zip"
$extractPath = Join-Path $env:TEMP "kube-workspaces-$tag-windows-$Arch"

Write-Host "Downloading $url to $zipPath"
Invoke-WebRequest -Uri $url -OutFile $zipPath

Write-Host "Extracting to $extractPath"
if (Test-Path $extractPath) {
    Remove-Item -Path $extractPath -Recurse -Force
}
Expand-Archive -Path $zipPath -DestinationPath $extractPath -Force

if ($PerMachine) {
    $installDir = "C:\Program Files\Kube Workspaces"
} else {
    $installDir = Join-Path $env:LocalAppData "Programs\Kube Workspaces"
}
if (!(Test-Path $installDir)) {
    Write-Host "Creating $installDir"
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
}

# The extracted folder contains the binaries; keep them together in one dir.
$extractedFolder = Get-ChildItem -Path $extractPath -Directory | Select-Object -First 1
Write-Host "Moving files from $($extractedFolder.FullName) to $installDir"
Move-Item -Path (Join-Path $extractedFolder.FullName "*") -Destination $installDir -Force

Write-Host "Unblocking downloaded binaries"
Get-ChildItem (Join-Path $installDir "*.exe") | Unblock-File

# Start Menu shortcut (per-user install -> per-user menu; per-machine -> all users).
$shell = New-Object -ComObject WScript.Shell
if ($PerMachine) {
    $menuDir = Join-Path $env:ProgramData "Microsoft\Windows\Start Menu\Programs\Kube Workspaces"
} else {
    $menuDir = Join-Path $env:AppData "Microsoft\Windows\Start Menu\Programs\Kube Workspaces"
}
if (!(Test-Path $menuDir)) {
    New-Item -ItemType Directory -Path $menuDir -Force | Out-Null
}
$link = $shell.CreateShortcut((Join-Path $menuDir "Kube Workspaces.lnk"))
$link.TargetPath = Join-Path $installDir "kube-workspaces.exe"
$link.WorkingDirectory = $installDir
$link.Save()

Write-Host "Successfully installed to $installDir"
Write-Host "Note: release builds are not code-signed; SmartScreen warns (More info -> Run anyway) until signing is funded."
