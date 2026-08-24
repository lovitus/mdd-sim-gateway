"""Windows SCM host.  This is the only production entrypoint that owns hardware."""

from __future__ import annotations

import logging
import threading

try:
    from config_store import ConfigStore
    from local_control import ControlServer
    from managed_runtime import ManagedAgentRuntime, configure_file_logging
except ModuleNotFoundError:
    from ..config_store import ConfigStore
    from ..local_control import ControlServer
    from ..managed_runtime import ManagedAgentRuntime, configure_file_logging


SERVICE_NAME = "MddAgent"
SERVICE_DISPLAY_NAME = "MDD Unified Modem and Smart Card Agent"


def _service_log(servicemanager, method: str, message: str) -> None:
    """Keep the SCM runtime independent from the optional Windows Event Log service."""
    try:
        getattr(servicemanager, method)(message)
    except Exception:
        logger = logging.getLogger("mdd-agent-service")
        if method == "LogErrorMsg":
            logger.error("%s", message)
        else:
            logger.info("%s", message)


def run_service_dispatcher() -> None:  # pragma: no cover - Windows integration
    try:
        import servicemanager
        import win32event
        import win32service
        import win32serviceutil
    except ImportError as exc:
        raise RuntimeError("pywin32 service support is not installed") from exc

    class MddAgentService(win32serviceutil.ServiceFramework):
        _svc_name_ = SERVICE_NAME
        _svc_display_name_ = SERVICE_DISPLAY_NAME
        _svc_description_ = ("Owns local 4G/5G modems and PC/SC readers and connects them "
                             "to the configured MDD gateway.")

        def __init__(self, args):
            super().__init__(args)
            self.stop_handle = win32event.CreateEvent(None, 0, 0, None)
            self.runtime = None
            self.control = None

        def SvcStop(self):
            self.ReportServiceStatus(win32service.SERVICE_STOP_PENDING, waitHint=30000)
            win32event.SetEvent(self.stop_handle)

        def SvcShutdown(self):
            self.SvcStop()

        def SvcDoRun(self):
            store = ConfigStore()
            configure_file_logging(store)
            _service_log(servicemanager, "LogInfoMsg", "MDD Agent service is starting")
            self.runtime = ManagedAgentRuntime(store)
            self.control = ControlServer(self.runtime, store.root)
            try:
                self.runtime.start()
                self.control.start()
                self.ReportServiceStatus(win32service.SERVICE_RUNNING)
                win32event.WaitForSingleObject(self.stop_handle, win32event.INFINITE)
            except Exception as exc:
                _service_log(servicemanager, "LogErrorMsg", f"MDD Agent service failed: {exc}")
                raise
            finally:
                if self.control:
                    self.control.stop()
                if self.runtime and not self.runtime.stop(30):
                    _service_log(servicemanager, "LogErrorMsg",
                                 "MDD Agent runtime exceeded its stop deadline")
                _service_log(servicemanager, "LogInfoMsg", "MDD Agent service stopped")

    servicemanager.Initialize()
    servicemanager.PrepareToHostSingle(MddAgentService)
    servicemanager.StartServiceCtrlDispatcher()
