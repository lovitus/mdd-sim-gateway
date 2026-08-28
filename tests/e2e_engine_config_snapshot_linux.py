#!/usr/bin/env python3
"""No-network E2E for the Engine's digest-bound, container-local config snapshot."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading


def canonical(value: dict) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"),
                      ensure_ascii=False).encode("utf-8")


def main() -> int:
    config = {
        "instance_id": 17,
        "name": "offline-config-e2e",
        "imsi": "001010000000017",
        "nested": {"enabled": True},
    }
    digest = hashlib.sha256(canonical(config)).hexdigest()
    proof = "a" * 64
    with tempfile.TemporaryDirectory(prefix="mdd-engine-config-e2e.") as directory:
        root = Path(directory)
        socket_path = root / "control.sock"
        target = root / "config" / "instance.json"
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        server.bind(str(socket_path))
        server.listen(1)
        server_errors: list[BaseException] = []

        def serve_once() -> None:
            try:
                connection, _ = server.accept()
                with connection:
                    request = json.loads(connection.makefile("rb").readline())
                    if request != {"version": 1, "instance": "17", "digest": digest,
                                   "proof": proof}:
                        raise AssertionError(f"unexpected request: {request!r}")
                    response = {"ok": True, "digest": digest, "config": config}
                    connection.sendall(
                        json.dumps(response, separators=(",", ":")).encode() + b"\n")
            except BaseException as exc:  # propagated below from the server thread
                server_errors.append(exc)

        thread = threading.Thread(target=serve_once, daemon=True)
        thread.start()
        environment = dict(os.environ, MDD_CONFIG_SOCKET=str(socket_path), MDD_ID="17",
                           MDD_CONFIG_DIGEST=digest, MDD_CONFIG_PROOF=proof,
                           MDD_INSTANCE=str(target))
        subprocess.run(["python3", "/usr/local/bin/config_fetch.py"], env=environment,
                       check=True, timeout=5)
        thread.join(5)
        server.close()
        if thread.is_alive():
            raise AssertionError("config service thread did not terminate")
        if server_errors:
            raise server_errors[0]
        socket_path.unlink(missing_ok=True)
        if json.loads(target.read_text(encoding="utf-8")) != config:
            raise AssertionError("written config does not match the served snapshot")
        if target.stat().st_mode & 0o777 != 0o600:
            raise AssertionError("config snapshot permissions are not 0600")

        before = target.read_bytes()
        subprocess.run(["python3", "/usr/local/bin/config_fetch.py"], env=environment,
                       check=True, timeout=5)
        if target.read_bytes() != before:
            raise AssertionError("offline reuse rewrote an already valid snapshot")

    print(json.dumps({"config_socket_roundtrip": True, "digest_verified": True,
                      "mode": "0600", "offline_exact_snapshot_reused": True},
                     sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
