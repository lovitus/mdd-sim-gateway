"""Fail-closed contract for platform cellular-interface isolation.

The small privileged companion is shipped with packaged agents because Python itself cannot
install WFP/netns/pf policy safely.  It returns a signed-by-execution JSON observation; absence,
failure, or an interpreter-wide identity is never treated as isolation.
"""
from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import sys
import threading


class IsolationGuard:
    def __init__(self, helper: str = ""):
        executable = Path(sys.executable).resolve()
        adjacent = executable.with_name("mdd-network-guard.exe" if os.name == "nt"
                                        else "mdd-network-guard")
        self.helper = helper or (str(adjacent) if adjacent.exists() else "")
        self.process = None
        self.interface = ""

    @property
    def active(self) -> bool:
        return bool(self.process and self.process.poll() is None and self.interface)

    def close(self):
        process, self.process = self.process, None
        self.interface = ""
        if process and process.poll() is None:
            process.terminate()

    def ensure(self, interface: str, source_ip: str) -> dict:
        if not self.helper:
            return {"ready": False, "mode": "strict", "error":
                    "The bundled cellular isolation guard is not installed."}
        # Allowing python.exe would also allow unrelated scripts on the workstation. Packaged
        # agents have their own executable identity; development runs remain safely disabled.
        if Path(sys.executable).name.casefold() in {"python.exe", "python3.exe", "python", "python3"}:
            return {"ready": False, "mode": "strict", "error":
                    "Cellular isolation requires the packaged MDD Agent executable."}
        try:
            if self.process and self.process.poll() is None and self.interface == interface:
                return {"ready": True, "mode": "strict", "backend": "platform-guard",
                        "interface": interface, "source_ip": source_ip}
            if self.process and self.process.poll() is None:
                self.process.terminate()
                self.process.wait(timeout=5)
            command = [
                self.helper, "--interface", interface, "--source-ip", source_ip,
                "--pid", str(os.getpid()), "--executable", str(Path(sys.executable).resolve()),
            ]
            # Native platform providers execute in a deliberately small companion process.
            # It is part of MDD's control plane, so WFP must permit its MBN activation request
            # while continuing to block every unrelated application on the cellular LUID.
            if os.name == "nt":
                control = Path(sys.executable).resolve().with_name("mdd-windows-mbn.exe")
                if control.is_file():
                    command += ["--control-executable", str(control)]
                netsh = Path(os.environ.get("SystemRoot", r"C:\Windows")) / "System32" / "netsh.exe"
                if netsh.is_file():
                    command += ["--compat-executable", str(netsh)]
            self.process = subprocess.Popen(
                command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            lines = []
            reader = threading.Thread(
                target=lambda: lines.append(self.process.stdout.readline(4096)), daemon=True)
            reader.start()
            reader.join(15)
            if reader.is_alive():
                self.process.terminate()
                return {"ready": False, "mode": "strict",
                        "error": "cellular isolation guard timed out"}
            line = lines[0] if lines else ""
            value = json.loads(line or "{}")
            if self.process.poll() is not None or value.get("ready") is not True:
                error = self.process.stderr.read(300) if self.process.stderr else ""
                return {"ready": False, "mode": "strict",
                        "error": str(value.get("error") or error or
                                     "cellular isolation verification failed")[:300]}
            self.interface = interface
            return {"ready": True, "mode": "strict", "backend": value.get("backend"),
                    "interface": interface, "source_ip": source_ip}
        except Exception as exc:
            return {"ready": False, "mode": "strict", "error": str(exc)[:300]}
