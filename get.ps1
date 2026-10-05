# One-line install for Windows (PowerShell):
#   irm https://raw.githubusercontent.com/DavidVinu/uni-vpn/main/get.ps1 | iex
# Downloads uni-vpn (no git needed) and runs install.ps1, which copies it to
# %ProgramFiles%\uni-vpn. With UNI_VPN_UPDATE=1 it updates an existing installation.
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
$ref = if ($env:UNI_VPN_REF) { $env:UNI_VPN_REF } else { "main" }
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("uni-vpn-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "-> downloading uni-vpn ($ref)"
    $zip = Join-Path $tmp "uni-vpn.zip"
    Invoke-WebRequest -Uri "https://codeload.github.com/DavidVinu/uni-vpn/zip/$ref" -OutFile $zip -UseBasicParsing
    Expand-Archive -Path $zip -DestinationPath $tmp -Force
    $src = Get-ChildItem -Path $tmp -Directory | Select-Object -First 1
    if (-not (Test-Path (Join-Path $src.FullName "uni_vpn\__init__.py"))) { throw "Download is incomplete" }
    Get-ChildItem -Path $src.FullName -Recurse -File | Unblock-File
    $arguments = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $src.FullName "install.ps1"))
    if ($env:UNI_VPN_UPDATE -eq "1") { $arguments += "-Update" }
    & powershell.exe @arguments
} finally {
    # No "exit" here: with "irm | iex" it would close the user's PowerShell window.
    Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
