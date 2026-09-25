@echo off
setlocal
cd /d "%~dp0"
set GOOS=windows
set GOARCH=amd64
set APP_VERSION=v1.2
set OUTPUT=quota-monitor-%APP_VERSION%.exe
go build -trimpath -ldflags="-H=windowsgui -X main.appVersion=%APP_VERSION%" -o "%OUTPUT%" .
if errorlevel 1 exit /b %errorlevel%
echo Built: %CD%\%OUTPUT%
