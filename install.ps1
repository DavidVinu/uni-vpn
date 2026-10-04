# Install uni-vpn on Windows: Python and OpenConnect (with Wintun) if missing, then "uni-vpn setup".
# Usage: powershell -ExecutionPolicy Bypass -File install.ps1 [-Uninstall | -Update] [-DryRun] [-NoGui] [-User UNIVERSITY-ID]
# Needs an administrator account: Wintun, the virtual network adapter openconnect uses on
# Windows, can only be created with administrator rights.
[CmdletBinding()]
param(
    [switch]$Uninstall,
    [switch]$Update,
    [switch]$DryRun,
    [switch]$NoGui,
    [switch]$NoBrowser,
    [string]$User = "",
    [string]$ForUser = ""
)
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"  # Invoke-WebRequest is many times slower with the progress bar
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
$Root = $PSScriptRoot

$OpenConnectUrl = "https://www.infradead.org/openconnect-gui/download/openconnect-gui-1.6.2-win64.exe"
$OpenConnectSha256 = "de08d8968e40e219932d01025521f879178ec99246802db488c0fdac9fcef11a"
$PythonVersion = "3.12.10"
$PythonUrl = "https://www.python.org/ftp/python/$PythonVersion/python-$PythonVersion-amd64.exe"

function Say($text) { Write-Host "-> $text" }

function Test-Admin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    return (New-Object Security.Principal.WindowsPrincipal $identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Find-Python {
    $candidates = @()
    foreach ($base in @($env:ProgramFiles, "$env:LOCALAPPDATA\Programs\Python")) {
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
        # Skip the Microsoft Store alias: it opens the Store instead of running Python.
        if (-not (Test-Path $candidate) -or $candidate -like "*\WindowsApps\*") { continue }
        & $candidate -c "import sys; sys.exit(0 if sys.version_info >= (3, 11) else 1)" 2>$null
        if ($LASTEXITCODE -eq 0) { return $candidate }
    }
    return $null
}

function Find-OpenConnect {
    foreach ($base in @($env:ProgramFiles, ${env:ProgramFiles(x86)})) {
        foreach ($name in @("OpenConnect-GUI", "OpenConnect")) {
            $exe = Join-Path $base "$name\openconnect.exe"
            if ($base -and (Test-Path $exe) -and (Test-Path (Join-Path $base "$name\wintun.dll"))) { return $exe }
        }
    }
    return $null
}

function Get-Download($url, $target) {
    Say "downloading $url"
    Invoke-WebRequest -Uri $url -OutFile $target -UseBasicParsing
}

function Install-Python {
    $winget = Get-Command winget.exe -ErrorAction SilentlyContinue
    if ($winget) {
        Say "installing Python 3.12 with winget"
        & $winget.Source install --exact --id Python.Python.3.12 --scope machine --silent `
            --accept-package-agreements --accept-source-agreements | Out-Host
        if (Find-Python) { return }
    }
    $installer = Join-Path $env:TEMP "python-$PythonVersion-amd64.exe"
    Get-Download $PythonUrl $installer
    $signature = Get-AuthenticodeSignature $installer
    if ($signature.Status -ne "Valid" -or $signature.SignerCertificate.Subject -notmatch "Python Software Foundation") {
        throw "The Python installer is not signed by the Python Software Foundation"
    }
    Say "installing Python $PythonVersion"
    $process = Start-Process -FilePath $installer -ArgumentList "/quiet", "InstallAllUsers=1", "PrependPath=0", "Include_test=0" -Wait -PassThru
    Remove-Item $installer -ErrorAction SilentlyContinue
    if ($process.ExitCode -ne 0) { throw "Python installer failed with exit code $($process.ExitCode)" }
}

function Install-OpenConnect {
    $installer = Join-Path $env:TEMP "openconnect-gui-setup.exe"
    Get-Download $OpenConnectUrl $installer
    $hash = (Get-FileHash -Algorithm SHA256 $installer).Hash.ToLower()
    if ($hash -ne $OpenConnectSha256) {
        Remove-Item $installer -ErrorAction SilentlyContinue
        throw "OpenConnect installer checksum mismatch ($hash)"
    }
    Say "installing OpenConnect (openconnect.exe and Wintun)"
    $process = Start-Process -FilePath $installer -ArgumentList "/S" -Wait -PassThru
    Remove-Item $installer -ErrorAction SilentlyContinue
    if ($process.ExitCode -ne 0) { throw "OpenConnect installer failed with exit code $($process.ExitCode)" }
}

$mode = if ($Uninstall) { "uninstall" } elseif ($Update) { "update" } else { "setup" }

if ([Environment]::Is64BitOperatingSystem -eq $false -or $env:PROCESSOR_ARCHITECTURE -eq "ARM64") {
    Write-Host "uni-vpn needs 64-bit Windows on an Intel or AMD processor (OpenConnect has no ARM build)."
    if (-not $DryRun) { exit 1 }
}

if (-not $DryRun -and -not (Test-Admin)) {
    # One UAC prompt for the whole installation. The elevated run must be the same account,
    # because the service and the proxy setting belong to the signed-in user.
    $arguments = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$PSCommandPath`"", "-ForUser", "`"$env:USERNAME`"")
    if ($Uninstall) { $arguments += "-Uninstall" }
    if ($Update) { $arguments += "-Update" }
    if ($NoGui) { $arguments += "-NoGui" }
    # The browser must not run elevated: this window opens it once the elevated part is done.
    $arguments += "-NoBrowser"
    if ($User) { $arguments += @("-User", "`"$User`"") }
    Say "asking for administrator rights"
    try {
        $process = Start-Process -FilePath "powershell.exe" -ArgumentList $arguments -Verb RunAs -Wait -PassThru
    } catch {
        Write-Host "Administrator rights are required (Wintun). Run this from an administrator account."
        exit 1
    }
    if ($process.ExitCode -eq 0 -and $mode -eq "setup" -and -not $NoGui) {
        Start-Process "http://127.0.0.1:1081/"
    }
    exit $process.ExitCode
}

if ($ForUser -and $ForUser -ne $env:USERNAME) {
    Write-Host "Installed as $env:USERNAME instead of $ForUser. uni-vpn has to run elevated as the signed-in user,"
    Write-Host "so install it from an account with administrator rights."
    Read-Host "Press Enter to close"
    exit 1
}

try {
    if ($mode -eq "setup") {
        if (-not (Find-Python)) {
            if ($DryRun) { Say "would install Python 3.12" } else { Install-Python }
        }
        if (-not (Find-OpenConnect)) {
            if ($DryRun) { Say "would install OpenConnect from $OpenConnectUrl" } else { Install-OpenConnect }
        }
    }
    $python = Find-Python
    if (-not $python) {
        if ($DryRun) { $python = (Get-Command python.exe).Source } else { throw "Python >= 3.11 was not found after installing it" }
    }
    $cliArgs = @("$Root\bin\uni-vpn", $mode)
    if ($DryRun) { $cliArgs += "--dry-run" }
    if ($mode -eq "setup") {
        if ($NoGui) { $cliArgs += "--no-gui" }
        if ($NoBrowser) { $cliArgs += "--no-browser" }
        if ($User) { $cliArgs += @("--user", $User) }
    }
    & $python @cliArgs
    $code = $LASTEXITCODE
} catch {
    Write-Host "Installation failed: $($_.Exception.Message)"
    $code = 1
}
if ($ForUser) {
    # This window was opened for the elevated run; keep it until the result has been read.
    Read-Host "Press Enter to close"
}
exit $code
