# Install uni-vpn on Windows: Python and OpenConnect (with Wintun) if missing, then "uni-vpn setup".
# Usage: powershell -ExecutionPolicy Bypass -File install.ps1 [-Uninstall | -Update | -Repair] [-DryRun] [-NoGui] [-User UNIVERSITY-ID] [-University ID]
# -Repair: what the app's Repair button runs: installs what is missing, registers the service
# again and opens no browser.
# Needs an administrator account: Wintun, the virtual network adapter openconnect uses on
# Windows, can only be created with administrator rights. The program goes to
# %ProgramFiles%\uni-vpn: the service runs it elevated, so only administrators may change it.
[CmdletBinding()]
param(
    [switch]$Uninstall,
    [switch]$Update,
    [switch]$Repair,
    [switch]$DryRun,
    [switch]$NoGui,
    [switch]$NoBrowser,
    [string]$User = "",
    [string]$University = "",
    [string]$ForUser = "",
    # Run by the Windows installer (packaging/windows): already elevated, no window to keep open.
    [switch]$Unattended
)
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"  # Invoke-WebRequest is many times slower with the progress bar
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
$Root = $PSScriptRoot
$AppDir = Join-Path $env:ProgramFiles "uni-vpn"

# openconnect.exe, its libraries and Wintun, built by CI (packaging/windows/build-openconnect.sh).
# The Windows installer ships the folder; this download is for installs without it.
$OpenConnectUrl = "https://github.com/DavidVinu/uni-vpn/releases/latest/download/uni-vpn-openconnect-win64.zip"
$OpenConnectDir = Join-Path $AppDir "openconnect"
# python.org's embeddable package, inside uni-vpn's own folder: it never meets a Python the
# user installed (with the same version already installed, the regular installer does nothing).
$PythonVersion = "3.12.10"
$PythonUrl = "https://www.python.org/ftp/python/$PythonVersion/python-$PythonVersion-embed-amd64.zip"
$PythonSha256 = "4acbed6dd1c744b0376e3b1cf57ce906f9dc9e95e68824584c8099a63025a3c3"
$BundledPython = Join-Path $AppDir "python\python.exe"

function Say($text) { Write-Host "-> $text" }

function Test-Admin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    return (New-Object Security.Principal.WindowsPrincipal $identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Find-Python([switch]$Anywhere) {
    # Native stderr (for example "py -3" without any Python 3) must not throw under "Stop"
    # in Windows PowerShell 5.1.
    $ErrorActionPreference = "Continue"
    # Only a Python in Program Files: a per-user one could be changed by any program of the user
    # and the service runs it elevated.
    $candidates = @($BundledPython)
    $bases = @($env:ProgramFiles)
    if ($Anywhere) { $bases += "$env:LOCALAPPDATA\Programs\Python" }
    foreach ($base in $bases) {
        if ($base -and (Test-Path $base)) {
            $candidates += Get-ChildItem -Path $base -Directory -Filter "Python3*" -ErrorAction SilentlyContinue |
                Sort-Object Name -Descending | ForEach-Object { Join-Path $_.FullName "python.exe" }
        }
    }
    $py = Get-Command py.exe -ErrorAction SilentlyContinue
    if ($py) {
        $found = & $py.Source -3 -c "import sys; print(sys.executable)" 2>$null
        if ($LASTEXITCODE -eq 0 -and $found) { $candidates += $found.Trim() }
    }
    foreach ($candidate in $candidates) {
        if (-not (Test-Path $candidate) -or $candidate -like "*\WindowsApps\*") { continue }
        if (-not $Anywhere -and -not $candidate.StartsWith("$env:ProgramFiles\", [StringComparison]::OrdinalIgnoreCase)) { continue }
        & $candidate -c "import sys; sys.exit(0 if sys.version_info >= (3, 11) else 1)" 2>$null
        if ($LASTEXITCODE -eq 0) { return $candidate }
    }
    return $null
}

function Find-OpenConnect {
    $exe = Join-Path $OpenConnectDir "openconnect.exe"
    if ((Test-Path $exe) -and (Test-Path (Join-Path $OpenConnectDir "wintun.dll"))) { return $exe }
    return $null
}

function Get-Download($url, $target) {
    Say "downloading $url"
    Invoke-WebRequest -Uri $url -OutFile $target -UseBasicParsing
}

function Install-Python {
    $zip = Join-Path $env:TEMP "python-$PythonVersion-embed-amd64.zip"
    Get-Download $PythonUrl $zip
    $hash = (Get-FileHash -Algorithm SHA256 $zip).Hash.ToLower()
    if ($hash -ne $PythonSha256) {
        Remove-Item $zip -ErrorAction SilentlyContinue
        throw "Python download checksum mismatch ($hash)"
    }
    $target = Split-Path $BundledPython
    Say "installing Python $PythonVersion to $target"
    if (Test-Path $target) { Remove-Item $target -Recurse -Force }
    Expand-Archive -Path $zip -DestinationPath $target -Force
    Remove-Item $zip -ErrorAction SilentlyContinue
    $signature = Get-AuthenticodeSignature $BundledPython
    if ($signature.Status -ne "Valid" -or $signature.SignerCertificate.Subject -notmatch "Python Software Foundation") {
        Remove-Item $target -Recurse -Force -ErrorAction SilentlyContinue
        throw "python.exe is not signed by the Python Software Foundation"
    }
}

function Install-OpenConnect {
    $zip = Join-Path $env:TEMP "uni-vpn-openconnect-win64.zip"
    Get-Download $OpenConnectUrl $zip
    Say "installing OpenConnect (openconnect.exe and Wintun) to $OpenConnectDir"
    if (Test-Path $OpenConnectDir) { Remove-Item $OpenConnectDir -Recurse -Force }
    Expand-Archive -Path $zip -DestinationPath $OpenConnectDir -Force
    Remove-Item $zip -ErrorAction SilentlyContinue
    $signature = Get-AuthenticodeSignature (Join-Path $OpenConnectDir "wintun.dll")
    if ($signature.Status -ne "Valid" -or $signature.SignerCertificate.Subject -notmatch "WireGuard") {
        Remove-Item $OpenConnectDir -Recurse -Force -ErrorAction SilentlyContinue
        throw "wintun.dll is not signed by WireGuard"
    }
    if (-not (Find-OpenConnect)) { throw "OpenConnect was not found after installing it" }
}

$mode = if ($Uninstall) { "uninstall" } elseif ($Update) { "update" } else { "setup" }
if ($Repair) { $NoBrowser = $true }

if ([Environment]::Is64BitOperatingSystem -eq $false -or $env:PROCESSOR_ARCHITECTURE -eq "ARM64") {
    Write-Host "uni-vpn needs 64-bit Windows on an Intel or AMD processor (OpenConnect has no ARM build)."
    if (-not $DryRun) { exit 1 }
}

if (-not $DryRun -and -not (Test-Admin) -and $Unattended) {
    Write-Host "Administrator rights are required (Wintun)."
    exit 1
}

if (-not $DryRun -and -not (Test-Admin)) {
    # One UAC prompt for the whole installation. The elevated run must be the same account,
    # because the service and the proxy setting belong to the signed-in user.
    $arguments = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$PSCommandPath`"", "-ForUser", "`"$env:USERNAME`"")
    if ($Uninstall) { $arguments += "-Uninstall" }
    if ($Update) { $arguments += "-Update" }
    if ($Repair) { $arguments += "-Repair" }
    if ($NoGui) { $arguments += "-NoGui" }
    # The app must not run elevated: this window opens it once the elevated part is done.
    $arguments += "-NoBrowser"
    if ($User) { $arguments += @("-User", "`"$User`"") }
    if ($University) { $arguments += @("-University", "`"$University`"") }
    Say "asking for administrator rights"
    try {
        $process = Start-Process -FilePath "powershell.exe" -ArgumentList $arguments -Verb RunAs -Wait -PassThru
    } catch {
        Write-Host "Administrator rights are required (Wintun). Run this from an administrator account."
        exit 1
    }
    if ($process.ExitCode -eq 0 -and $mode -eq "setup" -and -not $NoGui -and -not $Repair) {
        $port = 1081
        $config = Join-Path $env:LOCALAPPDATA "uni-vpn\config.toml"
        if (Test-Path $config) {
            $match = Select-String -Path $config -Pattern '^\s*http_port\s*=\s*(\d+)' | Select-Object -First 1
            if ($match) { $port = [int]$match.Matches[0].Groups[1].Value }
        }
        $url = "http://127.0.0.1:$port/"
        # A given ID and university are prefilled in the assistant, which writes the config.
        $query = @()
        if ($User) { $query += "user=$([uri]::EscapeDataString($User))" }
        if ($University) { $query += "university=$([uri]::EscapeDataString($University))" }
        $prefill = if ($query.Count -gt 0 -and -not (Test-Path $config)) { $query -join "&" } else { "" }
        $app = Join-Path $AppDir "desktop\Uni VPN.exe"
        if (Test-Path $app) {
            $appArgs = @("--port", "$port")
            if ($prefill) { $appArgs += @("--page", "`"&$prefill`"") }
            Start-Process -FilePath $app -ArgumentList $appArgs
        } else {
            if ($prefill) { $url += "?" + $prefill }
            Start-Process $url
        }
    }
    exit $process.ExitCode
}

if ($ForUser -and $ForUser -ne $env:USERNAME) {
    Write-Host "Installed as $env:USERNAME instead of $ForUser. uni-vpn has to run elevated as the signed-in user,"
    Write-Host "so install it from an account with administrator rights."
    if (-not $Unattended) { Read-Host "Press Enter to close" }
    exit 1
}

function Copy-App {
    # A fresh copy in Program Files, without files the new version no longer has; "desktop"
    # (the app window), "python" and "openconnect" (Install-*) are not part of the download.
    if ([IO.Path]::GetFullPath($Root).TrimEnd("\") -ieq [IO.Path]::GetFullPath($AppDir).TrimEnd("\")) { return }
    if ($DryRun) { Say "would copy uni-vpn to $AppDir"; return }
    Say "copying uni-vpn to $AppDir"
    # "desktop" holds the app that setup builds; it is not in the download and stays.
    & robocopy.exe $Root $AppDir /MIR /XD .git __pycache__ desktop python openconnect /R:2 /W:1 /NFL /NDL /NJH /NJS /NP | Out-Null
    if ($LASTEXITCODE -ge 8) { throw "Copying to $AppDir failed (robocopy $LASTEXITCODE)" }
}

try {
    if ($mode -ne "uninstall") {
        if (-not (Find-Python)) {
            if ($DryRun) { Say "would install Python 3.12" } else { Install-Python }
        }
        if (-not (Find-OpenConnect)) {
            if ($DryRun) { Say "would install OpenConnect from $OpenConnectUrl" } else { Install-OpenConnect }
        }
    }
    # Uninstalling runs nothing elevated afterwards, so any Python will do.
    $python = if ($mode -eq "uninstall") { Find-Python -Anywhere } else { Find-Python }
    if (-not $python) {
        if ($DryRun) { $python = (Get-Command python.exe).Source }
        elseif ($mode -eq "uninstall") { throw "Python >= 3.11 is needed to uninstall; run install.ps1 once to install it" }
        else { throw "Python >= 3.11 in Program Files was not found after installing it" }
    }
    $run = $Root
    if ($mode -ne "uninstall") {
        Copy-App
        if (-not $DryRun) { $run = $AppDir }
    } elseif (Test-Path (Join-Path $AppDir "bin\uni-vpn")) {
        $run = $AppDir
    }
    # The new files are in place; for an update "setup" registers the task and the command again
    # (paths and arguments may have changed) and restarts the service. It asks nothing.
    $command = if ($mode -eq "update" -and -not $DryRun) { "setup" } else { $mode }
    $cliArgs = @("-I", "$run\bin\uni-vpn", $command)
    if ($DryRun) { $cliArgs += "--dry-run" }
    if ($command -eq "setup" -and $mode -eq "update") { $cliArgs += "--no-browser" }
    if ($mode -eq "setup") {
        if ($NoGui) { $cliArgs += "--no-gui" }
        if ($NoBrowser) { $cliArgs += "--no-browser" }
        if ($User) { $cliArgs += @("--user", $User) }
        if ($University) { $cliArgs += @("--university", $University) }
    }
    & $python @cliArgs
    $code = $LASTEXITCODE
} catch {
    Write-Host "Installation failed: $($_.Exception.Message)"
    $code = 1
}
if ($ForUser -and -not $Unattended -and -not ($Repair -and $code -eq 0)) {
    # This window was opened for the elevated run; keep it until the result has been read.
    Read-Host "Press Enter to close"
}
exit $code
