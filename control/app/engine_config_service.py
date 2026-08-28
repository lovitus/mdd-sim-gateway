"""Root-only Unix-socket service for immutable per-Engine config snapshots.

Engine containers receive no host config path.  Control renders the authoritative snapshot,
binds its digest to the line id with an HMAC proof, and serves only that exact snapshot over a
host-local Unix socket.  The proof is scoped, contains no reusable configuration secret, and can
be recomputed after a Control restart while the authoritative config remains unchanged.
"""
from __future__ import annotations

import asyncio
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
from typing import Callable


PROTOCOL_VERSION = 1
MAX_REQUEST_BYTES = 4096
MAX_RESPONSE_BYTES = 2 * 1024 * 1024
_IID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$")
_HEX64 = re.compile(r"^[0-9a-f]{64}$")


def canonical_payload(payload: dict) -> bytes:
    if not isinstance(payload, dict):
        raise ValueError("Engine config snapshot must be an object")
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"),
                         ensure_ascii=False).encode("utf-8")
    if len(encoded) > MAX_RESPONSE_BYTES:
        raise ValueError("Engine config snapshot is too large")
    return encoded


def payload_digest(payload: dict) -> str:
    return hashlib.sha256(canonical_payload(payload)).hexdigest()


def config_proof(secret: str, iid: str, digest: str) -> str:
    if not secret or not _IID.fullmatch(str(iid)) or not _HEX64.fullmatch(str(digest)):
        raise ValueError("invalid Engine config proof input")
    message = f"mdd-engine-config-v1\0{iid}\0{digest}".encode("utf-8")
    return hmac.new(secret.encode("utf-8"), message, hashlib.sha256).hexdigest()


class EngineConfigServer:
    def __init__(self, socket_path: str, payload_provider: Callable[[str], dict],
                 secret_provider: Callable[[], str]):
        self.socket_path = Path(socket_path)
        self.payload_provider = payload_provider
        self.secret_provider = secret_provider
        self._server: asyncio.AbstractServer | None = None

    async def start(self) -> None:
        if self._server is not None:
            return
        self.socket_path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        os.chmod(self.socket_path.parent, 0o700)
        try:
            if self.socket_path.is_socket():
                self.socket_path.unlink()
            elif os.path.lexists(self.socket_path):
                raise RuntimeError("Engine config socket path is occupied by a non-socket")
        except OSError as exc:
            raise RuntimeError("cannot prepare Engine config socket") from exc
        self._server = await asyncio.start_unix_server(
            self._handle, path=str(self.socket_path), limit=MAX_REQUEST_BYTES + 1)
        os.chmod(self.socket_path, 0o600)

    async def close(self) -> None:
        server, self._server = self._server, None
        if server is not None:
            server.close()
            await server.wait_closed()
        try:
            if self.socket_path.is_socket():
                self.socket_path.unlink()
        except OSError:
            pass

    async def _handle(self, reader: asyncio.StreamReader,
                      writer: asyncio.StreamWriter) -> None:
        response: dict
        try:
            raw = await asyncio.wait_for(reader.readline(), timeout=5.0)
            if not raw or len(raw) > MAX_REQUEST_BYTES or not raw.endswith(b"\n"):
                raise ValueError("invalid request frame")
            request = json.loads(raw)
            if not isinstance(request, dict) or set(request) != {
                    "version", "instance", "digest", "proof"}:
                raise ValueError("invalid request schema")
            if request["version"] != PROTOCOL_VERSION:
                raise ValueError("unsupported request version")
            iid, digest = str(request["instance"]), str(request["digest"])
            proof = str(request["proof"])
            expected = config_proof(self.secret_provider(), iid, digest)
            if not hmac.compare_digest(proof, expected):
                raise PermissionError("invalid config proof")
            payload = self.payload_provider(iid)
            if payload_digest(payload) != digest:
                raise PermissionError("config generation changed")
            response = {"ok": True, "version": PROTOCOL_VERSION,
                        "digest": digest, "config": payload}
        except PermissionError:
            response = {"ok": False, "error": "configuration request rejected"}
        except Exception:
            response = {"ok": False, "error": "invalid configuration request"}
        encoded = json.dumps(response, sort_keys=True, separators=(",", ":"),
                             ensure_ascii=False).encode("utf-8") + b"\n"
        if len(encoded) <= MAX_RESPONSE_BYTES:
            writer.write(encoded)
            try:
                await writer.drain()
            except (BrokenPipeError, ConnectionError):
                pass
        writer.close()
        try:
            await writer.wait_closed()
        except (BrokenPipeError, ConnectionError):
            pass
