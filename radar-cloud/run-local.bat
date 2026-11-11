@echo off
REM Runs the local production imitation with NO npm / no npm install.
REM Usage: run-local.bat            (demo data)
REM        run-local.bat --sim      (demo data + simulated live uploader)
REM        run-local.bat --fresh    (wipe saved data)
cd /d "%~dp0"
set "ARGS=%*"
if "%ARGS%"=="" set "ARGS=--demo"

where node >nul 2>nul
if %errorlevel%==0 (
  node dev\server.mjs %ARGS%
  goto :eof
)

set "VSC=%LOCALAPPDATA%\Programs\Microsoft VS Code\Code.exe"
if exist "%VSC%" (
  echo node not found - using the Node bundled with VS Code
  set ELECTRON_RUN_AS_NODE=1
  "%VSC%" dev\server.mjs %ARGS%
  goto :eof
)

echo.
echo No Node found. Install Node.js 22 LTS from https://nodejs.org (the installer includes everything),
echo or install VS Code, then run this file again.
pause
