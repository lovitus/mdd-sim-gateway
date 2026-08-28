#!/usr/bin/env python3
"""Docker E2E: an Engine keeps seeing the config socket after Control recreates it."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import uuid


def canonical(value: dict) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def docker(*args: str, capture: bool = False) -> str:
    result = subprocess.run(["docker", *args], check=True, text=True,
                            stdout=subprocess.PIPE if capture else subprocess.DEVNULL)
    return result.stdout.strip() if capture else ""


def serve_once(path: Path, payload: dict, digest: str,
               errors: list[BaseException]) -> threading.Thread:
    ready = threading.Event()

    def serve() -> None:
        server: socket.socket | None = None
        try:
            path.unlink(missing_ok=True)
            server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            server.bind(str(path))
            server.listen(1)
            ready.set()
            connection, _ = server.accept()
            with connection:
                request = json.loads(connection.makefile("rb").readline())
                if request.get("digest") != digest:
                    raise AssertionError("Engine requested the wrong config digest")
                response = {"ok": True, "version": 1, "digest": digest,
                            "config": payload}
                connection.sendall(json.dumps(response, separators=(",", ":")).encode()
                                   + b"\n")
        except BaseException as exc:
            errors.append(exc)
            ready.set()
        finally:
            if server is not None:
                server.close()

    thread = threading.Thread(target=serve, daemon=True)
    thread.start()
    if not ready.wait(5):
        raise AssertionError("config server did not bind")
    if errors:
        raise errors[0]
    return thread


def main() -> int:
    image = os.environ.get("MDD_ENGINE_E2E_IMAGE", "")
    root = os.environ.get("MDD_E2E_ROOT", "")
    if not image or not root or not os.path.isabs(root):
        raise SystemExit("MDD_ENGINE_E2E_IMAGE and absolute MDD_E2E_ROOT are required")
    Path(root).mkdir(mode=0o700, parents=True, exist_ok=True)
    name = f"mdd-config-mount-e2e-{uuid.uuid4().hex[:12]}"
    errors: list[BaseException] = []
    with tempfile.TemporaryDirectory(prefix="cfgmount.", dir=root) as temporary:
        work = Path(temporary)
        service_dir = work / "service"
        output_dir = work / "output"
        service_dir.mkdir(mode=0o700)
        output_dir.mkdir(mode=0o700)
        socket_path = service_dir / "engine-config.sock"
        payload = {"id": "17", "generation": "control-recreated"}
        digest = hashlib.sha256(canonical(payload)).hexdigest()
        common = [
            "-e", "MDD_CONFIG_SOCKET=/run/mdd-control/engine-config.sock",
            "-e", "MDD_ID=17", "-e", f"MDD_CONFIG_DIGEST={digest}",
            "-e", f"MDD_CONFIG_PROOF={'a' * 64}",
        ]
        try:
            docker("run", "-d", "--name", name, "--network", "none",
                   "-v", f"{service_dir}:/run/mdd-control:ro",
                   "-v", f"{output_dir}:/work", "--entrypoint", "/bin/sh", image,
                   "-c", "sleep 120")
            before = docker("inspect", name, "--format",
                            "{{.Id}}|{{.State.StartedAt}}|{{.RestartCount}}", capture=True)
            first = serve_once(socket_path, payload, digest, errors)
            docker("exec", *common, "-e", "MDD_INSTANCE=/work/first.json", name,
                   "python3", "/usr/local/bin/config_fetch.py")
            first.join(5)
            if first.is_alive() or errors:
                raise errors[0] if errors else AssertionError("first server did not finish")

            # Simulate a new Control process: close/unlink the old socket and bind a new inode
            # at the same path while the Engine container and its directory mount stay alive.
            second = serve_once(socket_path, payload, digest, errors)
            docker("exec", *common, "-e", "MDD_INSTANCE=/work/second.json", name,
                   "python3", "/usr/local/bin/config_fetch.py")
            second.join(5)
            if second.is_alive() or errors:
                raise errors[0] if errors else AssertionError("second server did not finish")
            after = docker("inspect", name, "--format",
                           "{{.Id}}|{{.State.StartedAt}}|{{.RestartCount}}", capture=True)
            if before != after:
                raise AssertionError("Engine changed while Control config socket restarted")
            for target in (output_dir / "first.json", output_dir / "second.json"):
                if json.loads(target.read_text(encoding="utf-8")) != payload:
                    raise AssertionError("Engine fetched the wrong snapshot")
                if target.stat().st_mode & 0o777 != 0o600:
                    raise AssertionError("Engine snapshot is not mode 0600")
        finally:
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL)
    print(json.dumps({"directory_mount_reconnected": True,
                      "engine_generation_unchanged": True,
                      "snapshots_verified": 2}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
