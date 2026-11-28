# uni-vpn.msix for the Microsoft Store: the app window, the core with libopenconnect inside
# (build-core-inprocess.sh, built without a console window) and its DLLs, the core's support
# files. Unsigned: the Store signs it on submission (test-msix.ps1 signs a copy to install it).
# Name, Publisher and PublisherName are the package identity Partner Center shows under
# "Product identity"; the defaults are for CI only.
param(
  [Parameter(Mandatory = $true)][string]$Desktop,   # the app window's folder (Uni VPN.exe)
  [Parameter(Mandatory = $true)][string]$Core,      # build-core-inprocess.sh's folder
  [Parameter(Mandatory = $true)][string]$Support,   # stage-app.sh --support's folder
  [Parameter(Mandatory = $true)][string]$Version,   # 0.1.N
  [string]$Out = "out\uni-vpn.msix",
  [string]$Name = "UniVPN.CI",
  [string]$Publisher = "CN=uni-vpn CI",
  [string]$PublisherName = "uni-vpn"
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$stage = Join-Path $env:TEMP "uni-vpn-msix"
Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
Copy-Item -Recurse $Support $stage
Remove-Item -Recurse -Force "$stage\desktop" -ErrorAction SilentlyContinue
Copy-Item -Recurse $Desktop "$stage\desktop"
Copy-Item "$Core\*.dll" "$stage\bin\"
Copy-Item "$Core\uni-vpn-core-gui.exe" "$stage\bin\uni-vpn-core.exe"
Copy-Item -Recurse -Force "$Core\licenses" "$stage"
Copy-Item -Recurse "$root\msix\assets" "$stage\assets"
# The Store wants a.b.c.0, each part at most 65535.
$v = "$Version.0"
$manifest = Get-Content -Raw -Encoding UTF8 "$root\msix\AppxManifest.xml"
$manifest = $manifest.Replace("@NAME@", $Name).Replace("@PUBLISHER@", [Security.SecurityElement]::Escape($Publisher)).
  Replace("@PUBLISHER_NAME@", [Security.SecurityElement]::Escape($PublisherName)).Replace("@VERSION@", $v)
[IO.File]::WriteAllText("$stage\AppxManifest.xml", $manifest, (New-Object Text.UTF8Encoding $false))

$makeappx = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\makeappx.exe" | Sort-Object FullName | Select-Object -Last 1
if (-not $makeappx) { throw "makeappx.exe not found (Windows SDK)" }
New-Item -ItemType Directory -Force (Split-Path -Parent $Out) | Out-Null
& $makeappx.FullName pack /o /d $stage /p $Out
if ($LASTEXITCODE -ne 0) { throw "makeappx failed" }
Write-Host "built $Out, version $v"
