# What Windows says about uni-vpn-setup.exe as a user downloads it from the site.
$ErrorActionPreference = "Continue"
New-Item -ItemType Directory -Force probe | Out-Null
$os = Get-CimInstance Win32_OperatingSystem
"OS: $($os.Caption) $($os.Version)"
function Reg($path, $name) { try { (Get-ItemProperty -Path $path -Name $name -ErrorAction Stop).$name } catch { "<unset>" } }
"SmartScreenEnabled (Explorer): " + (Reg "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer" "SmartScreenEnabled")
"EnableSmartScreen (policy): " + (Reg "HKLM:\SOFTWARE\Policies\Microsoft\Windows\System" "EnableSmartScreen")
"Smart App Control (0 off, 1 on, 2 evaluation): " + (Reg "HKLM:\SYSTEM\CurrentControlSet\Control\CI\Policy" "VerifiedAndReputablePolicyState")
"UAC EnableLUA: " + (Reg "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System" "EnableLUA")
"Session: $([Environment]::UserInteractive) user $env:USERNAME"

$site = (Invoke-WebRequest -UseBasicParsing https://vpn.davidvinu.de/).Content
$link = [regex]::Match($site, 'https://[^"''\s]*uni-vpn-setup\.exe').Value
"Site links: $link"
$file = "$env:USERPROFILE\Downloads\uni-vpn-setup.exe"
New-Item -ItemType Directory -Force (Split-Path $file) | Out-Null
Invoke-WebRequest -UseBasicParsing $link -OutFile $file
"Size: $((Get-Item $file).Length)  SHA256: $((Get-FileHash $file).Hash)"
# What every browser adds to a download (Mark of the Web).
Set-Content -Path $file -Stream Zone.Identifier -Value "[ZoneTransfer]`r`nZoneId=3`r`nReferrerUrl=https://vpn.davidvinu.de/`r`nHostUrl=$link"
"Zone.Identifier:"; Get-Content $file -Stream Zone.Identifier
$sig = Get-AuthenticodeSignature $file
"Signature: $($sig.Status) $($sig.SignerCertificate.Subject)"

"--- Defender"
try { $mp = Get-MpComputerStatus; "Antivirus enabled: $($mp.AntivirusEnabled), realtime: $($mp.RealTimeProtectionEnabled), signatures: $($mp.AntivirusSignatureVersion)" } catch { "Get-MpComputerStatus: $_" }
$mpcmd = Get-ChildItem "$env:ProgramData\Microsoft\Windows Defender\Platform\*\MpCmdRun.exe" -ErrorAction SilentlyContinue | Select-Object -Last 1
if (-not $mpcmd) { $mpcmd = Get-Item "$env:ProgramFiles\Windows Defender\MpCmdRun.exe" -ErrorAction SilentlyContinue }
if ($mpcmd) { & $mpcmd.FullName -Scan -ScanType 3 -File $file -DisableRemediation; "MpCmdRun exit: $LASTEXITCODE (0 clean, 2 threat)" }
# Also what the installer unpacks: the core and the app window.
$x = "$env:TEMP\unpacked"; New-Item -ItemType Directory -Force $x | Out-Null

"--- Opening it like a double-click (Explorer, ShellExecute)"
Add-Type -AssemblyName System.Windows.Forms, System.Drawing, UIAutomationClient
function Shot($name) {
  $b = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
  if ($b.Width -eq 0) { "no screen"; return }
  $bmp = New-Object System.Drawing.Bitmap $b.Width, $b.Height
  $g = [System.Drawing.Graphics]::FromImage($bmp); $g.CopyFromScreen($b.Location, [System.Drawing.Point]::Empty, $b.Size)
  $bmp.Save("probe\$name.png"); $g.Dispose(); $bmp.Dispose()
}
function Windows() {
  $root = [System.Windows.Automation.AutomationElement]::RootElement
  $all = $root.FindAll([System.Windows.Automation.TreeScope]::Children, [System.Windows.Automation.Condition]::TrueCondition)
  foreach ($w in $all) {
    $p = try { (Get-Process -Id $w.Current.ProcessId).ProcessName } catch { "?" }
    "  window [$p] '$($w.Current.Name)'"
    if ($p -match "smartscreen|setup|uni-vpn") {
      $texts = $w.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)
      foreach ($t in $texts) { if ($t.Current.Name) { "    - $($t.Current.ControlType.ProgrammaticName): $($t.Current.Name)" } }
    }
  }
}
Start-Process explorer.exe -ArgumentList "`"$file`""
foreach ($i in 1..4) {
  Start-Sleep -Seconds 6
  "after $($i*6) s:"
  Get-Process | Where-Object { $_.ProcessName -match "smartscreen|uni-vpn|setup" } | ForEach-Object { "  process $($_.ProcessName) $($_.Id)" }
  Windows
  Shot "open-$i"
}
"--- Downloading it in the browsers (a fresh profile each)"
$browsers = @{ edge = "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe"; chrome = "$env:ProgramFiles\Google\Chrome\Application\chrome.exe" }
foreach ($name in $browsers.Keys) {
  $exe = $browsers[$name]
  if (-not (Test-Path $exe)) { "$name not installed"; continue }
  $profile = "$env:TEMP\profile-$name"
  $p = Start-Process $exe -PassThru -ArgumentList "--user-data-dir=$profile", "--no-first-run", "--no-default-browser-check", "--start-maximized", "https://vpn.davidvinu.de/"
  Start-Sleep -Seconds 8
  Shot "$name-site"
  Start-Process $exe -ArgumentList "--user-data-dir=$profile", $link
  Start-Sleep -Seconds 40
  Shot "$name-download"
  Start-Process $exe -ArgumentList "--user-data-dir=$profile", $(if ($name -eq "edge") { "edge://downloads" } else { "chrome://downloads" })
  Start-Sleep -Seconds 5
  Shot "$name-downloads-page"
  Get-ChildItem "$env:USERPROFILE\Downloads" | ForEach-Object { "  downloads folder: $($_.Name) $($_.Length)" }
  Get-Process | Where-Object { $_.Path -eq $exe } | Stop-Process -Force -ErrorAction SilentlyContinue
}
"--- SmartScreen event log"
Get-WinEvent -LogName "Microsoft-Windows-SmartScreen/Debug" -MaxEvents 20 -ErrorAction SilentlyContinue | Format-List TimeCreated, Id, Message
Get-Process | Where-Object { $_.ProcessName -match "uni-vpn-setup" } | Stop-Process -Force -ErrorAction SilentlyContinue
exit 0
