@echo off
rem ===========================================================
rem  WorkBuddy2API Desktop - portable run (no install needed)
rem
rem  Difference from the installed version: data lives in the
rem  "data" folder next to this file, instead of
rem  %APPDATA%\WorkBuddy2API. Delete this whole folder and
rem  nothing is left behind.
rem
rem  NOTE: keep this file pure ASCII. A UTF-8 .cmd without BOM is
rem  parsed by cmd using the system codepage (936 on zh-CN), and
rem  non-ASCII bytes tear command lines apart. That failure mode
rem  is silent and looks like "the window just never opens".
rem ===========================================================

rem Data dir follows this script's own location (%~dp0 ends with a backslash).
set "WB2A_DATA_DIR=%~dp0data"

if not exist "%WB2A_DATA_DIR%" mkdir "%WB2A_DATA_DIR%"

echo.
echo   Data folder : %WB2A_DATA_DIR%
echo   First run creates config.json with a random api_key automatically.
echo.
echo   Closing the window asks: minimize to tray, or quit.
echo   Tray icon is in the notification area; right-click for the menu.
echo   To fully exit, choose "Quit".
echo.

start "" "%~dp0wb2api-desktop.exe"
