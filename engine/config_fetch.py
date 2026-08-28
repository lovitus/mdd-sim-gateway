#!/usr/bin/env python3
"""Fetch one digest-bound Engine config snapshot from Control's Unix socket."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import socket
import tempfile
import time


MAX_RESPONSE_BYTES = 2 * 1024 * 1024
_HEX64 = re.compile(r"^[0-9a-f]{64}$")


def _canonical(value: dict) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"),
                      ensure_ascii=False).encode("utf-8")


def _existing_valid(path: Path, digest: str) -> bool:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
        return isinstance(value, dict) and hashlib.sha256(_canonical(value)).hexdigest() == digest
    except (OSError, ValueError, TypeError):
        return False


def fetch_once(socket_path: str, iid: str, digest: str, proof: str) -> dict:
    request = json.dumps({"version": 1, "instance": iid, "digest": digest,
                          "proof": proof}, separators=(",", ":")).encode("utf-8") + b"\n"
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(5.0)
        client.connect(socket_path)
        client.sendall(request)
        chunks, total = [], 0
        while True:
            chunk = client.recv(min(65536, MAX_RESPONSE_BYTES + 1 - total))
            if not chunk:
                break
            chunks.append(chunk)
            total += len(chunk)
            if total > MAX_RESPONSE_BYTES:
                raise RuntimeError("Engine config response is too large")
            if b"\n" in chunk:
                break
    response = json.loads(b"".join(chunks))
    if not isinstance(response, dict) or response.get("ok") is not True:
        raise RuntimeError("Control rejected Engine configuration request")
    payload = response.get("config")
    if not isinstance(payload, dict) or response.get("digest") != digest \
            or hashlib.sha256(_canonical(payload)).hexdigest() != digest:
        raise RuntimeError("Engine configuration digest mismatch")
    return payload


def main() -> None:
    socket_path = os.environ.get("MDD_CONFIG_SOCKET", "")
    iid = os.environ.get("MDD_ID", "")
    digest = os.environ.get("MDD_CONFIG_DIGEST", "")
    proof = os.environ.get("MDD_CONFIG_PROOF", "")
    target = Path(os.environ.get("MDD_INSTANCE", "/config/instance.json"))
    if not socket_path or not iid or not _HEX64.fullmatch(digest) or not _HEX64.fullmatch(proof):
        raise SystemExit("invalid Engine configuration service environment")
    if _existing_valid(target, digest):
        return
    deadline, delay = time.monotonic() + 30.0, 0.25
    while True:
        try:
            payload = fetch_once(socket_path, iid, digest, proof)
            break
        except (OSError, ValueError, RuntimeError, socket.timeout):
            if time.monotonic() >= deadline:
                raise SystemExit("Engine configuration service unavailable") from None
            time.sleep(delay)
            delay = min(2.0, delay * 2)
    target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=".instance.", dir=str(target.parent))
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, target)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


if __name__ == "__main__":
    main()
