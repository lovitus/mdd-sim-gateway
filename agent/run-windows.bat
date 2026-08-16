@echo off
title MDD Card Agent (Windows)
echo ========================================================
echo   MDD Card Agent - Smartcard Forwarding for Windows
echo ========================================================
echo.

set /p GATEWAY="Enter Gateway IP/Domain (e.g. 1.2.3.4 or 127.0.0.1) [127.0.0.1]: "
if "%GATEWAY%"=="" set GATEWAY=127.0.0.1

echo.
echo Connecting to Gateway %GATEWAY%:35963...
echo.

python -m pip install pyscard >nul 2>&1
python card_agent.py --gateway %GATEWAY% --port 35963

pause
