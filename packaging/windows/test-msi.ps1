#Requires -Version 5.1
# Destructive lifecycle test for a disposable GitHub-hosted Windows runner.
[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$StagingDir)
Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
if ($env:GITHUB_ACTIONS -ne "true") { throw "Run lifecycle tests only on a disposable GitHub Actions runner." }
$work = Join-Path $env:RUNNER_TEMP "msi-lifecycle"
New-Item -ItemType Directory -Path $work -Force | Out-Null
$install = Join-Path $env:LOCALAPPDATA "Programs\Kube Workspaces"
$shortcut = Join-Path ([Environment]::GetFolderPath('Programs')) "Kube Workspaces\Kube Workspaces.lnk"
$uninstallKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall"
$machineKey = "HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall"
function Get-Registration([string]$Root) {
  @(Get-ItemProperty "$Root\*" -ErrorAction SilentlyContinue | Where-Object { $_.PSObject.Properties['DisplayName'] -and $_.DisplayName -eq "Kube Workspaces" })
}
function Invoke-Msi([string]$Arguments, [string]$Log, [int]$Expected = 0) {
  $p = Start-Process msiexec.exe -ArgumentList "$Arguments /qn /norestart /l*v `"$work\$Log.log`"" -Wait -PassThru
  if ($p.ExitCode -ne $Expected) { throw "msiexec returned $($p.ExitCode), expected $Expected; see $Log.log" }
}
function Assert-Payload([string]$Directory) {
  foreach ($source in Get-ChildItem $StagingDir -File) {
    if ($source.Extension -notin @('.exe', '.dll')) { continue }
    $dest = Join-Path $Directory $source.Name
    if ((Get-FileHash $source.FullName).Hash -ne (Get-FileHash $dest).Hash) { throw "Installed payload mismatch: $dest" }
  }
}
if ((Test-Path $install) -or (Get-Registration $uninstallKey) -or (Get-Registration $machineKey)) {
  throw "Runner already contains Kube Workspaces; refusing to overwrite it."
}
$first = Join-Path $work "first.msi"
$second = Join-Path $work "second.msi"
& "$PSScriptRoot/build-msi.ps1" -Version v0.0.1 -Arch amd64 -StagingDir $StagingDir -OutFile $first
& "$PSScriptRoot/build-msi.ps1" -Version v0.0.2 -Arch amd64 -StagingDir $StagingDir -OutFile $second
$profileDir = Join-Path $env:APPDATA "kube-workspaces"
New-Item -ItemType Directory -Path $profileDir -Force | Out-Null
$sentinel = Join-Path $profileDir "msi-test-preserve.txt"
Set-Content $sentinel "preserve me"
try {
  Invoke-Msi "/i `"$first`"" "install"
  Assert-Payload $install
  if (-not (Test-Path $shortcut)) { throw "Start Menu shortcut missing." }
  $link = (New-Object -ComObject WScript.Shell).CreateShortcut($shortcut)
  if ($link.TargetPath -ne (Join-Path $install 'kube-workspaces.exe')) { throw "Incorrect shortcut target." }
  $registered = @(Get-Registration $uninstallKey)
  if ($registered.Count -ne 1 -or $registered[0].DisplayVersion -ne '0.0.1' -or (Get-Registration $machineKey)) {
    throw "Default install is not registered exclusively per-user."
  }
  $run = Start-Process (Join-Path $install 'kube-workspaces.exe') -ArgumentList 'version' -Wait -PassThru
  if ($run.ExitCode -ne 0) { throw "Installed shell failed to launch." }
  Invoke-Msi "/i `"$second`"" "upgrade"
  Assert-Payload $install
  $registered = @(Get-Registration $uninstallKey)
  if ($registered.Count -ne 1 -or $registered[0].DisplayVersion -ne '0.0.2') { throw "Major upgrade left wrong registration." }
  Invoke-Msi "/i `"$first`"" "downgrade" 1603
  Invoke-Msi "/x `"$second`"" "uninstall"
  if ((Test-Path (Join-Path $install 'kube-workspaces.exe')) -or (Test-Path $shortcut) -or (Get-Registration $uninstallKey)) {
    throw "Uninstall left installed files, shortcut or registration."
  }
  if ((Get-Content $sentinel -Raw).Trim() -ne 'preserve me') { throw "Uninstall changed profile data." }
  # Hosted runners are administrators: exercise the explicit machine override.
  $machineInstall = Join-Path $env:ProgramFiles 'Kube Workspaces'
  Invoke-Msi "/i `"$second`" ALLUSERS=1 INSTALLDIR=`"$machineInstall`"" "machine-install"
  Assert-Payload $machineInstall
  if (@(Get-Registration $machineKey).Count -ne 1) { throw "Machine registration missing." }
  Invoke-Msi "/x `"$second`" ALLUSERS=1" "machine-uninstall"
  if ((Get-Registration $machineKey) -or (Test-Path (Join-Path $machineInstall 'kube-workspaces.exe'))) { throw "Machine uninstall incomplete." }
  Write-Host "MSI lifecycle passed: per-user install, payload, shortcut, launch, upgrade, downgrade rejection, uninstall, data preservation, per-machine override."
} finally {
  Remove-Item $sentinel -ErrorAction SilentlyContinue
}
