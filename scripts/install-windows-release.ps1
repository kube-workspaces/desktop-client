# scripts/install-windows-release.ps1

# Get latest tag
$latestRelease = Invoke-RestMethod -Uri "https://api.github.com/repos/kube-workspaces/desktop-client/releases/latest"
$tag = $latestRelease.tag_name
if (-not $tag) {
    Write-Error "Could not find latest release tag"
    exit 1
}
Write-Host "Installing latest release: $tag"

$url = "https://github.com/kube-workspaces/desktop-client/releases/download/$tag/kube-workspaces-$tag-windows-amd64.zip"
$zipPath = Join-Path $env:TEMP "kube-workspaces-$tag-windows-amd64.zip"
$extractPath = Join-Path $env:TEMP "kube-workspaces-$tag"

Write-Host "Downloading $url to $zipPath"
Invoke-WebRequest -Uri $url -OutFile $zipPath

Write-Host "Extracting to $extractPath"
if (Test-Path $extractPath) {
    Remove-Item -Path $extractPath -Recurse -Force
}
Expand-Archive -Path $zipPath -DestinationPath $extractPath -Force

# Move to C:\Program Files\Kube Workspaces
$installDir = "C:\Program Files\Kube Workspaces"
if (!(Test-Path $installDir)) {
    Write-Host "Creating $installDir"
    New-Item -ItemType Directory -Path $installDir -Force
}

# The extracted folder contains the binary and other files
$extractedFolder = Get-ChildItem -Path $extractPath -Directory | Select-Object -First 1
Write-Host "Moving files from $($extractedFolder.FullName) to $installDir"
Move-Item -Path (Join-Path $extractedFolder.FullName "*") -Destination $installDir -Force

Write-Host "Successfully installed to $installDir"
