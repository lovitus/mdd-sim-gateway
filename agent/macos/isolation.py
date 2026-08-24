"""Continuous fail-closed host-network qualification for private macOS cellular links."""

from __future__ import annotations

import hashlib
import subprocess
import threading


class IsolationNotProven(RuntimeError):
    pass


_COMMANDS = {
    "interfaces": ["/sbin/ifconfig", "-l"],
    "hardware_ports": ["/usr/sbin/networksetup", "-listallhardwareports"],
    "routes": ["/usr/sbin/netstat", "-rn", "-f", "inet"],
    "dns": ["/usr/sbin/scutil", "--dns"],
}


def _fingerprint(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8", errors="replace")).hexdigest()


def _stable_output(name: str, value: str) -> str:
    if name != "routes":
        return value
    # macOS netstat appends volatile use/expiry counters.  Hashing the raw table
    # causes false revocation even though route ownership did not change.  The
    # security boundary is the default route's gateway and interface; all new
    # interfaces are independently covered by the topology fingerprints.
    lines = value.splitlines()
    header = next((line.split() for line in lines if line.split() and
                   line.split()[0] == "Destination"), [])
    try:
        gateway_index = header.index("Gateway")
        interface_index = header.index("Netif")
    except ValueError:
        gateway_index, interface_index = 1, 3
    stable = []
    for line in lines:
        fields = line.split()
        if not fields or fields[0] != "default":
            continue
        if max(gateway_index, interface_index) >= len(fields):
            raise IsolationNotProven("cannot parse the macOS default route")
        stable.append(f"default {fields[gateway_index]} {fields[interface_index]}")
    return "\n".join(sorted(stable))


class MacHostIsolationMonitor:
    """Revoke a private link when its USB or host-network proof changes."""

    def __init__(self, *, runner=subprocess.run, interval: float = 30.0):
        self.runner = runner
        self.interval = max(1.0, float(interval))
        self.startup_error = ""
        try:
            self.installation = self.capture()
        except Exception as exc:
            self.installation = None
            self.startup_error = str(exc) or type(exc).__name__

    def capture(self) -> dict:
        result = {}
        for name, command in _COMMANDS.items():
            completed = self.runner(
                command, capture_output=True, text=True, encoding="utf-8",
                errors="replace", timeout=10, check=False)
            if completed.returncode:
                detail = (completed.stderr or completed.stdout or name).strip()[:300]
                raise IsolationNotProven(f"cannot verify macOS {name}: {detail}")
            result[name] = _fingerprint(_stable_output(name, completed.stdout))
        return result

    def admit(self, backend) -> None:
        if self.installation is None:
            raise IsolationNotProven(
                f"isolation_not_proven: {self.startup_error or 'host baseline unavailable'}")
        current = self.capture()
        for name in ("interfaces", "hardware_ports"):
            if current[name] != self.installation[name]:
                raise IsolationNotProven(
                    f"isolation_not_proven: macOS {name} changed since Agent startup")
        backend.qualify()
        backend.isolation_ready = True
        backend.isolation_error = ""

        def watch():
            while not backend.disconnected.wait(self.interval):
                try:
                    observed = self.capture()
                    for name in ("interfaces", "hardware_ports"):
                        if observed[name] != self.installation[name]:
                            raise IsolationNotProven(
                                f"macOS {name} changed since Agent startup")
                    for name in ("routes", "dns"):
                        if observed[name] != current[name]:
                            raise IsolationNotProven(
                                f"macOS {name} changed during the private cellular session")
                    # Host topology is checked first even if a legitimate modem
                    # operation currently occupies the companion's serial request
                    # executor.  The USB proof then waits its turn instead of being
                    # misclassified as a ten-second isolation failure.
                    backend.qualify()
                except Exception as exc:
                    backend.revoke(f"isolation_not_proven: {str(exc).strip()}")
                    return

        threading.Thread(target=watch, name="mdd-mac-isolation", daemon=True).start()
