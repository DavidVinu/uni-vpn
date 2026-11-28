# Installs uni-vpn.msix the way the Store would (signed here with a throwaway certificate) and
# checks what only the package shows: Windows starts the window, the window starts the core,
# the core runs without administrator rights and without self-update, its rule for the websites
# reaches the real registry, and it gets as far as Heidelberg's login with a made-up account.
param(
  [Parameter(Mandatory = $true)][string]$Msix,
  [string]$Publisher = "CN=uni-vpn CI",
  [string]$Name = "UniVPN.CI"
)
$ErrorActionPreference = "Stop"
$Msix = (Resolve-Path $Msix).Path

$cert = New-SelfSignedCertificate -Type Custom -Subject $Publisher -KeyUsage DigitalSignature -FriendlyName "uni-vpn CI" `
  -CertStoreLocation Cert:\CurrentUser\My -TextExtension @("2.5.29.37={text}1.3.6.1.5.5.7.3.3", "2.5.29.19={text}")
$pfx = Join-Path $env:TEMP "uni-vpn-ci.pfx"
$password = ConvertTo-SecureString -String "ci" -Force -AsPlainText
Export-PfxCertificate -Cert $cert -FilePath $pfx -Password $password | Out-Null
Import-PfxCertificate -FilePath $pfx -CertStoreLocation Cert:\LocalMachine\TrustedPeople -Password $password | Out-Null
$signtool = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\signtool.exe" | Sort-Object FullName | Select-Object -Last 1
& $signtool.FullName sign /fd SHA256 /f $pfx /p ci $Msix
if ($LASTEXITCODE -ne 0) { throw "signtool failed" }

$settings = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings"
Remove-ItemProperty -Path $settings -Name AutoConfigURL -ErrorAction SilentlyContinue

Add-AppxPackage -Path $Msix
$package = Get-AppxPackage -Name $Name
if (-not $package) { throw "not installed" }
Write-Host "installed $($package.PackageFullName) in $($package.InstallLocation)"

function State() { Invoke-RestMethod "http://127.0.0.1:1081/status.json" }
function Logs() {
  Get-ChildItem "$env:LOCALAPPDATA\Packages\$($package.PackageFamilyName)" -Recurse -Filter daemon.log -ErrorAction SilentlyContinue |
    ForEach-Object { Write-Host "--- $($_.FullName)"; Get-Content $_.FullName -Tail 60 }
}

try {
  # As from the Start menu.
  Start-Process "shell:AppsFolder\$($package.PackageFamilyName)!App"
  $status = $null
  foreach ($i in 1..60) { try { $status = State; break } catch { Start-Sleep -Seconds 1 } }
  if (-not $status) { Logs; throw "the core did not start" }
  $core = Get-Process uni-vpn-core -ErrorAction SilentlyContinue | Select-Object -First 1
  Write-Host "core: $($core.Path)"
  if (-not $core -or -not $core.Path.StartsWith($package.InstallLocation)) { throw "the core is not the package's" }
  if ($null -ne $status.elevated) { throw "elevated is $($status.elevated), expected null" }
  if ($null -ne $status.auto_update) { throw "auto_update is $($status.auto_update), expected null" }

  # Read from outside the package, as a browser does.
  $rule = ""
  foreach ($i in 1..20) {
    $rule = (Get-ItemProperty -Path $settings -Name AutoConfigURL -ErrorAction SilentlyContinue).AutoConfigURL
    if ($rule) { break }
    Start-Sleep -Seconds 1
  }
  if ($rule -notlike "http://127.0.0.1:1081/*") { Logs; throw "rule for the websites: '$rule'" }
  Write-Host "rule for the websites: $rule"

  $headers = @{ "X-Uni-VPN" = "1" }
  Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:1081/api/setup" -Headers $headers -ContentType "application/json" `
    -Body '{"user": "ab123", "password": "wrong-password", "secret": "JBSWY3DPEHPK3PXP", "university": "heidelberg"}' | Out-Null
  Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:1081/api/connect" -Headers $headers -ContentType "application/json" -Body '{}' | Out-Null
  $state = ""
  foreach ($i in 1..90) {
    $state = (State).state
    if ($state -eq "auth_failed") { break }
    Start-Sleep -Seconds 1
  }
  if ($state -ne "auth_failed") { Logs; throw "state is '$state', expected auth_failed" }
  if (Get-NetAdapter -Name "uni-vpn" -ErrorAction SilentlyContinue) { throw "a uni-vpn network adapter exists" }
  Write-Host "package: the gateway refused the made-up account, no adapter"
} finally {
  Get-Process "Uni VPN", uni-vpn-core -ErrorAction SilentlyContinue | Stop-Process -Force
  Remove-AppxPackage -Package $package.PackageFullName -ErrorAction SilentlyContinue
}
