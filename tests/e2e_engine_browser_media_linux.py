#!/usr/bin/env python3
"""No-network runner for the Engine image's browser media Asterisk fixtures."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import time


RUN_ID = "22222222-3333-4444-8555-666666666666"


def wait_ami(process: subprocess.Popen, log_path: Path) -> None:
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(
                f"Asterisk exited during startup:\n{log_path.read_text(errors='replace')[-12000:]}")
        try:
            with socket.create_connection(("127.0.0.1", 5038), timeout=0.2):
                return
        except OSError:
            time.sleep(0.05)
    raise RuntimeError("Asterisk AMI did not become ready")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("outbound", "inbound"))
    args = parser.parse_args()
    config = {
        "id": "17", "name": "browser-media-e2e", "mcc": "001", "mnc": "01",
        "imsi": "001010000000017", "msisdn": "15550000000",
        "reader": "e2e-reader", "reader_index": 0, "local_addr": "127.0.0.1",
        "ami_user": "vowifi", "ami_secret": "mdd-e2e-ami-secret",
        "manager_url": "https://127.0.0.1:9",
        "manager_event_token": "A" * 43, "manager_tls_self_signed": True,
        "sip": {"webrtc": {"enable": False}, "ring_timeout": 10},
    }
    Path("/config").mkdir(mode=0o700, exist_ok=True)
    Path("/logs").mkdir(mode=0o700, exist_ok=True)
    Path("/run/mdd-sim-gateway").mkdir(mode=0o700, parents=True, exist_ok=True)
    Path("/config/instance.json").write_text(json.dumps(config), encoding="utf-8")
    os.chmod("/config/instance.json", 0o600)
    environment = dict(os.environ, MDD_ENGINE_RUN_ID=RUN_ID,
                       MDD_ADMISSION_SOCKET="/run/mdd-sim-gateway/test-admission.sock")
    subprocess.run(["python3", "/usr/local/bin/render.py"], env=environment,
                   check=True, timeout=10, stdout=subprocess.DEVNULL)
    shutil.copyfile("/e2e/browser_outbound_websocket_client.conf",
                    "/etc/asterisk/websocket_client.conf")
    if args.mode == "outbound":
        shutil.copyfile("/e2e/browser_outbound_pjsip.conf", "/etc/asterisk/pjsip.conf")
        fixture = "/e2e/browser_outbound_asterisk_e2e.py"
    else:
        shutil.copyfile("/e2e/browser_inbound_pjsip.conf", "/etc/asterisk/pjsip.conf")
        shutil.copyfile("/e2e/browser_inbound_extensions.conf",
                        "/etc/asterisk/extensions.conf")
        Path("/e3").mkdir(exist_ok=True)
        shutil.copyfile("/e2e/browser_outbound_asterisk_e2e.py", "/e3/e2base.py")
        fixture = "/e2e/browser_inbound_answer_asterisk_e2e.py"

    log_path = Path(f"/tmp/asterisk-browser-{args.mode}.log")
    with log_path.open("w", encoding="utf-8") as output:
        process = subprocess.Popen(["asterisk", "-f", "-C",
                                    "/etc/asterisk/asterisk.conf"],
                                   env=environment, stdout=output,
                                   stderr=subprocess.STDOUT, text=True)
        try:
            wait_ami(process, log_path)
            booted = subprocess.run(["asterisk", "-rx", "core waitfullybooted"],
                                    env=environment, text=True, capture_output=True,
                                    timeout=20)
            if booted.returncode:
                raise RuntimeError(
                    f"Asterisk did not become fully booted\n{booted.stdout}{booted.stderr}\n"
                    f"{log_path.read_text(errors='replace')[-12000:]}")
            expected_context = ("browser-media-outbound-warmup"
                                if args.mode == "outbound" else "e3-incoming")
            context_marker = f"[ Context '{expected_context}' created by 'pbx_config' ]"
            extension_marker = "'echo'" if args.mode == "outbound" else "'15550000000'"
            deadline = time.monotonic() + 20
            dialplan = None
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    break
                dialplan = subprocess.run(
                    ["asterisk", "-rx", "dialplan show"], env=environment,
                    text=True, capture_output=True, timeout=5)
                if (dialplan.returncode == 0 and context_marker in dialplan.stdout
                        and extension_marker in dialplan.stdout):
                    break
                time.sleep(0.1)
            assert dialplan is not None
            if (dialplan.returncode or context_marker not in dialplan.stdout
                    or extension_marker not in dialplan.stdout):
                settings = subprocess.run(
                    ["asterisk", "-rx", "core show settings"], env=environment,
                    text=True, capture_output=True, timeout=5)
                modules = subprocess.run(
                    ["asterisk", "-rx", "module show like pbx_config"], env=environment,
                    text=True, capture_output=True, timeout=5)
                raise RuntimeError(
                    f"expected dialplan context is unavailable\n"
                    f"{dialplan.stdout}{dialplan.stderr}\n"
                    f"Settings:\n{settings.stdout}{settings.stderr}\n"
                    f"Modules:\n{modules.stdout}{modules.stderr}\n"
                    f"extensions.conf:\n"
                    f"{Path('/etc/asterisk/extensions.conf').read_text(errors='replace')[-12000:]}\n"
                    f"Asterisk:\n{log_path.read_text(errors='replace')[-12000:]}")
            result = subprocess.run(["python3", fixture], env=environment, text=True,
                                    capture_output=True, timeout=90)
            if result.returncode:
                raise RuntimeError(
                    f"{args.mode} fixture failed rc={result.returncode}\n"
                    f"{result.stdout}{result.stderr}\n"
                    f"Dialplan:\n{dialplan.stdout}{dialplan.stderr}\n"
                    f"Asterisk:\n{log_path.read_text(errors='replace')[-12000:]}")
            print(result.stdout.strip())
        finally:
            process.terminate()
            try:
                process.wait(10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(5)
    print(json.dumps({"browser_media": args.mode, "network": "none",
                      "asterisk_stopped": True}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
