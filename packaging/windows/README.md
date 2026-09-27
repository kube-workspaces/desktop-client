# Windows MSI packaging

The **Build** workflow builds unsigned amd64 and arm64 installers from the
assembled Windows archives on main pushes, pull requests and manual runs.
Download `msi-windows-amd64` or `msi-windows-arm64` from the run's artifacts.
`SHA256SUMS` covers the six platform archives and both installers. A `v*` tag
publishes the same files to the GitHub Release; no release rebuild occurs.

## Build locally

Use Windows, .NET SDK 8 and WiX **5.0.2**:

The build script also installs/caches `WixToolset.UI.wixext/5.0.2` for the
standard setup dialogs.

The wizard uses Kube Workspaces artwork instead of WiX's stock disc graphic:
`dialog.bmp` (493x312) for welcome/completion pages and `banner.bmp` (493x58)
for the top banner, including the scope-selection page. These 24-bit bitmaps
are generated from `assets/icon.png` and kept with the installer sources;
normal builds do not need graphics tools. To regenerate on Windows:

```powershell
./packaging/windows/generate-ui-artwork.ps1
```

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

Double-click the MSI or run `msiexec /i <file.msi>`. The setup wizard asks:

* **Just me** (default): `%LocalAppData%\Programs\Kube Workspaces`, no elevation.
* **All users on this computer**: `C:\Program Files\Kube Workspaces`, with
  administrator permission requested by Windows Installer as needed.

The Start Menu shortcut follows the selected scope. After successful setup,
the standard completion page offers **Open Kube Workspaces now** (checked by
default). Clear it to finish without launching. The launch action runs only
from the interactive Finish button, not during silent install, repair or removal.
Profiles in `%AppData%` and Credential Manager tokens survive uninstall.

For silent per-machine installation, run this from an elevated terminal:

```powershell
msiexec /i <file.msi> /qn ALLUSERS=1
```

Installers are unsigned. WebView2 is an external runtime prerequisite for the
amd64 web child; the MSI does not download runtimes or codec DLLs.

The built-in updater replaces binary files using zip releases, not MSI
transactions: Add/Remove Programs continues to show the last MSI-installed
version, and MSI repair can restore that package's binaries. Use newer MSIs
when Windows Installer version tracking is required.

## Verification

`build-msi.ps1` runs WiX validation and `verify-msi.ps1` against the actual MSI:
architecture, ProductVersion, stable UpgradeCode, dual-scope per-user default,
required executables, payload inventory/sizes, setup dialogs and the guarded
UI-only launch event. It never patches the resulting MSI.

On disposable amd64 CI runners, `test-msi.ps1` additionally builds two test
versions and exercises default install, installed hashes, shortcut, shell
launch, major upgrade, downgrade rejection, uninstall/data preservation and
the explicit machine-scope override. Logs are uploaded as `msi-test-logs`.
ARM64 packages are built and table-checked on x64; native ARM64 installation,
interactive webview and unelevated auto-update acceptance remain separate.
