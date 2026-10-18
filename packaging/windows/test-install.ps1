# Installs out\uni-vpn-setup.exe like a user would and checks what it set up (ci.yml:
# windows-installer, and windows-arm-install on an ARM runner).
$ErrorActionPreference = "Stop"  # as in a GitHub Actions powershell step
$process = Start-Process -FilePath out\uni-vpn-setup.exe -ArgumentList "/VERYSILENT", "/SUPPRESSMSGBOXES", "/LOG=install-setup.log" -PassThru
if (-not $process.WaitForExit(300000)) {
  Write-Host "The installer did not finish within 5 minutes"
  Get-CimInstance Win32_Process | Where-Object { $_.Name -match "uni|python|powershell|cmd|robocopy|msiexec|openconnect|schtasks|setup|explorer|curl|csc" } |
    Format-Table ProcessId, ParentProcessId, CommandLine -AutoSize -Wrap | Out-String -Width 400 | Write-Host
  Get-Content "$env:LOCALAPPDATA\uni-vpn\logs\install.log" -ErrorAction SilentlyContinue
  Get-Content install-setup.log -Tail 30 -ErrorAction SilentlyContinue
  exit 1
}
Get-Content "$env:LOCALAPPDATA\uni-vpn\logs\install.log" -ErrorAction SilentlyContinue
if ($process.ExitCode -ne 0) { Get-Content install-setup.log; exit 1 }
$ok = $false
foreach ($i in 1..30) {
  try { Invoke-WebRequest -UseBasicParsing http://127.0.0.1:1081/status.json | Out-Null; $ok = $true; break } catch { Start-Sleep 1 }
}
if (-not $ok) {
  Get-ChildItem "$env:ProgramFiles", "${env:ProgramFiles(x86)}" | Format-Table -AutoSize | Out-String | Write-Host
  Get-ChildItem "$env:ProgramFiles", "${env:ProgramFiles(x86)}" -Recurse -Depth 2 -Filter "openconnect*.exe" -ErrorAction SilentlyContinue | ForEach-Object FullName
  throw "the service did not start"
}
if (-not (Test-Path "$env:ProgramFiles\uni-vpn\.commit")) { throw ".commit is missing" }
# The Go core of this commit answers, started by the task; no Python.
$status = Invoke-RestMethod http://127.0.0.1:1081/status.json
if ($status.commit -ne $env:GITHUB_SHA) { throw "the service is not the Go core of this commit ($($status.commit))" }
if (Test-Path "$env:ProgramFiles\uni-vpn\python") { throw "Python is in uni-vpn's folder" }
if (Test-Path "$env:ProgramFiles\uni-vpn\uni_vpn") { throw "the Python program is in uni-vpn's folder" }
$action = (Get-ScheduledTask -TaskName uni-vpn).Actions[0]
Write-Host "task: $($action.Execute) $($action.Arguments)"
if ($action.Execute -notlike "*\conhost.exe" -or $action.Arguments -notlike "--headless *\bin\uni-vpn-core.exe* daemon") {
  throw "the task does not run uni-vpn-core.exe in conhost.exe --headless"
}
if (-not (Test-Path "$env:ProgramFiles\uni-vpn\openconnect\wintun.dll")) { throw "Wintun is missing" }
# The core for this processor: ARM computers get the ARM one, the rest the x64 one.
$core = [IO.File]::ReadAllBytes("$env:ProgramFiles\uni-vpn\bin\uni-vpn-core.exe")
$machine = [BitConverter]::ToUInt16($core, [BitConverter]::ToInt32($core, 0x3C) + 4)
$expected = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { 0xAA64 } else { 0x8664 }
if ($machine -ne $expected) { throw ("uni-vpn-core.exe is for machine 0x{0:X4}, not 0x{1:X4}" -f $machine, $expected) }
# The tray app must keep running: one that ends at start would reach every user with the
# next update. Setup and the installer each start a copy (one stays, the other hands over
# and quits); the one that stays must still run 10 s later. Where the window cannot start
# (no WebView2 runtime) the page opens in the browser instead.
function Trays { @(Get-CimInstance Win32_Process -Filter "Name = 'Uni VPN.exe'") }
function ShowTrays {
  Trays | Format-Table ProcessId, ParentProcessId, SessionId, CreationDate, CommandLine -AutoSize -Wrap | Out-String -Width 400 | Write-Host
}
function Explain {
  ShowTrays
  Get-WinEvent -FilterHashtable @{LogName = "Application"; ProviderName = ".NET Runtime", "Application Error"} -MaxEvents 6 -ErrorAction SilentlyContinue |
    Format-List TimeCreated, ProviderName, Message | Out-String -Width 400 | Write-Host
  Get-Content "$env:LOCALAPPDATA\uni-vpn\logs\daemon.log" -Tail 40 -ErrorAction SilentlyContinue
}
$runtime = Get-ItemProperty "HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" -ErrorAction SilentlyContinue
Write-Host "WebView2 runtime: $(if ($runtime) { $runtime.pv } else { 'none' }); session: $((Get-Process -Id $PID).SessionId); interactive: $([Environment]::UserInteractive)"
Start-Sleep 5
ShowTrays
if ((Trays).Count -eq 0) { Explain; throw "no tray app runs after the install" }
Start-Sleep 10
if ((Trays).Count -eq 0) { Explain; throw "the tray app ends at start" }
# The installer's copy handed its window over: the app window opened (its WebView2 runs
# with the app's data folder), not the browser. A window that cannot start on a handover
# would open the browser on every Start menu click while the icon runs.
$web = @(Get-CimInstance Win32_Process -Filter "Name = 'msedgewebview2.exe'" | Where-Object { $_.CommandLine -like "*uni-vpn\WebView2*" })
$browser = @(Get-CimInstance Win32_Process -Filter "Name = 'msedge.exe' OR Name = 'chrome.exe' OR Name = 'firefox.exe'" | Where-Object { $_.CommandLine -like "*127.0.0.1:1081*" })
Write-Host "app window WebView2 processes: $($web.Count); browser with the page: $($browser.Count)"
Get-Content "$env:LOCALAPPDATA\uni-vpn\logs\app.log" -ErrorAction SilentlyContinue
$desktop = "$env:ProgramFiles\uni-vpn\desktop"
$env:PATH = "$desktop;$env:PATH"
try {
  Add-Type -Path "$desktop\Microsoft.Web.WebView2.Core.dll"
  Write-Host "WebView2 found: $([Microsoft.Web.WebView2.Core.CoreWebView2Environment]::GetAvailableBrowserVersionString())"
  # As the window does it (a failure here comes at once, as in the window's constructor).
  $start = [Microsoft.Web.WebView2.Core.CoreWebView2Environment]::CreateAsync($null, "$env:LOCALAPPDATA\uni-vpn\WebView2", $null)
  if ($start.Wait(30000)) { Write-Host "WebView2 starts: $($start.Result.BrowserVersionString)" } else { Write-Host "WebView2 did not start within 30 s" }
} catch {
  $e = $_.Exception
  while ($e.InnerException) { $e = $e.InnerException }
  Write-Host "WebView2 cannot start: $($e.GetType().FullName): $($e.Message)"
}
if ($web.Count -eq 0) { Explain; throw "the app window did not open on the handover" }
# A second start on purpose: the install's two starts race, and the copy that stays may
# have opened the window itself, without a handover.
function AppWebViews { @(Get-CimInstance Win32_Process -Filter "Name = 'msedgewebview2.exe'" | Where-Object { $_.CommandLine -like "*uni-vpn\WebView2*" }) }
Get-Process -Name "Uni VPN" -ErrorAction SilentlyContinue | Stop-Process -Force
foreach ($i in 1..30) { if ((AppWebViews).Count -eq 0) { break }; Start-Sleep 1 }
$old = @(AppWebViews | ForEach-Object ProcessId)
function NewWebViews { @(AppWebViews | Where-Object { $old -notcontains $_.ProcessId }) }
$exe = "$env:ProgramFiles\uni-vpn\desktop\Uni VPN.exe"
$icon = Start-Process $exe -ArgumentList "--port 1081 --hidden" -PassThru
Start-Sleep 5
if ($icon.HasExited -or (NewWebViews).Count -ne 0) { Explain; throw "the icon alone did not run as it should" }
$second = Start-Process $exe -ArgumentList "--port 1081" -PassThru
if (-not $second.WaitForExit(10000)) { Explain; throw "the second start did not hand over" }
foreach ($i in 1..15) { if ((NewWebViews).Count -gt 0) { break }; Start-Sleep 1 }
Get-Content "$env:LOCALAPPDATA\uni-vpn\logs\app.log" -ErrorAction SilentlyContinue
if ($icon.HasExited -or (NewWebViews).Count -eq 0) { Explain; throw "the app window did not open on a second start" }
$lnk = Get-ChildItem "$env:APPDATA\Microsoft\Windows\Start Menu\Programs" -Recurse -Filter "Uni VPN.lnk"
if (-not $lnk) { throw "no Start menu entry" }
