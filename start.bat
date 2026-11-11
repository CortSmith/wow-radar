@echo off
cd /d "%~dp0"

rem Preferred: the single-file Go radar (no Python needed).
if exist "go-radar\dist\radar.exe" (
  start "" "go-radar\dist\radar.exe" %*
  goto :eof
)
if exist "radar.exe" (
  start "" "radar.exe" %*
  goto :eof
)

rem Fallback: the original Python tracker.
where py >nul 2>nul
if %errorlevel%==0 (
  py -3 nearby_tracker.py %*
  goto :end
)
where python >nul 2>nul
if %errorlevel%==0 (
  python nearby_tracker.py %*
  goto :end
)
echo radar.exe not found. Build it with: powershell -ExecutionPolicy Bypass -File go-radar\build.ps1
echo (or install Python 3.9+ to use the old tracker).
:end
pause
