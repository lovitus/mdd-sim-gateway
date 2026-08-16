"""Local administrator authentication for the management UI.

Credentials are stored outside the source tree in ``$MDD_DATA/auth.json``. Passwords use
stdlib scrypt with a per-install salt. Sessions are memory-only, so a service restart logs all
browsers out. This is intentional for an appliance control surface.
"""
from __future__ import annotations

import hashlib
import hmac
import json
import os
import secrets
import threading
import time

from . import config as cfg

AUTH_PATH = os.path.join(cfg.DATA_DIR, "auth.json")
SESSION_COOKIE = "mdd_session"
SESSION_TTL = 12 * 60 * 60
_sessions: dict[str, dict] = {}
_failures: dict[str, list[float]] = {}
_lock = threading.RLock()


def _read() -> dict:
    try:
        with open(AUTH_PATH, encoding="utf-8") as handle:
            data = json.load(handle)
        return data if isinstance(data, dict) else {}
    except (OSError, ValueError, TypeError):
        return {}


def configured() -> bool:
    data = _read()
    return bool(data.get("salt") and data.get("password_hash"))


def username() -> str:
    """Return the configured single administrator name for the login screen."""
    return str(_read().get("username") or "admin")


def _derive(password: str, salt: bytes) -> bytes:
    return hashlib.scrypt(password.encode("utf-8"), salt=salt, n=2**15, r=8, p=1,
                          dklen=32, maxmem=64 * 1024 * 1024)


def setup(password: str, username: str = "admin") -> None:
    if configured():
        raise ValueError("administrator account is already configured")
    if len(password) < 10 or len(password) > 256:
        raise ValueError("password must contain 10-256 characters")
    username = str(username or "admin").strip()
    if not username or len(username) > 64:
        raise ValueError("username must contain 1-64 characters")
    salt = secrets.token_bytes(16)
    payload = {
        "version": 1,
        "username": username,
        "salt": salt.hex(),
        "password_hash": _derive(password, salt).hex(),
        "created_at": int(time.time()),
    }
    os.makedirs(cfg.DATA_DIR, exist_ok=True)
    temporary = AUTH_PATH + ".tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, ensure_ascii=False, indent=2)
    os.chmod(temporary, 0o600)
    os.replace(temporary, AUTH_PATH)


def throttled(peer: str) -> int:
    now = time.time()
    with _lock:
        attempts = [stamp for stamp in _failures.get(peer, []) if now - stamp < 900]
        _failures[peer] = attempts
    return max(0, 60 - int(now - attempts[-1])) if len(attempts) >= 5 else 0


def login(username: str, password: str, peer: str) -> tuple[str, str] | None:
    data = _read()
    try:
        expected = bytes.fromhex(data["password_hash"])
        actual = _derive(password, bytes.fromhex(data["salt"]))
    except (KeyError, ValueError, TypeError):
        return None
    valid = hmac.compare_digest(str(username), str(data.get("username") or "admin"))
    valid = hmac.compare_digest(actual, expected) and valid
    with _lock:
        if not valid:
            _failures.setdefault(peer, []).append(time.time())
            return None
        _failures.pop(peer, None)
        token, csrf = secrets.token_urlsafe(32), secrets.token_urlsafe(24)
        _sessions[token] = {"csrf": csrf, "expires": time.time() + SESSION_TTL}
        return token, csrf


def session(token: str | None) -> dict | None:
    if not token:
        return None
    with _lock:
        item = _sessions.get(token)
        if not item or item["expires"] < time.time():
            _sessions.pop(token, None)
            return None
        item["expires"] = time.time() + SESSION_TTL
        return dict(item)


def logout(token: str | None) -> None:
    if token:
        with _lock:
            _sessions.pop(token, None)


def change_password(current_password: str, new_password: str) -> None:
    if len(new_password) < 10 or len(new_password) > 256:
        raise ValueError("new password must contain 10-256 characters")
    data = _read()
    try:
        valid = hmac.compare_digest(
            _derive(current_password, bytes.fromhex(data["salt"])),
            bytes.fromhex(data["password_hash"]),
        )
    except (KeyError, ValueError, TypeError):
        valid = False
    if not valid:
        raise ValueError("current password is incorrect")
    salt = secrets.token_bytes(16)
    data.update({"salt": salt.hex(), "password_hash": _derive(new_password, salt).hex(),
                 "changed_at": int(time.time())})
    temporary = AUTH_PATH + ".tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        json.dump(data, handle, ensure_ascii=False, indent=2)
    os.chmod(temporary, 0o600)
    os.replace(temporary, AUTH_PATH)
    with _lock:
        _sessions.clear()
