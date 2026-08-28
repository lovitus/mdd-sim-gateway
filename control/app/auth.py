"""Local administrator authentication for the management UI.

Credentials are stored outside the source tree in ``$MDD_CONFIG_DIR/auth.json``. Passwords use
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

from . import config as cfg, paths

AUTH_PATH = os.path.join(cfg.CONFIG_DIR, "auth.json")
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
    agent_token = secrets.token_urlsafe(32)
    payload = {
        "version": 1,
        "username": username,
        "salt": salt.hex(),
        "password_hash": _derive(password, salt).hex(),
        "agent_token": agent_token,
        "created_at": int(time.time()),
    }
    paths.ensure_private_dir(cfg.CONFIG_DIR)
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


def generate_agent_token() -> str:
    """Generate a high-entropy random URL-safe agent token."""
    return secrets.token_urlsafe(32)


def set_agent_token(token: str) -> str:
    """Set and persist a custom agent token for multi-device agent authentication."""
    if not token or not isinstance(token, str):
        raise ValueError("Agent Token 不能为空")
    clean = token.strip()
    if len(clean) < 6 or len(clean) > 256:
        raise ValueError("Agent Token 长度必须在 6 到 256 个字符之间")
    data = _read()
    data["agent_token"] = clean
    paths.ensure_private_dir(cfg.CONFIG_DIR)
    temporary = AUTH_PATH + ".tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        json.dump(data, handle, ensure_ascii=False, indent=2)
    os.chmod(temporary, 0o600)
    os.replace(temporary, AUTH_PATH)
    return clean


def get_or_create_agent_token() -> str:
    """Return the dedicated agent token, generating and persisting one if absent."""
    env_token = os.environ.get("MDD_AGENT_TOKEN", "").strip()
    if env_token:
        return env_token

    data = _read()
    if data.get("agent_token"):
        return str(data["agent_token"])

    # Generate and persist default token
    new_token = generate_agent_token()
    data["agent_token"] = new_token
    paths.ensure_private_dir(cfg.CONFIG_DIR)
    temporary = AUTH_PATH + ".tmp"
    try:
        with open(temporary, "w", encoding="utf-8") as handle:
            json.dump(data, handle, ensure_ascii=False, indent=2)
        os.chmod(temporary, 0o600)
        os.replace(temporary, AUTH_PATH)
    except OSError:
        pass
    return new_token


def verify_agent_token(token: str | None) -> bool:
    """Verify if the token matches the configured agent token or an active admin session."""
    if not token or not isinstance(token, str):
        return False
    
    clean_token = token.strip()
    if not clean_token:
        return False

    # 1. Check MDD_AGENT_TOKEN env var
    env_token = os.environ.get("MDD_AGENT_TOKEN", "").strip()
    if env_token and hmac.compare_digest(clean_token, env_token):
        return True

    # 2. Check auth.json persistent agent_token
    configured_token = get_or_create_agent_token()
    if configured_token and hmac.compare_digest(clean_token, configured_token):
        return True

    # 3. Check active admin session
    if session(clean_token) is not None:
        return True

    return False


def get_cert_fingerprint(cert_path: str | None = None) -> str:
    """Compute and format the SHA-256 fingerprint of the current server TLS certificate."""
    if not cert_path:
        tls_cfg = cfg.get_settings().get("tls") or {}
        cert_path = tls_cfg.get("cert_path")
    if str(cert_path or "").startswith("/data/certs/"):
        migrated = os.path.join(
            cfg.CONFIG_DIR, os.path.relpath(str(cert_path), "/data"))
        if os.path.exists(migrated):
            cert_path = migrated
    if not cert_path or not os.path.exists(cert_path):
        for candidate in [
            os.path.join(cfg.CONFIG_DIR, "certs", "self-signed.crt"),
            os.path.join(cfg.CONFIG_DIR, "certs", "cert.pem"),
        ]:
            if os.path.exists(candidate):
                cert_path = candidate
                break
    try:
        if not cert_path or not os.path.exists(cert_path):
            return ""
        with open(cert_path, "rb") as f:
            content = f.read()
        
        # Strip PEM headers if present to get DER bytes, or hash raw DER
        import re
        b64_pattern = re.findall(r"-----BEGIN CERTIFICATE-----(.*?)-----END CERTIFICATE-----", content.decode("ascii", errors="ignore"), re.DOTALL)
        if b64_pattern:
            import base64
            der = base64.b64decode("".join(b64_pattern[0].split()))
            digest = hashlib.sha256(der).hexdigest().upper()
        else:
            digest = hashlib.sha256(content).hexdigest().upper()
            
        return ":".join(digest[i:i+2] for i in range(0, len(digest), 2))
    except Exception:
        return ""
