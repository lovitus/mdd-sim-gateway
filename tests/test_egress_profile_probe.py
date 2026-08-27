"""Exercise the real profile-probe loader, not only standalone node converters."""
import json
import os
from pathlib import Path
import queue
import socket
import stat
import struct
import subprocess
import sys
import threading
import time
from types import SimpleNamespace
from unittest.mock import Mock

import pytest

from control.app import egress


def test_orchestrator_loader_uses_real_package_and_cached_module():
    first = egress._orchestrator_module()
    assert first.__package__ == "host"
    assert Path(first.__file__).resolve() == (
        Path(egress.__file__).resolve().parents[2] / "host/mdd_orchestrator.py")
    assert egress._orchestrator_module() is first
    assert egress.validate_node_chain("socks5://example.invalid:1080") == 1


def test_orchestrator_loader_works_from_installed_style_control_cwd(tmp_path):
    repo = Path(egress.__file__).resolve().parents[2]
    script = """
import sys
from app import egress
assert 'host.mdd_orchestrator' not in sys.modules
module = egress._orchestrator_module()
assert module.__package__ == 'host'
assert egress._orchestrator_module() is module
assert egress.validate_node_chain('socks5://example.invalid:1080') == 1
print('PACKAGE_LOADER_PASS')
"""
    result = subprocess.run(
        [sys.executable, "-B", "-c", script], cwd=tmp_path,
        env={**os.environ, "PYTHONPATH": str(repo / "control"),
             "MDD_DATA": str(tmp_path / "unused-data")},
        capture_output=True, text=True, timeout=15)
    assert result.returncode == 0, result.stderr
    assert result.stdout.strip() == "PACKAGE_LOADER_PASS"
    assert list(tmp_path.iterdir()) == []  # Importing helpers does not run the host service.


def test_orchestrator_import_failure_is_safe_api_error(monkeypatch):
    monkeypatch.setattr(egress.importlib, "import_module", Mock(
        side_effect=ModuleNotFoundError("private diagnostic with secret.example")))
    with pytest.raises(egress.EgressError) as caught:
        egress._orchestrator_module()
    assert str(caught.value) == "proxy protocol support is unavailable"
    assert "secret.example" not in str(caught.value)
    assert isinstance(caught.value.__cause__, ModuleNotFoundError)


def test_orchestrator_loader_rejects_different_source_tree(monkeypatch):
    monkeypatch.setattr(egress.importlib, "import_module", Mock(return_value=SimpleNamespace(
        __file__="/different-release/host/mdd_orchestrator.py")))
    with pytest.raises(egress.EgressError, match="different source root"):
        egress._orchestrator_module()


@pytest.mark.parametrize("probe_fails", [False, True])
def test_node_probe_uses_real_parser_private_config_and_closes_process(monkeypatch, probe_fails):
    observed = []
    paths = []

    def check(argv, **kwargs):
        path = Path(argv[-1])
        paths.append(path)
        observed.append(json.loads(path.read_text()))
        assert stat.S_IMODE(path.stat().st_mode) == 0o600
        return SimpleNamespace(returncode=0, stdout="", stderr="")

    process = Mock()
    process.poll.return_value = None
    popen = Mock(return_value=process)
    probe = Mock(side_effect=egress.EgressError("UDP test failed: timed out")
                 if probe_fails else None, return_value=29)
    monkeypatch.setattr(egress.shutil, "which", lambda name: "/fixture/sing-box")
    monkeypatch.setattr(egress.subprocess, "run", check)
    monkeypatch.setattr(egress.subprocess, "Popen", popen)
    monkeypatch.setattr(egress, "_wait_tcp", Mock())
    monkeypatch.setattr(egress, "test_udp_proxy", probe)
    if probe_fails:
        with pytest.raises(egress.EgressError, match="timed out"):
            egress.test_proxy_profile({"type": "node", "value": "socks5://example.invalid:1080"})
    else:
        assert egress.test_proxy_profile({
            "type": "node", "value": "socks5://example.invalid:1080"}) == 29
    assert len(observed) == 1
    config = observed[0]
    assert config["inbounds"][0]["listen"] == "127.0.0.1"
    assert config["outbounds"] == [{"type": "socks", "tag": "test-out",
                                    "server": "example.invalid", "server_port": 1080,
                                    "version": "5"}]
    assert config["route"]["rules"] == [{"inbound": ["test-in"], "outbound": "test-out"}]
    assert config["route"]["default_domain_resolver"] == "dns-bootstrap"
    assert config["dns"]["servers"] == [{"type": "local", "tag": "dns-bootstrap"}]
    probe.assert_called_once_with("127.0.0.1", config["inbounds"][0]["listen_port"], 8.0)
    process.terminate.assert_called_once()
    process.wait.assert_called_once_with(3)
    assert all(not path.exists() for path in paths)


def test_invalid_node_config_does_not_start_proxy_or_expose_stderr(monkeypatch):
    monkeypatch.setattr(egress.shutil, "which", lambda _: "/fixture/sing-box")
    monkeypatch.setattr(egress.subprocess, "run", Mock(return_value=SimpleNamespace(
        returncode=1, stdout="", stderr="outbound username=private password=secret")))
    popen = Mock()
    monkeypatch.setattr(egress.subprocess, "Popen", popen)
    with pytest.raises(egress.EgressError) as caught:
        egress.test_proxy_profile({"type": "node", "value": "socks5://example.invalid:1080"})
    assert str(caught.value) == "node configuration is invalid"
    popen.assert_not_called()


@pytest.mark.parametrize(("answers", "fails"), [
    ((True, True), False),
    ((False, False), True),
    ((True, False), False),
    ((False, True), False),
])
def test_udp_probe_real_loopback_association_and_dns_transaction(answers, fails):
    """One local fake SOCKS server; no Internet/DNS/carrier traffic is sent."""
    tcp = socket.socket()
    tcp.bind(("127.0.0.1", 0))
    tcp.listen(1)
    tcp.settimeout(3)
    udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    udp.bind(("127.0.0.1", 0))
    udp.settimeout(3)
    result = queue.Queue()

    def server():
        try:
            with tcp.accept()[0] as connection:
                connection.settimeout(3)
                assert egress._recv_exact(connection, 3) == b"\x05\x01\x00"
                connection.sendall(b"\x05\x00")
                assert egress._recv_exact(connection, 10) == b"\x05\x03\x00\x01" + b"\x00" * 6
                connection.sendall(b"\x05\x00\x00\x01" + b"\x00" * 4 +
                                   struct.pack("!H", udp.getsockname()[1]))
                expected = (
                    (b"\x01\x01\x01\x01", b"\x0acloudflare\x03com\x00\x00\x01\x00\x01"),
                    (b"\x08\x08\x08\x08", b"\x06google\x03com\x00\x00\x01\x00\x01"),
                )
                for probe_index, (address, question) in enumerate(expected):
                    packet, sender = udp.recvfrom(4096)
                    assert packet[:4] == b"\x00\x00\x00\x01"
                    assert packet[4:8] == address
                    assert struct.unpack("!H", packet[8:10])[0] == 53
                    assert packet[22:] == question
                    response = bytearray(packet)
                    if answers[probe_index]:
                        response[12] |= 0x80
                        response[16:18] = b"\x00\x01"
                        response.extend(b"\xc0\x0c\x00\x01\x00\x01\x00\x00\x00\x3c\x00\x04\x01\x02\x03\x04")
                    udp.sendto(response, sender)
                result.put(None)
        except BaseException as exc:
            result.put(exc)

    worker = threading.Thread(target=server, daemon=True)
    worker.start()
    try:
        if fails:
            with pytest.raises(egress.EgressError, match="UDP probes failed"):
                egress.test_udp_proxy("127.0.0.1", tcp.getsockname()[1], timeout=2)
        else:
            assert egress.test_udp_proxy("127.0.0.1", tcp.getsockname()[1], timeout=2) >= 1
        worker.join(4)
        assert not worker.is_alive()
        error = result.get_nowait()
        if error is not None:
            raise error
    finally:
        tcp.close()
        udp.close()
        worker.join(4)


def test_udp_probe_accepts_reverse_order_replies_after_half_the_global_budget():
    """Both requests share one deadline; neither target is limited to half the budget."""
    tcp = socket.socket()
    tcp.bind(("127.0.0.1", 0))
    tcp.listen(1)
    udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    udp.bind(("127.0.0.1", 0))
    result = queue.Queue()

    def server():
        try:
            with tcp.accept()[0] as connection:
                assert egress._recv_exact(connection, 3) == b"\x05\x01\x00"
                connection.sendall(b"\x05\x00")
                assert egress._recv_exact(connection, 10) == b"\x05\x03\x00\x01" + b"\x00" * 6
                connection.sendall(b"\x05\x00\x00\x01" + b"\x00" * 4 +
                                   struct.pack("!H", udp.getsockname()[1]))
                first, sender = udp.recvfrom(4096)
                second, second_sender = udp.recvfrom(4096)
                assert first[4:8] == b"\x01\x01\x01\x01"
                assert second[4:8] == b"\x08\x08\x08\x08"
                time.sleep(.3)
                first_response = bytearray(first); first_response[12] |= 0x80
                first_response[16:18] = b"\x00\x01"
                first_response.extend(
                    b"\xc0\x0c\x00\x01\x00\x01\x00\x00\x00\x3c\x00\x04\x01\x02\x03\x04")
                current = bytearray(second); current[12] |= 0x80
                current[16:18] = b"\x00\x01"
                current.extend(
                    b"\xc0\x0c\x00\x01\x00\x01\x00\x00\x00\x3c\x00\x04\x01\x02\x03\x04")
                udp.sendto(current, second_sender)
                udp.sendto(first_response, sender)
                result.put(None)
        except BaseException as exc:
            result.put(exc)

    worker = threading.Thread(target=server, daemon=True)
    worker.start()
    try:
        assert egress.test_udp_proxy("127.0.0.1", tcp.getsockname()[1], timeout=.5) >= 300
        worker.join(2)
        assert not worker.is_alive()
        error = result.get_nowait()
        if error is not None:
            raise error
    finally:
        tcp.close()
        udp.close()
        worker.join(2)


def test_udp_probe_passes_when_only_google_answers_and_reports_target():
    """A Cloudflare outage must not turn a working applied UDP path red."""
    tcp = socket.socket()
    tcp.bind(("127.0.0.1", 0))
    tcp.listen(1)
    udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    udp.bind(("127.0.0.1", 0))
    result = queue.Queue()

    def server():
        try:
            with tcp.accept()[0] as connection:
                assert egress._recv_exact(connection, 3) == b"\x05\x01\x00"
                connection.sendall(b"\x05\x00")
                assert egress._recv_exact(connection, 10) == b"\x05\x03\x00\x01" + b"\x00" * 6
                connection.sendall(b"\x05\x00\x00\x01" + b"\x00" * 4 +
                                   struct.pack("!H", udp.getsockname()[1]))
                first, _ = udp.recvfrom(4096)
                second, sender = udp.recvfrom(4096)
                assert first[4:8] == b"\x01\x01\x01\x01"
                assert second[4:8] == b"\x08\x08\x08\x08"
                response = bytearray(second); response[12] |= 0x80
                response[16:18] = b"\x00\x01"
                response.extend(
                    b"\xc0\x0c\x00\x01\x00\x01\x00\x00\x00\x3c\x00\x04\x01\x02\x03\x04")
                udp.sendto(response, sender)
                result.put(None)
        except BaseException as exc:
            result.put(exc)

    worker = threading.Thread(target=server, daemon=True)
    worker.start()
    try:
        details = egress.test_udp_proxy(
            "127.0.0.1", tcp.getsockname()[1], timeout=.5, return_details=True)
        assert details["target"] == "8.8.8.8"
        assert details["attempted_targets"] == ["1.1.1.1", "8.8.8.8"]
        assert details["latency_ms"] >= 1
        worker.join(2)
        assert not worker.is_alive()
        error = result.get_nowait()
        if error is not None:
            raise error
    finally:
        tcp.close()
        udp.close()
        worker.join(2)
