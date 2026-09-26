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
        $values = for ($i = 1; $i -le $Columns; $i++) { Get-Com $record "StringData" @($i) }
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
  if ($properties.ContainsKey("ALLUSERS")) { throw "Per-user MSI must omit ALLUSERS." }
  if ($properties.ProductVersion -ne $Version) { throw "Incorrect ProductVersion: $($properties.ProductVersion)" }
  if ($properties.UpgradeCode -ne "{7A877129-11B3-4532-A7F8-1356F06496A5}") { throw "UpgradeCode changed." }
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
  Write-Host "Verified MSI: $template, version $Version, per-user default, $($names.Count) payload files."
} finally {
  foreach ($object in @($summary, $db, $installer)) {
    if ($null -ne $object) { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($object) }
  }
}
