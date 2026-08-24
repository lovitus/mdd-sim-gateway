@echo off
if exist "%~dp0mdd-agent-gui.exe" (
    start "MDD Agent" "%~dp0mdd-agent-gui.exe"
    exit /b 0
)
echo This legacy launcher is disabled because the unified service must be the only device owner.
echo Install mdd-agent.exe as documented in MODEM_AGENT.md, then use mdd-agent-gui.exe.
exit /b 4
