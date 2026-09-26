# Windows MSI packaging

The **Build** workflow builds unsigned amd64 and arm64 installers from the
assembled Windows archives on main pushes, pull requests and manual runs.
Download `msi-windows-amd64` or `msi-windows-arm64` from the run's artifacts.
`SHA256SUMS` covers the six platform archives and both installers. A `v*` tag
publishes the same files to the GitHub Release; no release rebuild occurs.

## Build locally

Use Windows, .NET SDK 8 and WiX **5.0.2**:

```powershell
dotnet tool install --global wix --version 5.0.2
Expand-Archive kube-workspaces-v0.1.8-windows-amd64.zip -DestinationPath stage
./packaging/windows/build-msi.ps1 -Version v0.1.8 -Arch amd64 `
  -StagingDir stage/kube-workspaces-windows-amd64 `
  -OutFile out/kube-workspaces-v0.1.8-windows-amd64.msi
```

The amd64 archive must contain both executables; arm64 is shell-only. Staged
DLLs are included beside the executables. Exact `vX.Y.Z` versions map to MSI
versions (maximum `255.255.65535`); development versions use `0.0.0` and are
for validation, not upgrades of released installations. Each build gets a
new ProductCode; the UpgradeCode must remain stable. Release versions must
increase for major upgrades; uninstall before switching architecture or scope.

## Install

Double-click the MSI or run `msiexec /i <file.msi>`. By default it installs for
the current user under `%LocalAppData%\Programs\Kube Workspaces`, including a
Start Menu shortcut and an Add/Remove Programs entry. Profiles in `%AppData%`
and Credential Manager tokens are preserved on uninstall.

An elevated per-machine install is explicit:

```powershell
msiexec /i <file.msi> ALLUSERS=1 INSTALLDIR="C:\Program Files\Kube Workspaces"
```

Installers are unsigned. WebView2 is an external runtime prerequisite for the
amd64 web child; the MSI does not download runtimes or codec DLLs.

The built-in updater replaces binary files using zip releases, not MSI
transactions: Add/Remove Programs continues to show the last MSI-installed
version, and MSI repair can restore that package's binaries. Use newer MSIs
when Windows Installer version tracking is required.

## Verification

`build-msi.ps1` runs WiX validation and `verify-msi.ps1` against the actual MSI:
architecture, ProductVersion, stable UpgradeCode, per-user default, required
executables and payload inventory/sizes. It never patches the resulting MSI.

On disposable amd64 CI runners, `test-msi.ps1` additionally builds two test
versions and exercises default install, installed hashes, shortcut, shell
launch, major upgrade, downgrade rejection, uninstall/data preservation and
the explicit machine-scope override. Logs are uploaded as `msi-test-logs`.
ARM64 packages are built and table-checked on x64; native ARM64 installation,
interactive webview and unelevated auto-update acceptance remain separate.
