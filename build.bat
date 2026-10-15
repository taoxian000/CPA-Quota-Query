@echo off
setlocal
cd /d "%~dp0"
set GOOS=windows
set GOARCH=amd64
rem Version: first argument, otherwise the nearest Git tag (e.g. build.bat v1.5).
set APP_VERSION=%~1
if "%APP_VERSION%"=="" for /f "delims=" %%v in ('git describe --tags --abbrev^=0 2^>nul') do set APP_VERSION=%%v
if "%APP_VERSION%"=="" (
  echo Cannot determine the version from Git tags. Pass it explicitly, e.g. build.bat v1.5
  exit /b 1
)
set OUTPUT=quota-monitor-%APP_VERSION%.exe
go build -trimpath -ldflags="-H=windowsgui -X main.appVersion=%APP_VERSION%" -o "%OUTPUT%" .
if errorlevel 1 exit /b %errorlevel%
echo Built: %CD%\%OUTPUT%
