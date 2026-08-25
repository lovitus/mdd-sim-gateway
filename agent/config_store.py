"""Installation-scoped configuration and secret storage for the unified Agent."""

from __future__ import annotations

import base64
import json
import os
import re
import stat
import sys
import tempfile
import threading
from pathlib import Path


SCHEMA_VERSION = 1
# ``win32crypt`` does not expose this constant in every supported pywin32 build.
# The value is part of the stable Windows DPAPI ABI (wincrypt.h).
CRYPTPROTECT_LOCAL_MACHINE = 0x4
DEFAULT_CONFIG = {
    "version": SCHEMA_VERSION,
    # Deployment identity is always supplied by the operator.  A baked-in site address would
    # silently attach a newly installed Agent to the wrong gateway after an image is moved.
    "server": "",
    "port": "auto",
    "baud": 115200,
    "name": "",
    "path": "/mdd/api/vpcd/ws",
    "pin": "",
    "retry": 3.0,
    "control_path": "/mdd/api/agent/modem/ws",
    "health_path": "/mdd/api/agent/health/ws",
    "cellular_interface": "",
    "advertise_host": "",
    "socks_port": 0,
    "isolation_helper": "",
    "gammu": "",
    "gammu_port": "",
    "call_audio_helper": "",
    "cellular_io": "",
    "pcsc_reader": "",
    "no_pcsc": False,
    # macOS evaluates this default as false until its raw-USB modem path is explicitly
    # enabled. Existing Windows installations continue to enable modem support by default.
    "modem_enabled": True,
}
SECRET_KEYS = frozenset({"token"})
CONFIG_KEYS = frozenset(DEFAULT_CONFIG) - {"version"}


class ConfigError(ValueError):
    pass


def default_config(*, platform: str | None = None) -> dict:
    """Return hardware defaults evaluated on the machine that will run the Agent."""
    value = dict(DEFAULT_CONFIG)
    value["modem_enabled"] = (platform or sys.platform) != "darwin"
    return value


def default_data_dir() -> Path:
    override = os.environ.get("MDD_AGENT_DATA_DIR")
    if override:
        return Path(override).expanduser().resolve()
    if os.name == "nt":
        return Path(os.environ.get("PROGRAMDATA", r"C:\ProgramData")) / "MDD" / "Agent"
    if sys.platform == "darwin":
        return Path.home() / "Library" / "Application Support" / "MDD Agent"
    return Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local" / "state")) / "mdd-agent"


def validate_config(candidate: dict, *, require_server: bool = True) -> dict:
    if not isinstance(candidate, dict):
        raise ConfigError("configuration must be an object")
    unknown = set(candidate) - CONFIG_KEYS - SECRET_KEYS - {"version"}
    if unknown:
        raise ConfigError("unknown configuration field(s): " + ", ".join(sorted(unknown)))
    if "modem_enabled" in candidate and type(candidate["modem_enabled"]) is not bool:
        raise ConfigError("modem_enabled must be a boolean")
    value = default_config()
    value.update({key: item for key, item in candidate.items() if key not in SECRET_KEYS})
    if int(value.get("version", 0)) != SCHEMA_VERSION:
        raise ConfigError(f"unsupported configuration version {value.get('version')!r}")
    server = str(value.get("server") or "").strip()
    if server:
        host, separator, port = server.rpartition(":")
        if not separator or not host or not port.isdigit() or not 1 <= int(port) <= 65535:
            raise ConfigError("server must use host:port format")
    elif require_server:
        raise ConfigError("server is not configured; set an explicit gateway host:port")
    if not 1200 <= int(value.get("baud", 0)) <= 4_000_000:
        raise ConfigError("baud is outside the supported range")
    if not 0.25 <= float(value.get("retry", 0)) <= 300:
        raise ConfigError("retry must be between 0.25 and 300 seconds")
    if not 0 <= int(value.get("socks_port", -1)) <= 65535:
        raise ConfigError("socks_port must be between 0 and 65535")
    for key in ("path", "control_path", "health_path"):
        if not str(value.get(key) or "").startswith("/"):
            raise ConfigError(f"{key} must be an absolute HTTP path")
    pin = str(value.get("pin") or "")
    if pin and not re.fullmatch(r"(?:[0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2}|[0-9A-Fa-f]{64}", pin):
        raise ConfigError("pin must be a SHA-256 fingerprint")
    for key in CONFIG_KEYS:
        if isinstance(DEFAULT_CONFIG[key], bool):
            value[key] = bool(value[key])
        elif isinstance(DEFAULT_CONFIG[key], int):
            value[key] = int(value[key])
        elif isinstance(DEFAULT_CONFIG[key], float):
            value[key] = float(value[key])
        else:
            value[key] = str(value[key] or "")
    return value


class ConfigStore:
    """One owner-only atomic configuration file.

    On macOS/POSIX the Agent token intentionally lives in owner-only ``config.json``.  Windows
    keeps its machine-scoped DPAPI ``secrets.bin`` so changing the macOS UX does not weaken the
    service security boundary.  Production never accesses the macOS Keychain.
    """

    def __init__(self, root: Path | str | None = None, *, keychain=None):
        self.root = Path(root) if root else default_data_dir()
        self.config_path = self.root / "config.json"
        self.secret_path = self.root / "secrets.bin"
        self.log_dir = self.root / "logs"
        self.state_dir = self.root / "state"
        self._lock = threading.RLock()
        self._session_token = ""
        # Retain the keyword for source compatibility with older tests/callers.  It is
        # deliberately ignored: production never accesses the macOS Keychain.
        del keychain

    def set_session_token(self, token: str) -> None:
        """Provide an ephemeral SSH token without persisting it outside this process."""
        self._session_token = str(token or "")

    def _remove_legacy_secret(self) -> None:
        try:
            self.secret_path.unlink()
        except FileNotFoundError:
            return
        if os.name != "nt":
            directory = os.open(self.secret_path.parent, os.O_RDONLY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)

    def _validate_private_path(self, path: Path, *, directory: bool = False) -> None:
        """Reject symlinks, foreign ownership and group/other-readable private paths."""
        if os.name == "nt":
            return
        metadata = os.lstat(path)
        expected = stat.S_ISDIR if directory else stat.S_ISREG
        if not expected(metadata.st_mode):
            raise ConfigError(f"Agent private path has an invalid type: {path}")
        if metadata.st_uid != os.geteuid():
            raise ConfigError(f"Agent private path is owned by uid {metadata.st_uid}: {path}")
        if stat.S_IMODE(metadata.st_mode) & 0o077:
            expected_mode = "0700" if directory else "0600"
            raise ConfigError(f"Agent private path must use {expected_mode} permissions: {path}")

    def _read_config_raw(self) -> dict:
        try:
            os.lstat(self.config_path)
        except FileNotFoundError:
            return {}
        try:
            self._validate_private_path(self.root, directory=True)
            self._validate_private_path(self.config_path)
            raw = json.loads(self.config_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise ConfigError(f"cannot read configuration: {exc}") from exc
        if not isinstance(raw, dict):
            raise ConfigError("configuration must be an object")
        return raw

    def _read_legacy_token(self) -> str:
        try:
            os.lstat(self.secret_path)
        except FileNotFoundError:
            return ""
        try:
            if os.name != "nt":
                self._validate_private_path(self.root, directory=True)
                self._validate_private_path(self.secret_path)
            value = json.loads(self._unprotect(self.secret_path.read_bytes()))
            return str(value.get("token") or "")
        except (OSError, ValueError, json.JSONDecodeError, AttributeError) as exc:
            raise ConfigError(f"cannot read Agent credentials: {exc}") from exc

    def persistent_token(self) -> str:
        """Return only on-disk credentials, never a CLI/environment fallback."""
        raw = self._read_config_raw()
        if os.name != "nt":
            token = str(raw.get("token") or "")
            if token:
                return token
        return self._read_legacy_token()

    def has_persisted_token(self) -> bool:
        return bool(self.persistent_token())

    def ensure_dirs(self) -> None:
        for path in (self.root, self.log_dir, self.state_dir):
            path.mkdir(parents=True, exist_ok=True)
            if os.name != "nt":
                metadata = os.lstat(path)
                if not stat.S_ISDIR(metadata.st_mode) or metadata.st_uid != os.geteuid():
                    raise ConfigError(f"Agent private directory is unsafe: {path}")
                os.chmod(path, 0o700)

    @staticmethod
    def _protect(raw: bytes) -> bytes:
        if os.name == "nt":
            try:
                import win32crypt
            except ImportError as exc:  # pragma: no cover - Windows packaging guard
                raise ConfigError("pywin32 is required to protect Windows Agent credentials") from exc
            return b"DPAPI1\0" + win32crypt.CryptProtectData(
                raw, "MDD Agent credentials", None, None, None,
                CRYPTPROTECT_LOCAL_MACHINE,
            )
        return b"FILE1\0" + base64.b64encode(raw)

    @staticmethod
    def _unprotect(blob: bytes) -> bytes:
        if blob.startswith(b"DPAPI1\0") and os.name == "nt":
            import win32crypt
            return win32crypt.CryptUnprotectData(blob[7:], None, None, None, 0)[1]
        if blob.startswith(b"FILE1\0"):
            return base64.b64decode(blob[6:])
        raise ConfigError("unsupported or corrupt Agent credential store")

    @staticmethod
    def _atomic_write(path: Path, data: bytes, mode: int = 0o600) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        fd, temporary = tempfile.mkstemp(prefix=path.name + ".", suffix=".tmp", dir=path.parent)
        try:
            with os.fdopen(fd, "wb") as handle:
                handle.write(data)
                handle.flush()
                os.fsync(handle.fileno())
            if os.name != "nt":
                os.chmod(temporary, mode)
            os.replace(temporary, path)
            if os.name != "nt":
                directory = os.open(path.parent, os.O_RDONLY)
                try:
                    os.fsync(directory)
                finally:
                    os.close(directory)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)

    def load(self, *, include_secrets: bool = True) -> dict:
        with self._lock:
            raw = self._read_config_raw()
            if raw:
                value = validate_config(raw, require_server=False)
            else:
                value = default_config()
            if include_secrets:
                value["token"] = self.persistent_token()
                # An explicit file value always wins. CLI arguments/stdin and environment
                # variables are fallback inputs and never silently override saved config.
                if not value["token"] and self._session_token:
                    value["token"] = self._session_token
            return value

    def show(self) -> dict:
        value = self.load(include_secrets=False)
        configured = self.has_persisted_token() or bool(self._session_token)
        value["token"] = {"configured": configured, "value": "<redacted>"}
        return value

    def redact(self, value, *additional: str):
        """Recursively scrub configured and request-scoped secrets at control boundaries."""
        secrets = {str(item) for item in additional if str(item)}
        try:
            secrets.add(self.persistent_token())
        except ConfigError:
            pass
        if self._session_token:
            secrets.add(self._session_token)
        secrets.discard("")

        def scrub(item, key: str = ""):
            if any(marker in key.casefold() for marker in ("token", "password", "secret")):
                return "<redacted>"
            if isinstance(item, dict):
                return {name: scrub(member, str(name)) for name, member in item.items()}
            if isinstance(item, list):
                return [scrub(member) for member in item]
            if isinstance(item, tuple):
                return tuple(scrub(member) for member in item)
            if isinstance(item, str):
                for secret in secrets:
                    item = item.replace(secret, "<redacted>")
            return item

        return scrub(value)

    def save(self, changes: dict, *, replace: bool = False) -> dict:
        if not isinstance(changes, dict):
            raise ConfigError("configuration changes must be an object")
        with self._lock:
            current = default_config() if replace else self.load(include_secrets=False)
            stored_token = self.persistent_token()
            config_changes = {key: item for key, item in changes.items() if key not in SECRET_KEYS}
            current.update(config_changes)
            checked = validate_config(current, require_server=False)
            token = str(changes.get("token") if "token" in changes else stored_token or "")
            self.ensure_dirs()
            persisted = dict(checked)
            if os.name != "nt":
                persisted["token"] = token
            encoded = (json.dumps(persisted, indent=2, sort_keys=True) + "\n").encode("utf-8")
            self._atomic_write(self.config_path, encoded)
            if os.name == "nt" and "token" in changes:
                payload = json.dumps({"token": token}, separators=(",", ":")).encode("utf-8")
                self._atomic_write(self.secret_path, self._protect(payload))
            elif os.name != "nt" and self.secret_path.exists():
                # Verify the new owner-only file before deleting compatibility storage.
                if self.persistent_token() != token:
                    raise ConfigError("Agent token migration could not be verified")
                self._remove_legacy_secret()
            return self.show()
