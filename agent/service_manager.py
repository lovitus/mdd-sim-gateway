"""SCM operations shared by the SSH CLI and GUI clients."""

from __future__ import annotations

import ctypes
import os
import subprocess
import sys
import time
from pathlib import Path


SERVICE_NAME = "MddAgent"
UNIFIED_WINDOWS_MUTEX = r"Global\MDDUnifiedAgent-v1"


def _wait_agent_lease_released(timeout: float = 90.0) -> None:
    """Wait until the stopped hardware owner has closed its host-wide lease."""
    if os.name != "nt":
        return
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.OpenMutexW.argtypes = [ctypes.c_uint32, ctypes.c_bool, ctypes.c_wchar_p]
    kernel32.OpenMutexW.restype = ctypes.c_void_p
    kernel32.CloseHandle.argtypes = [ctypes.c_void_p]
    kernel32.CloseHandle.restype = ctypes.c_bool
    synchronize = 0x00100000
    error_file_not_found = 2
    error_access_denied = 5
    deadline = time.monotonic() + timeout
    while True:
        ctypes.set_last_error(0)
        handle = kernel32.OpenMutexW(synchronize, False, UNIFIED_WINDOWS_MUTEX)
        error = ctypes.get_last_error()
        if handle:
            kernel32.CloseHandle(handle)
            existed = True
        elif error == error_file_not_found:
            existed = False
        elif error == error_access_denied:
            # LocalSystem may create the lease with a DACL that an Operators client cannot
            # inspect.  Access denied proves the named object still exists, so keep waiting.
            existed = True
        else:
            raise OSError(error, "OpenMutexW failed")
        if not existed:
            return
        if time.monotonic() >= deadline:
            raise TimeoutError("the previous MDD Agent process did not release its runtime lease")
        time.sleep(0.25)


def is_windows_admin() -> bool:
    if os.name != "nt":
        return os.geteuid() == 0
    try:
        import win32api
        import win32con
        import win32security
        sid = win32security.CreateWellKnownSid(
            win32security.WinBuiltinAdministratorsSid, None)
        return bool(win32security.CheckTokenMembership(None, sid))
    except Exception:
        return False


class ServiceManager:
    def __init__(self, executable: str | None = None, data_dir: str | None = None):
        self.executable = executable or sys.executable
        self.data_dir = data_dir or os.environ.get("MDD_AGENT_DATA_DIR", "")

    def status(self) -> dict:
        if os.name != "nt":
            return {"installed": False, "state": "unsupported", "service": SERVICE_NAME}
        import pywintypes
        import win32service
        manager = win32service.OpenSCManager(None, None, win32service.SC_MANAGER_CONNECT)
        service = None
        try:
            try:
                service = win32service.OpenService(
                    manager, SERVICE_NAME, win32service.SERVICE_QUERY_STATUS)
            except pywintypes.error as exc:
                if exc.winerror == 1060:
                    return {"installed": False, "state": "not_installed",
                            "service": SERVICE_NAME}
                if exc.winerror == 5:
                    raise PermissionError("permission_denied") from exc
                raise
            status = win32service.QueryServiceStatusEx(service)
            names = {
                win32service.SERVICE_STOPPED: "stopped",
                win32service.SERVICE_START_PENDING: "start_pending",
                win32service.SERVICE_STOP_PENDING: "stop_pending",
                win32service.SERVICE_RUNNING: "running",
                win32service.SERVICE_CONTINUE_PENDING: "continue_pending",
                win32service.SERVICE_PAUSE_PENDING: "pause_pending",
                win32service.SERVICE_PAUSED: "paused",
            }
            return {"installed": True, "state": names.get(status["CurrentState"], "unknown"),
                    "service": SERVICE_NAME, "pid": int(status.get("ProcessId") or 0),
                    "checkpoint": int(status.get("CheckPoint") or 0),
                    "wait_hint_ms": int(status.get("WaitHint") or 0)}
        finally:
            if service is not None:
                win32service.CloseServiceHandle(service)
            win32service.CloseServiceHandle(manager)

    def action(self, action: str) -> dict:
        if os.name != "nt":
            raise RuntimeError("Windows service management is only available on Windows")
        if action not in {"start", "stop", "restart"}:
            raise ValueError(f"unsupported service action {action}")
        import pywintypes
        import win32service
        manager = win32service.OpenSCManager(None, None, win32service.SC_MANAGER_CONNECT)
        service = None
        try:
            access = win32service.SERVICE_QUERY_STATUS
            access |= win32service.SERVICE_START if action in {"start", "restart"} else 0
            access |= win32service.SERVICE_STOP if action in {"stop", "restart"} else 0
            try:
                service = win32service.OpenService(manager, SERVICE_NAME, access)
            except pywintypes.error as exc:
                if exc.winerror == 1060:
                    raise FileNotFoundError("MddAgent service is not installed") from exc
                if exc.winerror == 5:
                    raise PermissionError("permission_denied") from exc
                raise

            def current_state():
                return win32service.QueryServiceStatusEx(service)["CurrentState"]

            def wait_for(target, timeout=45):
                deadline = time.monotonic() + timeout
                while current_state() != target:
                    if time.monotonic() >= deadline:
                        raise TimeoutError(f"MddAgent did not reach service state {target}")
                    time.sleep(0.25)

            state = current_state()
            if action in {"stop", "restart"} and state != win32service.SERVICE_STOPPED:
                if state != win32service.SERVICE_STOP_PENDING:
                    try:
                        win32service.ControlService(service, win32service.SERVICE_CONTROL_STOP)
                    except pywintypes.error as exc:
                        if exc.winerror not in {1062}:  # already stopped
                            raise
                wait_for(win32service.SERVICE_STOPPED)
            if action in {"start", "restart"}:
                state = current_state()
                if state == win32service.SERVICE_START_PENDING:
                    wait_for(win32service.SERVICE_RUNNING)
                elif state != win32service.SERVICE_RUNNING:
                    _wait_agent_lease_released()
                    state = current_state()
                    if state == win32service.SERVICE_START_PENDING:
                        wait_for(win32service.SERVICE_RUNNING)
                    elif state != win32service.SERVICE_RUNNING:
                        try:
                            win32service.StartService(service, None)
                        except pywintypes.error as exc:
                            if exc.winerror not in {1056}:  # already running
                                raise
                        wait_for(win32service.SERVICE_RUNNING)
            return self.status()
        except pywintypes.error as exc:
            if exc.winerror == 5:
                raise PermissionError("permission_denied") from exc
            raise RuntimeError(f"Windows service action failed ({exc.winerror}): {exc}") from exc
        finally:
            if service is not None:
                win32service.CloseServiceHandle(service)
            win32service.CloseServiceHandle(manager)

    def install_script(self) -> Path:
        if getattr(sys, "frozen", False):
            base = Path(getattr(sys, "_MEIPASS", Path(self.executable).parent))
        else:
            base = Path(__file__).resolve().parent
        return base / "windows" / "install.ps1"

    def install(self, reader_only: bool = False,
                supervised_legacy_idle_migration: bool = False) -> dict:
        if os.name != "nt":
            raise RuntimeError("Windows service installation is only available on Windows")
        if not is_windows_admin():
            raise PermissionError("elevation_required")
        script = self.install_script()
        command = ["powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy",
                   "Bypass", "-File", str(script), "-Action", "Install",
                   "-BinaryPath", str(Path(self.executable).resolve())]
        if self.data_dir:
            command.extend(["-DataDir", self.data_dir])
        if reader_only:
            command.append("-ReaderOnly")
        if supervised_legacy_idle_migration:
            command.append("-AllowLegacyMaintenancePreflight")
        result = subprocess.run(command, capture_output=True, text=True, timeout=300, check=False)
        if result.returncode:
            raise RuntimeError((result.stderr or result.stdout).strip() or "service install failed")
        return self.status()

    def prepare(self) -> None:
        if os.name != "nt":
            raise RuntimeError("Windows service preparation is only available on Windows")
        if not is_windows_admin():
            raise PermissionError("elevation_required")
        command = ["powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy",
                   "Bypass", "-File", str(self.install_script()), "-Action", "Prepare",
                   "-BinaryPath", str(Path(self.executable).resolve())]
        if self.data_dir:
            command.extend(["-DataDir", self.data_dir])
        result = subprocess.run(command, capture_output=True, text=True, timeout=60, check=False)
        if result.returncode:
            raise RuntimeError((result.stderr or result.stdout).strip() or
                               "service data directory preparation failed")

    def uninstall(self, purge: bool = False) -> dict:
        if os.name != "nt":
            raise RuntimeError("Windows service removal is only available on Windows")
        if not is_windows_admin():
            raise PermissionError("elevation_required")
        script = self.install_script()
        command = ["powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy",
                   "Bypass", "-File", str(script), "-Action", "Uninstall",
                   "-BinaryPath", str(Path(self.executable).resolve())]
        if purge:
            command.append("-PurgeData")
        result = subprocess.run(command, capture_output=True, text=True, timeout=300, check=False)
        if result.returncode:
            raise RuntimeError((result.stderr or result.stdout).strip() or "service removal failed")
        return self.status()
