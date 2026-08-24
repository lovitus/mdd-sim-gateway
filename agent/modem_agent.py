#!/usr/bin/env python3
"""Generic remote 3GPP modem agent.

The agent discovers a standards-based AT port and publishes only capabilities that the modem
actually supports. Vendor/model quirks belong in optional backends; ICCID remains the stable SIM
identity and no host name, COM port, network-interface name or gateway address is hard-coded.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import errno
import hashlib
import json
import logging
import os
import platform
import re
import secrets
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import uuid
from xml.sax.saxutils import escape

import serial
from serial.tools import list_ports
import websocket

try:
    from card_agent import (
        VPCD_CTRL_ATR, VPCD_CTRL_OFF, VPCD_CTRL_ON, VPCD_CTRL_RESET,
        agent_ws_path, connect_wss, get_agent_id, is_forbidden_apdu, load_pin_store,
        run_pcsc_reader_supervisor, verify_or_pin_fingerprint,
    )
    from embedded_socks import SocksServer, _encoded_address, _packet_address
    from cellular_isolation import IsolationGuard
    from call_audio import (CallAudioController, CallAudioProbe,
                            prepare_call_audio_usb, probe_call_audio)
    from apn_providers import lookup_by_imsi
    from config_store import default_data_dir
    from uicc_health import UiccHealthMaintainer
    from voice_registration import VoiceRegistrationMaintainer
    from cellular_backend import PrivateCellularBackend
    from device_supervisor import MacUsbModemDiscovery, cellular_io_command
    from macos.isolation import MacHostIsolationMonitor
    import sms_history
    from modem_providers import (
        AuxiliaryAtProvider, CompositeModemProvider, GammuCliProvider, WindowsMbnProvider,
        parse_clcc_voice, verified_at_hangup,
    )
except ModuleNotFoundError:  # Imported as agent.modem_agent by tests and packaging.
    from .card_agent import (
        VPCD_CTRL_ATR, VPCD_CTRL_OFF, VPCD_CTRL_ON, VPCD_CTRL_RESET,
        agent_ws_path, connect_wss, get_agent_id, is_forbidden_apdu, load_pin_store,
        run_pcsc_reader_supervisor, verify_or_pin_fingerprint,
    )
    from .embedded_socks import SocksServer, _encoded_address, _packet_address
    from .cellular_isolation import IsolationGuard
    from .call_audio import (CallAudioController, CallAudioProbe,
                             prepare_call_audio_usb, probe_call_audio)
    from .apn_providers import lookup_by_imsi
    from .config_store import default_data_dir
    from .uicc_health import UiccHealthMaintainer
    from .voice_registration import VoiceRegistrationMaintainer
    from .cellular_backend import PrivateCellularBackend
    from .device_supervisor import MacUsbModemDiscovery, cellular_io_command
    from .macos.isolation import MacHostIsolationMonitor
    from . import sms_history
    from .modem_providers import (
        AuxiliaryAtProvider, CompositeModemProvider, GammuCliProvider, WindowsMbnProvider,
        parse_clcc_voice, verified_at_hangup,
    )


_FROZEN_PACKAGE_DIGEST_LOCK = threading.Lock()
_frozen_package_digest_cache: str | None = None


ATR = bytes.fromhex("3B9F95801FC78031E073FE211B66D0017797020C000B")
CSIM_RE = re.compile(rb'\+CSIM:\s*(\d+)\s*,\s*"([0-9A-Fa-f]*)"')
ICCID_RE = re.compile(rb'(?<!\d)(89\d{17,20})(?!\d)')
IMEI_RE = re.compile(rb'(?<!\d)(\d{15})(?!\d)')
IMSI_RE = re.compile(rb'(?<!\d)(\d{14,15})(?!\d)')


def _is_local_vpcd_close_error(exc: BaseException) -> bool:
    """Return true only for local socket close failures caused by a deliberate pause."""
    if isinstance(exc, OSError) and getattr(exc, "errno", None) in {
            errno.EBADF, errno.ENOTCONN, errno.ECONNABORTED, errno.ECONNRESET}:
        return True
    text = str(exc or "").casefold()
    return ("bad file descriptor" in text or "closed socket" in text or
            "socket is closed" in text)


def _expected_private_data_vpcd_pause(modem, control, attempt_marker, exc=None) -> bool:
    """Classify a current VPCD client close that was requested by private cellular data."""
    if not getattr(modem, "private_raw_usb", False):
        return False
    if attempt_marker is None or not attempt_marker.is_set():
        return False
    cellular_active = getattr(control, "cellular_active", None)
    if cellular_active is None or not cellular_active.is_set():
        return False
    paid_call_active = getattr(control, "paid_call_active", None)
    if paid_call_active is None or paid_call_active.is_set():
        return False
    safety_hold = getattr(control, "_paid_call_safety_hold", None)
    if safety_hold is None or safety_hold():
        return False
    if exc is not None and not _is_local_vpcd_close_error(exc):
        return False
    return True


def _paid_call_armed(control) -> bool:
    armed = getattr(control, "_paid_call_armed", None)
    return bool(armed and armed())


def _data_owner_holds_sim_channel(modem, control, *, include_mbn: bool = False,
                                  allow_bootstrap_connect: bool = False) -> bool:
    if getattr(modem, "private_raw_usb", False):
        guard = getattr(control, "_private_data_sim_guard_active", None)
        if callable(guard):
            try:
                return bool(guard(allow_bootstrap_connect=allow_bootstrap_connect))
            except TypeError:
                return bool(guard())
        cellular_active = getattr(control, "cellular_active", None)
        return bool(cellular_active is not None and cellular_active.is_set())
    if not (include_mbn and getattr(modem, "sim_via_mbn", False)):
        return False
    cellular_active = getattr(control, "cellular_active", None)
    return bool(cellular_active is not None and cellular_active.is_set())


def _wait_data_owner_release(modem, control, stopped, *, include_mbn: bool = False,
                             context: str = "modem I/O",
                             allow_bootstrap_connect: bool = False) -> bool:
    """Wait while cellular data owns the SIM lane, but never delay paid-call cleanup."""
    if (not _data_owner_holds_sim_channel(
                modem, control, include_mbn=include_mbn,
                allow_bootstrap_connect=allow_bootstrap_connect) or
            _paid_call_armed(control)):
        return False
    log.info("Cellular data owns this modem SIM channel; %s is paused", context)
    while (_data_owner_holds_sim_channel(
                modem, control, include_mbn=include_mbn,
                allow_bootstrap_connect=allow_bootstrap_connect) and
           not _paid_call_armed(control) and
           not getattr(control, "stop").is_set() and not stopped.is_set()):
        stopped.wait(1)
    return True

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] [modem-agent] %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("mdd-modem-agent")
_PROCESS_LOCK = None
UNIFIED_WINDOWS_MUTEX = r"Global\MDDUnifiedAgent-v1"
CALL_CONTRACT_VERSION = 2
_SHA256_RE = re.compile(r"^(?:sha256:)?([a-fA-F0-9]{64})$")


def _normalise_sha256(value: object) -> str:
    match = _SHA256_RE.match(str(value or "").strip())
    return match.group(1).lower() if match else ""


def _manifest_package_root(manifest_path: str) -> str:
    path = os.path.abspath(manifest_path)
    return os.path.dirname(path)


def _safe_manifest_name(value: object) -> str:
    if type(value) is not str:
        return ""
    raw = value.replace("\\", "/")
    if not raw or raw.startswith("/") or raw.startswith("../") or "/../" in raw:
        return ""
    normal = os.path.normpath(raw).replace("\\", "/")
    if normal in {"", "."} or normal.startswith("../") or normal.startswith("/"):
        return ""
    return normal


def _safe_manifest_symlink_target(root: str, relative: str, target: object) -> str:
    if type(target) is not str:
        return ""
    raw = target.replace("\\", "/")
    if not raw or raw.startswith("/") or "\x00" in raw:
        return ""
    normal = os.path.normpath(raw).replace("\\", "/")
    if normal in {"", "."} or normal.startswith("/"):
        return ""
    try:
        root_real = os.path.realpath(root)
        candidate = os.path.realpath(os.path.join(root, os.path.dirname(relative), normal))
    except (OSError, ValueError):
        return ""
    if not os.path.exists(candidate):
        return ""
    if candidate == root_real or candidate.startswith(root_real + os.sep):
        return target
    return ""


def _verified_package_manifest_digest(manifest_path: str) -> str:
    try:
        if os.path.islink(manifest_path):
            return ""
        with open(manifest_path, "rb") as handle:
            raw_manifest = handle.read()
        manifest = json.loads(raw_manifest.decode("utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError):
        return ""
    if (not isinstance(manifest, dict) or
            type(manifest.get("version")) is not int or
            manifest.get("version") not in (1, 2)):
        return ""
    version = manifest["version"]
    if version == 1:
        if set(manifest) != {"version", "architecture", "files"}:
            return ""
        architecture = manifest.get("architecture")
        if type(architecture) is not str or not architecture:
            return ""
        if sys.platform in {"darwin", "win32"}:
            machine = str(platform.machine() or "").strip().casefold()
            pointer_bits = struct.calcsize("P") * 8
            if sys.platform == "darwin":
                runtime_architecture = (
                    "macos-arm64" if pointer_bits == 64 and
                    machine in {"arm64", "aarch64"} else "")
            else:
                runtime_architecture = (
                    "windows-amd64" if pointer_bits == 64 and
                    machine in {"amd64", "x86_64"} else "")
            if not runtime_architecture or architecture != runtime_architecture:
                return ""
    entries = manifest.get("files")
    if not isinstance(entries, list) or not entries:
        return ""
    root = _manifest_package_root(manifest_path)
    if os.path.islink(root):
        return ""
    expected: dict[str, dict] = {}
    for entry in entries:
        if not isinstance(entry, dict):
            return ""
        if version == 1 and set(entry) != {"name", "size", "sha256"}:
            return ""
        name = _safe_manifest_name(entry.get("name"))
        entry_type = entry.get("type", "file") if version >= 2 else "file"
        if entry_type not in {"file", "symlink"}:
            return ""
        if not name or name in expected:
            return ""
        if entry_type == "file":
            if type(entry.get("sha256")) is not str:
                return ""
            size = entry.get("size")
            if type(size) is not int:
                return ""
            digest = _normalise_sha256(entry.get("sha256"))
            if not digest or size < 0:
                return ""
            expected[name] = {"type": "file", "sha256": digest, "size": size}
        else:
            target = _safe_manifest_symlink_target(root, name, entry.get("target"))
            if not target:
                return ""
            expected[name] = {"type": "symlink", "target": target}

    observed: set[str] = set()
    metadata_paths = {
        os.path.abspath(os.path.join(root, "control-agent-allowlist.env")),
        os.path.abspath(manifest_path),
    }
    for dirpath, dirnames, filenames in os.walk(root, topdown=True, followlinks=False):
        for dirname in list(dirnames):
            path = os.path.join(dirpath, dirname)
            relative = os.path.relpath(path, root).replace("\\", "/")
            if dirname in {"manifest.json", "control-agent-allowlist.env"}:
                return ""
            if os.path.islink(path):
                target = os.readlink(path)
                if not _safe_manifest_symlink_target(root, relative, target):
                    return ""
                expected_entry = expected.get(relative)
                if (not expected_entry or expected_entry.get("type") != "symlink" or
                        expected_entry.get("target") != target):
                    return ""
                observed.add(relative)
                dirnames.remove(dirname)
        for filename in filenames:
            path = os.path.join(dirpath, filename)
            if os.path.abspath(path) in metadata_paths:
                continue
            relative = os.path.relpath(path, root).replace("\\", "/")
            if filename in {"manifest.json", "control-agent-allowlist.env"}:
                return ""
            if os.path.islink(path):
                target = os.readlink(path)
                if not _safe_manifest_symlink_target(root, relative, target):
                    return ""
                expected_entry = expected.get(relative)
                if (not expected_entry or expected_entry.get("type") != "symlink" or
                        expected_entry.get("target") != target):
                    return ""
            else:
                expected_entry = expected.get(relative)
                if not expected_entry or expected_entry.get("type") != "file":
                    return ""
            if relative not in expected:
                return ""
            observed.add(relative)
    if observed != set(expected):
        return ""

    for relative, expected_entry in expected.items():
        if expected_entry.get("type") != "file":
            continue
        path = os.path.join(root, *relative.split("/"))
        try:
            if os.path.islink(path):
                return ""
            if os.path.getsize(path) != expected_entry["size"]:
                return ""
            digest = hashlib.sha256()
            with open(path, "rb") as handle:
                for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                    digest.update(chunk)
            if digest.hexdigest() != expected_entry["sha256"]:
                return ""
        except OSError:
            return ""
    return hashlib.sha256(raw_manifest).hexdigest()


def _agent_package_version() -> str:
    for path in (
            os.environ.get("MDD_AGENT_VERSION_FILE", ""),
            os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "VERSION"),
            os.path.join(os.path.dirname(os.path.abspath(__file__)), "VERSION"),
    ):
        if not path:
            continue
        try:
            value = open(path, encoding="utf-8").read().strip()
            if value:
                return value[:40]
        except OSError:
            continue
    return str(os.environ.get("MDD_AGENT_VERSION") or "unknown")[:40]


def _agent_package_digest() -> str:
    if bool(getattr(sys, "frozen", False)):
        return _installed_runtime_package_digest()
    explicit = _normalise_sha256(os.environ.get("MDD_AGENT_PACKAGE_DIGEST"))
    if explicit:
        return explicit
    base_dir = os.path.dirname(os.path.abspath(__file__))
    exe_dir = os.path.dirname(os.path.abspath(sys.executable))
    meipass_dir = str(getattr(sys, "_MEIPASS", "") or "")
    candidates = [
        os.environ.get("MDD_AGENT_MANIFEST_FILE", ""),
        os.environ.get("MDD_AGENT_PACKAGE_MANIFEST", ""),
        os.path.join(meipass_dir, "manifest.json") if meipass_dir else "",
        os.path.join(exe_dir, "manifest.json"),
        os.path.abspath(os.path.join(exe_dir, "..", "..", "..", "manifest.json")),
        os.path.join(exe_dir, "_internal", "manifest.json"),
        os.path.join(base_dir, "manifest.json"),
        os.path.join(os.path.dirname(base_dir), "manifest.json"),
    ]
    for path in candidates:
        if not path:
            continue
        digest = _verified_package_manifest_digest(path)
        if digest:
            return digest
    return "unknown"


def _installed_runtime_package_digest() -> str:
    """Verify only the external package manifest of a frozen installed runtime.

    Installer health must not be satisfied by an environment override or PyInstaller's
    internal extraction tree. Development/source runs retain the general discovery helper.
    """
    if not bool(getattr(sys, "frozen", False)):
        return _agent_package_digest()
    global _frozen_package_digest_cache
    with _FROZEN_PACKAGE_DIGEST_LOCK:
        if _frozen_package_digest_cache is not None:
            return _frozen_package_digest_cache
        executable_dir = os.path.dirname(os.path.abspath(sys.executable))
        if (sys.platform == "darwin" and
                os.path.basename(executable_dir) == "MacOS" and
                os.path.basename(os.path.dirname(executable_dir)) == "Contents"):
            candidates = [os.path.abspath(os.path.join(
                executable_dir, "..", "..", "..", "manifest.json"))]
        else:
            candidates = [os.path.join(executable_dir, "manifest.json")]
        for path in candidates:
            digest = _verified_package_manifest_digest(path)
            if digest:
                _frozen_package_digest_cache = digest
                break
        else:
            _frozen_package_digest_cache = "unknown"
        return _frozen_package_digest_cache


class RestartBlockedError(RuntimeError):
    """A child hardware owner survived stop; only SCM process replacement is safe."""


def acquire_process_lock(agent_id: str, *, installation_scope: bool = False) -> bool:
    """Acquire one host-local Agent lease without requiring administrator privileges."""
    global _PROCESS_LOCK
    identity_source = "installation" if installation_scope else str(agent_id or "default")
    identity = hashlib.sha256(identity_source.encode("utf-8")).hexdigest()[:24]
    if os.name == "nt":
        import ctypes
        kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel32.CreateMutexW.argtypes = [ctypes.c_void_p, ctypes.c_bool,
                                          ctypes.c_wchar_p]
        kernel32.CreateMutexW.restype = ctypes.c_void_p
        kernel32.CloseHandle.argtypes = [ctypes.c_void_p]
        kernel32.CloseHandle.restype = ctypes.c_bool
        mutex_name = UNIFIED_WINDOWS_MUTEX if installation_scope else f"Global\\MDDModemAgent-{identity}"
        handle = kernel32.CreateMutexW(None, False, mutex_name)
        if not handle:
            raise OSError(ctypes.get_last_error(), "CreateMutexW failed")
        if ctypes.get_last_error() == 183:  # ERROR_ALREADY_EXISTS
            kernel32.CloseHandle(handle)
            return False
        _PROCESS_LOCK = (kernel32, handle)
        return True

    import fcntl
    path = os.path.join(tempfile.gettempdir(), f"mdd-modem-agent-{identity}.lock")
    lock_file = open(path, "a+b")  # noqa: SIM115 -- retained for the process lifetime.
    try:
        fcntl.flock(lock_file.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        lock_file.close()
        return False
    _PROCESS_LOCK = lock_file
    return True


def windows_mbn_profile_xml(name: str, subscriber_id: str, apn: str,
                            auth: str = "NONE", username: str = "",
                            password: str = "") -> str:
    """Build a Windows MBN v1 profile without the application-owned SimIccID field."""
    auth = str(auth or "NONE").upper()
    if auth not in {"NONE", "PAP", "CHAP", "MSCHAPV2"}:
        raise ModemError("unsupported mobile-broadband authentication method")
    if not re.fullmatch(r"\d{14,15}", str(subscriber_id or "")):
        raise ModemError("SIM IMSI is required to create a Windows mobile-broadband profile")
    if not str(name or "").strip() or not str(apn or "").strip():
        raise ModemError("profile name and APN are required")
    windows_auth = "MsCHAPv2" if auth == "MSCHAPV2" else auth
    credentials = ""
    if username or password:
        credentials = ("<UserLogonCred><UserName>" + escape(username) +
                       "</UserName><Password>" + escape(password) +
                       "</Password></UserLogonCred>")
    return ("<?xml version=\"1.0\"?>"
            "<MBNProfile xmlns=\"http://www.microsoft.com/networking/WWAN/profile/v1\">"
            f"<Name>{escape(name.strip())}</Name><IsDefault>true</IsDefault>"
            "<ProfileCreationType>UserProvisioned</ProfileCreationType>"
            f"<SubscriberID>{subscriber_id}</SubscriberID>"
            "<AutoConnectOnInternet>false</AutoConnectOnInternet>"
            "<ConnectionMode>manual</ConnectionMode><Context>"
            f"<AccessString>{escape(apn.strip())}</AccessString>{credentials}"
            f"<Compression>DISABLE</Compression><AuthProtocol>{windows_auth}</AuthProtocol>"
            "</Context></MBNProfile>")


class ModemError(RuntimeError):
    pass


class AgentShuttingDownError(ModemError):
    """A paid action reached its commit point after the shutdown gate closed."""


class ModemCard:
    def __init__(self, port: str, baud: int, timeout: float = 10.0, platform_provider=None,
                 gammu: str = "", gammu_port: str = "", call_audio_helper: str = "",
                 allow_audio_permission_prompt: bool = False):
        self.requested_port = port
        self.port_name = port
        self.baud = baud
        self.timeout = timeout
        self.serial = None
        self.iccid = ""
        self.imei = ""
        self.imsi = ""
        self.msisdn = ""
        self.operator = ""
        self.model = ""
        # The marketing model name is shared by incompatible hardware branches, so the exact
        # revision string is the only value a firmware compatibility check may key on.
        self.firmware = ""
        self.capabilities = {"sms": False, "call": False, "call_audio": False,
                             "cellular_data": False}
        # Set only by a transport that owns the complete raw-USB modem session. Generic
        # serial attachments must not acquire the private backend's recovery authority.
        self.private_raw_usb = False
        self.sim_via_mbn = False
        self.platform_provider = platform_provider
        self.gammu = str(gammu or "")
        self.gammu_port = str(gammu_port or "")
        self.call_audio_helper = str(call_audio_helper or "")
        self.allow_audio_permission_prompt = bool(allow_audio_permission_prompt)
        self.call_audio_probe = CallAudioProbe(reason="call audio has not been probed")
        self.call_audio_controller = None
        self.voice_registration = VoiceRegistrationMaintainer(
            state_path=default_data_dir() / "state" / "voice-registration.json")
        self.uicc_health = UiccHealthMaintainer(
            state_path=default_data_dir() / "state" / "uicc-health.json")
        self._reenumeration_pending = False
        self._sim_apdu_failures = 0
        # The control thread must not publish a partially initialized capability snapshot.
        # connect() sets this only after identity, provider and all non-billable self-tests end.
        self.registration_ready = threading.Event()
        self.lock = threading.RLock()
        self._sms_readiness_cache = (0.0, {"ready": None, "reason": "not checked"})
        self._smsc_cache = (0.0, "")

    @property
    def connection(self):
        return self.serial

    @staticmethod
    def _cnum_number(raw: bytes | str) -> str:
        text = raw.decode("ascii", "replace") if isinstance(raw, bytes) else str(raw or "")
        match = re.search(r'\+CNUM:\s*[^\r\n]*?"(\+?\d{5,20})"', text)
        return match.group(1) if match else ""

    @staticmethod
    def _windows_single_cached_iccid() -> str:
        """Return one unambiguous Windows subscription identity for upgrade migration.

        Older Agent builds did not persist the last successful modem/SIM association. Windows
        keeps subscription keys after a successful MBN attachment, but can retain several SIMs;
        therefore this evidence is usable only when exactly one syntactically valid ICCID exists.
        It authorizes one bounded UICC reset and is never published as the current SIM identity.
        """
        if os.name != "nt":
            return ""
        try:
            import winreg
            root = winreg.OpenKey(
                winreg.HKEY_LOCAL_MACHINE,
                r"SOFTWARE\Microsoft\WcmSvc\SubscriptionManager")
            values = set()
            index = 0
            while True:
                try:
                    name = str(winreg.EnumKey(root, index) or "")
                except OSError:
                    break
                digits = "".join(character for character in name if character.isdigit())
                if name == digits and 18 <= len(digits) <= 22:
                    values.add(digits)
                index += 1
            try:
                winreg.CloseKey(root)
            except Exception:
                pass
            return next(iter(values)) if len(values) == 1 else ""
        except (ImportError, OSError):
            return ""

    def _pre_identity_uicc_maintenance(self) -> None:
        """Run one persisted UICC recovery before CCID is available.

        Windows MBN and the private raw-USB provider both own a complete modem attachment.
        They may finish a previously interrupted CFUN transition, and may reinitialize a SIM
        only when this exact IMEI already has a successfully observed ICCID in the local
        health store. A new/unknown SIM never authorizes an automatic radio reset.
        """
        if self.platform_provider is not None or not (
                os.name == "nt" or self.private_raw_usb):
            return
        bootstrap = self.uicc_health.ensure_full_function(self._at, self.imei)
        if bootstrap.action:
            log.warning("UICC bootstrap: action=%s state=%s reason=%s",
                        bootstrap.action, bootstrap.state, bootstrap.reason)
        if bootstrap.state == "restarting":
            self._reenumeration_pending = True
            raise ModemError("modem is restoring full-function mode")

        known_iccid = self.uicc_health.known_iccid(self.imei)
        if not known_iccid and os.name == "nt":
            known_iccid = self._windows_single_cached_iccid()
        if not known_iccid:
            return
        self.uicc_health.set_context(self.imei, known_iccid)
        early_uicc = self.uicc_health.check(self._at, force=True, allow_repair=True)
        emit = log.warning if early_uicc.action or early_uicc.ready is False else log.info
        emit("Early UICC check: action=%s state=%s reason=%s diagnostics=%s",
             early_uicc.action, early_uicc.state, early_uicc.reason,
             early_uicc.diagnostics)
        if early_uicc.state == "recovered" and os.name == "nt":
            # Windows MBN must refresh its OS-owned subscription object after a radio cycle.
            # The private raw-USB provider still owns its transport and may read CCID now.
            self._reenumeration_pending = True
            raise ModemError("UICC recovered; waiting for Windows MBN re-enumeration")

    def _at(self, command: str, timeout: float | None = None) -> bytes:
        with self.lock:
            if not self.serial:
                raise ModemError("modem is not connected")
            budget = self.timeout if timeout is None else max(0.1, min(self.timeout, timeout))
            if self.private_raw_usb and getattr(self, "backend", None):
                return self.backend.at(command, timeout=budget).encode("utf-8")
            self.serial.reset_input_buffer()
            self.serial.write((command + "\r").encode("ascii"))
            self.serial.flush()
            return self._read_result(command, timeout=budget)

    def _read_result(self, command: str, timeout: float | None = None) -> bytes:
        buffer = bytearray()
        deadline = time.monotonic() + (self.timeout if timeout is None else timeout)
        while time.monotonic() < deadline:
            chunk = self.serial.read(1024)
            if chunk:
                buffer.extend(chunk)
                lines = bytes(buffer).replace(b"\r", b"\n").splitlines()
                if any(line.strip() == b"OK" for line in lines):
                    return bytes(buffer)
                if any(line.strip() == b"ERROR" or line.strip().startswith(
                        (b"+CME ERROR:", b"+CMS ERROR:")) for line in lines):
                    detail = bytes(buffer).decode("ascii", "replace").strip()
                    raise ModemError(f"{command}: {detail}")
        raise ModemError(f"timeout waiting for {command}: {bytes(buffer[-200:])!r}")

    def sms_send(self, recipient: str, body: str) -> dict:
        uicc = self.uicc_health_status(force=True, allow_repair=True)
        if uicc.get("ready") is False:
            raise ModemError(str(uicc.get("reason") or "SIM is unavailable"))
        platform_status = (self.platform_provider.status(refresh=True)
                           if self.platform_provider else {})
        authoritative = bool(platform_status.get("sms_readiness_authoritative"))
        if authoritative:
            if not platform_status.get("sms_ready"):
                raise ModemError(str(platform_status.get("sms_error") or
                                     "Platform SMS service is not ready"))
        else:
            readiness = self.sms_submit_readiness(force=True)
            if readiness.get("ready") is False:
                raise ModemError(str(readiness.get("reason") or "SMS bearer is unavailable"))
        # A modem can be registered for LTE/packet data while its legacy circuit-switched
        # domain is unavailable (common while roaming).  3GPP TS 27.007 defines CGSMS=2 as
        # packet-domain preferred with circuit-switched fallback.  Select it only when the
        # registration state proves that this is necessary; do not key this behaviour to a
        # modem model, operating system, carrier or country.
        if not authoritative:
            try:
                self._prepare_sms_bearer()
            except Exception as exc:
                # Some modems do not implement CGSMS and can still send SMS through their own
                # default/IMS path. Preparation must not turn that existing path into a failure.
                log.warning("Could not prepare the preferred SMS bearer: %s", exc)
        if self.platform_provider:
            try:
                result = self.platform_provider.sms_send(recipient, body)
                if result.get("ok") and self.iccid:
                    # Record what this successful submission actually used, not a status value
                    # cached before a SIM/network change.
                    sms_history.record(self.iccid, self.service_centre(force=True))
                return result
            except Exception as exc:
                raise ModemError(self._submit_failure_detail(exc)) from exc
        if not re.fullmatch(r"\+?\d{1,32}", recipient):
            raise ModemError("invalid SMS recipient")
        try:
            result = self._sms_submit_at(recipient, body)
            if result.get("ok") and self.iccid:
                sms_history.record(self.iccid, self.service_centre(force=True))
            return result
        except ModemError as exc:
            raise ModemError(self._submit_failure_detail(exc)) from exc

    def _submit_failure_detail(self, exc: Exception) -> str:
        """Attach the submit preconditions an SMS failure never reports on its own.

        A rejected submit is reported by the network as an unspecified error, which is
        indistinguishable from a gateway defect.  Recording the SMS centre and registration
        that were in effect makes the difference visible without a second billable attempt.
        """
        detail = str(exc).strip() or exc.__class__.__name__
        try:
            centre = self.service_centre(force=True)
        except Exception:
            log.debug("service_centre() failed in SMS failure context", exc_info=True)
            centre = ""
        context = [f"SMS centre {centre}" if centre else
                   "no SMS centre was readable through MBN or AT+CSCA"]
        readiness = dict(self._sms_readiness_cache[1] or {})
        bearer = str(readiness.get("bearer") or "")
        if bearer:
            context.append(f"bearer {bearer}")
        if readiness.get("cs") is not None:
            context.append(f"CREG {readiness['cs']}")
        return f"{detail} (at submit time: {', '.join(context)})"[:400]

    def _sms_submit_at(self, recipient: str, body: str) -> dict:
        with self.lock:
            self._at("AT+CMGF=1")
            unicode_text = any(ord(char) > 127 for char in body)
            if unicode_text:
                self._at('AT+CSCS="UCS2"')
                wire_recipient = recipient.encode("utf-16-be").hex().upper()
                wire_body = body.encode("utf-16-be").hex().upper().encode("ascii")
            else:
                self._at('AT+CSCS="GSM"')
                wire_recipient, wire_body = recipient, body.encode("ascii")
            self.serial.reset_input_buffer()
            self.serial.write(f'AT+CMGS="{wire_recipient}"\r'.encode("ascii"))
            self.serial.flush()
            prompt = bytearray()
            deadline = time.monotonic() + 8
            while time.monotonic() < deadline and b">" not in prompt:
                prompt.extend(self.serial.read(256))
            if b">" not in prompt:
                raise ModemError("SMS modem did not return a submit prompt")
            self.serial.write(wire_body + b"\x1a")
            self.serial.flush()
            raw = self._read_result("AT+CMGS")
        match = re.search(rb"\+CMGS:\s*(\d+)", raw)
        return {"ok": True, "status": "sent", "reference": int(match.group(1)) if match else None,
                "audio": False}

    def smsc_changed(self) -> bool:
        """Return True when the current SMSC differs from the last successful value.

        This is the only way to distinguish a missing SMSC from a changed/wrong one without
        another billable attempt.  A fresh change is advisory: it does not block the operator
        from trying, because the operator or the network may have updated the centre.
        """
        return sms_history.changed(self.iccid, self.service_centre())

    def service_centre(self, *, force: bool = False) -> str:
        """Read the SMS centre address the modem will submit through.

        This is observational and read-only: ``AT+CSCA?`` is answered from EF_SMSP, and the
        value is never written back. A wrong or absent centre makes ``AT+CMGS`` fail with an
        unspecified network error, so the address must appear in status and in failure
        reports instead of leaving the operator to guess.  Absence alone is not treated as
        proof of failure, because some modems keep the address below the AT interface.
        """
        checked_at, cached = self._smsc_cache
        now = time.monotonic()
        if not force and checked_at and now - checked_at < 60:
            return cached
        value = ""
        if self.platform_provider:
            value = str(self.platform_provider.status().get("sms_service_center") or "")
            if not value:
                # Verified on real hardware: the heartbeat status path leaves MBN's SMS
                # getters unsubscribed, so they answer E_PENDING and the address looks
                # absent. An empty platform field is therefore missing information, not an
                # absent centre; ask the subscribed reader before concluding anything.
                reader = getattr(self.platform_provider, "sms_configuration", None)
                if reader:
                    try:
                        config = reader()
                        if isinstance(config, dict):
                            value = str(config.get("service_center") or "")
                    except Exception as exc:
                        log.debug("MBN SMS configuration unavailable: %s", exc)
        if not value:
            try:
                raw = self._at("AT+CSCA?").decode("ascii", "replace")
                match = re.search(r'\+CSCA:\s*"([^"]*)"', raw)
                value = (match.group(1).strip() if match else "")
            except Exception:
                value = ""
        self._smsc_cache = (now, value)
        return value

    def sms_submit_readiness(self, *, force: bool = False) -> dict:
        """Report only authoritative SMS bearer failures; unknown remains usable.

        The signalling provider being installed proves that SMS can be read and submitted to
        the modem, not that the serving network currently offers an MO-SMS bearer.  In an
        LTE-only attachment, no CS registration plus an explicitly unavailable IMS session is
        a definitive precondition failure.  Cache the read-only AT probe so status heartbeats
        do not turn into a command flood; transient registration/searching remains ``None``
        rather than a false failure and is checked again later.
        """
        checked_at, cached = self._sms_readiness_cache
        now = time.monotonic()
        if not force and checked_at and now - checked_at < 60:
            return dict(cached)
        try:
            cs = self._registration_state(self._at("AT+CREG?"), "CREG")
            packet = []
            for command, name in (("AT+CGREG?", "CGREG"), ("AT+CEREG?", "CEREG")):
                try:
                    packet.append(self._registration_state(self._at(command), name))
                except Exception:
                    pass
            registered = {1, 5}
            if cs in registered:
                result = {"ready": True, "bearer": "cs", "cs": cs, "packet": packet}
            elif not any(value in registered for value in packet):
                # Searching, radio-off and reconnect transitions are not proof that a
                # configured bearer is permanently unavailable.
                result = {"ready": None, "reason": "mobile network registration is pending",
                          "cs": cs, "packet": packet}
            else:
                try:
                    raw = self._at('AT+QCFG="ims"')
                except Exception:
                    # QCFG is vendor-specific.  Other modems may expose IMS differently, so
                    # absence of this probe must not disable their existing send path.
                    result = {"ready": None, "reason": "IMS state is not exposed by this modem",
                              "cs": cs, "packet": packet}
                else:
                    match = re.search(rb'\+QCFG:\s*"ims"\s*,\s*(\d+)\s*,\s*(\d+)', raw)
                    ims = int(match.group(2)) if match else None
                    if ims == 1:
                        result = {"ready": True, "bearer": "ims", "cs": cs,
                                  "packet": packet, "ims": ims}
                    elif ims == 0:
                        result = {
                            "ready": False, "cs": cs, "packet": packet, "ims": ims,
                            "reason": ("SMS unavailable: LTE data is registered, but the modem "
                                       "has neither circuit-switched registration nor an available "
                                       "IMS session. Update/provision the modem firmware or carrier "
                                       "profile before retrying."),
                        }
                    else:
                        result = {"ready": None, "reason": "IMS state is inconclusive",
                                  "cs": cs, "packet": packet}
        except Exception as exc:
            result = {"ready": None, "reason": f"SMS bearer probe failed: {exc}"}
        self._sms_readiness_cache = (now, dict(result))
        return result

    def voice_registration_status(self, *, force: bool = False,
                                  allow_repair: bool = True) -> dict:
        # Probe the function that will actually execute ATD/ATA/ATH. On Windows the MBN data
        # function and an auxiliary signalling function can expose different registration
        # views. Standalone 3GPP modems continue to use their primary AT connection.
        command = getattr(self.platform_provider, "voice_command", None)
        if not callable(command):
            command = self._at
        return self.voice_registration.check(
            command, force=force, allow_repair=allow_repair).public()

    def uicc_health_status(self, *, force: bool = False,
                           allow_repair: bool = True) -> dict:
        """Maintain the UICC on the signalling function without assuming a modem model."""
        signalling = getattr(self.platform_provider, "voice_command", None)

        def command(value: str) -> bytes:
            if callable(signalling):
                try:
                    return signalling(value)
                except Exception as exc:
                    # A returned AT/CME/CMS failure is an authoritative modem response. Only
                    # transport loss may fall back to the already identity-verified primary
                    # function, and only for this maintainer's non-business commands.
                    detail = str(exc).casefold()
                    if any(token in detail for token in (
                            "+cme error", "+cms error", "\r\nerror", ": error")):
                        raise
            return self._at(value)
        return self.uicc_health.check(
            command, force=force, allow_repair=allow_repair).public()

    @staticmethod
    def _revision_from(output) -> str:
        """Extract the firmware revision from ATI or AT+GMR output.

        Vendors place the revision on a ``Revision:`` line, as a bare token, or not at all.
        Return an empty string rather than guessing: an invented revision would be checked
        against the compatibility matrix and could produce a false verdict.
        """
        lines = output if isinstance(output, list) else str(output or "").replace(
            "\r", "\n").split("\n")
        candidates = [str(line).strip() for line in lines if str(line).strip()]
        for line in candidates:
            match = re.match(r"^(?:revision|firmware)\s*:\s*(\S+)$", line, re.I)
            if match:
                return match.group(1)[:100]
        for line in candidates:
            if line.upper() in {"OK", "ATI", "AT+GMR"}:
                continue
            if re.fullmatch(r"[A-Z0-9._\-]*R\d{2}[A-Z0-9._\-]*", line.upper()):
                return line[:100]
        return ""

    @staticmethod
    def _registration_state(raw: bytes, name: str) -> int | None:
        match = re.search(
            rb"\+" + re.escape(name.encode("ascii")) + rb":\s*\d+\s*,\s*(\d+)", raw)
        return int(match.group(1)) if match else None

    def _prepare_sms_bearer(self) -> dict:
        """Prefer packet-domain MO SMS only when packet service is the sole registered domain."""
        cs = self._registration_state(self._at("AT+CREG?"), "CREG")
        packet_states = []
        for command, name in (("AT+CGREG?", "CGREG"), ("AT+CEREG?", "CEREG")):
            try:
                packet_states.append(self._registration_state(self._at(command), name))
            except Exception:
                pass
        registered = {1, 5}
        if cs in registered or not any(value in registered for value in packet_states):
            return {"changed": False, "cs": cs, "packet": packet_states}
        supported = self._at("AT+CGSMS=?")
        support_match = re.search(rb"\+CGSMS:\s*\(([^\r\n)]*)\)", supported)
        support_tokens = re.findall(rb"\d+(?:\s*-\s*\d+)?",
                                    support_match.group(1) if support_match else b"")
        packet_preferred_supported = any(
            (int(token) == 2 if b"-" not in token else
             int(token.split(b"-", 1)[0]) <= 2 <= int(token.split(b"-", 1)[1]))
            for token in support_tokens)
        if not packet_preferred_supported:
            return {"changed": False, "cs": cs, "packet": packet_states,
                    "reason": "packet-preferred SMS is unsupported"}
        current_raw = self._at("AT+CGSMS?")
        current_match = re.search(rb"\+CGSMS:\s*(\d+)", current_raw)
        current = int(current_match.group(1)) if current_match else None
        if current != 2:
            self._at("AT+CGSMS=2")
        return {"changed": current != 2, "previous": current, "selected": 2,
                "cs": cs, "packet": packet_states}

    def sms_list(self) -> list[dict]:
        if self.platform_provider:
            return self.platform_provider.sms_list()
        self._at("AT+CMGF=1")
        self._at('AT+CSCS="GSM"')
        raw = self._at('AT+CMGL="ALL"').decode("utf-8", "replace")
        lines, messages, index = raw.replace("\r", "").split("\n"), [], 0
        while index < len(lines):
            line = lines[index].strip()
            match = re.match(r'^\+CMGL:\s*(\d+),"([^"]*)","([^"]*)"(.*)$', line)
            if match:
                body = lines[index + 1] if index + 1 < len(lines) else ""
                status = match.group(2).upper()
                identity = hashlib.sha256(f"{line}\0{body}".encode("utf-8")).hexdigest()
                messages.append({"id": match.group(1),
                                 "fingerprint": identity,
                                 "direction": "out" if status.startswith("STO") else "in",
                                 "peer": match.group(3), "body": body, "ts": int(time.time())})
                index += 1
            index += 1
        return messages

    def connect(self, *, allow_uicc_maintenance: bool = True) -> bool:
        # An auto-discovered COM/tty name belongs only to the current USB attachment.  After
        # unplug/replug Windows commonly renumbers it, so never turn an `auto` preference into
        # a permanent pin to the first successful port.
        if self.requested_port and self.requested_port != "auto":
            candidates = [self.requested_port]
        else:
            ports = list(list_ports.comports())
            def priority(item):
                description = str(getattr(item, "description", "") or "")
                if re.search(r"\bAT\b", description, re.I):
                    return 0
                if re.search(r"modem|wwan|mobile broadband|cellular", description, re.I):
                    return 1
                if re.search(r"bluetooth|nmea|diagnostic|\bDM\b|\bgps\b", description, re.I):
                    return 3
                return 2
            candidates = [item.device for item in sorted(ports, key=priority)]
        self._reenumeration_pending = False
        for candidate in candidates:
            if (self._connect_port(candidate) if allow_uicc_maintenance
                    else self._connect_port(candidate, allow_uicc_maintenance=False)):
                return True
            # A persistent USB-composition change invalidates the complete port snapshot.
            # Wait for a fresh enumeration instead of spending time opening stale DM/NMEA and
            # host COM ports from the list captured before the module reset.
            if self._reenumeration_pending:
                break
        return False

    def _connect_port(self, candidate: str, *, allow_uicc_maintenance: bool = True) -> bool:
        try:
            self.close()
            self.serial = self._open_serial(candidate)
            # EC20 autobaud can reject the first command after the port is opened.
            for _ in range(3):
                try:
                    self._at("AT")
                    break
                except ModemError:
                    time.sleep(0.2)
            self._at("ATE0")
            self._at("AT+CMEE=2")
            self.sim_via_mbn = False
            raw = self._at("AT+CGSN")
            match = IMEI_RE.search(raw)
            if not match:
                raise ModemError("modem IMEI is unavailable")
            self.imei = match.group(1).decode("ascii")
            if allow_uicc_maintenance:
                self._pre_identity_uicc_maintenance()
            if os.name == "nt" and self.platform_provider is None:
                self.platform_provider = WindowsMbnProvider.discover(self.imei)
                if self.platform_provider is None:
                    raise ModemError(
                        "Windows MBN has not enumerated this modem; automatic Generic AT "
                        "ownership is disabled")
                configured_port = self.gammu_port or str(os.environ.get("MDD_GAMMU_PORT") or "")
                if configured_port:
                    signalling_ports = [configured_port]
                else:
                    discovered_ports = list(list_ports.comports())
                    def signalling_priority(item):
                        description = str(getattr(item, "description", "") or "")
                        if re.search(r"\bmodem\b", description, re.I):
                            return 0
                        if re.search(r"\bAT\b", description, re.I):
                            return 1
                        return 2
                    signalling_ports = [item.device for item in sorted(
                        discovered_ports, key=signalling_priority)
                        if item.device != candidate and not re.search(
                            r"bluetooth|nmea|diagnostic|\bDM\b|\bgps\b",
                            str(getattr(item, "description", "") or ""), re.I)]
                # Windows MBN already owns the modem UICC. A CUAD/CSIM probe on the
                # signalling function can steal that session and destabilize SMS, network
                # registration and calls. External PC/SC readers remain independently owned
                # by the card supervisor.
                signalling = AuxiliaryAtProvider.discover(
                    self.imei, signalling_ports, probe_sim_apdu=False)
                if not signalling:
                    signalling = GammuCliProvider.discover(
                        self.imei, signalling_ports, configured=self.gammu)
                if signalling:
                    apdu = signalling if signalling.capabilities.sim_apdu else None
                    self.platform_provider = CompositeModemProvider(
                        self.platform_provider, signalling, apdu)
                    log.info("%s signalling attached on %s", signalling.name, signalling.port)
                    if apdu:
                        log.info("Auxiliary SIM APDU access verified on %s", signalling.port)
            system_iccid = system_imsi = ""
            if self.platform_provider:
                # Once the operating-system provider owns this modem, it is authoritative for
                # every SIM/business operation.  Do not silently mix MBN data with AT SIM,
                # APDU, SMS or calling merely because a diagnostic COM port also responds.
                system_iccid, system_imsi = self.platform_provider.identity()
                self.iccid = system_iccid
                self.sim_via_mbn = True
            else:
                try:
                    raw = self._at("AT+CCID")
                    match = ICCID_RE.search(raw)
                    self.iccid = match.group(1).decode("ascii") if match else ""
                except Exception:
                    self.iccid = ""
            if not self.iccid and os.name == "nt" and not self.platform_provider:
                system_iccid, system_imsi = self._windows_mbn_identity()
                self.iccid = system_iccid
                self.sim_via_mbn = bool(system_iccid)
            if not self.iccid:
                raise ModemError("SIM ICCID is unavailable through AT or Windows MBIM")
            if self.platform_provider:
                self.imsi = system_imsi
                numbers = self.platform_provider.snapshot.get("telephone_numbers") or []
                self.msisdn = str(next((value for value in numbers if value), ""))
                if not self.msisdn:
                    # IMbnSubscriberInformation and EF_MSISDN are often empty even on a
                    # working SIM.  3GPP AT+CNUM is a non-mutating fallback on the already-
                    # owned AT function; never infer a telephone number from IMSI/ICCID.
                    try:
                        self.msisdn = self._cnum_number(self._at("AT+CNUM"))
                    except Exception:
                        self.msisdn = ""
            else:
                try:
                    raw = self._at("AT+CIMI")
                    match = IMSI_RE.search(raw)
                    self.imsi = match.group(1).decode("ascii") if match else ""
                except Exception:
                    if os.name == "nt" and not system_imsi:
                        _, system_imsi = self._windows_mbn_identity()
                    self.imsi = system_imsi
                try:
                    self.msisdn = self._cnum_number(self._at("AT+CNUM"))
                except Exception:
                    self.msisdn = ""
            self.port_name = candidate
            try:
                raw = self._at("ATI").decode("ascii", "replace")
                values = [line.strip() for line in raw.replace("\r", "").split("\n")
                          if line.strip() and line.strip() not in {"OK", "ATI"}]
                self.model = " ".join(values[:2])[:100]
                self.firmware = self._revision_from(values)
            except Exception:
                self.model = "3GPP modem"
            if not self.firmware:
                # 3GPP TS 27.007 AT+GMR is the authoritative revision request. ATI only
                # includes it on some modems, and never as a stable field position.
                try:
                    self.firmware = self._revision_from(
                        self._at("AT+GMR").decode("ascii", "replace"))
                except Exception:
                    self.firmware = ""
            self.uicc_health.set_context(self.imei, self.iccid)
            self.uicc_health.remember_identity(self.imei, self.iccid)
            uicc = {"ready": None}
            if allow_uicc_maintenance:
                uicc = self.uicc_health_status(force=True, allow_repair=True)
                if uicc.get("action"):
                    log.info("UICC maintenance: action=%s state=%s reason=%s",
                             uicc.get("action"), uicc.get("state"), uicc.get("reason"))
            # When Windows MBIM owns the SIM channel, accepting generic AT commands does not
            # mean that AT SMS, calls, or CSIM APDUs are usable.  Advertise only capabilities
            # that can actually reach the SIM in the current ownership mode.
            if self.platform_provider:
                provider_model = " ".join(filter(None, (
                    str(self.platform_provider.snapshot.get("manufacturer") or ""),
                    str(self.platform_provider.snapshot.get("model") or ""))))
                if provider_model:
                    self.model = provider_model[:100]
                provider_firmware = str(
                    self.platform_provider.snapshot.get("firmware") or "").strip()
                if provider_firmware:
                    self.firmware = provider_firmware[:100]
                platform_caps = self.platform_provider.capabilities
                # SMS is enabled when the provider implements the operations, not merely when
                # the physical driver reports an SMS capability bit.
                self.capabilities["sms"] = bool(
                    platform_caps.sms_list and platform_caps.sms_send and
                    hasattr(self.platform_provider, "sms_list") and
                    hasattr(self.platform_provider, "sms_send"))
                self.capabilities["call"] = bool(platform_caps.call_signalling)
                self.capabilities["sim_apdu"] = bool(platform_caps.sim_apdu)
                self.capabilities["cellular_data"] = bool(platform_caps.cellular_data)
            else:
                self.capabilities["sms"] = (not self.sim_via_mbn and
                                            self._supports("AT+CMGF=?"))
                self.capabilities["call"] = (not self.sim_via_mbn and
                                             self._supports("AT+CLCC"))
                self.capabilities["cellular_data"] = True
            self._sim_apdu_failures = 0
            # Voice signalling and media are separate capabilities. The startup media probe is
            # bounded and non-billable: it never dials, answers, or changes the default sound
            # device. Only an explicitly matched endpoint in this modem's hardware container
            # can make call_audio true.
            self.call_audio_probe = CallAudioProbe(
                reason="call signalling is unavailable on the selected provider")
            self.capabilities["call_audio"] = False
            if self.capabilities.get("call"):
                if allow_uicc_maintenance:
                    self.voice_registration.set_context(self.imei, self.iccid, self.firmware)
                    if os.name == "nt":
                        preparation = prepare_call_audio_usb(self._at)
                        if preparation.changed and preparation.restart_required:
                            self._reenumeration_pending = True
                            log.info(
                                "Enabled modem USB call audio while preserving its USB "
                                "composition; waiting for device re-enumeration "
                                "(original=%s, configured=%s)",
                                preparation.original, preparation.configured)
                            raise ModemError("modem is restarting after enabling USB call audio")
                        if preparation.reason:
                            log.info("USB call-audio preparation skipped: %s", preparation.reason)
                    self.call_audio_probe = probe_call_audio(
                        candidate, self._at, helper=self.call_audio_helper,
                        allow_permission_prompt=self.allow_audio_permission_prompt)
                    self.capabilities["call_audio"] = self.call_audio_probe.ready
                    if self.call_audio_probe.ready:
                        self.call_audio_controller = CallAudioController(
                            self.call_audio_probe, self._at, helper=self.call_audio_helper)
                        log.info("Call audio self-test passed (%s)",
                                 self.call_audio_probe.backend)
                    else:
                        log.warning("Call audio unavailable: %s", self.call_audio_probe.reason)
                    if uicc.get("ready") is not False:
                        voice = self.voice_registration_status(force=True, allow_repair=True)
                        if voice.get("action"):
                            log.info(
                                "Voice registration maintenance: action=%s state=%s reason=%s",
                                voice.get("action"), voice.get("state"), voice.get("reason"))
                        if voice.get("restart_required"):
                            self._reenumeration_pending = True
                            raise ModemError(
                                "modem is restarting after bounded voice-registration recovery")
                else:
                    self.call_audio_probe = CallAudioProbe(
                        reason="paid-call cleanup reconnect skips startup audio probe")
            log.info("Connected to %s (%s; ICCID ending %s)", candidate,
                     self.model or "3GPP modem", self.iccid[-4:])
            self.registration_ready.set()
            return True
        except Exception as exc:
            log.warning("Cannot open modem %s: %s", candidate, exc)
            self.close()
            return False

    def reprobe_call_audio(self) -> dict:
        """Reconcile only non-billable audio capability without reconnecting the modem."""
        with self.lock:
            if not self.connection or not self.capabilities.get("call"):
                result = {"ready": False, "reason": "call signalling is unavailable"}
            else:
                existing = self.call_audio_controller
                if existing and existing.process and existing.process.poll() is None:
                    result = {"ready": True, "reason": "call audio is currently in use",
                              "backend": self.call_audio_probe.backend}
                else:
                    self.call_audio_controller = None
                    self.call_audio_probe = probe_call_audio(
                        self.port_name, self._at, helper=self.call_audio_helper,
                        allow_permission_prompt=False)
                    self.capabilities["call_audio"] = self.call_audio_probe.ready
                    if self.call_audio_probe.ready:
                        self.call_audio_controller = CallAudioController(
                            self.call_audio_probe, self._at, helper=self.call_audio_helper)
                        log.info("Call audio re-probe passed (%s)",
                                 self.call_audio_probe.backend)
                    else:
                        log.warning("Call audio re-probe unavailable: %s",
                                    self.call_audio_probe.reason)
                    result = {
                        "ready": self.call_audio_probe.ready,
                        "reason": ("" if self.call_audio_probe.ready else
                                   self.call_audio_probe.reason),
                        "backend": self.call_audio_probe.backend,
                    }
        callback = getattr(self, "status_refresh_callback", None)
        if callable(callback):
            callback()
        return result

    def _open_serial(self, candidate: str):
        return serial.Serial(candidate, self.baud, timeout=0.25, write_timeout=2)

    def _supports(self, command: str) -> bool:
        try:
            self._at(command)
            return True
        except Exception:
            return False

    def _windows_mbn_identity(self) -> tuple[str, str]:
        """Read SIM identity through Windows MBIM when the AT SIM channel is owned by WWAN."""
        if os.name != "nt" or not self.imei:
            return "", ""
        try:
            interfaces = subprocess.run(
                ["netsh", "mbn", "show", "interfaces"], capture_output=True,
                text=True, timeout=8, check=False).stdout
            blocks = re.split(r"\n\s*\n", interfaces)
            block = next((value for value in blocks if self.imei in value), "")
            match = re.search(r"^\s*Name\s*:\s*(.+?)\s*$", block, re.I | re.M)
            if not match:
                return "", ""
            ready = subprocess.run(
                ["netsh", "mbn", "show", "readyinfo", f"interface={match.group(1).strip()}"],
                capture_output=True, text=True, timeout=8, check=False).stdout
            iccid_match = re.search(r"^\s*SIM\s+ICC\s+Id\s*:\s*(\d{18,22})\s*$",
                                    ready, re.I | re.M)
            imsi_match = re.search(r"^\s*Subscriber\s+Id\s*:\s*(\d{14,15})\s*$",
                                   ready, re.I | re.M)
            return ((iccid_match.group(1) if iccid_match else ""),
                    (imsi_match.group(1) if imsi_match else ""))
        except Exception:
            return "", ""

    def transmit(self, apdu: bytes) -> bytes:
        if is_forbidden_apdu(apdu):
            return bytes.fromhex("6985")
        try:
            if (self.platform_provider and self.capabilities.get("sim_apdu") and
                    hasattr(self.platform_provider, "transmit_apdu")):
                response = self.platform_provider.transmit_apdu(apdu)
                self._sim_apdu_failures = 0
                return response
            value = apdu.hex().upper()
            raw = self._at(f'AT+CSIM={len(value)},"{value}"')
            match = CSIM_RE.search(raw)
            if not match:
                raise ModemError(f"missing +CSIM response: {raw[-200:]!r}")
            self._sim_apdu_failures = 0
            return bytes.fromhex(match.group(2).decode("ascii"))
        except Exception as exc:
            self._sim_apdu_failures += 1
            log.warning("APDU failed: %s", exc)
            if self._sim_apdu_failures >= 2:
                # A startup probe may win a race before Windows claims the SIM. Consecutive
                # failures during real exchange withdraw the runtime capability and close the
                # VPCD bridge. Re-enumeration creates a fresh provider and probes again.
                self.capabilities["sim_apdu"] = False
                raise ModemError(
                    "SIM APDU runtime circuit opened after consecutive exchange failures")
            return bytes.fromhex("6F00")

    def reset(self):
        try:
            if (self.platform_provider and self.capabilities.get("sim_apdu") and
                    hasattr(self.platform_provider, "reset_apdu")):
                self.platform_provider.reset_apdu()
                return
            self._at("AT+CPIN?")
        except Exception:
            # Some Windows MBIM drivers own the SIM channel and return "SIM failure" for
            # CPIN/CCID while the physical AT port remains healthy.  A VPCD reset must not
            # tear down modem control in that case.
            try:
                self._at("AT")
            except Exception:
                self.close()

    def close(self):
        self.registration_ready.clear()
        self.capabilities["call_audio"] = False
        self.call_audio_probe = CallAudioProbe(reason="modem is disconnected")
        audio, self.call_audio_controller = self.call_audio_controller, None
        if audio:
            try:
                audio.close()
            except Exception:
                pass
        provider, self.platform_provider = self.platform_provider, None
        if provider:
            closer = getattr(provider, "close", None)
            if closer:
                try:
                    closer()
                except Exception:
                    pass
        current, self.serial = self.serial, None
        if current:
            try:
                current.close()
            except Exception:
                pass


class _PrivateUsbSerialAdapter:
    """Present the existing AT domain code over one private raw-USB companion."""

    def __init__(self, backend):
        self.backend = backend
        self.buffer = bytearray()
        self.closed = False

    def reset_input_buffer(self):
        self.buffer.clear()

    def write(self, value: bytes):
        if self.closed:
            raise OSError("private USB modem is closed")
        payload = bytes(value)
        if payload.startswith(b"AT") and payload.endswith(b"\r"):
            result = self.backend.at(payload[:-1].decode("ascii")).encode("utf-8")
        else:
            result = self.backend.exchange(payload)
        self.buffer.extend(result)
        return len(payload)

    def flush(self):
        return None

    def read(self, size: int) -> bytes:
        value = bytes(self.buffer[:size])
        del self.buffer[:size]
        return value

    def close(self):
        if not self.closed:
            self.closed = True
            self.backend.close()


class PrivateUsbModemCard(ModemCard):
    """Reuse ModemCard's SIM/SMS/call logic with no tty or host network interface."""

    def __init__(self, backend, attachment: dict, **kwargs):
        self.backend = backend
        self.attachment = dict(attachment)
        identity = (f"usb:{int(attachment['vid']):04x}:{int(attachment['pid']):04x}:"
                    f"{int(attachment['bus'])}:{int(attachment['address'])}:"
                    f"{str(backend.identity.get('serial') or '')}")
        super().__init__(identity, 115200, **kwargs)
        self.private_raw_usb = True
        self.hardware_id = identity

    def _open_serial(self, candidate: str):
        if candidate != self.requested_port:
            raise ModemError("private USB attachment identity changed")
        return _PrivateUsbSerialAdapter(self.backend)

    def transmit(self, apdu: bytes) -> bytes:
        """Distinguish an APDU rejection from loss of the raw USB/UICC transport.

        Physical USIMs legitimately reject eUICC discovery commands.  Several modems report
        that as a bare AT ``ERROR`` instead of returning an ISO 7816 status word.  Treating two
        such replies as a dead UICC tears down an otherwise healthy SIM and creates a reconnect
        loop.  A bounded CPIN probe proves whether the transport/UICC is still alive; only a
        failed health probe contributes to the runtime circuit breaker.
        """
        if is_forbidden_apdu(apdu):
            return bytes.fromhex("6985")
        if getattr(self, "cellular_active", None) is not None and \
                self.cellular_active.is_set():
            # Some raw modems invalidate their packet-data context when the SIM
            # logical channel is used, even when AT and PPP occupy distinct USB
            # interfaces.  Identity stays cached and the bridge resumes after
            # data is disabled; do not turn this expected exclusion into a UICC
            # circuit-breaker failure.
            return bytes.fromhex("6985")
        value = apdu.hex().upper()
        try:
            raw = self._at(f'AT+CSIM={len(value)},"{value}"')
            match = CSIM_RE.search(raw)
            if not match:
                raise ModemError(f"missing +CSIM response: {raw[-200:]!r}")
            self._sim_apdu_failures = 0
            return bytes.fromhex(match.group(2).decode("ascii"))
        except Exception as apdu_error:
            try:
                health = self._at("AT+CPIN?")
                if b"READY" in health:
                    self._sim_apdu_failures = 0
                    header = apdu[:5].hex().upper()
                    log.info("Raw USB modem rejected unsupported APDU header=%s length=%d: %s",
                             header, len(apdu), str(apdu_error).strip() or "ERROR")
                    return bytes.fromhex("6F00")
            except Exception:
                pass
            self._sim_apdu_failures += 1
            log.warning("Raw USB SIM APDU transport failed: %s", apdu_error)
            if self._sim_apdu_failures >= 2:
                self.capabilities["sim_apdu"] = False
                raise ModemError(
                    "SIM APDU runtime circuit opened after consecutive UICC health failures")
            return bytes.fromhex("6F00")


def path_with_card_id(path: str, reader_name: str, card_id: str, imei: str) -> str:
    allocated = agent_ws_path(path, reader_name)
    split = urllib.parse.urlsplit(allocated)
    query = dict(urllib.parse.parse_qsl(split.query, keep_blank_values=True))
    query["card_id"] = card_id
    query["imei"] = imei
    return urllib.parse.urlunsplit(("", "", split.path, urllib.parse.urlencode(query), ""))


class ModemControl:
    """Versioned control channel; all business identity remains ICCID-based."""
    def __init__(self, args, modem: ModemCard, dial_backend=None):
        self.args, self.modem = args, modem
        self.dial_backend = dial_backend
        self.stop = threading.Event()
        self.results = {}
        self.socks_server = None
        self.isolation = IsolationGuard(args.isolation_helper)
        self.acked_sms = set()
        self.allow_roaming = False
        self.selected_profile = ""
        self._isolation_armed = False
        self._source_miss_count = 0
        self.operation_lock = threading.Lock()
        self._sms_refresh_failures = 0
        self._sms_refresh_last = 0.0
        self._sms_refresh_error = ""
        self._restart_target_cache = (0.0, {})
        self._restart_pending = threading.Event()
        self._sms_list_failures = 0
        self._sms_list_blocked_until = 0.0
        self._sms_list_error = ""
        self.reset_pin = bool(getattr(args, "reset_pin", False))
        # Windows WWAN can misreport an otherwise readable SIM as absent if a vendor driver
        # sees a USIM logical channel before it activates the saved data profile.  The control
        # plane establishes the desired data/off state first; only then may the VPCD bridge
        # open an auxiliary UICC session.
        self.data_reconciled = threading.Event()
        self.cellular_active = threading.Event()
        self._private_data_release_proven = threading.Event()
        self._private_data_owner_probe_authorized = False
        self.status_refresh = threading.Event()
        self.shutdown_started = threading.Event()
        self.paid_call_active = threading.Event()
        # Capability probing belongs to the modem while publication belongs to this control
        # channel. This observer makes a TCC change visible without reconnecting other devices.
        self.modem.status_refresh_callback = self.status_refresh.set
        self._paid_call_lock = threading.RLock()
        self._paid_call_lease_id = ""
        self._paid_call_deadline = 0.0
        self._paid_call_recovered = False
        self._paid_call_seen_nonterminal = False
        self._paid_call_termination_requested = False
        self._paid_call_terminal_samples = 0
        self._paid_call_marker_error = ""
        self._paid_call_fail_safe_claim = ""
        self._paid_call_fail_safe_result = None
        self._paid_call_condition = threading.Condition(self._paid_call_lock)
        self.paid_call_cleared = threading.Event()
        self._paid_call_cleanup_retries = 0
        self._paid_call_watchdog_timeout = 50.0
        self._paid_call_retry_delay = 2.0
        self._paid_call_retry_limit = 2
        self._install_maintenance_nonce = ""
        identity = re.sub(r"[^A-Za-z0-9_.-]+", "_", str(self.modem.imei or ""))
        self._paid_call_marker = (default_data_dir() / "state" /
                                  f"paid-call-{identity}.json") if identity else None
        self._recover_paid_call_marker()
        if getattr(self.modem, "private_raw_usb", False):
            self.modem.cellular_active = self.cellular_active
        threading.Thread(target=self._watch_isolation, name="cellular-isolation-watch",
                         daemon=True).start()
        threading.Thread(target=self._watch_paid_call_lease,
                         name="paid-call-lease-watch", daemon=True).start()

    def _write_paid_call_marker(self, value: dict | None) -> None:
        path = self._paid_call_marker
        if not path:
            return
        path.parent.mkdir(parents=True, exist_ok=True)
        if value is None:
            try:
                path.unlink()
            except FileNotFoundError:
                pass
            else:
                if os.name != "nt":
                    directory_fd = os.open(path.parent, os.O_RDONLY |
                                           getattr(os, "O_DIRECTORY", 0))
                    try:
                        os.fsync(directory_fd)
                    finally:
                        os.close(directory_fd)
            return
        temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
        try:
            fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "w", encoding="utf-8") as handle:
                json.dump(value, handle, sort_keys=True)
                handle.flush()
                os.fsync(handle.fileno())
            try:
                os.chmod(temporary, 0o600)
            except OSError:
                pass
            os.replace(temporary, path)
            if os.name != "nt":
                directory_fd = os.open(path.parent, os.O_RDONLY |
                                       getattr(os, "O_DIRECTORY", 0))
                try:
                    os.fsync(directory_fd)
                finally:
                    os.close(directory_fd)
        finally:
            try:
                temporary.unlink()
            except FileNotFoundError:
                pass

    def _recover_paid_call_marker(self) -> None:
        path = self._paid_call_marker
        if not path:
            return
        try:
            marker = json.loads(path.read_text(encoding="utf-8"))
            if (str(marker.get("imei") or "") != str(self.modem.imei or "") or
                    not str(marker.get("lease_id") or "")):
                raise ValueError("paid-call marker identity mismatch")
            self._paid_call_lease_id = str(marker["lease_id"])
            self._paid_call_recovered = True
            self._paid_call_termination_requested = True
            self.paid_call_active.set()
            # A process restart invalidates every remote/browser lease. Give initialization a
            # short grace period, then terminate locally unless a matching live gateway renews.
            self._paid_call_deadline = time.monotonic() + 2.0
            log.warning("Recovered unresolved paid call lease %s; local termination armed",
                        self._paid_call_lease_id[:12])
        except FileNotFoundError:
            return
        except Exception as exc:
            # Corruption is evidence of an unresolved safety boundary, never permission to
            # overwrite it with a new call. Terminate this exact modem and hold radio/data off.
            self._paid_call_marker_error = str(exc).strip() or type(exc).__name__
            self._paid_call_lease_id = "corrupt-marker"
            self._paid_call_recovered = True
            self._paid_call_termination_requested = True
            self.paid_call_active.set()
            self._paid_call_deadline = time.monotonic() + 2.0
            log.critical("Invalid paid-call marker; fail-closed termination armed: %s", exc)

    def _arm_paid_call_lease(self, lease_id: str, direction: str) -> None:
        lease_id = str(lease_id or "")
        if self._paid_call_marker_error:
            raise ModemError("Paid-call safety marker is invalid; modem remains quarantined")
        if not re.fullmatch(r"[A-Za-z0-9_.:-]{8,160}", lease_id):
            raise ModemError("A valid paid-call lease_id is required before dial or answer")
        with self._paid_call_lock:
            if self.shutdown_started.is_set():
                raise AgentShuttingDownError(
                    "Agent shutdown started before the paid-call lease could be committed")
            if self._paid_call_lease_id and self._paid_call_lease_id != lease_id:
                raise ModemError("Another unresolved paid call lease owns this modem")
            self._write_paid_call_marker({
                "version": 1, "lease_id": lease_id, "imei": str(self.modem.imei or ""),
                "iccid": str(self.modem.iccid or ""), "direction": direction,
                "started_at": int(time.time()),
            })
            self._paid_call_lease_id = lease_id
            self._paid_call_recovered = False
            self._paid_call_seen_nonterminal = False
            self._paid_call_termination_requested = False
            self._paid_call_terminal_samples = 0
            self._paid_call_marker_error = ""
            self._paid_call_fail_safe_claim = ""
            self._paid_call_fail_safe_result = None
            self._paid_call_cleanup_retries = 0
            self.paid_call_cleared.clear()
            self._paid_call_deadline = time.monotonic() + 12.0
            self.paid_call_active.set()

    def _renew_paid_call_lease(self, lease_id: str) -> dict:
        with self._paid_call_lock:
            if not self._paid_call_lease_id:
                return {"ok": False, "status": "missing", "error": "No paid call is armed"}
            if str(lease_id or "") != self._paid_call_lease_id:
                return {"ok": False, "status": "conflict",
                        "error": "Paid-call lease identity does not match"}
            if self._paid_call_recovered:
                return {"ok": False, "status": "restart_recovery",
                        "error": "Agent restart invalidated the remote paid-call lease"}
            if (self.shutdown_started.is_set() or
                    self._paid_call_termination_requested or
                    bool(self._paid_call_fail_safe_claim)):
                # Once any local safety path starts, remote media freshness is no longer
                # permission to postpone it. In particular this preserves a watchdog retry
                # scheduled after operation_lock contention.
                return {"ok": False, "status": "terminating",
                        "error": "Paid-call termination has already started"}
            self._paid_call_deadline = time.monotonic() + 12.0
            return {"ok": True, "status": "renewed", "ttl_seconds": 12}

    def _clear_paid_call_lease(self) -> None:
        with self._paid_call_lock:
            self._paid_call_lease_id = ""
            self._paid_call_deadline = 0.0
            self._paid_call_recovered = False
            self._paid_call_seen_nonterminal = False
            self._paid_call_termination_requested = False
            self._paid_call_terminal_samples = 0
            self._paid_call_marker_error = ""
            self._write_paid_call_marker(None)
            self.paid_call_active.clear()
            self.paid_call_cleared.set()
            self._paid_call_condition.notify_all()

    def _paid_call_safety_hold(self) -> bool:
        with self._paid_call_lock:
            return bool(self._paid_call_lease_id and
                        (self._paid_call_termination_requested or
                         self._paid_call_recovered)) or bool(self._paid_call_marker_error)

    def _paid_call_armed(self) -> bool:
        """Return true for every durable paid lease, including pre-termination calls."""
        with self._paid_call_lock:
            return bool(self._paid_call_lease_id or self._paid_call_marker_error or
                        self.paid_call_active.is_set())

    def _fresh_call_status(self, timeout: float = 5.0) -> dict:
        if self.modem.platform_provider and hasattr(
                self.modem.platform_provider, "call_status"):
            return self.modem.platform_provider.call_status(timeout=timeout)
        return {**parse_clcc_voice(self.modem._at("AT+CLCC", timeout=timeout)),
                "audio": bool(self.modem.capabilities.get("call_audio") and
                              self.modem.call_audio_probe.ready),
                "audio_error": "" if self.modem.call_audio_probe.ready else
                               self.modem.call_audio_probe.reason}

    def _verified_call_hangup(self, timeout: float = 20.0) -> dict:
        with self._paid_call_lock:
            if self._paid_call_lease_id:
                self._paid_call_termination_requested = True
        if self.modem.platform_provider and hasattr(
                self.modem.platform_provider, "call_hangup"):
            return self.modem.platform_provider.call_hangup(timeout=timeout)
        return verified_at_hangup(
            lambda command, command_timeout: self.modem._at(
                command, timeout=command_timeout), total_timeout=timeout)

    @staticmethod
    def _terminal_hangup_confirmed(result: dict) -> bool:
        return bool(
            result.get("terminal_confirmed") and result.get("fresh") and
            result.get("authoritative") and
            str(result.get("status") or "").casefold() in
            {"idle", "ended", "terminated"})

    def _paid_call_radio_cutoff(self, timeout: float) -> dict:
        """One deadline-bounded radio cutoff without slow graceful data reconciliation."""
        deadline = time.monotonic() + max(0.1, float(timeout))
        server, self.socks_server = self.socks_server, None
        if server:
            server.close()
        self.isolation.close()
        self._isolation_armed = False
        if self.dial_backend is not None:
            budget = max(0.1, deadline - time.monotonic())
            # The private companion owns the raw modem; CFUN is the authoritative cutoff.
            self.dial_backend.at("AT+CFUN=4", timeout=budget)
            budget = max(0.1, deadline - time.monotonic())
            self.dial_backend.disable(timeout=budget)
        elif os.name == "nt" and self.modem.sim_via_mbn:
            interface = self._cellular_interface()
            if not interface:
                raise ModemError("No mobile-broadband interface matches this modem")
            result = subprocess.run(
                ["netsh", "mbn", "set", "powerstate", f"interface={interface}",
                 "state=off"], capture_output=True, text=True,
                timeout=max(1, min(15, int(deadline - time.monotonic()))), check=False)
            if result.returncode and str(result.stderr or result.stdout).strip():
                raise ModemError(str(result.stderr or result.stdout).strip()[:300])
        else:
            self.modem._at("AT+CFUN=4", timeout=max(0.1, deadline - time.monotonic()))
        self.cellular_active.clear()
        return {"ok": True, "status": "off", "radio_enabled": False}

    def _terminate_paid_call_fail_safe(self, *, radio_cutoff: bool,
                                       total_timeout: float = 250.0) -> dict:
        """Perform one bounded local termination attempt for an armed paid call.

        This is shared by lease expiry and orderly process shutdown. It never dials, retries
        forever or clears durable evidence without fresh authoritative terminal samples.
        """
        deadline = time.monotonic() + max(0.1, float(total_timeout))
        with self._paid_call_condition:
            lease_id = self._paid_call_lease_id
            if not lease_id and not self._paid_call_marker_error:
                return {"ok": True, "status": "idle", "terminal_confirmed": True,
                        "no_lease": True}
            if self._paid_call_fail_safe_claim == lease_id:
                while (self._paid_call_fail_safe_result is None and
                       self._paid_call_fail_safe_claim == lease_id and
                       time.monotonic() < deadline):
                    self._paid_call_condition.wait(
                        max(0.0, deadline - time.monotonic()))
                if self._paid_call_fail_safe_result is not None:
                    return dict(self._paid_call_fail_safe_result)
                if self._paid_call_fail_safe_claim == lease_id:
                    return {"ok": False, "status": "termination_pending",
                            "terminal_confirmed": False,
                            "error": "paid-call fail-safe is still in progress"}
                # The previous claimant timed out before touching the modem and explicitly
                # released its claim. This waiter now becomes the one allowed fresh attempt.
            self._paid_call_fail_safe_claim = lease_id
            self._paid_call_fail_safe_result = None
            self._paid_call_termination_requested = True
            # Prevent the watchdog from starting a second attempt while an orderly shutdown
            # is waiting for the operation lock. The marker remains durable until confirmed.
            self._paid_call_deadline = float("inf")
        # Reserve the final 50 seconds for hangup/cutoff/evidence. Waiting for a long SMS or
        # data operation is part of the same total budget, never an unbounded prefix.
        reserve = min(46.0, max(0.1, float(total_timeout) * 0.9))
        lock_budget = max(0.0, deadline - time.monotonic() - reserve)
        acquired = self.operation_lock.acquire(timeout=lock_budget)
        if not acquired:
            result = {"ok": False, "status": "cleanup_blocked",
                      "terminal_confirmed": False, "cleanup_blocked": True,
                      "error": "another modem operation exceeded the paid-call cleanup budget"}
            with self._paid_call_condition:
                # No hangup/cutoff was attempted, so a later controlled shutdown may make one
                # fresh claim after the bounded operation finishes.
                self._paid_call_fail_safe_claim = ""
                self._paid_call_fail_safe_result = None
                self._paid_call_condition.notify_all()
            return result
        try:
            try:
                result = self._verified_call_hangup(
                    timeout=max(0.1, min(20.0, deadline - time.monotonic())))
                if not self._terminal_hangup_confirmed(result) and radio_cutoff:
                    # One fail-safe radio cutoff is permitted only while a durable paid-call
                    # safety hold exists. It also tears down data and is never retried here.
                    cutoff = self._paid_call_radio_cutoff(
                        max(0.1, min(15.0, deadline - time.monotonic())))
                    first = self._fresh_call_status(
                        timeout=max(0.1, min(5.0, deadline - time.monotonic())))
                    pause = max(0.0, min(0.4, deadline - time.monotonic()))
                    if pause:
                        time.sleep(pause)
                    second = self._fresh_call_status(
                        timeout=max(0.1, min(5.0, deadline - time.monotonic())))
                    confirmed = bool(
                        first.get("fresh") and first.get("authoritative") and
                        second.get("fresh") and second.get("authoritative") and
                        str(first.get("status") or "").casefold() in
                        {"idle", "ended", "terminated"} and
                        str(second.get("status") or "").casefold() in
                        {"idle", "ended", "terminated"})
                    result = {**second, "terminal_confirmed": confirmed,
                              "radio_cutoff": cutoff}
                if self._terminal_hangup_confirmed(result):
                    self._clear_paid_call_lease()
            except Exception as exc:
                result = {"ok": False, "status": "unknown", "terminal_confirmed": False,
                          "error": str(exc)}
        finally:
            self.operation_lock.release()
            with self._paid_call_condition:
                self._paid_call_fail_safe_result = dict(result)
                self._paid_call_condition.notify_all()
        return result

    def begin_shutdown(self) -> None:
        """Close the paid-action admission gate before inspecting the current lease."""
        # Serialize with the durable lease commit. A dial that commits first is protected by
        # _paid_call_armed(); a shutdown that commits first makes every later arm fail.
        with self._paid_call_lock:
            self.shutdown_started.set()
            self._install_maintenance_nonce = ""

    def prepare_install_maintenance(self) -> dict:
        """Atomically fence new paid calls only when no paid lease is armed.

        The Windows installer calls this before asking SCM to stop the service.  The same
        lock serializes this gate with durable ATD/ATA lease admission, so a successful
        result proves that no paid call committed before the maintenance fence and none can
        commit afterwards.  A busy result rolls the temporary fence back immediately.
        """
        with self._paid_call_lock:
            if self.shutdown_started.is_set():
                return {"ready": False, "status": "shutting_down",
                        "error": "Agent shutdown or maintenance is already in progress"}
            self.shutdown_started.set()
            armed = bool(self._paid_call_lease_id or self._paid_call_marker_error or
                         self.paid_call_active.is_set() or
                         self._paid_call_fail_safe_claim)
            if armed:
                self.shutdown_started.clear()
                return {"ready": False, "status": "paid_call_active",
                        "error": "A paid call is active, uncertain, or terminating"}
            nonce = uuid.uuid4().hex
            self._install_maintenance_nonce = nonce
            return {"ready": True, "status": "maintenance_ready", "nonce": nonce}

    def cancel_install_maintenance(self, nonce: str) -> dict:
        """Reopen paid-call admission if an installer aborts before requesting SCM stop."""
        with self._paid_call_lock:
            if (not self._install_maintenance_nonce or
                    not secrets.compare_digest(
                        str(nonce or ""), self._install_maintenance_nonce)):
                return {"cancelled": False, "status": "nonce_mismatch"}
            # A successful prepare fenced _arm_paid_call_lease under this same lock, so an
            # armed lease here would mean an invariant violation. Keep the gate closed.
            if (self._paid_call_lease_id or self._paid_call_marker_error or
                    self.paid_call_active.is_set() or self._paid_call_fail_safe_claim):
                return {"cancelled": False, "status": "paid_call_active"}
            self._install_maintenance_nonce = ""
            self.shutdown_started.clear()
            return {"cancelled": True, "status": "maintenance_cancelled"}

    def shutdown_paid_call(self) -> dict:
        """End any billed call before a controlled host shutdown closes the modem."""
        try:
            result = self._terminate_paid_call_fail_safe(radio_cutoff=True)
        except Exception as exc:
            # Keep the durable marker. The next Agent start re-arms termination; never turn a
            # failed cleanup into permission to restore radio/data or place another call.
            log.critical("Orderly shutdown could not confirm paid call termination: %s", exc)
            return {"ok": False, "terminal_confirmed": False, "error": str(exc)}
        if not result.get("terminal_confirmed"):
            log.critical("Orderly shutdown left physical call termination unconfirmed: %s",
                         result)
        return result

    def _watch_paid_call_lease(self) -> None:
        while not self.stop.wait(0.5):
            with self._paid_call_lock:
                lease_id = self._paid_call_lease_id
                expired = bool(lease_id and time.monotonic() >= self._paid_call_deadline)
                if expired:
                    # Disarm the timer while this one bounded termination is executing.
                    self._paid_call_deadline = float("inf")
            if not expired:
                continue
            log.error("Paid call lease %s expired; terminating physical call locally",
                      lease_id[:12])
            try:
                result = self._terminate_paid_call_fail_safe(
                    radio_cutoff=True, total_timeout=self._paid_call_watchdog_timeout)
                if result.get("cleanup_blocked"):
                    with self._paid_call_lock:
                        if (self._paid_call_lease_id == lease_id and
                                self._paid_call_cleanup_retries <
                                self._paid_call_retry_limit):
                            self._paid_call_cleanup_retries += 1
                            self._paid_call_deadline = (
                                time.monotonic() + self._paid_call_retry_delay)
                if not result.get("terminal_confirmed"):
                    log.critical("Physical call termination remains unconfirmed: %s", result)
            except Exception as exc:
                # Keep the durable marker. A future Agent start will re-arm termination; do not
                # create an infinite retry loop in this process.
                log.critical("Paid call fail-safe could not confirm termination: %s", exc)

    @staticmethod
    def _is_sms_configuration_pending(error: str) -> bool:
        normalized = str(error or "").strip().casefold().replace("_", "")
        return normalized in {"0x8000000a", "epending"}

    def _refresh_sms_configuration(self, *, force: bool = False) -> dict:
        """Resolve Windows' asynchronous SMS configuration without flooding WwanSvc."""
        now = time.monotonic()
        if not force and self._sms_refresh_last and now - self._sms_refresh_last < 300:
            return {"ok": self._sms_refresh_failures == 0,
                    "service_center": self.modem.service_centre(),
                    "error": self._sms_refresh_error, "cached": True}
        self._sms_refresh_last = now
        try:
            centre = self.modem.service_centre(force=True)
            if not centre:
                raise ModemError("The mobile-broadband SMS configuration is still pending.")
            self._sms_refresh_failures = 0
            self._sms_refresh_error = ""
            return {"ok": True, "service_center": centre}
        except Exception as exc:
            self._sms_refresh_failures += 1
            self._sms_refresh_error = str(exc).strip() or type(exc).__name__
            return {"ok": False, "service_center": "", "error": self._sms_refresh_error}

    def _windows_restart_target(self, *, force: bool = False) -> dict:
        """Return one exact OS-owned WWAN PnP function, or no restart capability.

        Model names and COM numbers are deliberately not used.  Windows' current mobile-
        broadband interface is joined to Win32_NetworkAdapter and the operation is offered
        only when that join yields one physical USB/PCI PnP instance and the Agent is elevated.
        """
        if os.name != "nt":
            return {"available": False, "reason": "Soft restart is not implemented on this OS."}
        checked_at, cached = self._restart_target_cache
        now = time.monotonic()
        if not force and checked_at and now - checked_at < 60:
            return dict(cached)
        result = {"available": False, "reason": "The modem PnP target could not be resolved uniquely."}
        try:
            import ctypes
            if not bool(ctypes.windll.shell32.IsUserAnAdmin()):
                result["reason"] = "The Agent service is not running with administrator privileges."
            else:
                interface = self._cellular_interface()
                command = (
                    "Get-CimInstance Win32_NetworkAdapter | "
                    "Select-Object Name,NetConnectionID,PNPDeviceID | ConvertTo-Json -Compress")
                process = subprocess.run(
                    ["powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command],
                    capture_output=True, text=True, timeout=12, check=False)
                payload = json.loads(str(process.stdout or "null"))
                rows = payload if isinstance(payload, list) else [payload] if isinstance(payload, dict) else []
                matches = [row for row in rows
                           if str(row.get("NetConnectionID") or "") == interface and
                           str(row.get("PNPDeviceID") or "").upper().startswith(("USB\\", "PCI\\"))]
                help_result = subprocess.run(
                    ["pnputil.exe", "/?"], capture_output=True, text=True,
                    timeout=8, check=False)
                if len(matches) == 1 and "/restart-device" in (
                        str(help_result.stdout or "") + str(help_result.stderr or "")):
                    result = {"available": True, "reason": "",
                              "target": str(matches[0]["PNPDeviceID"]),
                              "interface": interface}
                elif len(matches) == 1:
                    result["reason"] = "This Windows version does not support PnP soft restart."
        except Exception as exc:
            result["reason"] = f"Soft-restart preflight failed: {exc}"
        self._restart_target_cache = (now, dict(result))
        return result

    def _soft_restart(self) -> dict:
        target = self._windows_restart_target(force=True)
        if not target.get("available"):
            return {"ok": False, "unavailable": True, "error": target.get("reason")}
        if self._restart_pending.is_set():
            return {"ok": True, "accepted": True, "already_pending": True}
        try:
            call_state = self.modem.call_status() if self.modem.capabilities.get("call") else {}
        except Exception:
            call_state = {}
        if str(call_state.get("status") or "idle") not in {"", "idle", "ended"}:
            return {"ok": False, "unavailable": True,
                    "error": "Soft restart is blocked while a cellular call is active."}
        self._restart_pending.set()

        def restart():
            try:
                time.sleep(1.5)  # let the RPC acknowledgement leave before USB disappears
                subprocess.run(["pnputil.exe", "/restart-device", str(target["target"])],
                               capture_output=True, text=True, timeout=45, check=False)
                self._sms_refresh_last = 0.0
                self._restart_target_cache = (0.0, {})
            finally:
                self._restart_pending.clear()

        threading.Thread(target=restart, name="modem-soft-restart", daemon=True).start()
        return {"ok": True, "accepted": True,
                "warning": "Cellular data, SMS and calls will be interrupted briefly."}

    def _cellular_interface(self) -> str:
        if self.args.cellular_interface:
            return self.args.cellular_interface
        # Once isolation is armed, its verified interface is the stable attachment for this
        # data context. Re-running Windows MBN discovery on every status sample can return an
        # empty transient result and must not erase that known-good binding.
        guarded_interface = str(getattr(self.isolation, "interface", "") or "")
        if guarded_interface:
            return guarded_interface
        try:
            if os.name == "nt":
                raw = subprocess.run(["netsh", "mbn", "show", "interfaces"],
                                     capture_output=True, text=True, timeout=8, check=False).stdout
                blocks = re.split(r"\n\s*\n", raw)
                block = next((value for value in blocks if self.modem.imei in value), raw)
                match = re.search(r"^\s*Name\s*:\s*(.+?)\s*$", block, re.I | re.M)
                return match.group(1).strip() if match else ""
            if sys.platform == "darwin":
                raw = subprocess.run(["networksetup", "-listallhardwareports"],
                                     capture_output=True, text=True, timeout=8, check=False).stdout
                blocks = re.split(r"\n\s*\n", raw)
                block = next((value for value in blocks if re.search(
                    r"Hardware Port:.*(cellular|wwan|mobile|usb)", value, re.I)), "")
                match = re.search(r"Device:\s*(\S+)", block)
                return match.group(1) if match else ""
            names = os.listdir("/sys/class/net")
            preferred = [name for name in names if re.search(r"wwan|cdc|usb|rmnet", name, re.I)]
            return preferred[0] if preferred else ""
        except Exception:
            return ""

    def _advertise_host(self) -> str:
        if self.args.advertise_host:
            return self.args.advertise_host
        try:
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as probe:
                probe.connect((self.args.host, self.args.gateway_port))
                return probe.getsockname()[0]
        except OSError:
            return socket.gethostbyname(socket.gethostname())

    def _cellular_ip(self, interface: str) -> str:
        if not interface:
            return ""
        try:
            if os.name == "nt":
                command = ["powershell", "-NoProfile", "-Command",
                           f"(Get-NetIPAddress -InterfaceAlias '{interface.replace(chr(39), chr(39) * 2)}' -AddressFamily IPv4 -ErrorAction Stop | Where-Object {{$_.IPAddress -notlike '169.254.*'}} | Select-Object -First 1).IPAddress"]
                result = subprocess.run(
                    command, capture_output=True, text=True, timeout=8, check=False)
                candidates = result.stdout.strip().splitlines()
                def usable(value: str) -> bool:
                    try:
                        packed = socket.inet_aton(value.strip())
                    except OSError:
                        return False
                    first, second = packed[0], packed[1]
                    # A few WWAN drivers transiently expose a netmask-looking value through
                    # CIM. Only unicast source addresses are valid for a bound data socket.
                    return (first not in (0, 127) and first < 224 and
                            not (first == 169 and second == 254))
                # Some WWAN miniports make Get-NetIPAddress fail with a generic CIM error
                # while netsh and the actual adapter both have a valid address. Use the
                # independent Windows IPv4 store as a deterministic fallback.
                candidates = [value.strip() for value in candidates if usable(value)]
                if not candidates:
                    fallback = subprocess.run(
                        ["netsh", "interface", "ipv4", "show", "addresses",
                         f"name={interface}"],
                        capture_output=True, text=True, timeout=8, check=False)
                    candidates = re.findall(
                        r"(?<![\d.])(?:\d{1,3}\.){3}\d{1,3}(?![\d.])",
                        fallback.stdout)
                for value in candidates:
                    value = value.strip()
                    if usable(value):
                        return value
                return ""
            elif sys.platform == "darwin":
                command = ["ipconfig", "getifaddr", interface]
            else:
                command = ["sh", "-c", "ip -4 -o addr show dev \"$1\" | awk '{print $4}' | cut -d/ -f1 | head -1", "sh", interface]
            result = subprocess.run(command, capture_output=True, text=True, timeout=8, check=False)
            value = result.stdout.strip().splitlines()[0] if result.stdout.strip() else ""
            socket.inet_aton(value)
            return value
        except Exception:
            return ""

    def _modem_transport_present(self) -> bool:
        """Check the already-open serial attachment without guessing a replacement port."""
        port = str(getattr(self.modem, "port_name", "") or "").strip()
        if not port or port.casefold() == "auto" or list_ports is None:
            return True
        try:
            expected = os.path.normcase(port)
            return any(os.path.normcase(str(item.device)) == expected
                       for item in list_ports.comports())
        except Exception:
            # Enumeration failure is not proof of unplug; the serial I/O path will make the
            # final decision. This check may revoke, but must never manufacture, presence.
            return True

    def _enrich_at_status(self, values: dict) -> None:
        for command in ("AT+CEREG?", "AT+CREG?"):
            try:
                raw = self.modem._at(command).decode("ascii", "replace")
                match = re.search(r"\+(?:CE|C)REG:\s*\d+\s*,\s*(\d+)", raw)
                if match:
                    values["registration"] = {
                        "0": "unregistered", "1": "home", "2": "searching",
                        "3": "denied", "4": "unknown", "5": "roaming",
                    }.get(match.group(1), "unknown")
                    break
            except Exception:
                log.debug("AT registration query failed: %s", command, exc_info=True)
        try:
            raw = self.modem._at("AT+COPS?").decode("ascii", "replace")
            match = re.search(r'\+COPS:\s*\d+(?:,\d+,"([^"]*)")?', raw)
            values["operator"] = match.group(1) if match and match.group(1) else ""
        except Exception:
            log.debug("AT+COPS query failed", exc_info=True)
        try:
            raw = self.modem._at("AT+CSQ").decode("ascii", "replace")
            match = re.search(r"\+CSQ:\s*(\d+)", raw)
            if match and int(match.group(1)) <= 31:
                values["signal"] = round(int(match.group(1)) * 100 / 31)
        except Exception:
            log.debug("AT+CSQ query failed", exc_info=True)
        try:
            raw = self.modem._at("AT+CFUN?").decode("ascii", "replace")
            match = re.search(r"\+CFUN:\s*(\d+)", raw)
            if match:
                values["radio_enabled"] = match.group(1) == "1"
        except Exception:
            log.debug("AT+CFUN query failed", exc_info=True)
        values["sms_service_center"] = self.modem.service_centre()
        values["sms_service_center_changed"] = self.modem.smsc_changed()
        values["sms_service_center_advisory"] = (
            "The SMS centre differs from the last successful send; check with your "
            "carrier if sends fail." if values["sms_service_center_changed"] else "")

        # Generic AT transports (including the macOS private raw-USB backend) do not have a
        # platform-provider snapshot to publish runtime SMS/voice readiness.  Reuse the same
        # UICC and bearer maintainers used by platform providers so the server does not turn
        # an absent field into a false "call signalling is not ready" state.  These probes are
        # cached and bounded; they never dial, answer, or submit a message.
        capabilities = getattr(self.modem, "capabilities", {})
        try:
            uicc = self.modem.uicc_health_status(allow_repair=False)
        except Exception as exc:
            uicc = {"ready": None, "reason": f"SIM readiness probe failed: {exc}"}
        values["uicc_health"] = uicc

        values["sms_ready"] = bool(capabilities.get("sms"))
        values["sms_error"] = ""
        if capabilities.get("sms") and uicc.get("ready") is not False:
            try:
                sms = self.modem.sms_submit_readiness()
                if sms.get("ready") is False:
                    values["sms_ready"] = False
                    values["sms_error"] = str(
                        sms.get("reason") or "SMS bearer unavailable")
            except Exception as exc:
                values["sms_ready"] = False
                values["sms_error"] = f"SMS bearer probe failed: {exc}"
        elif uicc.get("ready") is False:
            values["sms_ready"] = False
            values["sms_error"] = str(uicc.get("reason") or "SIM is unavailable")

        values["call_ready"] = False
        values["call_error"] = "Cellular call bearer is unavailable"
        if capabilities.get("call") and uicc.get("ready") is not False:
            try:
                voice = self.modem.voice_registration_status(allow_repair=False)
                values["voice_registration"] = voice
                values["call_ready"] = voice.get("ready") is True
                values["call_error"] = ("" if values["call_ready"] else str(
                    voice.get("reason") or "Cellular call bearer is unavailable"))
            except Exception as exc:
                values["call_error"] = f"Voice registration probe failed: {exc}"
        elif uicc.get("ready") is False:
            values["call_error"] = str(uicc.get("reason") or "SIM is unavailable")

    def _private_data_owner_active(self) -> bool:
        return bool(self.dial_backend is not None and self.cellular_active.is_set())

    def _private_data_bootstrap_connect_released(self) -> bool:
        if self.dial_backend is None or not getattr(self.modem, "private_raw_usb", False):
            return False
        if self.cellular_active.is_set():
            return False
        link_state = str(getattr(self.dial_backend, "link_state", "") or "").casefold()
        return bool(self._private_data_release_proven.is_set() and link_state == "down")

    def _private_data_owner_probe_allowed(self) -> bool:
        return self._private_data_bootstrap_connect_released()

    def _bootstrap_private_data_release(self) -> None:
        if self.dial_backend is None or not getattr(self.modem, "private_raw_usb", False):
            return
        if self._paid_call_armed() or self._paid_call_safety_hold():
            return
        try:
            self.dial_backend.disable()
        except Exception as exc:
            self._private_data_release_proven.clear()
            raise ModemError(
                f"private cellular bootstrap disable failed: {exc}") from exc
        self.cellular_active.clear()
        self._private_data_release_proven.set()

    def _private_data_sim_guard_active(self, *, allow_bootstrap_connect: bool = False) -> bool:
        """Return True until private raw-USB data ownership is explicitly released.

        ``cellular_active`` is only a liveness hint.  A failed DATA_ENABLE/DATA_DISABLE, a
        companion crash, or a transient ``link_state=down`` sample is not proof that it is safe
        for ordinary AT/VPCD/SMS/call probing to reclaim the SIM lane.  A successful
        DATA_DISABLE response is the release proof for non-paid operations.
        """
        if self.dial_backend is None or not getattr(self.modem, "private_raw_usb", False):
            return False
        if allow_bootstrap_connect and self._private_data_bootstrap_connect_released():
            return False
        if not self.data_reconciled.is_set():
            return True
        if self.cellular_active.is_set():
            return True
        link_state = str(getattr(self.dial_backend, "link_state", "") or "").casefold()
        if link_state in {"starting", "connecting", "up"}:
            return True
        return not self._private_data_release_proven.is_set()

    def _private_data_owner_unavailable(self, method: str) -> dict:
        return {
            "ok": False,
            "unavailable": True,
            "degraded": True,
            "paused": True,
            "status": "cellular_data_active",
            "reason": "cellular_data_active",
            "method": method,
            "retry_after": 10,
            "error": (
                "Private cellular data currently owns this modem SIM channel; "
                "AT/SIM operations are paused until data is disabled."
            ),
        }

    def _status(self, *, allow_private_probe: bool = False) -> dict:
        values = {"sim": "ready" if self.modem.connection else "offline",
                  "data": "disconnected", "data_active": False,
                  "proxy": {"ready": bool(self.socks_server and self.socks_server.ready)},
                  "roaming_allowed": self.allow_roaming,
                  "interface": "", "registration": "unknown", "operator": "",
                  "signal": None, "radio_enabled": None}
        audio_probe = getattr(self.modem, "call_audio_probe", CallAudioProbe())
        modem_capabilities = getattr(self.modem, "capabilities", {})
        values["call_audio_ready"] = bool(
            modem_capabilities.get("call_audio") and audio_probe.ready)
        values["call_audio_backend"] = audio_probe.backend
        values["call_audio_error"] = "" if values["call_audio_ready"] else audio_probe.reason
        values["profile"] = self.selected_profile
        if self.dial_backend is not None:
            isolation_ready = bool(getattr(self.dial_backend, "isolation_ready", False))
            ready = self.dial_backend.link_state == "up" and isolation_ready
            values.update({
                "interface": "private-userspace",
                "data": "connected" if ready else "disconnected",
                "data_active": ready,
                "proxy": {"ready": ready, "port": 1, "udp": True,
                          "transport": "private_dial"},
                "isolation": {
                    "ready": isolation_ready, "mode": "private-userspace",
                    "host_interface": False,
                    "error": "" if isolation_ready else str(getattr(
                        self.dial_backend, "isolation_error", "isolation_not_proven")),
                },
            })
            if (self._private_data_sim_guard_active() and
                    not (allow_private_probe and
                         self._private_data_owner_probe_authorized)):
                pause_error = (
                    "Private cellular data currently owns this modem SIM channel; "
                    "AT/SIM status probes are paused until data is disabled."
                )
                values.update({
                    "at_probes_paused": True,
                    "probes_paused": True,
                    "pause_reason": "cellular_data_active",
                    "uicc_health": {
                        "ready": None,
                        "degraded": True,
                        "paused": True,
                        "reason": pause_error,
                    },
                    "sms_ready": False,
                    "sms_error": pause_error,
                    "sms_readiness_authoritative": False,
                    "call_ready": False,
                    "call_error": pause_error,
                    "voice_registration": {
                        "ready": False,
                        "degraded": True,
                        "paused": True,
                        "reason": pause_error,
                    },
                    "sim_apdu_ready": False,
                    "sim_apdu_error": pause_error,
                })
                return values
            values["at_probes_paused"] = False
            self._enrich_at_status(values)
            static_apdu = bool(modem_capabilities.get("sim_apdu"))
            values["sim_apdu_ready"] = static_apdu
            values["sim_apdu_error"] = ("" if static_apdu else
                                         "SIM APDU access is unavailable")
            self.modem.operator = str(values.get("operator") or "")
            return values
        source_lost = False
        try:
            interface = self._cellular_interface()
            values["interface"] = interface
            # A running SOCKS server was created only after this address existed and the WFP
            # guard verified the same interface. Reuse that established data-context binding
            # for status; repeated Windows address queries are advisory and can fail inside a
            # packaged process even while the bound socket and MBN connection remain healthy.
            raw_established_ip = getattr(self.socks_server, "source_ip", "")
            established_ip = raw_established_ip if isinstance(raw_established_ip, str) else ""
            if established_ip:
                observed_ip = self._cellular_ip(interface)
                if observed_ip == established_ip:
                    self._source_miss_count = 0
                else:
                    self._source_miss_count += 1
                if self._source_miss_count >= 3:
                    # One failed Windows query is common; three consecutive independent
                    # samples are a revoked attachment. Tear down only the data-plane objects
                    # created by this Agent generation, then let desired-state reconciliation
                    # rebuild them if the bearer returns.
                    server, self.socks_server = self.socks_server, None
                    if server:
                        server.close()
                    self.isolation.close()
                    self._isolation_armed = False
                    self._source_miss_count = 0
                    source_lost = True
                    established_ip = ""
                    if not self._modem_transport_present():
                        self.modem.close()
            else:
                self._source_miss_count = 0
            source_ip = established_ip or ("" if source_lost else self._cellular_ip(interface))
            if source_ip:
                values["data"] = "connected"
                values["data_active"] = True
                values["ip"] = source_ip
            elif self.socks_server:
                # Status collection is observational. A transient Windows query failure must
                # not mutate the data plane; report fail-closed and let the idempotent desired-
                # state reconciler decide whether to reconnect. The WFP guard and source-bound
                # socket continue to prevent fallback to another interface in the meantime.
                values["proxy"] = {"ready": False}
                values["cellular"] = {
                    "ok": False, "status": "unavailable",
                    "error": "The cellular address could not be confirmed.",
                    "proxy": {"ready": False},
                }
            platform_provider = getattr(self.modem, "platform_provider", None)
            if platform_provider:
                platform = platform_provider.status()
                # A status sample is observational.  Recovery (including CFUN cycling) belongs
                # only to explicit, bounded maintenance paths; a transient platform query must
                # never mutate radio state merely because the UI refreshed.
                uicc = self.modem.uicc_health_status(allow_repair=False)
                values["uicc_health"] = uicc
                values.update({key: platform[key] for key in (
                    "registration", "operator", "operator_id", "signal", "radio_enabled",
                    "data", "data_active", "profile", "provider", "owner",
                ) if key in platform})
                platform_sms_ready = platform.get("sms_ready")
                values["sms_ready"] = (platform_sms_ready
                                       if isinstance(platform_sms_ready, bool) else None)
                values["sms_error"] = str(platform.get("sms_error") or "")
                if uicc.get("ready") is False:
                    values["sim"] = "error"
                    values["sms_ready"] = False
                    values["sms_error"] = str(uicc.get("reason") or "SIM is unavailable")
                values["sms_service_center"] = str(platform.get("sms_service_center") or "")
                if self._is_sms_configuration_pending(values["sms_error"]):
                    refreshed = self._refresh_sms_configuration()
                    if refreshed.get("ok"):
                        values["sms_service_center"] = str(
                            refreshed.get("service_center") or "")
                        values["sms_ready"] = True
                        values["sms_error"] = ""
                values["sms_service_center_changed"] = self.modem.smsc_changed()
                values["sms_service_center_advisory"] = (
                    "The SMS centre differs from the last successful send; check with your "
                    "carrier if sends fail." if values["sms_service_center_changed"] else "")
                values["sms_provider"] = str(platform.get("sms_provider") or "")
                restart = self._windows_restart_target()
                refresh_recommended = self._is_sms_configuration_pending(values["sms_error"])
                restart_recommended = bool(refresh_recommended and
                                           self._sms_refresh_failures >= 3 and
                                           restart.get("available"))
                values["recovery"] = {
                    "refresh": {
                        "available": callable(getattr(platform_provider,
                                                      "sms_configuration", None)),
                        "recommended": refresh_recommended,
                        "reason": (self._sms_refresh_error or
                                   "Windows is still initializing the SMS configuration.")
                                  if refresh_recommended else "",
                        "failures": self._sms_refresh_failures,
                    },
                    "soft_restart": {
                        "available": bool(restart.get("available")),
                        "recommended": restart_recommended,
                        "reason": ("SMS configuration remained blocked after bounded refreshes."
                                   if restart_recommended else str(restart.get("reason") or "")),
                        "disruptive": True,
                    },
                }
                values["call_ready"] = False
                values["call_error"] = "Cellular call bearer is unavailable"
                authoritative_sms = bool(platform.get("sms_readiness_authoritative"))
                if self.modem.capabilities.get("call") and uicc.get("ready") is not False:
                    voice = self.modem.voice_registration_status(allow_repair=False)
                    values["voice_registration"] = voice
                    values["call_ready"] = bool(
                        self.modem.capabilities.get("call") and
                        voice.get("ready") is True)
                    call_reason = str(voice.get("reason") or
                                      "Cellular call bearer is unavailable")
                    values["call_error"] = "" if values["call_ready"] else call_reason
                elif uicc.get("ready") is False:
                    values["call_error"] = str(
                        uicc.get("reason") or "SIM is unavailable")
                if not authoritative_sms and self.modem.capabilities.get("sms"):
                    readiness = self.modem.sms_submit_readiness()
                    if readiness.get("ready") is False:
                        values["sms_ready"] = False
                        values["sms_error"] = str(
                            readiness.get("reason") or "SMS bearer unavailable")
                    elif readiness.get("ready") is not False:
                        # Unknown means the platform must decide during the explicit user
                        # submission; it is not proof of unavailability and is never retried.
                        values["sms_ready"] = True
            else:
                self._enrich_at_status(values)
            if os.name == "nt":
                if (self.modem.sim_via_mbn and interface and
                        not getattr(self.modem, "platform_provider", None)):
                    radio = subprocess.run(
                        ["netsh", "mbn", "show", "radio", f"interface={interface}"],
                        capture_output=True, text=True, timeout=8, check=False).stdout
                    software = re.search(r"Software\s+radio\s+state\s*:\s*(On|Off)",
                                         radio, re.I)
                    if software:
                        values["radio_enabled"] = software.group(1).casefold() == "on"
                command = ["netsh", "mbn", "show", "interfaces"]
                if interface:
                    command = ["netsh", "mbn", "show", "interface", f"name={interface}"]
                result = subprocess.run(command,
                                        capture_output=True, text=True, timeout=8, check=False)
                text = result.stdout
                if re.search(r"State\s*:\s*connected", text, re.I):
                    values["data"] = "connected"
                    values["data_active"] = True
        except Exception:
            log.debug("status() diagnostic enrichment failed", exc_info=True)
        if source_lost:
            # Native providers may retain a last-known "connected" snapshot briefly after USB
            # removal. The revoked source binding is authoritative for this status sample.
            values.update(data="disconnected", data_active=False,
                          proxy={"ready": False})
            values.pop("ip", None)
            values["cellular"] = {
                "ok": False, "status": "unavailable",
                "error": "The cellular attachment disappeared.",
                "proxy": {"ready": False},
            }
        static_apdu = bool(modem_capabilities.get("sim_apdu"))
        apdu_paused = bool(getattr(self.modem, "sim_via_mbn", False) and
                           values.get("data_active"))
        values["sim_apdu_ready"] = bool(static_apdu and not apdu_paused)
        values["sim_apdu_error"] = (
            "SIM APDU access is paused while Windows cellular data owns the SIM"
            if static_apdu and apdu_paused else
            "SIM APDU access is unavailable" if not static_apdu else "")
        self.modem.operator = str(values.get("operator") or "")
        return values

    def _capabilities_snapshot(self) -> dict:
        """Publish the current complete contract, including immutable call safety support."""
        capabilities = getattr(self.modem, "capabilities", {})
        call_control = bool(capabilities.get("call"))
        call_audio = bool(capabilities.get("call_audio"))
        audio_probe = getattr(self.modem, "call_audio_probe", CallAudioProbe())
        audio_telemetry_version = 0
        try:
            audio_telemetry_version = int(
                (getattr(audio_probe, "details", {}) or {}).get("helper_version") or 0)
        except (TypeError, ValueError):
            audio_telemetry_version = 0
        return {
            "sms": bool(capabilities.get("sms")),
            "call_control": call_control,
            # A voice service is usable only when signalling and local PCM transport both
            # passed. This value may change after macOS grants TCC without reconnecting.
            "call_signalling": bool(call_control and call_audio),
            "call_audio": call_audio,
            "paid_call_lease_version": 1,
            "call_contract": {
                "version": CALL_CONTRACT_VERSION,
                "audio_telemetry_version": audio_telemetry_version,
                "package_version": _agent_package_version(),
                "package_digest": _agent_package_digest(),
            },
            "sim_apdu": bool(capabilities.get("sim_apdu") or not self.modem.sim_via_mbn),
            "cellular_data": bool(capabilities.get("cellular_data")),
            "socks5_udp": True,
        }

    @staticmethod
    def _windows_profile_names(output: str) -> list[str]:
        names = [value.strip() for value in re.findall(
            r"^\s*(?:All User Profile|所有用户配置文件)\s*:\s*(.+?)\s*$",
            output or "", re.I | re.M) if value.strip()]
        if names:
            return names
        after_separator = False
        for line in str(output or "").splitlines():
            value = line.strip()
            if value and set(value) == {"-"}:
                after_separator = True
            elif after_separator and value and value.lower() not in {"<none>", "<无>"}:
                names.append(value)
        return names

    def _modem_profile_candidates(self) -> list[dict]:
        """Read non-service PDP contexts and any active network-assigned APN.

        If the modem reports no stored contexts, also query ``AT+CGCONTRDP`` for any
        currently active context assigned by the network.  This is a read-only fallback that
        lets the operator see the APN the network chose, which is often enough to create a
        matching profile without guessing.  3GPP TS 27.007 §10.1.23 mandates the first three fields
        are ``cid``, ``bearer_id`` and ``apn``; the rest is variable and ignored here.
        """
        reserved = {"ims", "sos", "emergency", "mms", "supl", "xcap"}
        values: list[dict] = []
        try:
            raw = self.modem._at("AT+CGDCONT?").decode("utf-8", "replace")
        except Exception:
            log.debug("AT+CGDCONT query failed", exc_info=True)
            raw = ""
        for match in re.finditer(
                r'^\s*\+CGDCONT:\s*(\d+)\s*,\s*"([^"]*)"\s*,\s*"([^"]*)"',
                raw, re.I | re.M):
            cid, pdp_type, apn = int(match.group(1)), match.group(2).strip(), match.group(3).strip()
            leaf = apn.lower().split(".", 1)[0]
            if apn and leaf not in reserved and not any(item["apn"] == apn for item in values):
                values.append({"id": f"pdp-{cid}", "source": "modem", "cid": cid,
                               "name": f"{apn} (CID {cid})", "apn": apn,
                               "pdp_type": pdp_type or "IP", "auth": "NONE",
                               "username": ""})
        if values:
            return values
        try:
            raw = self.modem._at("AT+CGCONTRDP").decode("utf-8", "replace")
        except Exception:
            log.debug("AT+CGCONTRDP fallback failed", exc_info=True)
            return values
        for match in re.finditer(
                r'^\s*\+CGCONTRDP:\s*(\d+)\s*,\s*\d+\s*,\s*"([^"]*)"',
                raw, re.I | re.M):
            cid, apn = int(match.group(1)), match.group(2).strip()
            leaf = apn.lower().split(".", 1)[0]
            if apn and leaf not in reserved and not any(item["apn"] == apn for item in values):
                values.append({"id": f"network-{cid}", "source": "network", "cid": cid,
                               "name": f"{apn} (network assigned)", "apn": apn,
                               "pdp_type": "IP", "auth": "NONE",
                               "username": ""})
        return values

    def _modem_apn_candidates(self) -> list[str]:
        return [item["apn"] for item in self._modem_profile_candidates()]

    def _provider_apn_candidates(self) -> list[dict]:
        """Look up public-domain APN candidates for the current SIM's MCC/MNC.

        mobile-broadband-provider-info is a Creative Commons public-domain dataset used by
        NetworkManager/ModemManager.  Entries are keyed by MCC/MNC and limited to APNs whose
        usage is ``internet``.  The returned list is advisory: it is never used to auto-
        provision, only to offer the operator a starting point.
        """
        imsi = str(getattr(self.modem, "imsi", "") or "")
        return lookup_by_imsi(imsi) if imsi else []

    def _apn_guidance(self, names: list[str]) -> str:
        """Explain exactly why no data profile could be selected.

        "No mobile-broadband profile is configured" is a dead end for a SIM whose operator is
        not in any local APN database: it neither says whether the modem offered candidates
        nor which network is being attached.  Report the observed facts instead, so the
        operator can supply the APN once rather than retry blindly.  This uses only values
        already known to this Agent; a connect attempt must not add fresh AT round trips.
        """
        candidates = self._modem_apn_candidates()
        imsi = str(getattr(self.modem, "imsi", "") or "")
        operator = str(getattr(self.modem, "operator", "") or "")
        # MCC plus the first MNC digits identify the network, not the subscriber.  The MNC
        # length is carrier specific, so present the prefix without asserting a split.
        network = f"MCC/MNC {imsi[:5]}" if len(imsi) >= 5 else "an unidentified network"
        if operator:
            network = f"{operator} ({network})"
        if len(names) > 1:
            return ("More than one mobile-broadband profiles exist "
                    f"({', '.join(names[:5])}) for {network}; select one in MDD.")[:300]
        if len(candidates) > 1:
            return (f"This modem reports {len(candidates)} APN candidates "
                    f"({', '.join(candidates[:5])}) for {network}; select or enter one under "
                    "4G network / APN in MDD.")[:300]
        provider = [item["apn"] for item in self._provider_apn_candidates()]
        if not candidates and provider:
            return (f"No mobile-broadband profile is configured for {network}, but the public "
                    f"APN database suggests {', '.join(provider[:5])}. Select one or enter the "
                    "APN supplied by this SIM's carrier under 4G network / APN in MDD.")[:300]
        return ("No mobile-broadband profile is configured, and this modem reports no usable "
                f"APN for {network}. Enter the APN supplied by this SIM's carrier under "
                "4G network / APN in MDD.")[:300]

    def _cellular_profiles(self) -> dict:
        interface = self._cellular_interface()
        if not interface:
            raise ModemError("No mobile-broadband interface matches this modem.")
        suggested_profiles = self._modem_profile_candidates()
        existing_apns = {item["apn"] for item in suggested_profiles}
        for item in self._provider_apn_candidates():
            if item["apn"] not in existing_apns:
                suggested_profiles.append({
                    "id": f"provider-{item['apn']}",
                    "source": "provider",
                    "name": f"{item['name']} ({item['apn']})",
                    "apn": item["apn"],
                    "pdp_type": "IP",
                    "auth": "NONE",
                    "username": "",
                })
                existing_apns.add(item["apn"])
        suggested = [item["apn"] for item in suggested_profiles]
        if os.name == "nt":
            result = subprocess.run(
                ["netsh", "mbn", "show", "profiles", f"interface={interface}"],
                capture_output=True, text=True, timeout=10, check=False)
            names = self._windows_profile_names(result.stdout)
            # Windows returns exit code 1 with completely empty output when the interface is
            # valid but has no profiles, and may also return 1 while printing a valid list.
            # Parsed profiles are the authoritative postcondition; only unparseable diagnostic
            # output is a real failure.
            if result.returncode and not names and str(result.stderr or result.stdout).strip():
                raise ModemError(str(result.stderr or result.stdout).strip()[:300])
        elif sys.platform == "darwin":
            return {"ok": True, "platform": "macos", "supported": False,
                    "system_managed": True, "suggested_apns": suggested,
                    "suggested_profiles": suggested_profiles, "profiles": [],
                    "error": "This macOS adapter must be provisioned by its system or vendor network service."}
        else:
            result = subprocess.run(
                ["nmcli", "-t", "-f", "NAME,TYPE", "connection", "show"],
                capture_output=True, text=True, timeout=10, check=False)
            if result.returncode:
                raise ModemError(str(result.stderr or result.stdout).strip()[:300])
            names = [line.rsplit(":", 1)[0].replace("\\:", ":") for line in
                     result.stdout.splitlines() if line.rsplit(":", 1)[-1] == "gsm"]
        return {"ok": True, "platform": "windows" if os.name == "nt" else "linux",
                "supported": True, "selected": self.selected_profile,
                "suggested_apns": suggested, "suggested_profiles": suggested_profiles,
                "profiles": [{"name": name} for name in names]}

    def _save_cellular_profile(self, params: dict) -> dict:
        interface = self._cellular_interface()
        name = str(params.get("name") or "").strip()
        apn = str(params.get("apn") or "").strip()
        auth = str(params.get("auth") or "NONE").upper()
        username = str(params.get("username") or "")
        password = str(params.get("password") or "")
        if not interface:
            raise ModemError("No mobile-broadband interface matches this modem.")
        if not name or len(name) > 100 or not apn or len(apn) > 100:
            raise ModemError("profile name and APN are required and must not exceed 100 characters")
        if auth not in {"NONE", "PAP", "CHAP", "MSCHAPV2"}:
            raise ModemError("unsupported mobile-broadband authentication method")
        if os.name == "nt":
            content = windows_mbn_profile_xml(
                name, self.modem.imsi, apn, auth, username, password)
            path = ""
            try:
                with tempfile.NamedTemporaryFile(
                        mode="w", suffix=".xml", encoding="utf-8", delete=False) as handle:
                    handle.write(content)
                    path = handle.name
                result = subprocess.run(
                    ["netsh", "mbn", "add", "profile", f"interface={interface}",
                     f"name={path}"], capture_output=True, text=True, timeout=20, check=False)
            finally:
                if path:
                    try:
                        os.unlink(path)
                    except OSError:
                        pass
        elif sys.platform == "darwin":
            raise ModemError("Mobile-broadband profile management is not available for this macOS adapter.")
        else:
            existing = {item["name"] for item in self._cellular_profiles()["profiles"]}
            command = (["nmcli", "connection", "modify", name] if name in existing else
                       ["nmcli", "connection", "add", "type", "gsm", "ifname", "*",
                        "con-name", name])
            command += ["gsm.apn", apn, "gsm.username", username,
                        "gsm.password", password, "gsm.auth-type",
                        {"NONE": "none", "PAP": "pap", "CHAP": "chap",
                         "MSCHAPV2": "mschapv2"}[auth]]
            result = subprocess.run(command, capture_output=True, text=True,
                                    timeout=20, check=False)
        if result.returncode:
            # netsh can create/update the MBN profile and still exit 1 with no output. Verify
            # the postcondition before reporting failure so a successful side effect is never
            # presented as safe to retry.
            if os.name == "nt":
                current = {item["name"] for item in self._cellular_profiles()["profiles"]}
                if name not in current:
                    detail = str(result.stderr or result.stdout).strip()
                    raise ModemError((detail or
                                      f"netsh mbn add profile exited {result.returncode} without diagnostic output")[:300])
            else:
                raise ModemError(str(result.stderr or result.stdout).strip()[:300])
        self.selected_profile = name
        return {"ok": True, "name": name, "apn": apn,
                "platform": "windows" if os.name == "nt" else "linux"}

    def _connect_cellular(self, interface: str) -> str:
        """Ask the platform to activate an existing operator-approved data profile."""
        try:
            if os.name == "nt":
                # Windows keeps data enablement and roaming permission outside the MBN
                # profile.  A valid APN profile can therefore still fail with 0x139f when
                # the Settings app left Internet roaming at "Home carrier only".
                for command in (
                    ["netsh", "mbn", "set", "dataenablement", f"interface={interface}",
                     "profileset=internet", "mode=yes"],
                    ["netsh", "mbn", "set", "dataroamcontrol", f"interface={interface}",
                     "profileset=internet", "state=all" if self.allow_roaming else "state=none"],
                ):
                    policy = subprocess.run(command, capture_output=True, text=True,
                                            timeout=15, check=False)
                    # Like profile creation, current Windows netsh can apply these policy
                    # changes and still exit 1 with no output.  A real diagnostic remains a
                    # failure; an empty one is followed by the connect postcondition below.
                    detail = str(policy.stderr or policy.stdout).strip()
                    if policy.returncode and detail:
                        return detail[:300]
                listing = subprocess.run(
                    ["netsh", "mbn", "show", "profiles", f"interface={interface}"],
                    capture_output=True, text=True, timeout=10, check=False)
                names = self._windows_profile_names(listing.stdout)
                profile = self.selected_profile or (names[0] if len(names) == 1 else "")
                if not profile and not names:
                    candidates = self._modem_apn_candidates()
                    if len(candidates) == 1:
                        profile = f"MDD-Auto-{self.modem.iccid[-4:]}"
                        self._save_cellular_profile({"name": profile, "apn": candidates[0],
                                                     "auth": "NONE"})
                if not profile:
                    return self._apn_guidance(names)
                platform_provider = getattr(self.modem, "platform_provider", None)
                if platform_provider:
                    native = platform_provider.connect(profile, interface)
                    if native.get("ok"):
                        self.selected_profile = profile
                        return ""
                    detail = ["Windows MBN connection failed"]
                    if native.get("hresult"):
                        detail.append(f"HRESULT {native['hresult']}")
                    if native.get("network_error"):
                        detail.append(f"network cause {native['network_error']}")
                    if native.get("activation_state"):
                        detail.append(str(native["activation_state"]))
                    if native.get("error"):
                        detail.append(str(native["error"]))
                    return ": ".join(detail)[:300]
                result = subprocess.run(
                    ["netsh", "mbn", "connect", f"interface={interface}",
                     "connmode=name", f"name={profile}"],
                    capture_output=True, text=True, timeout=30, check=False)
            elif sys.platform == "darwin":
                return "Automatic cellular activation is not available for this macOS adapter."
            else:
                result = subprocess.run(["nmcli", "device", "connect", interface],
                                        capture_output=True, text=True, timeout=30, check=False)
            return "" if result.returncode == 0 else str(result.stderr or result.stdout).strip()[:300]
        except (OSError, subprocess.TimeoutExpired) as exc:
            return str(exc)[:300]

    def _disconnect_cellular(self, interface: str) -> str:
        try:
            if os.name == "nt":
                platform_provider = getattr(self.modem, "platform_provider", None)
                if platform_provider:
                    current = platform_provider.status()
                    if not current.get("data_active"):
                        return ""
                    native = platform_provider.disconnect()
                    if native.get("ok"):
                        return ""
                    detail = str(native.get("error") or native.get("hresult") or
                                 "Windows MBN disconnect failed")
                    if not platform_provider.status().get("data_active"):
                        return ""
                    return detail[:300]
                result = subprocess.run(
                    ["netsh", "mbn", "disconnect", f"interface={interface}"],
                    capture_output=True, text=True, timeout=20, check=False)
            elif sys.platform == "darwin":
                result = subprocess.run(["networksetup", "-setnetworkserviceenabled",
                                         interface, "off"], capture_output=True, text=True,
                                        timeout=20, check=False)
            else:
                result = subprocess.run(["nmcli", "device", "disconnect", interface],
                                        capture_output=True, text=True, timeout=20, check=False)
            if result.returncode == 0:
                return ""
            detail = str(result.stderr or result.stdout).strip()[:300]
            # Windows reports "Context Not Activated" when disconnecting an already-off
            # profile.  The missing interface address is the authoritative postcondition.
            if os.name == "nt" and not self._cellular_ip(interface):
                return ""
            return detail
        except (OSError, subprocess.TimeoutExpired) as exc:
            return str(exc)[:300]

    def _watch_isolation(self):
        """Tear down every managed data socket if the privileged guard disappears."""
        while not self.stop.wait(0.5):
            if self.isolation.active:
                self._isolation_armed = True
                # The native guard and source-bound sockets are the security authorities.
                # Re-running platform interface discovery here is both redundant and unsafe:
                # Windows MBN can return an empty list during routine provider refreshes,
                # which used to flap a perfectly guarded bearer. If the interface genuinely
                # disappears, its bound source address cannot fall back to another route and
                # the regular status path closes the proxy when the address is gone.
                continue
            elif not self._isolation_armed:
                continue
            server, self.socks_server = self.socks_server, None
            if server:
                server.close()
            interface = self.isolation.interface or self._cellular_interface()
            if interface:
                self._disconnect_cellular(interface)
            self.isolation.close()
            self._isolation_armed = False

    def _cellular_failure(self, interface: str, error: str, isolation: dict) -> dict:
        server, self.socks_server = self.socks_server, None
        if server:
            server.close()
        self._disconnect_cellular(interface)
        self.isolation.close()
        self._isolation_armed = False
        return {"ok": False, "status": "unavailable", "unavailable": True,
                "error": error, "isolation": {**isolation, "ready": False},
                "proxy": {"ready": False}}

    def _cellular_ensure(self, params: dict) -> dict:
        if "allow_roaming" in params:
            self.allow_roaming = bool(params.get("allow_roaming"))
        if self.dial_backend is not None:
            owner_probe_allowed = self._private_data_owner_probe_allowed()
            self._private_data_release_proven.clear()
            if not getattr(self.dial_backend, "isolation_ready", False):
                error = str(getattr(
                    self.dial_backend, "isolation_error", "isolation_not_proven"))
                return {"ok": False, "status": "unavailable", "unavailable": True,
                        "error": error, "proxy": {"ready": False},
                        "isolation": {"ready": False, "mode": "private-userspace",
                                      "error": error}}
            try:
                self.dial_backend.qualify()
            except Exception as exc:
                error = f"isolation_not_proven: {exc}"
                self.dial_backend.revoke(error)
                return {"ok": False, "status": "unavailable", "unavailable": True,
                        "error": error, "proxy": {"ready": False},
                        "isolation": {"ready": False, "mode": "private-userspace",
                                      "error": error}}
            network = {"registration": "unknown"}
            if owner_probe_allowed:
                self.cellular_active.set()
                self._private_data_owner_probe_authorized = True
                try:
                    network = self._status(allow_private_probe=True)
                finally:
                    self._private_data_owner_probe_authorized = False
            if network.get("registration") == "roaming" and not self.allow_roaming:
                self.dial_backend.disable()
                self.cellular_active.clear()
                self._private_data_release_proven.set()
                return {"ok": False, "status": "unavailable", "unavailable": True,
                        "error": "Data roaming is disabled for this SIM.",
                        "registration": "roaming", "roaming_allowed": False,
                        "proxy": {"ready": False}}
            try:
                # Reserve the SIM/control domain before PPP negotiation starts.
                # The raw-USB VPCD bridge observes this event and stops issuing
                # AT+CSIM while the modem establishes or owns packet data.
                self.cellular_active.set()
                self.dial_backend.enable()
            except Exception as exc:
                stopped = False
                cleanup_error = ""
                try:
                    self.dial_backend.disable()
                    stopped = True
                except Exception as cleanup_exc:
                    cleanup_error = str(cleanup_exc).strip()
                try:
                    backend_disconnected = (
                        self.dial_backend.disconnected.is_set() is True)
                except Exception:
                    backend_disconnected = False
                # A cached ``down`` link_state may predate this enable attempt:
                # the companion can still be scanning USB interfaces and later
                # establish PPP.  After a failed compensating disable, only the
                # backend's explicit disconnected event proves that the SIM can
                # safely return to the APDU/VPCD owner.
                if stopped or backend_disconnected:
                    self.cellular_active.clear()
                may_be_active = self.cellular_active.is_set()
                detail = str(exc)
                if cleanup_error:
                    detail = f"{detail}; PPP cleanup not confirmed: {cleanup_error}"
                return {"ok": False, "status": "unavailable", "unavailable": True,
                        "error": detail,
                        "data": "connected" if may_be_active else "disconnected",
                        "data_active": may_be_active, "proxy": {"ready": False},
                        "isolation": {"ready": True, "mode": "private-userspace"}}
            return {"ok": True, "status": "ready", "data": "connected",
                    "data_active": True, "roaming_allowed": self.allow_roaming,
                    "proxy": {"ready": True, "host": "private", "port": 1,
                              "udp": True, "transport": "private_dial"},
                    "isolation": {"ready": True, "mode": "private-userspace",
                                  "host_interface": False}, "error": None}
        port = int(params.get("port") or self.args.socks_port)
        interface = self._cellular_interface()
        if not interface:
            return {"ok": False, "status": "unavailable", "unavailable": True,
                    "error": "No mobile-broadband interface matches this modem.",
                    "proxy": {"ready": False}}
        network = self._status()
        if network.get("registration") == "roaming" and not self.allow_roaming:
            self._disconnect_cellular(interface)
            return {"ok": False, "status": "unavailable", "unavailable": True,
                    "error": "Data roaming is disabled for this SIM.",
                    "registration": "roaming", "roaming_allowed": False,
                    "proxy": {"ready": False}}
        # Isolation must be installed before the OS is allowed to establish a default route;
        # otherwise there is a leak window between connection and policy installation.
        already_isolated = self.isolation.active
        isolation = self.isolation.ensure(interface, "")
        if not isolation.get("ready"):
            return {"ok": False, "status": "unavailable", "unavailable": True,
                    "error": isolation.get("error") or "Cellular isolation is not ready.",
                    "isolation": isolation, "proxy": {"ready": False}}
        # Connections that existed before MDD took ownership are not grandfathered in.  Cycle
        # the data context after WFP/netns/pf is active so every new flow is classified.
        if self._status()["data"] == "connected" and not already_isolated:
            problem = self._disconnect_cellular(interface)
            if problem:
                self.isolation.close()
                return {"ok": False, "status": "unavailable", "unavailable": True,
                        "error": f"Cannot reset the pre-existing cellular connection: {problem}",
                        "isolation": {"ready": False, "mode": "strict"},
                        "proxy": {"ready": False}}
            self._isolation_armed = True
        if self._status()["data"] != "connected":
            problem = self._connect_cellular(interface)
            if problem:
                return self._cellular_failure(interface, problem, isolation)
        source_ip = self._cellular_ip(interface)
        if not source_ip:
            return self._cellular_failure(
                interface, "The cellular interface has no usable IPv4 address.", isolation)
        isolation = self.isolation.ensure(interface, source_ip)
        if not isolation.get("ready"):
            return self._cellular_failure(
                interface, isolation.get("error") or "Cellular isolation is not ready.",
                isolation)
        if not self.socks_server or not self.socks_server.ready:
            try:
                self.socks_server = SocksServer("0.0.0.0", port, source_ip)
                self.socks_server.start()
            except OSError as exc:
                return self._cellular_failure(interface, str(exc)[:300], isolation)
        ready = self.socks_server.ready
        if not ready:
            return self._cellular_failure(
                interface, "Embedded SOCKS server failed", isolation)
        self.cellular_active.set()
        return {"ok": True, "status": "ready",
                "proxy": {"ready": ready, "host": self._advertise_host(),
                          "port": self.socks_server.port,
                          "udp": True}, "isolation": isolation,
                "error": None}

    def _cellular_disable(self) -> dict:
        if self.dial_backend is not None:
            try:
                self.dial_backend.disable()
                self.cellular_active.clear()
                self._private_data_release_proven.set()
                return {"ok": True, "status": "off", "data": "disconnected",
                        "data_active": False, "proxy": {"ready": False}, "error": None}
            except Exception as exc:
                may_be_active = (self.cellular_active.is_set() or
                                 getattr(self.dial_backend, "link_state", "") in
                                 {"starting", "connecting", "up"})
                return {"ok": False, "status": "unavailable",
                        "data": "connected" if may_be_active else "disconnected",
                        "data_active": may_be_active, "proxy": {"ready": False},
                        "error": str(exc)}
        server, self.socks_server = self.socks_server, None
        if server:
            server.close()
        interface = self.isolation.interface or self._cellular_interface()
        problem = self._disconnect_cellular(interface) if interface else ""
        self.isolation.close()
        self._isolation_armed = False
        if not problem:
            self.cellular_active.clear()
        may_be_active = bool(problem and self.cellular_active.is_set())
        return {"ok": not bool(problem),
                "status": "unavailable" if problem else "off",
                "data": "connected" if may_be_active else "disconnected",
                "data_active": may_be_active,
                "proxy": {"ready": False}, "error": problem or None}

    def _reverse_tunnel_source_ip(self) -> str:
        """Return the source address already admitted by the fail-closed data plane.

        The isolation watcher owns continuous liveness checking.  Re-running the Windows MBN
        and IP discovery commands for every SOCKS connection is both redundant and slow enough
        to race the gateway's tunnel handshake timeout.  The established SOCKS listener and
        guard therefore form one immutable admission snapshot; a dead guard tears both down,
        while a stale source address simply makes the bound outbound connect fail closed.
        """
        interface = self.isolation.interface
        server = self.socks_server
        if not server or not server.ready:
            raise OSError("cellular proxy is not enabled")
        if not self.isolation.active or not interface:
            raise OSError("cellular isolation is not active for this interface")
        source_ip = str(server.source_ip or "")
        if not source_ip:
            raise OSError("cellular proxy has no isolated source address")
        return source_ip

    def _connect_reverse_websocket(self, url: str):
        """Open one reverse data channel with a bounded handshake, then persistent reads.

        websocket-client keeps the connect timeout on the underlying socket.  Without clearing
        it, an otherwise healthy low-traffic UDP association is torn down every 20 seconds and
        sing-box continuously recreates it.  The gateway owns the bounded UDP idle lifetime;
        TCP lifetime follows either endpoint as usual.
        """
        tunnel = websocket.create_connection(url, timeout=20, sslopt={"cert_reqs": 0})
        tunnel.settimeout(None)
        return tunnel

    def _open_reverse_tunnel(self, message: dict, session_id: str) -> None:
        tunnel_id = str(message.get("id") or "")
        mode = str(message.get("mode") or "tcp")
        host = str(message.get("host") or "")
        port = int(message.get("port") or 0)
        if not tunnel_id or mode not in {"tcp", "udp"}:
            return

        def bridge():
            local = tunnel = None
            try:
                if self.dial_backend is not None:
                    local = (self.dial_backend.open_tcp(host, port) if mode == "tcp" else
                             self.dial_backend.open_udp())
                elif mode == "tcp":
                    source_ip = self._reverse_tunnel_source_ip()
                    target = socket.getaddrinfo(host, port, socket.AF_INET,
                                                socket.SOCK_STREAM)[0][-1]
                    local = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
                    local.bind((source_ip, 0))
                    local.settimeout(15)
                    local.connect(target)
                    local.settimeout(None)
                else:
                    source_ip = self._reverse_tunnel_source_ip()
                    local = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                    local.bind((source_ip, 0))
                query = urllib.parse.urlencode({"token": self.args.token,
                                                "session_id": session_id,
                                                "tunnel_id": tunnel_id})
                url = (f"wss://{self.args.host}:{self.args.gateway_port}"
                       f"/mdd/api/agent/modem/tunnel?{query}")
                tunnel = self._connect_reverse_websocket(url)
                if mode == "tcp":
                    def upload():
                        try:
                            while True:
                                data = local.recv(65536)
                                if not data:
                                    break
                                tunnel.send_binary(data)
                        except Exception:
                            pass
                        finally:
                            try:
                                tunnel.close()
                            except Exception:
                                pass

                    threading.Thread(target=upload, name="modem-tunnel-upload", daemon=True).start()
                    while True:
                        data = tunnel.recv()
                        if not isinstance(data, bytes) or not data:
                            break
                        local.sendall(data)
                else:
                    def upload_udp():
                        try:
                            while True:
                                payload, remote = local.recvfrom(65535)
                                tunnel.send_binary(
                                    b"\x00\x00\x00" + _encoded_address(remote[0], remote[1]) + payload)
                        except Exception:
                            pass

                    threading.Thread(target=upload_udp, name="modem-tunnel-udp", daemon=True).start()
                    while True:
                        packet = tunnel.recv()
                        if not isinstance(packet, bytes) or len(packet) < 10 or packet[:3] != b"\0\0\0":
                            break
                        target_host, target_port, offset = _packet_address(packet)
                        local.sendto(packet[offset:], (target_host, target_port))
            except Exception as exc:
                log.warning("Reverse cellular tunnel failed: %s", exc)
            finally:
                if local:
                    try:
                        local.close()
                    except Exception:
                        pass
                if tunnel:
                    try:
                        tunnel.close()
                    except Exception:
                        pass

        threading.Thread(target=bridge, name="modem-reverse-tunnel", daemon=True).start()

    def execute(self, method: str, params: dict) -> dict:
        if self.shutdown_started.is_set() and method in {"call.dial", "call.answer"}:
            return {"ok": False, "unavailable": True, "status": "shutting_down",
                    "error": "Agent shutdown has started; new paid calls are rejected."}
        if self.paid_call_active.is_set() and not method.startswith(("call.", "audio.")):
            with self._paid_call_lock:
                terminating = self._paid_call_termination_requested or self._paid_call_recovered
            # Preserve the stronger existing safety-hold response and allow an explicit radio
            # off during termination. All ordinary non-call work remains paused.
            if not (terminating and method in {"cellular.ensure", "radio.set"}):
                return {"ok": False, "unavailable": True, "status": "paid_call_active",
                        "error": "Non-call modem operations are paused during a paid call."}
        if self._private_data_sim_guard_active():
            if method == "sms.list":
                return {"ok": True, "messages": [], "degraded": True,
                        "paused": True, "authoritative": False,
                        "status": "cellular_data_active",
                        "reason": "cellular_data_active", "retry_after": 10,
                        "error": (
                            "Private cellular data currently owns this modem SIM channel; "
                            "SMS listing is paused until data is disabled.")}
            if method in {
                    "sms.ack", "sms.config.refresh", "sms.send",
                    "call.dial", "call.answer", "call.dtmf"}:
                return self._private_data_owner_unavailable(method)
            if method == "call.status" and not self._paid_call_armed():
                return self._private_data_owner_unavailable(method)
        if method.startswith("call.") and not self.modem.capabilities["call"]:
            return {"ok": False, "unavailable": True, "status": "unsupported",
                    "error": "The selected modem provider exposes no call-signalling capability."}
        if method == "call.dial":
            uicc = self.modem.uicc_health_status(force=True, allow_repair=True)
            if uicc.get("ready") is False:
                return {"ok": False, "unavailable": True, "status": "unavailable",
                        "error": str(uicc.get("reason") or "SIM is unavailable")}
            readiness = self.modem.voice_registration_status(
                force=True, allow_repair=True)
            # Dial is potentially billable and must be fail-closed. Pending maintenance or an
            # unknown bearer is not permission to issue ATD.
            if readiness.get("ready") is not True:
                return {"ok": False, "unavailable": True, "status": "unavailable",
                        "error": str(readiness.get("reason") or
                                     "Cellular call bearer is unavailable")}
        if method == "sms.list":
            now = time.monotonic()
            if now < self._sms_list_blocked_until:
                return {"ok": True, "messages": [], "degraded": True,
                        "error": self._sms_list_error,
                        "retry_after": max(1, round(self._sms_list_blocked_until - now))}
            try:
                messages = self.modem.sms_list()
            except Exception as exc:
                self._sms_list_failures += 1
                self._sms_list_error = str(exc).strip() or type(exc).__name__
                if self._sms_list_failures >= 3:
                    self._sms_list_blocked_until = now + 300
                    return {"ok": True, "messages": [], "degraded": True,
                            "error": self._sms_list_error, "retry_after": 300}
                raise
            self._sms_list_failures = 0
            self._sms_list_blocked_until = 0.0
            self._sms_list_error = ""
            return {"ok": True, "messages": messages, "degraded": False}
        if method == "sms.config.refresh":
            return self._refresh_sms_configuration(force=True)
        if method == "sms.send":
            if not self.modem.capabilities["sms"]:
                return {"ok": False, "unavailable": True,
                        "error": "No installed modem provider currently exposes SMS."}
            return self.modem.sms_send(str(params.get("to") or ""), str(params.get("body") or ""))
        if method == "sms.ack":
            sms_id = str(params.get("id") or "")
            if not sms_id:
                raise ModemError("SMS identifier is required")
            fingerprint = str(params.get("fingerprint") or "")
            ack_key = f"{sms_id}:{fingerprint}"
            if ack_key not in self.acked_sms:
                current = next((item for item in self.modem.sms_list()
                                if item["id"] == sms_id), None)
                if not current:
                    self.acked_sms.add(ack_key)
                    return {"ok": True, "already_absent": True}
                if fingerprint and current.get("fingerprint") != fingerprint:
                    return {"ok": False, "status": "stale",
                            "error": "SMS index now identifies a different message"}
                if self.modem.platform_provider:
                    result = self.modem.platform_provider.sms_delete(sms_id)
                    if not result.get("ok"):
                        raise ModemError(str(result.get("hresult") or "SMS delete failed"))
                else:
                    self.modem._at(f"AT+CMGD={sms_id}")
                self.acked_sms.add(ack_key)
            return {"ok": True}
        if method == "call.dial":
            number = str(params.get("to") or "")
            if not re.fullmatch(r"\+?\d{1,32}", number):
                raise ModemError("invalid telephone number")
            try:
                self._arm_paid_call_lease(str(params.get("lease_id") or ""), "out")
            except AgentShuttingDownError:
                return {"ok": False, "unavailable": True, "status": "shutting_down",
                        "error": "Agent shutdown has started; new paid calls are rejected."}
            try:
                if self.modem.platform_provider and hasattr(
                        self.modem.platform_provider, "call_dial"):
                    return self.modem.platform_provider.call_dial(number)
                self.modem._at(f"ATD{number};")
                return {"ok": True, "status": "dialing", "audio": False,
                        "audio_error": "This modem exposes no usable USB audio endpoint."}
            except Exception:
                # The command may have reached the modem before its transport failed. Keep the
                # durable lease and let the local watchdog terminate; never infer "not dialled".
                raise
        if method == "call.answer":
            # A drained asynchronous RING/NO CARRIER URC is not authoritative call state.
            # Re-read CLCC immediately before admitting a paid ATA, then let the existing
            # lease/shutdown lock close the race between this read and the command itself.
            incoming = self._fresh_call_status()
            if not (incoming.get("fresh") and incoming.get("authoritative") and
                    str(incoming.get("status") or "").casefold() in
                    {"ringing-in", "waiting"}):
                return {"ok": False, "unavailable": True, "status": "not_ringing",
                        "error": str(incoming.get("error") or
                                     "No fresh incoming cellular call is available to answer.")}
            try:
                self._arm_paid_call_lease(str(params.get("lease_id") or ""), "in")
            except AgentShuttingDownError:
                return {"ok": False, "unavailable": True, "status": "shutting_down",
                        "error": "Agent shutdown has started; new paid calls are rejected."}
            try:
                if self.modem.platform_provider and hasattr(
                        self.modem.platform_provider, "call_answer"):
                    return self.modem.platform_provider.call_answer()
                self.modem._at("ATA")
                return {"ok": True, "status": "active", "audio": False}
            except Exception:
                # ATA can succeed before the response channel fails, so the paid-call lease is
                # retained exactly like ATD and expires into local termination.
                raise
        if method == "call.lease.renew":
            return self._renew_paid_call_lease(str(params.get("lease_id") or ""))
        if method == "call.hangup":
            result = self._verified_call_hangup()
            if self._terminal_hangup_confirmed(result):
                self._clear_paid_call_lease()
            return result
        if method == "call.status":
            result = self._fresh_call_status()
            if result.get("fresh") and result.get("authoritative"):
                state = str(result.get("status") or "").casefold()
                with self._paid_call_lock:
                    if state not in {"idle", "ended", "terminated", "unknown"}:
                        self._paid_call_seen_nonterminal = True
                        self._paid_call_terminal_samples = 0
                    elif state in {"idle", "ended", "terminated"}:
                        self._paid_call_terminal_samples += 1
                    clear = bool(
                        state in {"idle", "ended", "terminated"} and
                        (self._paid_call_termination_requested or
                         self._paid_call_seen_nonterminal) and
                        self._paid_call_terminal_samples >= 2)
                if clear:
                    self._clear_paid_call_lease()
                result["terminal_samples"] = self._paid_call_terminal_samples
            return result
        if method == "call.dtmf":
            digits = str(params.get("digits") or "")
            if not re.fullmatch(r"[0-9A-D*#]+", digits, re.I):
                raise ModemError("invalid DTMF digits")
            if self.modem.platform_provider and hasattr(
                    self.modem.platform_provider, "call_dtmf"):
                return self.modem.platform_provider.call_dtmf(digits)
            for digit in digits:
                self.modem._at(f"AT+VTS={digit}")
            return {"ok": True}
        if method == "audio.open":
            controller = getattr(self.modem, "call_audio_controller", None)
            if not self.modem.capabilities.get("call_audio") or not controller:
                return {"ok": False, "unavailable": True, "status": "unsupported",
                        "error": self.modem.call_audio_probe.reason or
                                 "No compatible call-audio backend was detected."}
            call_id = str(params.get("call_id") or "")
            token = str(params.get("token") or "")
            query = urllib.parse.urlencode({"call_id": call_id})
            media_url = (f"wss://{self.args.host}:{self.args.gateway_port}"
                         f"/mdd/api/agent/modem/media?{query}")
            tls_pin = str(getattr(self.args, "pin", "") or
                          load_pin_store().get(self.args.host) or "")
            return controller.open(call_id, media_url, token, tls_pin)
        if method == "audio.close":
            controller = getattr(self.modem, "call_audio_controller", None)
            return (controller.close(str(params.get("call_id") or "")) if controller else
                    {"ok": True, "closed": False})
        if method == "cellular.status":
            return {"ok": True, **self._status()}
        if method == "modem.soft_restart":
            return self._soft_restart()
        if method == "cellular.profile.list":
            return self._cellular_profiles()
        if method == "cellular.profile.save":
            return self._save_cellular_profile(params)
        if method == "cellular.ensure":
            if self._paid_call_safety_hold():
                return {"ok": False, "status": "safety_hold", "data_active": False,
                        "error": "Cellular data is held off until physical call termination is confirmed."}
            return self._cellular_ensure(params)
        if method == "cellular.disable":
            return self._cellular_disable()
        if method == "cellular.roaming.set":
            self.allow_roaming = bool(params.get("enabled"))
            if os.name == "nt":
                interface = self._cellular_interface()
                if not interface:
                    return {"ok": False, "status": "unavailable",
                            "error": "No mobile-broadband interface matches this modem."}
                policy = subprocess.run(
                    ["netsh", "mbn", "set", "dataroamcontrol",
                     f"interface={interface}", "profileset=internet",
                     "state=all" if self.allow_roaming else "state=none"],
                    capture_output=True, text=True, timeout=15, check=False)
                detail = str(policy.stderr or policy.stdout).strip()
                if policy.returncode and detail:
                    return {"ok": False, "status": "unavailable", "error": detail[:300],
                            "roaming_allowed": self.allow_roaming}
            status = self._status()
            if status.get("registration") == "roaming" and not self.allow_roaming:
                result = self._cellular_disable()
                result.update({"registration": "roaming", "roaming_allowed": False})
                return result
            return {"ok": True, "status": "on" if self.allow_roaming else "off",
                    "roaming_allowed": self.allow_roaming,
                    "registration": status.get("registration")}
        if method == "radio.set":
            enabled = bool(params.get("enabled"))
            if enabled and self._paid_call_safety_hold():
                return {"ok": False, "status": "safety_hold", "radio_enabled": False,
                        "error": "Radio remains off until physical call termination is confirmed."}
            if not enabled:
                self._cellular_disable()
            if os.name == "nt" and self.modem.sim_via_mbn:
                interface = self._cellular_interface()
                if not interface:
                    return {"ok": False, "status": "unavailable",
                            "error": "No mobile-broadband interface matches this modem."}
                result = subprocess.run(
                    ["netsh", "mbn", "set", "powerstate", f"interface={interface}",
                     "state=on" if enabled else "state=off"],
                    capture_output=True, text=True, timeout=20, check=False)
                detail = str(result.stderr or result.stdout).strip()
                if result.returncode and detail:
                    return {"ok": False, "status": "unavailable", "error": detail[:300],
                            "radio_enabled": not enabled}
                if not enabled:
                    self.cellular_active.clear()
                return {"ok": True, "status": "on" if enabled else "off",
                        "radio_enabled": enabled}
            command = "AT+CFUN=1" if enabled else "AT+CFUN=4"
            write_error = None
            try:
                self.modem._at(command)
            except Exception as exc:
                # The USB function can lose the command response while still applying it.
                # Read back once; never blindly repeat a radio mutation.
                write_error = exc
            try:
                raw = self.modem._at("AT+CFUN?")
                text = raw.decode("ascii", "replace") if isinstance(raw, bytes) else str(raw)
                match = re.search(r"\+CFUN:\s*(\d+)", text, re.I)
                actual = int(match.group(1)) if match else None
            except Exception:
                actual = None
            expected = 1 if enabled else 4
            if actual != expected:
                detail = (f"radio command outcome is unconfirmed (CFUN={actual})"
                          if actual is not None else
                          "radio command outcome is unconfirmed; readback was unavailable")
                if write_error:
                    detail = f"{detail}: {write_error}"
                return {"ok": False, "status": "uncertain", "radio_enabled": actual == 1,
                        "error": detail}
            if not enabled:
                self.cellular_active.clear()
            return {"ok": True, "status": "on" if enabled else "off",
                    "radio_enabled": enabled}
        raise ModemError(f"unsupported method {method}")

    def run(self):
        while not self.stop.is_set():
            registration_ready = getattr(self.modem, "registration_ready", None)
            if (not self.modem.connection or not self.modem.iccid or
                    (registration_ready is not None and
                     not registration_ready.is_set())):
                time.sleep(1)
                continue
            session_id = ""
            ws = None
            try:
                url = (f"wss://{self.args.host}:{self.args.gateway_port}"
                       f"{self.args.control_path}?token={urllib.parse.quote(self.args.token)}")
                ws = websocket.create_connection(url, timeout=20,
                                                 sslopt={"cert_reqs": 0})
                transport = getattr(ws, "sock", None)
                tls_socket = getattr(transport, "sock", transport)
                certificate = (tls_socket.getpeercert(binary_form=True)
                               if tls_socket and hasattr(tls_socket, "getpeercert") else None)
                if not certificate:
                    raise ModemError("modem control WSS did not expose its peer certificate")
                verify_or_pin_fingerprint(
                    self.args.host, certificate, explicit_pin=self.args.pin,
                    reset_pin=self.reset_pin)
                self.reset_pin = False
                # Discard only requests made before this transport's hello snapshot. A reprobe
                # racing after this point leaves the event set and gets an immediate status.
                self.status_refresh.clear()
                ws.send(json.dumps({"version": 1, "type": "hello",
                                    "agent_id": self.args.agent_id, "modem_id": self.modem.imei,
                                    "imei": self.modem.imei, "iccid": self.modem.iccid,
                                    "imsi": self.modem.imsi,
                                    "phone": self.modem.msisdn,
                                    "model": self.modem.model,
                                    "firmware": self.modem.firmware,
                                    "capabilities": self._capabilities_snapshot(),
                                    "status": self._status()}))
                ack = json.loads(ws.recv())
                session_id = ack["session_id"]
                ws.settimeout(15)
                last_status = time.monotonic()
                log.info("Modem control online (session %s)", session_id[:8])
                send_lock = threading.Lock()

                def send(value: dict):
                    with send_lock:
                        ws.send(json.dumps(value))

                status_pending = threading.Event()

                def publish_status():
                    try:
                        # Platform providers and mutating RPCs may share one native helper or
                        # device handle. Build the snapshot behind the operation lock, but do
                        # it on this worker so the WebSocket receive loop remains responsive.
                        with self.operation_lock:
                            snapshot = self._status()
                        send({"version": 1, "type": "status",
                              "session_id": session_id,
                              "modem_id": self.modem.imei,
                              "capabilities": self._capabilities_snapshot(),
                              "status": snapshot})
                    except Exception:
                        # The control loop owns reconnects. A late status result from an old
                        # transport must not interfere with the next session.
                        pass
                    finally:
                        status_pending.clear()

                def schedule_status(status_executor):
                    nonlocal last_status
                    # Windows MBN and vendor status providers can occasionally take seconds.
                    # Keep that work off the only thread receiving RPC and tunnel.open frames;
                    # one in-flight sample is enough and prevents an unbounded stale queue.
                    if not status_pending.is_set():
                        status_pending.set()
                        status_executor.submit(publish_status)
                        last_status = time.monotonic()
                        return True
                    return False

                def perform(message: dict):
                    operation_id = str(message.get("operation_id") or "")
                    method = str(message.get("method") or "")
                    try:
                        with self.operation_lock:
                            if method == "operation.result":
                                target = str((message.get("params") or {}).get(
                                    "operation_id") or "")
                                result = {"ok": True, "found": target in self.results,
                                          "result": self.results.get(target)}
                            elif operation_id and operation_id in self.results:
                                result = self.results[operation_id]
                            else:
                                result = self.execute(method, message.get("params") or {})
                                if operation_id:
                                    self.results[operation_id] = result
                                    if len(self.results) > 256:
                                        self.results.pop(next(iter(self.results)))
                        response = {"version": 1, "type": "rpc.result", "id": message.get("id"),
                                    "session_id": session_id, "modem_id": self.modem.imei,
                                    "ok": True, "result": result}
                    except Exception as exc:
                        detail = str(exc).strip() or f"{type(exc).__name__} without diagnostic details"
                        log.warning("RPC %s failed: %s", method or "unknown", detail)
                        response = {"version": 1, "type": "rpc.result", "id": message.get("id"),
                                    "session_id": session_id, "modem_id": self.modem.imei,
                                    "ok": False, "error": detail}
                    if method in {"cellular.ensure", "cellular.disable"}:
                        self.data_reconciled.set()
                    try:
                        send(response)
                    except Exception:
                        # The operation may have completed after this transport session ended.
                        # Keep its operation_id result in memory; never resend a paid action.
                        pass

                with concurrent.futures.ThreadPoolExecutor(
                        max_workers=1, thread_name_prefix="modem-rpc") as executor, \
                        concurrent.futures.ThreadPoolExecutor(
                            max_workers=1, thread_name_prefix="modem-status") as status_executor:
                    refresh_stop = threading.Event()

                    def publish_requested_status():
                        while not refresh_stop.is_set():
                            if not self.status_refresh.wait(0.5):
                                continue
                            if refresh_stop.is_set():
                                return
                            # If a sample is in flight, keep the event set until a later sample
                            # is admitted; it must contain the just-updated capability.
                            if schedule_status(status_executor):
                                self.status_refresh.clear()
                            else:
                                refresh_stop.wait(0.1)

                    refresh_thread = threading.Thread(
                        target=publish_requested_status,
                        name="modem-status-refresh", daemon=True)
                    refresh_thread.start()
                    try:
                        while self.modem.connection and not self.stop.is_set():
                            try:
                                message = json.loads(ws.recv())
                            except websocket.WebSocketTimeoutException:
                                schedule_status(status_executor)
                                continue
                            if (message.get("type") == "rpc.request" and
                                    message.get("session_id") == session_id):
                                executor.submit(perform, message)
                            elif (message.get("type") == "tunnel.open" and
                                  message.get("session_id") == session_id):
                                self._open_reverse_tunnel(message, session_id)
                            if time.monotonic() - last_status >= 15:
                                schedule_status(status_executor)
                    finally:
                        refresh_stop.set()
                        self.status_refresh.set()
                        refresh_thread.join(2)
            except Exception as exc:
                log.warning("Modem control connection failed: %s", exc)
            finally:
                if ws:
                    ws.close()
            self.stop.wait(self.args.retry)


class _MacPrivateModemWorker:
    """Own one raw-USB generation; all domain behaviour stays in the shared modem runtime."""

    def __init__(self, args, attachment, state_callback=None, isolation_monitor=None):
        self.args = argparse.Namespace(**vars(args))
        self.args.no_pcsc = True
        self.attachment = attachment
        self.state_callback = state_callback
        self.stop = threading.Event()
        self.thread = None
        self.modem = None
        self.control = None
        self.error = ""
        self.isolation_monitor = isolation_monitor
        self.isolation_admitted = False

    def start(self):
        if self.thread and self.thread.is_alive():
            return

        def worker():
            backend = None
            try:
                backend = PrivateCellularBackend.launch(
                    self.args.cellular_io, self.attachment.public())
                if self.isolation_monitor is None:
                    raise RuntimeError("isolation_not_proven: macOS monitor is unavailable")
                self.isolation_monitor.admit(backend)
                self.isolation_admitted = True
                if self.state_callback:
                    # Only this explicit proof may clear a previous failure for
                    # the same physical generation. A later ordinary startup
                    # error must leave the old isolation failure fail-closed.
                    self.state_callback("ready", isolation_error="")
                self.modem = PrivateUsbModemCard(
                    backend, self.attachment.public(),
                    gammu=getattr(self.args, "gammu", ""),
                    gammu_port=getattr(self.args, "gammu_port", ""),
                    call_audio_helper=getattr(self.args, "call_audio_helper", ""),
                    allow_audio_permission_prompt=getattr(
                        self.args, "allow_audio_permission_prompt", False),
                )
                self.control = ModemControl(self.args, self.modem, dial_backend=backend)
                def interrupt_on_companion_loss():
                    backend.disconnected.wait()
                    if not self.stop.is_set():
                        self.error = (str(getattr(backend, "isolation_error", "") or "") or
                                      "private cellular companion disconnected")
                        self.stop.set()
                        self.modem.close()
                threading.Thread(
                    target=interrupt_on_companion_loss,
                    name=f"modem-watch-{self.attachment.bus}-{self.attachment.address}",
                    daemon=True,
                ).start()
                run(self.args, self.stop, self.state_callback,
                    _allow_private_supervisor=False, modem_override=self.modem,
                    control_override=self.control)
            except Exception as exc:
                self.error = str(exc).strip() or type(exc).__name__
                log.warning("Private modem generation %s stopped: %s",
                            self.attachment.generation, self.error)
            finally:
                if self.modem is not None:
                    self.modem.close()
                elif backend is not None:
                    backend.close()
                if self.state_callback:
                    objects = {"modem": self.modem, "control": self.control}
                    if self.error.startswith("isolation_not_proven"):
                        objects["isolation_error"] = self.error
                    self.state_callback("stopped", **objects)

        self.thread = threading.Thread(
            target=worker,
            name=f"modem-{self.attachment.bus}-{self.attachment.address}",
            # A paid-call cleanup quarantine must keep the process alive; daemon workers
            # would let AppKit/CLI exit while the physical call remained unconfirmed.
            daemon=False,
        )
        self.thread.start()

    def alive(self):
        return bool(self.thread and self.thread.is_alive())

    def close(self, timeout=90.0):
        self.stop.set()
        if self.thread:
            self.thread.join(timeout)
        # Normal shutdown is ordered inside run(): close WSS/control first, then the modem.
        # Force-close only when an unresponsive worker exceeded that bounded deadline.
        if self.alive() and self.modem is not None:
            self.modem.close()
            self.thread.join(5)
        if self.alive():
            raise RestartBlockedError(
                f"private modem generation {self.attachment.generation} did not stop")


def _close_mac_workers(active, timeout: float = 90.0,
                       force_timeout: float = 5.0) -> list[str]:
    """Stop parallel modem workers against one shared deadline, preserving call quarantine."""
    failures = []
    workers = list(active)
    # Close every paid-action gate before delivering stop. begin_shutdown() is serialized with
    # the durable lease commit, so the force-close decisions below cannot race a late ATD/ATA.
    for item in workers:
        control = getattr(item, "control", None)
        if control is not None:
            control.begin_shutdown()
    for item in workers:
        item.stop.set()
    deadline = time.monotonic() + max(0.0, timeout)
    for item in workers:
        if item.thread:
            item.thread.join(max(0.0, deadline - time.monotonic()))
    for item in workers:
        if not item.alive():
            continue
        control = getattr(item, "control", None)
        if control is not None and control._paid_call_armed():
            failures.append(
                f"private modem generation {item.attachment.generation} retained for "
                "unconfirmed paid-call cleanup")
            continue
        if item.modem is not None:
            item.modem.close()
    force_deadline = time.monotonic() + max(0.0, force_timeout)
    for item in workers:
        if item.thread and item.alive():
            control = getattr(item, "control", None)
            if control is not None and control._paid_call_armed():
                continue
            item.thread.join(max(0.0, force_deadline - time.monotonic()))
        if item.alive() and not any(item.attachment.generation in value for value in failures):
            failures.append(
                f"private modem generation {item.attachment.generation} did not stop")
    return failures


def _run_macos_private_supervisor(args, stopped, state_callback=None):
    """Reconcile all raw-USB modems while one independent PC/SC supervisor owns readers.

    An active raw USB function is exclusively claimed by its companion and is therefore absent
    from a second discovery probe.  Absence from a snapshot is not interpreted as removal;
    companion liveness is authoritative.  Unplug closes the claimed transport, and the next
    successful snapshot creates exactly one worker for the new USB generation.
    """
    executable = cellular_io_command(getattr(args, "cellular_io", ""))
    if not executable:
        raise RuntimeError("the bundled mdd-cellular-io companion is unavailable")
    args.cellular_io = executable
    discovery = MacUsbModemDiscovery(executable)
    isolation_monitor = MacHostIsolationMonitor()
    workers = {}
    workers_lock = threading.RLock()
    isolation_errors = {}
    isolation_absence = {}
    pcsc_thread = None
    pcsc_result = {"clean": True}

    if not args.no_pcsc:
        def run_pcsc():
            pcsc_result["clean"] = run_pcsc_reader_supervisor(
                args.host, args.gateway_port, token=args.token, use_wss=True,
                ws_path=args.path, explicit_pin=args.pin, reset_pin=args.reset_pin,
                retry_delay=args.retry, reader_filter=args.pcsc_reader,
                stop_event=stopped)
        pcsc_thread = threading.Thread(
            target=run_pcsc, name="pcsc-supervisor", daemon=True)
        pcsc_thread.start()

    def publish(_state="ready", **objects):
        generation = str(objects.pop("worker_generation", "") or "")
        if not state_callback:
            return
        with workers_lock:
            if generation:
                if "isolation_error" in objects:
                    error = str(objects.get("isolation_error") or "")[:500]
                    if error:
                        isolation_errors[generation] = error
                        isolation_absence.pop(generation, None)
                    else:
                        isolation_errors.pop(generation, None)
                        isolation_absence.pop(generation, None)
                if (_state == "online" and
                        getattr(objects.get("modem"), "connection", None)):
                    isolation_errors.pop(generation, None)
                    isolation_absence.pop(generation, None)
            modems = [item.modem for item in workers.values()
                      if item.modem is not None]
            online = any(getattr(item, "connection", None) for item in modems)
            isolation_error = "; ".join(
                isolation_errors[key] for key in sorted(isolation_errors))[:500]
        state_callback("online" if online else "ready", modems=modems,
                       isolation_error=isolation_error)

    try:
        publish()
        while not stopped.is_set():
            with workers_lock:
                finished = [key for key, item in workers.items() if not item.alive()]
                for key in finished:
                    workers.pop(key, None)
                claimed_locations = {
                    (item.attachment.bus, item.attachment.address)
                    for item in workers.values() if item.alive()
                }
            try:
                attachments = discovery.enumerate(exclude=claimed_locations)
                discovery_succeeded = True
            except Exception as exc:
                log.warning("Raw USB modem discovery failed: %s", exc)
                attachments = []
                discovery_succeeded = False
            with workers_lock:
                if discovery_succeeded:
                    known_generations = set(workers) | {
                        attachment.generation for attachment in attachments}
                    for generation in list(isolation_errors):
                        if generation in known_generations:
                            isolation_absence.pop(generation, None)
                            continue
                        count = isolation_absence.get(generation, 0) + 1
                        isolation_absence[generation] = count
                        # One enumeration snapshot is not a removal event.
                        # Three consecutive successful full snapshots make
                        # physical absence authoritative.
                        if count >= 3:
                            isolation_errors.pop(generation, None)
                            isolation_absence.pop(generation, None)
                for attachment in attachments:
                    # Generation contains the physical USB address. It permits two modules
                    # with missing or accidentally duplicated factory serial strings; ICCID,
                    # EID and IMEI remain the business identities published upstream.
                    if attachment.generation in workers:
                        continue
                    def publish_worker(_state="ready", *, _generation=attachment.generation,
                                       **objects):
                        publish(_state, worker_generation=_generation, **objects)
                    item = _MacPrivateModemWorker(
                        args, attachment, publish_worker,
                        isolation_monitor=isolation_monitor)
                    workers[attachment.generation] = item
                    item.start()
            publish()
            stopped.wait(max(0.5, float(args.retry)))
    finally:
        with workers_lock:
            active = list(workers.values())
            workers.clear()
        failures = _close_mac_workers(active)
        retained_workers = [
            item for item in active
            if item.alive() and getattr(item, "control", None) is not None and
            item.control._paid_call_armed()
        ]
        if retained_workers:
            if state_callback:
                state_callback("cleanup_blocked", modems=[
                    item.modem for item in active if item.modem is not None])
            log.critical(
                "macOS Agent shutdown retained its installation lease for %d paid-call "
                "cleanup worker(s)", len(retained_workers))
            # Each worker keeps its signalling/control channel alive while run() waits on the
            # real paid_call_cleared event.  Joining here preserves the installation lease but
            # lets the supervisor finish as soon as later authoritative idle evidence clears
            # every durable call marker.
            for item in retained_workers:
                item.thread.join()
            failures = [
                value for value in failures
                if "unconfirmed paid-call cleanup" not in value
            ]
        if pcsc_thread:
            pcsc_thread.join(min(30.0, max(5.0, float(args.retry) + 5.0)))
            if pcsc_thread.is_alive():
                failures.append("PC/SC supervisor did not stop before restart deadline")
            elif not pcsc_result["clean"]:
                failures.append("one or more PC/SC workers survived the stop deadline")
        if state_callback:
            state_callback("stopped", modems=[])
        if failures:
            raise RestartBlockedError("; ".join(failures))


def run(args, stop_event=None, state_callback=None, *, _allow_private_supervisor=True,
        modem_override=None, control_override=None):
    """Run the shared modem and PC/SC runtime until *stop_event* is set.

    The Windows service host is the production owner of this function.  Keeping the lifecycle
    event outside the device providers lets SCM, tests and the legacy foreground entrypoint use
    exactly the same runtime without teaching providers about Windows service APIs.
    """
    stopped = stop_event or threading.Event()
    if sys.platform == "darwin" and _allow_private_supervisor:
        return _run_macos_private_supervisor(args, stopped, state_callback)
    pcsc_thread = None
    pcsc_result = {"clean": True}
    if not args.no_pcsc:
        def run_pcsc():
            pcsc_result["clean"] = run_pcsc_reader_supervisor(
                args.host, args.gateway_port, token=args.token, use_wss=True,
                ws_path=args.path, explicit_pin=args.pin, reset_pin=args.reset_pin,
                retry_delay=args.retry, reader_filter=args.pcsc_reader, stop_event=stopped)
        pcsc_thread = threading.Thread(
            target=run_pcsc,
            name="pcsc-supervisor",
            daemon=True,
        )
        pcsc_thread.start()
    modem = modem_override or ModemCard(
        args.port, args.baud,
        gammu=getattr(args, "gammu", ""),
        gammu_port=getattr(args, "gammu_port", ""),
        call_audio_helper=getattr(args, "call_audio_helper", ""),
        allow_audio_permission_prompt=getattr(args, "allow_audio_permission_prompt", False),
    )
    control = control_override or ModemControl(args, modem)
    bootstrap_release = getattr(control, "_bootstrap_private_data_release", None)
    if callable(bootstrap_release):
        bootstrap_release()
    control_thread = threading.Thread(target=control.run, name="modem-control", daemon=True)
    control_thread.start()
    if state_callback:
        state_callback("ready", modem=modem, control=control)
    reset_pin = args.reset_pin
    while not stopped.is_set():
        if state_callback:
            state_callback("ready", modem=modem, control=control)
        if _wait_data_owner_release(
                modem, control, stopped, context="modem reconnect",
                allow_bootstrap_connect=True):
            if stopped.is_set() or control.stop.is_set():
                break
            continue
        while not modem.connection and not stopped.is_set():
            if (_paid_call_armed(control) and
                    modem.connect(allow_uicc_maintenance=False)):
                break
            if (not _paid_call_armed(control) and modem.connect()):
                break
            stopped.wait(args.retry)
        if stopped.is_set():
            break
        if state_callback:
            state_callback("online", modem=modem, control=control)
        reader_name = args.name or f"3GPP modem {modem.imei[-6:]}"
        if modem.sim_via_mbn and not modem.capabilities.get("sim_apdu"):
            log.info("Windows WWAN owns the SIM; modem control remains online without VPCD APDU bridging")
            while (modem.connection and modem.sim_via_mbn and
                   not modem.capabilities.get("sim_apdu") and not stopped.is_set()):
                try:
                    modem._at("AT")
                except Exception:
                    modem.close()
                    break
                stopped.wait(max(args.retry, 5.0))
            continue
        if (modem.sim_via_mbn and modem.capabilities.get("sim_apdu") and
                not control.data_reconciled.is_set()):
            log.info("Waiting for Windows cellular desired state before exposing SIM APDUs")
            if not control.data_reconciled.wait(90):
                log.warning("Cellular desired-state wait timed out; exposing SIM APDUs offline")
        if _paid_call_armed(control):
            # Call signalling/audio owns this modem until terminal evidence clears the lease.
            stopped.wait(0.5)
            continue
        if _wait_data_owner_release(
                modem, control, stopped, include_mbn=True, context="VPCD APDUs"):
            # This is a driver ownership boundary, not a missing feature.  The EC20 Windows
            # WWAN miniport invalidates its active MBN context when an auxiliary function opens
            # a USIM logical channel.  Keep the data exit stable and expose card APDUs only
            # after the persisted cellular switch has been turned off.
            if stopped.is_set() or control.stop.is_set():
                break
            continue
        client = None
        private_data_pause_requested = None
        private_data_pause_logged = False
        def log_private_data_pause_once():
            nonlocal private_data_pause_logged
            if private_data_pause_logged:
                return
            log.info(
                "vpcd_private_data_pause: cellular data owns this modem SIM channel; "
                "VPCD APDUs are paused")
            private_data_pause_logged = True
        try:
            client = connect_wss(
                args.host,
                args.gateway_port,
                path_with_card_id(args.path, reader_name, modem.iccid, modem.imei),
                token=args.token,
                explicit_pin=args.pin,
                reset_pin=reset_pin,
            )
            def interrupt_bridge(active_client=client):
                while not stopped.is_set() and not control.paid_call_active.wait(0.25):
                    pass
                try:
                    active_client.close()
                except Exception:
                    pass
            threading.Thread(target=interrupt_bridge, name="vpcd-stop", daemon=True).start()
            reset_pin = False
            log.info("Bridge online; forwarding AT+CSIM APDUs")
            if modem.private_raw_usb:
                private_data_pause_requested = threading.Event()
                def interrupt_bridge_for_private_data(active_client=client):
                    while not stopped.is_set() and not control.stop.is_set():
                        if control.cellular_active.wait(0.25):
                            private_data_pause_requested.set()
                            try:
                                active_client.close()
                            except Exception:
                                pass
                            return
                threading.Thread(
                    target=interrupt_bridge_for_private_data,
                    name="vpcd-private-data-pause", daemon=True).start()
            while not stopped.is_set():
                payload = client.recv_frame()
                if payload is None:
                    if _expected_private_data_vpcd_pause(
                            modem, control, private_data_pause_requested):
                        log_private_data_pause_once()
                    break
                if len(payload) == 1:
                    if payload[0] == VPCD_CTRL_ATR:
                        client.send_frame(ATR)
                    elif payload[0] in (VPCD_CTRL_OFF, VPCD_CTRL_ON, VPCD_CTRL_RESET):
                        modem.reset()
                    continue
                client.send_frame(modem.transmit(payload))
        except Exception as exc:
            if _expected_private_data_vpcd_pause(
                    modem, control, private_data_pause_requested, exc):
                log_private_data_pause_once()
            else:
                log.warning("Gateway connection failed: %s", exc)
        finally:
            if private_data_pause_requested is not None:
                private_data_pause_requested.clear()
            if client:
                client.close()
            if not modem.connection:
                modem.close()
        stopped.wait(args.retry)
    # A controlled GUI/CLI/service stop must terminate any billed physical call while the
    # native signalling handle is still open. Only after this bounded safety step may the WSS
    # worker/watchdog stop and the modem transport close.
    control.begin_shutdown()
    termination = control.shutdown_paid_call()
    if not termination.get("terminal_confirmed"):
        if state_callback:
            state_callback("cleanup_blocked", modem=modem, control=control)
        log.critical(
            "Agent shutdown is quarantined until paid call termination can be proven: %s",
            termination)
        # Do not close signalling or release the installation lease. A later remote status
        # sample can still prove the call idle and _clear_paid_call_lease() then wakes this
        # exact wait. This is intentionally event-driven, not a polling or retry loop.
        while control._paid_call_safety_hold():
            control.paid_call_cleared.wait()
    control.stop.set()
    modem.close()
    if pcsc_thread:
        pcsc_thread.join(min(30.0, max(5.0, float(args.retry) + 5.0)))
        if pcsc_thread.is_alive():
            raise RestartBlockedError("PC/SC supervisor did not stop before restart deadline")
        if not pcsc_result["clean"]:
            raise RestartBlockedError("one or more PC/SC workers survived the stop deadline")
    control_thread.join(min(30.0, max(20.0, float(args.retry) + 5.0)))
    if control_thread.is_alive():
        raise RestartBlockedError("modem control worker did not stop before restart deadline")
    if state_callback:
        state_callback("stopped", modem=modem, control=control)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", required=True, help="gateway address in host:port format")
    parser.add_argument("--port", default="auto", help="AT serial port, or auto")
    parser.add_argument("--baud", type=int, default=115200)
    parser.add_argument("--token", default="")
    parser.add_argument("--name", default="")
    parser.add_argument("--path", default="/mdd/api/vpcd/ws")
    parser.add_argument("--pin", default="")
    parser.add_argument("--reset-pin", action="store_true")
    parser.add_argument("--retry", type=float, default=3.0)
    parser.add_argument("--control-path", default="/mdd/api/agent/modem/ws")
    parser.add_argument("--agent-id", default=get_agent_id())
    parser.add_argument("--cellular-interface", default="",
                        help="mobile-broadband interface override (normally auto-detected by IMEI)")
    parser.add_argument("--advertise-host", default="",
                        help="gateway-reachable address override (normally route-detected)")
    parser.add_argument("--socks-port", type=int, default=0,
                        help="SOCKS listen port; 0 chooses a collision-free ephemeral port")
    parser.add_argument("--isolation-helper", default="",
                        help="bundled privileged cellular isolation guard override")
    parser.add_argument("--gammu", default="",
                        help="Gammu executable override (or set MDD_GAMMU)")
    parser.add_argument("--gammu-port", default="",
                        help="separate Gammu AT/Modem port override (or set MDD_GAMMU_PORT)")
    parser.add_argument("--call-audio-helper", default="",
                        help="bundled call-audio helper override (or set MDD_CALL_AUDIO_HELPER)")
    parser.add_argument("--cellular-io", default="",
                        help="bundled private raw-USB cellular companion override")
    parser.add_argument("--pcsc-reader", default="",
                        help="optional name filter for external PC/SC readers; default manages all")
    parser.add_argument("--no-pcsc", action="store_true",
                        help="disable external PC/SC reader discovery")
    args = parser.parse_args()
    host, separator, port = args.server.rpartition(":")
    if not separator or not host or not port.isdigit() or not 1 <= int(port) <= 65535:
        parser.error("--server must use host:port format")
    args.host = host
    args.gateway_port = int(port)
    if not acquire_process_lock(args.agent_id, installation_scope=True):
        log.error("Another MDD modem Agent with this agent-id is already running; exiting")
        return 9
    run(args)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
