@echo off
setlocal
cd /d "%~dp0"
set GOOS=windows
set GOARCH=amd64
go build -trimpath -ldflags="-H=windowsgui" -o quota-monitor.exe .
if errorlevel 1 exit /b %errorlevel%
echo Built: %CD%\quota-monitor.exe
