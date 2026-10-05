# Install uni-vpn on Windows: Python and OpenConnect (with Wintun) if missing, then "uni-vpn setup".
# Usage: powershell -ExecutionPolicy Bypass -File install.ps1 [-Uninstall | -Update] [-DryRun] [-NoGui] [-User UNIVERSITY-ID] [-University ID]
# Needs an administrator account: Wintun, the virtual network adapter openconnect uses on
# Windows, can only be created with administrator rights. The program goes to
# %ProgramFiles%\uni-vpn: the service runs it elevated, so only administrators may change it.
[CmdletBinding()]
param(
    [switch]$Uninstall,
    [switch]$Update,
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

$OpenConnectUrl = "https://www.infradead.org/openconnect-gui/download/openconnect-gui-1.6.2-win64.exe"
$OpenConnectSha256 = "de08d8968e40e219932d01025521f879178ec99246802db488c0fdac9fcef11a"
$PythonVersion = "3.12.10"
$PythonUrl = "https://www.python.org/ftp/python/$PythonVersion/python-$PythonVersion-amd64.exe"

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
    $candidates = @()
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
    if ($NoGui) { $arguments += "-NoGui" }
    # The browser must not run elevated: this window opens it once the elevated part is done.
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
    if ($process.ExitCode -eq 0 -and $mode -eq "setup" -and -not $NoGui) {
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
        if ($query.Count -gt 0 -and -not (Test-Path $config)) { $url += "?" + ($query -join "&") }
        Start-Process $url
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
    # A fresh copy in Program Files, without files the new version no longer has.
    if ([IO.Path]::GetFullPath($Root).TrimEnd("\") -ieq [IO.Path]::GetFullPath($AppDir).TrimEnd("\")) { return }
    if ($DryRun) { Say "would copy uni-vpn to $AppDir"; return }
    Say "copying uni-vpn to $AppDir"
    & robocopy.exe $Root $AppDir /MIR /XD .git __pycache__ /R:2 /W:1 /NFL /NDL /NJH /NJS /NP | Out-Null
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
if ($ForUser -and -not $Unattended) {
    # This window was opened for the elevated run; keep it until the result has been read.
    Read-Host "Press Enter to close"
}
exit $code
