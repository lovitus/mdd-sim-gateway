"""Versioned local management contract shared by the CLI and GUI."""

from __future__ import annotations

import json
import time
import uuid


PROTOCOL_VERSION = 1
MAX_MESSAGE_BYTES = 1024 * 1024
READ = "read"
OPERATE = "operate"
ADMIN = "admin"

METHOD_PERMISSIONS = {
    "status": READ,
    "devices": READ,
    "doctor": READ,
    "logs": READ,
    "config.show": READ,
    "config.validate": READ,
    "config.set": ADMIN,
    "reconnect": OPERATE,
    "audio.reprobe": OPERATE,
    "maintenance.prepare-install": ADMIN,
    "maintenance.cancel-install": ADMIN,
    "self-test": OPERATE,
}

EXIT_USAGE = 2
EXIT_CONFIG = 3
EXIT_NOT_INSTALLED = 4
EXIT_UNAVAILABLE = 5
EXIT_PERMISSION = 6
EXIT_ACTION_FAILED = 7
EXIT_UNHEALTHY = 8
EXIT_CONFLICT = 9
EXIT_ELEVATION_REQUIRED = 10


class ProtocolError(ValueError):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


def request(method: str, params: dict | None = None, deadline_ms: int = 15_000) -> dict:
    if method not in METHOD_PERMISSIONS:
        raise ProtocolError("unknown_method", f"unknown method {method}")
    return {
        "version": PROTOCOL_VERSION,
        "id": str(uuid.uuid4()),
        "method": method,
        "params": params or {},
        "deadline_ms": int(deadline_ms),
        "created_ms": int(time.time() * 1000),
    }


def decode_request(payload: bytes) -> dict:
    if len(payload) > MAX_MESSAGE_BYTES:
        raise ProtocolError("message_too_large", "local control message exceeds 1 MiB")
    try:
        value = json.loads(payload.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ProtocolError("invalid_json", "local control message is not valid JSON") from exc
    if not isinstance(value, dict):
        raise ProtocolError("invalid_request", "request must be an object")
    if value.get("version") != PROTOCOL_VERSION:
        raise ProtocolError("unsupported_version", "unsupported local control protocol version")
    if not isinstance(value.get("id"), str) or not value["id"]:
        raise ProtocolError("invalid_request", "request id is required")
    method = value.get("method")
    if method not in METHOD_PERMISSIONS:
        raise ProtocolError("unknown_method", f"unknown method {method!r}")
    if not isinstance(value.get("params", {}), dict):
        raise ProtocolError("invalid_params", "params must be an object")
    deadline = value.get("deadline_ms", 0)
    if not isinstance(deadline, int) or not 1 <= deadline <= 120_000:
        raise ProtocolError("invalid_deadline", "deadline_ms must be between 1 and 120000")
    created_ms = value.get("created_ms")
    if not isinstance(created_ms, int) or created_ms <= 0:
        raise ProtocolError("invalid_request", "created_ms is required")
    if int(time.time() * 1000) > created_ms + deadline:
        raise ProtocolError("deadline_exceeded", "local control request deadline elapsed")
    return value


def response(request_id: str | None, *, result=None, error: ProtocolError | None = None) -> dict:
    value = {"version": PROTOCOL_VERSION, "id": request_id, "ok": error is None}
    if error:
        value["error"] = {"code": error.code, "message": str(error)}
    else:
        value["result"] = result
    return value


def encode_message(value: dict) -> bytes:
    payload = json.dumps(value, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
    if len(payload) > MAX_MESSAGE_BYTES:
        raise ProtocolError("message_too_large", "local control response exceeds 1 MiB")
    return payload
