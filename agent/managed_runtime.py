"""Lifecycle wrapper around the existing unified modem/PCSC runtime."""

from __future__ import annotations

import argparse
import json
import logging
import os
import shutil
import sys
import threading
import time
from pathlib import Path

try:
    from config_store import ConfigError, ConfigStore, validate_config
    from card_agent import get_agent_id
    from modem_agent import RestartBlockedError, acquire_process_lock, run
except ModuleNotFoundError:
    from .config_store import ConfigError, ConfigStore, validate_config
    from .card_agent import get_agent_id
    from .modem_agent import RestartBlockedError, acquire_process_lock, run


log = logging.getLogger("mdd-agent-runtime")


def _args_from_config(config: dict, *, host_mode: str = "") -> argparse.Namespace:
    host, _, port = config["server"].rpartition(":")
    return argparse.Namespace(
        server=config["server"], host=host, gateway_port=int(port),
        port=config["port"], baud=config["baud"], token=config.get("token", ""),
        name=config["name"], path=config["path"], pin=config["pin"], reset_pin=False,
        retry=config["retry"], control_path=config["control_path"],
        agent_id=get_agent_id(), cellular_interface=config["cellular_interface"],
        advertise_host=config["advertise_host"], socks_port=config["socks_port"],
        isolation_helper=config["isolation_helper"], gammu=config["gammu"],
        gammu_port=config["gammu_port"], call_audio_helper=config["call_audio_helper"],
        cellular_io=config["cellular_io"],
        # GUI/CLI hosts request TCC only after this runtime starts. Hardware workers and
        # reconnects remain non-interactive so permission UI cannot block modem discovery.
        allow_audio_permission_prompt=False,
        pcsc_reader=config["pcsc_reader"], no_pcsc=config["no_pcsc"],
    )


class ManagedAgentRuntime:
    """The sole process-local owner of modem and PC/SC hardware."""

    def __init__(self, store: ConfigStore):
        self.store = store
        self._lock = threading.RLock()
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self._state = "stopped"
        self._state_revision = 0
        self._started_at = 0.0
        self._last_error = ""
        self._isolation_error = ""
        self._modem = None
        self._modems = {}
        self._control = None
        self._lease_acquired = False
        self._restart_blocked = False
        self._health_reporter = None

    def _update(self, state: str, **objects) -> None:
        with self._lock:
            self._state = state
            self._state_revision += 1
            self._modem = objects.get("modem", self._modem)
            if "modems" in objects:
                incoming = objects["modems"] or []
                if isinstance(incoming, dict):
                    self._modems = dict(incoming)
                else:
                    self._modems = {
                        self._modem_identity(item): item for item in incoming
                    }
            elif "modem" in objects and objects["modem"] is not None:
                modem = objects["modem"]
                self._modems[self._modem_identity(modem)] = modem
            self._control = objects.get("control", self._control)
            if "isolation_error" in objects:
                self._isolation_error = str(objects.get("isolation_error") or "")[:500]
            reporter = self._health_reporter
        if reporter is not None:
            reporter.notify_changed()

    @staticmethod
    def _reason_code(value: str, fallback: str) -> str:
        text = str(value or "").casefold()
        if not text:
            return ""
        for needle, code in (
                ("isolation_not_proven", "isolation_not_proven"),
                ("permission", "permission_required"),
                ("restart", "restart_required"),
                ("cleanup", "cleanup_blocked"),
                ("configuration", "configuration_invalid"),
                ("token", "token_invalid")):
            if needle in text:
                return code
        return fallback

    def health_snapshot(self) -> dict:
        """Return cached, non-invasive host health; never probe modem/PCSC/audio here."""
        current = self.snapshot()
        runtime = str(current.get("runtime") or "stopped")
        support = "supported" if os.name == "nt" or sys.platform == "darwin" else "unsupported"
        last_error_code = self._reason_code(current.get("last_error") or "", "runtime_failed")
        isolation = dict(current.get("isolation") or {})
        isolation_code = self._reason_code(isolation.get("error") or "", "isolation_unavailable")
        if support == "unsupported":
            overall = "unsupported"
        elif runtime in {"failed", "cleanup_blocked"}:
            overall = "failed"
        elif runtime in {"starting", "stopping", "stopped"}:
            overall = runtime
        elif not isolation.get("ready", True):
            overall = "degraded"
        else:
            overall = "healthy"
        modems = list(current.get("modems") or [])
        try:
            usage = shutil.disk_usage(self.store.root)
            used_percent = int(round((usage.used / usage.total) * 100)) if usage.total else 0
            storage_state = ("critical" if used_percent >= 95 else
                             "warning" if used_percent >= 85 else "ok")
            storage = {"state": storage_state, "used_percent": used_percent,
                       # Quantized so ordinary file churn does not turn every heartbeat into
                       # a full semantic status frame.
                       "free_mb": int(usage.free // (512 * 1024 * 1024) * 512)}
        except OSError:
            storage = {"state": "unknown"}
        return {
            "support": support,
            "overall": overall,
            "runtime": {"state": runtime, "last_error_code": last_error_code},
            "manager": {
                "kind": ("scm" if os.name == "nt" else
                         "gui" if current.get("host_mode") == "gui" else "cli"),
                "host_mode": str(current.get("host_mode") or ""),
                "autostart": bool(current.get("autostart")),
                "session_scope": str(current.get("session_scope") or ""),
            },
            "config": {"state": "ok", "token_configured": True},
            "isolation": {
                "state": ("unsupported" if support == "unsupported" else
                          "ok" if isolation.get("ready", True) else "error"),
                "backend": str(current.get("isolation_backend") or ""),
                "reason_code": isolation_code,
            },
            "inventory": {
                "modems_total": len(modems),
                "modems_connected": sum(bool(item.get("connected")) for item in modems),
            },
            "resources": {"storage": storage},
            "started_at": current.get("started_at"),
        }

    def _start_health_reporter(self, config: dict) -> None:
        if self._health_reporter is not None:
            return
        try:
            try:
                from health_reporter import AgentHealthReporter
            except ModuleNotFoundError:
                from .health_reporter import AgentHealthReporter
            self._health_reporter = AgentHealthReporter(
                config=config, agent_id=get_agent_id(),
                snapshot_provider=self.health_snapshot)
            self._health_reporter.start()
        except Exception:
            self._health_reporter = None
            log.exception("Agent health reporter could not start")

    def _stop_health_reporter(self) -> bool:
        reporter = self._health_reporter
        if reporter is None:
            return True
        stopped = reporter.stop()
        # Health is diagnostic and owns no hardware. It may never retain AgentHost's install
        # lease; a rare stuck daemon is fenced by its stop flag/run_id and the next server
        # session while the actual modem/PCSC runtime is allowed to exit normally.
        if self._health_reporter is reporter:
            self._health_reporter = None
        if not stopped:
            log.warning("Agent health reporter did not exit within its bounded stop window")
        return True

    @staticmethod
    def _modem_identity(modem) -> str:
        for attribute in ("hardware_id", "imei", "port_name"):
            value = str(getattr(modem, attribute, "") or "").strip()
            if value:
                return f"{attribute}:{value}"
        return f"object:{id(modem)}"

    @staticmethod
    def _modem_snapshot(modem) -> dict:
        return {
            "identity": ManagedAgentRuntime._modem_identity(modem),
            "connected": bool(getattr(modem, "connection", None)),
            "imei": str(getattr(modem, "imei", "") or ""),
            "iccid": str(getattr(modem, "iccid", "") or ""),
            "model": str(getattr(modem, "model", "") or ""),
            "port": str(getattr(modem, "port_name", "") or ""),
            "capabilities": dict(getattr(modem, "capabilities", {}) or {}),
        }

    def start(self) -> None:
        with self._lock:
            if self._thread and self._thread.is_alive():
                return
            if self._restart_blocked:
                raise RuntimeError("runtime restart is blocked until the SCM process is replaced")
            self.store.ensure_dirs()
            os.environ["MDD_AGENT_DATA_DIR"] = str(self.store.root)
            config = validate_config(self.store.load(include_secrets=True))
            if not config.get("token"):
                raise ConfigError("Agent token is not configured")
            args = _args_from_config(
                config,
                host_mode=getattr(
                    self, "host_mode", "service" if os.name == "nt" else "cli"),
            )
            if not self._lease_acquired:
                # AgentHost's ownership-checked state-directory lease is the POSIX boundary.
                # Windows has no file lease in AgentHost and retains the named machine mutex.
                if os.name == "nt" and not acquire_process_lock(
                        args.agent_id, installation_scope=True):
                    raise RuntimeError("another installation-scoped MDD Agent runtime is active")
                self._lease_acquired = True
            self._stop = threading.Event()
            self._started_at = time.time()
            self._last_error = ""
            self._isolation_error = ""
            self._update("starting")

            def worker():
                try:
                    run(args, self._stop, self._update)
                except Exception as exc:
                    log.exception("unified Agent runtime failed")
                    with self._lock:
                        self._last_error = str(exc) or type(exc).__name__
                        if isinstance(exc, RestartBlockedError):
                            self._restart_blocked = True
                    self._update("failed")
                finally:
                    if self._state != "failed":
                        self._update("stopped")

            # Cleanup-blocked paid calls intentionally keep the process alive; a daemon owner
            # would let CLI/AppKit exit and abandon the physical call.
            self._thread = threading.Thread(target=worker, name="mdd-agent-runtime", daemon=False)
            self._thread.start()
            self._start_health_reporter(config)

    def stop(self, timeout: float = 300.0) -> bool:
        already_stopped = False
        with self._lock:
            thread = self._thread
            if not thread or not thread.is_alive():
                self._update("stopped")
                already_stopped = True
            else:
                self._update("stopping")
                self._stop.set()
        if already_stopped:
            # The reporter's final snapshot takes this lock. Never join it while holding the
            # runtime lock or a clean no-hardware stop becomes a five-second false failure.
            return self._stop_health_reporter()
        thread.join(timeout)
        if thread.is_alive():
            # Paid-call quarantine intentionally keeps both the runtime and health reporter
            # alive until authoritative terminal evidence arrives.
            return False
        return self._stop_health_reporter()

    def restart(self) -> dict:
        if not self.stop():
            raise RuntimeError("Agent runtime did not stop within the 300-second safety deadline")
        self.start()
        return self.snapshot()

    def snapshot(self) -> dict:
        with self._lock:
            modem = self._modem
            modem_snapshots = [self._modem_snapshot(item) for item in self._modems.values()]
            # Discovery may briefly report the same physical modem through two provider
            # objects (for example Windows WWAN plus its serial control port). Present one
            # physical identity to status/health without changing either worker lifecycle.
            unique_modems = {}
            for item in modem_snapshots:
                identity = str(item.get("identity") or "")
                existing = unique_modems.get(identity)
                score = (
                    int(bool(item.get("connected"))),
                    sum(bool(item.get(key)) for key in ("imei", "iccid", "model", "port")),
                    len(item.get("capabilities") or {}),
                )
                existing_score = (
                    int(bool((existing or {}).get("connected"))),
                    sum(bool((existing or {}).get(key))
                        for key in ("imei", "iccid", "model", "port")),
                    len((existing or {}).get("capabilities") or {}),
                )
                if existing is None or score > existing_score:
                    unique_modems[identity] = item
            modems = list(unique_modems.values())
            if modem is not None and not modems:
                modems = [self._modem_snapshot(modem)]
            compatibility_modem = modems[0] if modems else None
            return {
                "version": 1,
                "state_revision": self._state_revision,
                "runtime": self._state,
                "pid": os.getpid(),
                "started_at": self._started_at or None,
                "uptime_seconds": max(0, int(time.time() - self._started_at)) if self._started_at else 0,
                "last_error": self._last_error or None,
                "agent_id": get_agent_id(),
                "host_mode": getattr(self, "host_mode", "service" if os.name == "nt" else "cli"),
                "autostart": os.name == "nt",
                "session_scope": "machine" if os.name == "nt" else "user",
                "isolation_backend": ("network-guard" if os.name == "nt" else
                                      "private-userspace" if sys.platform == "darwin" else
                                      "platform"),
                "isolation": {
                    "ready": not bool(self._isolation_error),
                    "error": self._isolation_error or None,
                },
                "approval_state": "not_required" if os.name == "nt" else "user_session",
                "modems": modems,
                # Deprecated v1 compatibility view.  New clients must consume modems[].
                "modem": compatibility_modem,
            }

    def devices(self) -> dict:
        snapshot = self.snapshot()
        readers = []
        ports = []
        try:
            from serial.tools import list_ports
            ports = [{"device": item.device, "description": item.description,
                      "hwid": item.hwid} for item in list_ports.comports()]
        except Exception as exc:
            ports = [{"error": str(exc)}]
        try:
            from smartcard.System import readers as pcsc_readers
            readers = [{"name": str(item)} for item in pcsc_readers()]
        except Exception as exc:
            readers = [{"error": str(exc)}]
        return {"version": 1, "state_revision": snapshot["state_revision"],
                "modems": snapshot["modems"], "modem": snapshot["modem"],
                "serial_ports": ports, "pcsc_readers": readers}

    def doctor(self) -> dict:
        checks = []
        try:
            config = self.store.load(include_secrets=True)
            validate_config(config)
            checks.append({"name": "configuration", "ok": bool(config.get("token")),
                           "detail": "valid" if config.get("token") else "token is not configured"})
        except Exception as exc:
            checks.append({"name": "configuration", "ok": False, "detail": str(exc)})
        snapshot = self.snapshot()
        checks.append({"name": "runtime", "ok": snapshot["runtime"] in {"ready", "online"},
                       "detail": snapshot["runtime"]})
        devices = self.devices()
        # A reader-only host is a valid unified Agent deployment.  Discovery is therefore an
        # informational check; provider errors are reported without making the whole service
        # unhealthy solely because no modem is currently inserted.
        discovery_errors = [item["error"] for item in devices["serial_ports"]
                            if isinstance(item, dict) and item.get("error")]
        pcsc_readers = [item for item in devices["pcsc_readers"]
                        if isinstance(item, dict) and item.get("name")]
        checks.append({"name": "device-discovery", "ok": not discovery_errors,
                       "detail": discovery_errors[0] if discovery_errors else
                       f"{len(devices['serial_ports'])} serial port(s), "
                       f"{len(pcsc_readers)} PC/SC reader(s)"})
        return {"version": 1, "healthy": all(item["ok"] for item in checks), "checks": checks,
                "state_revision": snapshot["state_revision"]}

    def logs(self, lines: int = 200) -> dict:
        lines = max(1, min(int(lines), 2000))
        path = self.store.log_dir / "agent.log"
        if not path.exists():
            return {"version": 1, "path": str(path), "lines": []}
        content = path.read_text(encoding="utf-8", errors="replace").splitlines()[-lines:]
        return {"version": 1, "path": str(path), "lines": content}

    def reprobe_audio(self) -> dict:
        """Refresh audio capability only; never restart PPP, modem or PC/SC workers."""
        with self._lock:
            modems = list(self._modems.values())
            if not modems and self._modem is not None:
                modems = [self._modem]
        results = []
        for modem in modems:
            method = getattr(modem, "reprobe_call_audio", None)
            if not method:
                continue
            try:
                result = dict(method() or {})
            except Exception as exc:
                result = {"ready": False, "reason": str(exc) or type(exc).__name__}
            result["identity"] = self._modem_identity(modem)
            results.append(result)
        self._update(self._state)
        return {"version": 1, "ready": any(item.get("ready") for item in results),
                "modems": results, "state_revision": self._state_revision}

    def prepare_install_maintenance(self) -> dict:
        """Atomically fence new paid calls before a Windows service upgrade."""
        with self._lock:
            control = self._control
            state = self._state
        if control is None or state not in {"ready", "online"}:
            return {"version": 1, "ready": False, "status": "runtime_unavailable",
                    "error": f"Agent runtime is {state}; a live modem control is required"}
        method = getattr(control, "prepare_install_maintenance", None)
        if method is None:
            return {"version": 1, "ready": False, "status": "unsupported",
                    "error": "Running Agent does not support atomic install maintenance"}
        return {"version": 1, **dict(method() or {})}

    def cancel_install_maintenance(self, nonce: str) -> dict:
        """Cancel a maintenance fence only before the installer asks SCM to stop."""
        with self._lock:
            control = self._control
        method = getattr(control, "cancel_install_maintenance", None) if control else None
        if method is None:
            return {"version": 1, "cancelled": False, "status": "unsupported"}
        return {"version": 1, **dict(method(nonce) or {})}

    def execute(self, method: str, params: dict, role: str = "admin"):
        if method == "status":
            return self.snapshot()
        if method == "devices":
            return self.devices()
        if method == "doctor":
            return self.doctor()
        if method == "logs":
            return self.logs(params.get("lines", 200))
        if method == "config.show":
            return self.store.show()
        if method == "config.validate":
            candidate = dict(self.store.load(include_secrets=False))
            candidate.update(params.get("changes") or {})
            return {"valid": True, "config": validate_config(candidate)}
        if method == "config.set":
            if role != "admin":
                raise PermissionError("changing Agent configuration requires administrator membership")
            result = self.store.save(params.get("changes") or {})
            return {"config": result, "restart_required": True}
        if method == "reconnect":
            return self.restart()
        if method == "audio.reprobe":
            return self.reprobe_audio()
        if method == "maintenance.prepare-install":
            if role != "admin":
                raise PermissionError("install maintenance requires administrator membership")
            return self.prepare_install_maintenance()
        if method == "maintenance.cancel-install":
            if role != "admin":
                raise PermissionError("install maintenance requires administrator membership")
            return self.cancel_install_maintenance(str(params.get("nonce") or ""))
        if method == "self-test":
            return self.doctor()
        raise ValueError(f"unsupported local control method {method}")


def configure_file_logging(store: ConfigStore) -> None:
    from logging.handlers import RotatingFileHandler
    store.ensure_dirs()
    path = store.log_dir / "agent.log"
    root = logging.getLogger()
    if any(getattr(handler, "baseFilename", None) == str(path) for handler in root.handlers):
        return
    handler = RotatingFileHandler(path, maxBytes=5 * 1024 * 1024, backupCount=5,
                                  encoding="utf-8")
    handler.setFormatter(logging.Formatter(
        "%(asctime)s [%(levelname)s] [%(name)s] %(message)s"))
    root.addHandler(handler)
    root.setLevel(logging.INFO)
