from types import SimpleNamespace
import threading

import pytest

from agent.macos.isolation import IsolationNotProven, MacHostIsolationMonitor


class Backend:
    def __init__(self):
        self.disconnected = threading.Event()
        self.isolation_ready = False
        self.isolation_error = ""
        self.qualified = 0
        self.revoked = []

    def qualify(self):
        self.qualified += 1

    def revoke(self, reason):
        self.revoked.append(reason)
        self.disconnected.set()


def runner(values):
    def run(command, **_kwargs):
        name = {"ifconfig": "interfaces", "networksetup": "hardware_ports",
                "netstat": "routes", "scutil": "dns"}[command[0].rsplit("/", 1)[-1]]
        value = values[name]
        if isinstance(value, list):
            value = value.pop(0)
        return SimpleNamespace(returncode=0, stdout=value, stderr="")
    return run


def test_monitor_refuses_topology_changed_before_private_link_admission():
    values = {"interfaces": ["lo0 en0", "lo0 en0 en9"],
              "hardware_ports": ["wifi", "wifi"], "routes": ["r", "r"],
              "dns": ["d", "d"]}
    monitor = MacHostIsolationMonitor(runner=runner(values), interval=1)
    with pytest.raises(IsolationNotProven, match="interfaces changed"):
        monitor.admit(Backend())


def test_monitor_revokes_session_when_routes_change():
    values = {"interfaces": ["lo0 en0", "lo0 en0", "lo0 en0"],
              "hardware_ports": ["wifi", "wifi", "wifi"],
              "routes": [
                  "Destination Gateway Flags Netif Expire\ndefault 10.0.0.1 UGSc en0 4",
                  "Destination Gateway Flags Netif Expire\ndefault 10.0.0.1 UGSc en0 3",
                  "Destination Gateway Flags Netif Expire\ndefault 10.0.0.2 UGSc en0 2",
              ],
              "dns": ["dns-a", "dns-a", "dns-a"]}
    monitor = MacHostIsolationMonitor(runner=runner(values), interval=1)
    monitor.interval = .01
    backend = Backend()
    monitor.admit(backend)
    assert backend.disconnected.wait(1)
    assert backend.revoked and "routes changed" in backend.revoked[0]
