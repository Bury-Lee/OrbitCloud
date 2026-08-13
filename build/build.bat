@echo off
rem ============================================================
rem  OrbitCloud build entry (Windows)
rem  Usage: build\build.bat [-Frontend] [-Package] [-Version x.y.z]
rem  Equal to: powershell -ExecutionPolicy Bypass -File build\build.ps1 ...
rem ============================================================

setlocal

rem Switch console to UTF-8 codepage to avoid garbled Chinese output
chcp 65001 >nul

rem Default: cross-compile backend for windows/linux/darwin.
rem Add -Frontend to also build the Vue frontend,
rem add -Package to produce zip/tar.gz archives.
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0build.ps1" %*

if %errorlevel% neq 0 (
    echo.
    echo [ERROR] build failed, see build\dist\build.log
    exit /b %errorlevel%
)

endlocal
