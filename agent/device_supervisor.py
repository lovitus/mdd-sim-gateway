"""Capability-driven attachment discovery and idempotent modem reconciliation."""

from __future__ import annotations

import re
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path


DEVICE_LINE = re.compile(
    r"^device vid=([0-9a-f]{4}) pid=([0-9a-f]{4}) bus=(\d+) address=(\d+) serial=(.*)$",
    re.I)


def cellular_io_command(configured: str = "") -> str:
    value = str(configured or "").strip()
    if value:
        return value
    module = Path(__file__).resolve().parent
    bundle = Path(str(getattr(sys, "_MEIPASS", "") or module))
    candidates = [
        Path(sys.executable).resolve().with_name("mdd-cellular-io"),
        module / "mdd-cellular-io",
        bundle / "mdd-cellular-io",
    ]
    return str(next((item for item in candidates if item.is_file()), "") or
               shutil.which("mdd-cellular-io") or "")


@dataclass(frozen=True)
class UsbModemAttachment:
    vid: int
    pid: int
    bus: int
    address: int
    serial: str = ""

    @property
    def physical_identity(self) -> str:
        stable = self.serial or f"location:{self.bus}:{self.address}"
        return f"usb:{self.vid:04x}:{self.pid:04x}:{stable}"

    @property
    def generation(self) -> str:
        return f"{self.physical_identity}@{self.bus}:{self.address}"

    def public(self) -> dict:
        return {"vid": self.vid, "pid": self.pid, "bus": self.bus,
                "address": self.address, "serial": self.serial,
                "physical_identity": self.physical_identity,
                "generation": self.generation}


class MacUsbModemDiscovery:
    """The fixed companion performs raw USB probing; Python only parses its stable contract."""

    def __init__(self, executable: str = "", runner=subprocess.run):
        self.executable = cellular_io_command(executable)
        self.runner = runner

    def enumerate(self, exclude: set[tuple[int, int]] | None = None
                  ) -> list[UsbModemAttachment]:
        if not self.executable:
            return []
        command = [self.executable, "--list"]
        for bus, address in sorted(exclude or set()):
            command.extend(("--exclude", f"{int(bus)}:{int(address)}"))
        result = self.runner(
            command, capture_output=True, text=True,
            encoding="utf-8", errors="replace", timeout=30, check=False)
        if result.returncode:
            raise RuntimeError((result.stderr or "raw USB modem discovery failed").strip()[:500])
        devices = []
        for line in str(result.stdout or "").splitlines():
            match = DEVICE_LINE.fullmatch(line.strip())
            if not match:
                continue
            devices.append(UsbModemAttachment(
                vid=int(match.group(1), 16), pid=int(match.group(2), 16),
                bus=int(match.group(3)), address=int(match.group(4)),
                serial=match.group(5).strip()))
        return devices
