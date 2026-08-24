#!/usr/bin/env python3
"""Private-runner E2E for the host-mode maintenance supervisor.

This is intentionally not auto-collected by the ordinary unit suite. The caller must provide a
fresh host-visible root and a Linux image; the process itself must run root, host-networked,
host-PIDed and with the Docker socket mounted. No carrier, modem, call or SMS is involved.
"""
from __future__ import annotations

import json
import os
from pathlib import Path
import sqlite3
import socket
import subprocess
import sys
import time
import uuid

from host import mdd_maintenance_supervisor as supervisor


def run(*args: str, check: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(args, check=check, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, timeout=60)


def docker(*args: str, check: bool = True) -> subprocess.CompletedProcess:
    return run("docker", *args, check=check)


def wait_until(predicate, timeout: float = 30.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.1)
    raise RuntimeError("E2E readiness deadline expired")


def tcp_ready(host: str, port: int) -> bool:
    try:
        with socket.create_connection((host, port), timeout=0.2):
            return True
    except OSError:
        return False


def main() -> int:
    if sys.platform != "linux" or os.geteuid() != 0:
        raise RuntimeError("Linux root is required")
    root = Path(os.environ["MDD_E2E_HOST_ROOT"]).resolve()
    source_root = Path(__file__).resolve().parents[1]
    image = os.environ["MDD_E2E_IMAGE"]
    apk_main = os.environ["MDD_E2E_APK_MAIN"]
    apk_community = os.environ["MDD_E2E_APK_COMMUNITY"]
    data = root / "data"
    orchestrator = data / "orchestrator"
    orchestrator.mkdir(mode=0o700, parents=True, exist_ok=True)
    token = uuid.uuid4().hex[:10]
    control_name = f"mdd-pre8-control-{token}"
    proxy_name = f"mdd-pre8-proxy-{token}"
    txid = f"deploy-e2e-{token}"
    control_script = root / "control_stub.py"
    control_script.write_text(
        """import asyncio
import json
import ssl

BODY = json.dumps(
    {
        "configured": False,
        "authenticated": False,
        "username": "admin",
        "token": "",
        "csrf": None,
    },
    separators=(",", ":"),
).encode()


async def handle(reader, writer):
    try:
        await reader.readuntil(b"\\r\\n\\r\\n")
        writer.write(
            b"HTTP/1.1 200 OK\\r\\n"
            b"Content-Type: application/json\\r\\n"
            b"Connection: close\\r\\n"
            b"Content-Length: "
            + str(len(BODY)).encode()
            + b"\\r\\n\\r\\n"
            + BODY
        )
        await writer.drain()
    finally:
        writer.close()
        await writer.wait_closed()


async def main():
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain("/data/cert.pem", "/data/key.pem")
    tls_server = await asyncio.start_server(
        handle, "127.0.0.1", 18443, ssl=context
    )
    plain_server = await asyncio.start_server(handle, "127.0.0.1", 18000)
    async with tls_server, plain_server:
        await asyncio.gather(
            tls_server.serve_forever(), plain_server.serve_forever()
        )


asyncio.run(main())
""",
        encoding="utf-8")
    database = data / "mdd-sim-gateway.sqlite"
    with sqlite3.connect(database) as connection:
        connection.executescript("""
            CREATE TABLE cellular_call_leases(state TEXT);
            CREATE TABLE messages(direction TEXT,status TEXT);
            CREATE TABLE sms_submission_guards(state TEXT);
            CREATE TABLE allowance_queries(status TEXT);
        """)
    base_command = (
        f"apk add --no-cache --repository '{apk_main}' --repository "
        f"'{apk_community}' python3 openssl >/dev/null")
    try:
        docker("run", "--rm", "-v", f"{data}:/data", image, "sh", "-lc",
               base_command + " && openssl req -x509 -newkey rsa:2048 -nodes -days 1 "
               "-subj /CN=maintenance-e2e -keyout /data/key.pem -out /data/cert.pem >/dev/null 2>&1")
        docker("run", "-d", "--name", control_name, "--network", "host",
               "--label", "io.mdd-sim-gateway.component=control",
               "--label", "io.mdd-sim-gateway.maintenance-upstream=true",
               "-v", f"{source_root}:/work:ro", "-v", f"{root}:/runtime:ro",
               "-v", f"{data}:/data:ro", image,
               "sh", "-lc", base_command + " && exec python3 /runtime/control_stub.py")
        inspector = supervisor.DockerInspector(timeout=2.0)
        wait_until(lambda: docker("inspect", control_name, check=False).returncode == 0)
        control_id = docker("inspect", "-f", "{{.Id}}", control_name).stdout.strip()
        wait_until(lambda: _running_fact(inspector, control_id))
        wait_until(lambda: tcp_ready("127.0.0.1", 18000))
        control = inspector.container(control_id)

        proxy_cidfile = orchestrator / "maintenance-proxy.cid"
        proxy_id = docker(
            "create", "--name", proxy_name, "--network", "host", "--restart", "no",
            "--cidfile", str(proxy_cidfile),
            "--label", "io.mdd-sim-gateway.component=maintenance-proxy",
            "-v", f"{source_root}:/work:ro", "-v", f"{data}:/data", image,
            "sh", "-lc", base_command + " && exec python3 /work/host/mdd_maintenance_proxy.py "
            f"--manifest /data/orchestrator/control-upgrade.json "
            f"--mode-state /data/orchestrator/maintenance-proxy.json "
            f"--ready-state /data/orchestrator/maintenance-proxy-ready.json --txid {txid} "
            "--self-facts /data/orchestrator/proxy-self.json "
            "--container-id-file /data/orchestrator/maintenance-proxy.cid "
            "--cert /data/cert.pem --key /data/key.pem --bind 0.0.0.0 "
            "--tls-port 8443 --plain-port 8000 --admin-bind 127.0.0.1 --admin-port 19090 "
            "--upstream-tls-host 127.0.0.1 --upstream-tls-port 18443 "
            "--upstream-plain-host 127.0.0.1 --upstream-plain-port 18000").stdout.strip()
        raw_proxy = json.loads(docker("inspect", proxy_id).stdout)[0]
        proxy_image = raw_proxy["Image"]
        supervisor._atomic_json(orchestrator / "proxy-self.json", {
            "version": 1, "txid": txid, "container_id": proxy_id,
            "image_id": proxy_image,
        })
        manifest = {
            "version": 1, "txid": txid, "phase": "rollback_committed",
            "owner": {"id": "e2e-owner", "epoch": 1},
            "source_control": {
                "container_id": control.container_id, "image_id": control.image_id,
                "started_at": "2026-08-23T00:00:00Z", "network_mode": "host",
            },
            "rollback_control": {
                "container_id": control.container_id, "image_id": control.image_id,
                "started_at": control.started_at, "pid": control.pid,
                "restart_count": control.restart_count, "network_mode": "host",
                "create_spec_hash": control.create_spec_hash,
            },
            "proxy": {"container_id": proxy_id, "image_id": proxy_image},
            "rollback_upstream": {
                "tls_host": "127.0.0.1", "tls_port": 18443,
                "plain_host": "127.0.0.1", "plain_port": 18000,
                "engine_peers": [],
            },
            "lines": [],
        }
        supervisor._atomic_json(orchestrator / "control-upgrade.json", manifest)
        docker("start", proxy_id)
        try:
            wait_until(lambda: (orchestrator / "maintenance-proxy-ready.json").exists())
        except RuntimeError as exc:
            diagnostic = docker("logs", "--tail", "80", proxy_name, check=False)
            raise RuntimeError(
                "maintenance proxy did not become ready\n" + diagnostic.stdout
                + diagnostic.stderr) from exc
        proxy = inspector.container(proxy_id)
        app = supervisor.MaintenanceSupervisor(
            data, socket_path=root / "supervisor.sock", inspector=inspector)
        app.DRAIN_QUIET = 0.2
        recovered = app.recover()
        if recovered["state"] != "full" or app.entry_fence_path.exists():
            raise RuntimeError("full recovery did not commit its exact fence")
        revoked = app.revoke(wait=True)
        if revoked["state"] != "deny_applied":
            raise RuntimeError("proxy did not acknowledge exact revocation")
        print("host-mode supervisor E2E: PASS")
        return 0
    finally:
        docker("rm", "-f", proxy_name, check=False)
        docker("rm", "-f", control_name, check=False)


def _running_fact(inspector: supervisor.DockerInspector, container_id: str) -> bool:
    try:
        inspector.container(container_id)
        return True
    except Exception:
        return False


if __name__ == "__main__":
    raise SystemExit(main())
