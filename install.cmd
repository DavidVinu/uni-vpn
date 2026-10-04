@echo off
rem Double-click to install uni-vpn on Windows (asks for administrator rights once).
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1" %*
if errorlevel 1 pause
