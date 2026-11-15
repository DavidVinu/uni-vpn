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
# In a process of its own: one that loaded the DLLs from Program Files keeps them in use, and
# installing again could not replace them.
powershell -NoProfile -NonInteractive -Command {
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
$problems = @()
# The service's processes: the task runs conhost.exe, which starts uni-vpn-core.exe.
function Daemons { @(Get-CimInstance Win32_Process -Filter "Name = 'uni-vpn-core.exe'" | Where-Object { $_.CommandLine -like "* daemon*" }) }
function ShowService {
  Write-Host "task: $((Get-ScheduledTask -TaskName uni-vpn).State)"
  Get-CimInstance Win32_Process | Where-Object { ($_.Name -eq "uni-vpn-core.exe" -or $_.Name -eq "conhost.exe") -and $_.CommandLine -like "*uni-vpn*" } |
    Format-Table ProcessId, ParentProcessId, CreationDate, CommandLine -AutoSize -Wrap | Out-String -Width 400 | Write-Host
}
function Answers { try { Invoke-WebRequest -UseBasicParsing -TimeoutSec 1 http://127.0.0.1:1081/status.json | Out-Null; return $true } catch { return $false } }
# The tray app starts a stopped service again: nobody should need a terminal. Here it ends as
# in a crash. "/End" alone ends only conhost, not the service it started: Repair, setup and
# uninstall end that one too (checked below and in test-uninstall.ps1).
ShowService
$end = cmd /c "schtasks /End /TN uni-vpn 2>&1"  # stderr through PowerShell would stop the script
Write-Host "schtasks /End: $end (exit $LASTEXITCODE)"
$sw = [Diagnostics.Stopwatch]::StartNew()
Daemons | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
$down = $null; $up = $null
while ($sw.Elapsed.TotalSeconds -lt 30 -and -not $up) {
  $answers = Answers
  if (-not $answers -and -not $down) { $down = $sw.Elapsed.TotalSeconds }
  if ($answers -and $down) { $up = $sw.Elapsed.TotalSeconds }
  Start-Sleep -Milliseconds 250
}
ShowService
Write-Host "service ended at $down s, the tray app started it again at $up s"
if (-not $up) { Explain; $problems += "the tray app did not start the stopped service again" }
foreach ($i in 1..30) { if (Answers) { break }; Start-Sleep 1 }
# Installing again while it all runs (the website's installer over an installed one): the
# installer stops everything that runs from Program Files, the tray app first, or it starts
# the service again meanwhile, which then holds its files ("DeleteFile failed; code 5").
$sweep = Measure-Command { powershell -NoProfile -NonInteractive -Command "Get-Process | Where-Object { `$_.Path -like '$env:ProgramFiles\uni-vpn\*' } | Out-Null" }
Write-Host "the installer's search for running programs takes $($sweep.TotalSeconds) s"
$process = Start-Process -FilePath out\uni-vpn-setup.exe -ArgumentList "/VERYSILENT", "/SUPPRESSMSGBOXES", "/LOG=reinstall-setup.log" -PassThru
if (-not $process.WaitForExit(300000)) { Explain; throw "installing again did not finish within 5 minutes" }
if ($process.ExitCode -ne 0) { Get-Content reinstall-setup.log -Tail 40; Explain; throw "installing again over the running app failed" }
$ok = $false
foreach ($i in 1..30) {
  try { $status = Invoke-RestMethod http://127.0.0.1:1081/status.json; $ok = $true; break } catch { Start-Sleep 1 }
}
if (-not $ok -or $status.commit -ne $env:GITHUB_SHA) { Explain; throw "the service does not run after installing again" }
# Repair stops and starts the service while the tray app starts a stopped one again. Here at the
# worst moment, as soon as Task Scheduler no longer counts it as running: setup ends it again,
# else it fails ("The running service did not stop") and skips the proxy rule and the app.
$core = "$env:ProgramFiles\uni-vpn\bin\uni-vpn-core.exe"
$before = @(Daemons | ForEach-Object ProcessId)
[IO.File]::WriteAllText("$PWD\empty-input.txt", "")
$repair = Start-Process -FilePath $core -ArgumentList "setup --no-browser --repair" -RedirectStandardInput empty-input.txt `
  -RedirectStandardOutput repair.log -RedirectStandardError repair-err.log -NoNewWindow -PassThru
$null = $repair.Handle  # else ExitCode stays empty
$sw = [Diagnostics.Stopwatch]::StartNew()
$restarted = $null
while (-not $repair.HasExited -and $sw.Elapsed.TotalSeconds -lt 60) {
  if ((Get-ScheduledTask -TaskName uni-vpn).State -ne "Running") { schtasks /Run /TN uni-vpn | Out-Null; $restarted = $sw.Elapsed.TotalSeconds; break }
  Start-Sleep -Milliseconds 50
}
if (-not $repair.WaitForExit(300000)) { Explain; throw "Repair's setup did not finish within 5 minutes" }
Get-Content repair.log, repair-err.log -ErrorAction SilentlyContinue
Write-Host "the test started the task again at $restarted s; setup ended with $($repair.ExitCode) at $($sw.Elapsed.TotalSeconds) s"
if ($repair.ExitCode -ne 0) { Explain; $problems += "Repair failed when the service was started again meanwhile" }
$ok = $false
foreach ($i in 1..30) {
  try { $status = Invoke-RestMethod http://127.0.0.1:1081/status.json; $ok = $true; break } catch { Start-Sleep 1 }
}
if (-not $ok -or $status.commit -ne $env:GITHUB_SHA) { Explain; throw "the service does not run after Repair" }
ShowService
if (@(Daemons | Where-Object { $before -contains $_.ProcessId }).Count -gt 0) { $problems += "Repair left the old service running" }
# Normal priority: Task Scheduler's default runs a task below normal, with low I/O and memory
# priority, while all of the browser's traffic to the university goes through the service.
$taskPriority = (Get-ScheduledTask -TaskName uni-vpn).Settings.Priority
$daemon = @(Get-CimInstance Win32_Process -Filter "Name = 'uni-vpn-core.exe'" | Where-Object { $_.CommandLine -like "* daemon*" })
Write-Host "task priority $taskPriority; service base priority $(($daemon | ForEach-Object Priority) -join ', ') (8 is normal, 6 below normal)"
if ($taskPriority -ne 4) { $problems += "the task has priority $taskPriority, not 4" }
if ($daemon.Count -eq 0 -or ($daemon | Where-Object { $_.Priority -lt 8 })) { $problems += "the service does not run at normal priority" }
# An otpauth:// link's key must not stay on disk in the window's browsing history, as it does
# not in the browser's (the fallback without WebView2 leaves it out): uninstall deletes the key
# everywhere else. Looked for again below, once the window's browser process has ended.
$key = "UNIVPNCIKEYABCDEFG"
function KeyOnDisk {
  $read = 0; $unread = @()
  foreach ($file in Get-ChildItem "$env:LOCALAPPDATA\uni-vpn\WebView2" -Recurse -File -ErrorAction SilentlyContinue) {
    try {
      # Also files the running WebView2 has open.
      $stream = [IO.File]::Open($file.FullName, [IO.FileMode]::Open, [IO.FileAccess]::Read, ([IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete))
      $bytes = New-Object byte[] $stream.Length
      $n = 0
      while ($n -lt $bytes.Length) { $got = $stream.Read($bytes, $n, $bytes.Length - $n); if ($got -le 0) { break }; $n += $got }
      $stream.Close()
      $read++
      if ([Text.Encoding]::ASCII.GetString($bytes).Contains($key) -or [Text.Encoding]::Unicode.GetString($bytes).Contains($key)) { return $file.FullName }
    } catch { $unread += "$($file.Name): $($_.Exception.Message)" }
  }
  Write-Host "looked for the key in $read files of the WebView2 folder; could not read $($unread.Count) $($unread -join '; ')"
  if ($read -eq 0 -and $unread.Count -gt 0) { return "none of its files (none could be read)" }
  return $null
}
# The link starts the app (here without a window yet, so the page loads with the key in it).
Get-Process -Name "Uni VPN" -ErrorAction SilentlyContinue | Stop-Process -Force
foreach ($i in 1..30) { if ((AppWebViews).Count -eq 0) { break }; Start-Sleep 1 }
Start-Process $exe -ArgumentList "otpauth://totp/uni-vpn-ci?secret=$key"
Start-Sleep 20  # Chromium writes its history every 10 s
$found = KeyOnDisk
if ($found) { $problems += "the otpauth link's key is on disk in $found" }
Get-ChildItem "$env:LOCALAPPDATA\uni-vpn\WebView2" -Depth 2 -ErrorAction SilentlyContinue | ForEach-Object FullName
# A page process that ends (crash, out of memory, Task Manager) loads the page again; without
# its browser process the window gets a new one. Both left the window blank until Exit.
function AppBrowser { @(AppWebViews | Where-Object { $_.CommandLine -notmatch "--type=" }) }
function Renderers($browser) { @(Get-CimInstance Win32_Process -Filter "ParentProcessId = $browser" | Where-Object { $_.CommandLine -match "--type=renderer" }) }
foreach ($i in 1..30) { if ((AppBrowser).Count -gt 0 -and (Renderers (AppBrowser)[0].ProcessId).Count -gt 0) { break }; Start-Sleep 1 }
if ((AppBrowser).Count -eq 0) { Explain; throw "the otpauth link did not open the app window" }
$browser = (AppBrowser)[0].ProcessId
$renderers = @(Renderers $browser | ForEach-Object ProcessId)
Write-Host "app window: browser process $browser, page processes $($renderers -join ', ')"
$renderers | ForEach-Object { Stop-Process -Id $_ -Force -ErrorAction SilentlyContinue }
Start-Sleep 5
Get-Content "$env:LOCALAPPDATA\uni-vpn\logs\app.log" -ErrorAction SilentlyContinue
# WebView2 shows an error page in a new page process by itself: only the window's note says the
# page itself loaded again.
if ((Get-Content "$env:LOCALAPPDATA\uni-vpn\logs\app.log" -Raw -ErrorAction SilentlyContinue) -notlike "*RenderProcessExited, the page loaded again*") {
  Explain; $problems += "the page did not load again after its process ended"
}
Stop-Process -Id $browser -Force
function NewBrowser { @(AppBrowser | Where-Object { $_.ProcessId -ne $browser }) }
foreach ($i in 1..20) { if ((NewBrowser).Count -gt 0) { break }; Start-Sleep 1 }
Get-Content "$env:LOCALAPPDATA\uni-vpn\logs\app.log" -ErrorAction SilentlyContinue
if ((NewBrowser).Count -eq 0) { Explain; $problems += "the window did not come back after its browser process ended" }
if ((Trays).Count -eq 0) { Explain; $problems += "the tray app ended with the window's browser process" }
if (-not $found) {
  $found = KeyOnDisk
  if ($found) { $problems += "the otpauth link's key is on disk in $found once the browser process ended" }
}
if ($problems.Count -gt 0) { throw ($problems -join "; ") }
