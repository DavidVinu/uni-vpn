# One-line install for Windows (PowerShell):
#   irm https://raw.githubusercontent.com/DavidVinu/uni-vpn/main/get.ps1 | iex
# Downloads uni-vpn (no git needed) to %LOCALAPPDATA%\uni-vpn\app and runs install.ps1.
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
$ref = if ($env:UNI_VPN_REF) { $env:UNI_VPN_REF } else { "main" }
$app = Join-Path $env:LOCALAPPDATA "uni-vpn\app"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("uni-vpn-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "-> downloading uni-vpn ($ref)"
    $zip = Join-Path $tmp "uni-vpn.zip"
    Invoke-WebRequest -Uri "https://codeload.github.com/DavidVinu/uni-vpn/zip/$ref" -OutFile $zip -UseBasicParsing
    Expand-Archive -Path $zip -DestinationPath $tmp -Force
    $src = Get-ChildItem -Path $tmp -Directory | Select-Object -First 1
    if (-not (Test-Path (Join-Path $src.FullName "uni_vpn\__init__.py"))) { throw "Download is incomplete" }
    New-Item -ItemType Directory -Path $app -Force | Out-Null
    Copy-Item -Path (Join-Path $src.FullName "*") -Destination $app -Recurse -Force
    Get-ChildItem -Path $app -Recurse -File | Unblock-File
    Write-Host "-> installed to $app"
} finally {
    Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
& powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $app "install.ps1")
