# Uninstalls what test-install.ps1 installed, like a user would (ci.yml).
$ErrorActionPreference = "Stop"  # as in a GitHub Actions powershell step
# An update unpacked next to the app that waited for the VPN to go idle.
$staged = "$env:ProgramFiles\.uni-vpn-core-update-ci"
New-Item -ItemType Directory $staged | Out-Null
New-Item -ItemType File "$staged\uni-vpn-core.exe" | Out-Null
$process = Start-Process -FilePath "$env:ProgramFiles\uni-vpn\unins000.exe" -ArgumentList "/VERYSILENT", "/SUPPRESSMSGBOXES" -Wait -PassThru
Start-Sleep 5
if (Test-Path "$env:ProgramFiles\uni-vpn\bin\uni-vpn-core.exe") { throw "files are left in Program Files" }
if (Test-Path $staged) { throw "a waiting update is left in Program Files" }
if (Get-ScheduledTask -TaskName uni-vpn -ErrorAction SilentlyContinue) { throw "the task is still registered" }
if (Test-Path "$env:ProgramFiles\uni-vpn") { Write-Host "note: the folder is still there:"; Get-ChildItem -Recurse "$env:ProgramFiles\uni-vpn" | ForEach-Object FullName }
