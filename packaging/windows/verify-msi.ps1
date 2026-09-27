#Requires -Version 5.1
# Read the actual MSI tables (not just the WiX source). COM methods/properties
# use IDispatch explicitly so Windows PowerShell and PowerShell 7 behave alike.
[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$Path,
  [Parameter(Mandatory = $true)][ValidateSet("amd64", "arm64")][string]$Arch,
  [Parameter(Mandatory = $true)][string]$Version,
  [Parameter(Mandatory = $true)][string]$StagingDir
)
Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
function Invoke-Com($Object, [string]$Name, [object[]]$Arguments) {
  $Object.GetType().InvokeMember($Name, "InvokeMethod", $null, $Object, $Arguments)
}
function Get-Com($Object, [string]$Name, [object[]]$Arguments) {
  $Object.GetType().InvokeMember($Name, "GetProperty", $null, $Object, $Arguments)
}
function Read-Rows([string]$Query, [int]$Columns) {
  $view = Invoke-Com $db "OpenView" @($Query)
  try {
    Invoke-Com $view "Execute" @() | Out-Null
    while ($record = Invoke-Com $view "Fetch" @()) {
      try {
        $values = @(for ($i = 1; $i -le $Columns; $i++) { Get-Com $record "StringData" @($i) })
        ,$values
      } finally { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($record) }
    }
  } finally {
    Invoke-Com $view "Close" @() | Out-Null
    [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($view)
  }
}
$installer = New-Object -ComObject WindowsInstaller.Installer
$db = $null
$summary = $null
try {
  $db = Invoke-Com $installer "OpenDatabase" @((Resolve-Path $Path).Path, 0)
  $properties = @{}
  Read-Rows 'SELECT `Property`, `Value` FROM `Property`' 2 | ForEach-Object { $properties[$_[0]] = $_[1] }
  if ($properties.ALLUSERS -ne '2' -or $properties.MSIINSTALLPERUSER -ne '1') {
    throw "Dual-scope MSI must default to per-user (ALLUSERS=2, MSIINSTALLPERUSER=1)."
  }
  if ($properties.ProductVersion -ne $Version) { throw "Incorrect ProductVersion: $($properties.ProductVersion)" }
  if ($properties.UpgradeCode -ne "{7A877129-11B3-4532-A7F8-1356F06496A5}") { throw "UpgradeCode changed." }
  if ($properties.ContainsKey('ARPINSTALLLOCATION')) { throw "ARPINSTALLLOCATION must not be a Property-table row: such values reach the uninstall key literally." }
  $actions = @(Read-Rows 'SELECT `Action`, `Type`, `Source`, `Target` FROM `CustomAction`' 4)
  $setLoc = @($actions | Where-Object { $_[0] -eq 'SetARPINSTALLLOCATION' })
  if ($setLoc.Count -ne 1 -or $setLoc[0][1] -ne '51' -or $setLoc[0][2] -ne 'ARPINSTALLLOCATION' -or $setLoc[0][3] -ne '[INSTALLDIR]') {
    throw "ARPINSTALLLOCATION must be assigned from [INSTALLDIR] by a type-51 action, or the uninstall key records the literal text and blinds updater detection."
  }
  $seq = @{}
  Read-Rows 'SELECT `Action`, `Sequence` FROM `InstallExecuteSequence`' 2 | ForEach-Object { $seq[$_[0]] = [int]$_[1] }
  if (-not $seq.ContainsKey('SetARPINSTALLLOCATION') -or -not $seq.ContainsKey('CostFinalize') -or $seq['SetARPINSTALLLOCATION'] -le $seq['CostFinalize']) {
    throw "SetARPINSTALLLOCATION must run after CostFinalize so the registry holds the resolved directory."
  }
  $summary = Get-Com $db "SummaryInformation" @(0)
  $template = Get-Com $summary "Property" @(7)
  $expected = @{ amd64 = "x64"; arm64 = "Arm64" }[$Arch]
  if ($template -ne "$expected;1033") { throw "Incorrect MSI architecture: $template" }
  $files = @(Read-Rows 'SELECT `File`, `FileName`, `FileSize` FROM `File`' 3)
  $names = @()
  foreach ($file in $files) {
    $name = ($file[1] -split '\|')[-1]
    $names += $name
    $source = Get-Item -LiteralPath (Join-Path $StagingDir $name)
    if ($source.Length -ne [long]$file[2]) { throw "Payload size mismatch: $name" }
  }
  if ($names -notcontains "kube-workspaces.exe") { throw "Shell missing from MSI." }
  if ($Arch -eq "amd64" -and $names -notcontains "kube-workspaces-web.exe") { throw "Web child missing from MSI." }
  $staged = @(Get-ChildItem $StagingDir -File | Where-Object { $_.Extension -in @('.exe', '.dll') } | ForEach-Object { $_.Name })
  if (Compare-Object $staged $names) { throw "MSI payload differs from archive." }
  $dialogs = @(Read-Rows 'SELECT `Dialog` FROM `Dialog`' 1 | ForEach-Object { $_[0] })
  foreach ($required in @('WelcomeDlg', 'ScopeDlg', 'VerifyReadyDlg', 'ProgressDlg', 'ExitDialog')) {
    if ($dialogs -notcontains $required) { throw "Missing installer dialog: $required" }
  }
  $events = @(Read-Rows 'SELECT `Event`, `Argument`, `Condition`, `Ordering` FROM `ControlEvent` WHERE `Dialog_` = ''ExitDialog'' AND `Control_` = ''Finish''' 4)
  $launch = @($events | Where-Object { $_[0] -eq 'DoAction' -and $_[1] -eq 'LaunchKubeWorkspaces' })
  if ($launch.Count -ne 1 -or $launch[0][2] -notmatch 'WIXUI_EXITDIALOGOPTIONALCHECKBOX = 1 AND NOT Installed') {
    throw 'Finish must launch only when the checkbox is selected on a new install.'
  }
  $sequence = @(Read-Rows 'SELECT `Action` FROM `InstallExecuteSequence`' 1 | ForEach-Object { $_[0] })
  if ($sequence -contains 'LaunchKubeWorkspaces') { throw 'Launch must remain UI-only.' }
  Write-Host "Verified MSI: $template, version $Version, per-user default, $($names.Count) payload files."
} finally {
  foreach ($object in @($summary, $db, $installer)) {
    if ($null -ne $object) { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($object) }
  }
}
