@echo off
setlocal

rem Resolve the repository root relative to this launcher so the scheduled task
rem keeps using the current Prizm config and durable binary.
for %%I in ("%~dp0..\..") do set "PRIZM_ROOT=%%~fI"
cd /d "%PRIZM_ROOT%"
if not exist "logs" mkdir "logs"

"%PRIZM_ROOT%\bin\prizm.exe" serve --config "%PRIZM_ROOT%\prizm.yaml" >> "%PRIZM_ROOT%\logs\prizm-serve.log" 2>&1
