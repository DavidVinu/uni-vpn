# The Windows core with libopenconnect inside (build-core-inprocess.sh) on real Windows: it
# loads its DLLs, checks Heidelberg's gateway against Windows' own certificates and gets as
# far as the login with a made-up account, with no network adapter. The tunnel itself is
# tested under Wine against ocserv (packaging/inprocess/e2e.sh).
# Usage: test-inprocess.ps1 DIR (uni-vpn-core.exe and its DLLs)
param([Parameter(Mandatory = $true)][string]$Dir)
$ErrorActionPreference = "Stop"
$core = Join-Path (Resolve-Path $Dir) "uni-vpn-core.exe"
$env:UNI_VPN_INPROCESS = "1"
$out = Join-Path $env:RUNNER_TEMP "core.out"
$proc = Start-Process $core -ArgumentList "daemon" -PassThru -NoNewWindow -RedirectStandardOutput $out -RedirectStandardError "$out.err"

function Api($path, $body) {
  Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:1081$path" -Headers @{ "X-Uni-VPN" = "1" } -ContentType "application/json" -Body $body
}
function State() { (Invoke-RestMethod "http://127.0.0.1:1081/status.json").state }
function Logs() {
  Get-Content $out, "$out.err" -ErrorAction SilentlyContinue
  Get-ChildItem $env:LOCALAPPDATA, $env:APPDATA -Recurse -Filter daemon.log -ErrorAction SilentlyContinue |
    ForEach-Object { Get-Content $_.FullName -Tail 60 }
}

try {
  foreach ($i in 1..60) { try { State | Out-Null; break } catch { Start-Sleep -Milliseconds 500 } }
  Api "/api/setup" '{"user": "ab123", "password": "wrong-password", "secret": "JBSWY3DPEHPK3PXP", "university": "heidelberg"}' | Out-Null
  Api "/api/connect" '{}' | Out-Null
  $state = ""
  foreach ($i in 1..90) {
    $state = State
    if ($state -eq "auth_failed") { break }
    Start-Sleep -Seconds 1
  }
  $log = (Logs) -join "`n"
  if ($state -ne "auth_failed") { Write-Host $log; throw "state is '$state', expected auth_failed" }
  if ($log -notmatch "Login failed") { Write-Host $log; throw "no login answer from the gateway" }
  if (Get-NetAdapter -Name "uni-vpn" -ErrorAction SilentlyContinue) { throw "a uni-vpn network adapter exists" }
  Write-Host "in-process core on Windows: the gateway refused the made-up account, no adapter"
} finally {
  Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
}
