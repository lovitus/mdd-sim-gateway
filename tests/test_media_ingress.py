import hashlib
import json
import time
from pathlib import Path
from types import SimpleNamespace

import pytest

from control.app import media_ingress
from control.app.sip_media_proxy import SipMediaRewriteError, rewrite_engine_sdp
from host import mdd_orchestrator


def _inventory(candidates):
    projected = [{key: item[key] for key in
                  ("id", "interface", "ifindex", "address", "family", "kind", "up")}
                 for item in candidates]
    generation = hashlib.sha256(json.dumps(
        projected, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return {"version": 1, "generation": generation,
            "candidates": candidates, "updated_at": int(time.time())}


def _singbox_config(*endpoints):
    inbounds = []
    for country, interface, address in endpoints:
        inbounds.append({
            "type": "tun",
            "tag": f"tun-{country}",
            "interface_name": interface,
            "address": [address],
        })
    return {"inbounds": inbounds}


def _candidate(interface="tun0", ifindex=7, address="10.44.0.23"):
    return {
        "id": media_ingress.candidate_id(interface, ifindex, address),
        "interface": interface, "ifindex": ifindex, "address": address,
        "family": "ipv4", "scope": "global", "kind": "tun",
        "up": True, "bridge": False,
    }


def test_media_route_is_host_inventory_and_control_generation_fenced(tmp_path, monkeypatch):
    monkeypatch.setattr(media_ingress.cfg, "DATA_DIR", str(tmp_path))
    candidate = _candidate()
    path = Path(tmp_path) / "orchestrator" / "network-inventory.json"
    path.parent.mkdir()
    document = _inventory([candidate])
    path.write_text(json.dumps(document))

    status = media_ingress.status("10.44.0.23:8443")
    assert status["candidate"]["id"] == candidate["id"]
    assert not status["confirmed"]
    confirmed = media_ingress.confirm(
        candidate["id"], document["generation"], "10.44.0.23:8443")
    assert confirmed["confirmed"]
    assert not media_ingress.status("192.168.111.225:8443")["confirmed"]

    # A deployment/control restart invalidates the proof without deleting the user's choice.
    monkeypatch.setattr(media_ingress, "CONTROL_EPOCH", "next-control-run")
    assert not media_ingress.status("10.44.0.23:8443")["confirmed"]


def test_admission_binding_changes_with_host_inventory_generation(tmp_path, monkeypatch):
    monkeypatch.setattr(media_ingress.cfg, "DATA_DIR", str(tmp_path))
    path = Path(tmp_path) / "orchestrator" / "network-inventory.json"
    path.parent.mkdir()
    primary = _candidate(interface="eth0", ifindex=2, address="192.0.2.10")
    first_inventory = _inventory([primary])
    path.write_text(json.dumps(first_inventory))
    media_ingress.confirm(primary["id"], first_inventory["generation"],
                          "192.0.2.10:8443")
    first_binding = media_ingress.binding_id(
        media_ingress.status("192.0.2.10:8443"))
    assert first_binding

    secondary = _candidate(interface="tun0", ifindex=3, address="198.51.100.20")
    second_inventory = _inventory([primary, secondary])
    path.write_text(json.dumps(second_inventory))
    media_ingress.confirm(primary["id"], second_inventory["generation"],
                          "192.0.2.10:8443")
    second_binding = media_ingress.binding_id(
        media_ingress.status("192.0.2.10:8443"))
    assert second_binding
    assert second_binding != first_binding


def test_mdd_owned_egress_tunnels_do_not_affect_media_binding(tmp_path, monkeypatch):
    monkeypatch.setattr(media_ingress.cfg, "DATA_DIR", str(tmp_path))
    root = Path(tmp_path) / "orchestrator"
    root.mkdir()
    inventory_path = root / "network-inventory.json"
    singbox_path = root / "sing-box.json"
    primary = _candidate(interface="tun0", ifindex=3, address="10.44.0.23")
    managed = _candidate(interface="mdd-gb", ifindex=21, address="172.29.21.1")
    managed_changed = _candidate(interface="mdd-gb", ifindex=91, address="172.29.21.1")
    singbox_path.write_text(json.dumps(_singbox_config(("gb", "mdd-gb", "172.29.21.1/30"))))

    inventory_path.write_text(json.dumps(_inventory([primary, managed])))
    first = media_ingress.status("10.44.0.23:8443")
    assert [item["interface"] for item in first["candidates"]] == ["tun0"]
    assert media_ingress.resolve("172.29.21.1:8443")["candidate"] is None
    confirmed = media_ingress.confirm(primary["id"], first["inventory_generation"],
                                      "10.44.0.23:8443")
    first_binding = media_ingress.binding_id(confirmed)
    assert first_binding

    inventory_path.write_text(json.dumps(_inventory([primary])))
    without_mdd = media_ingress.status("10.44.0.23:8443")
    assert without_mdd["confirmed"]
    assert media_ingress.binding_id(without_mdd) == first_binding

    inventory_path.write_text(json.dumps(_inventory([primary, managed_changed])))
    changed_mdd = media_ingress.status("10.44.0.23:8443")
    assert changed_mdd["confirmed"]
    assert media_ingress.binding_id(changed_mdd) == first_binding


def test_user_vpn_named_mdd_prefix_is_not_excluded_without_ownership(tmp_path, monkeypatch):
    monkeypatch.setattr(media_ingress.cfg, "DATA_DIR", str(tmp_path))
    root = Path(tmp_path) / "orchestrator"
    root.mkdir()
    candidate = _candidate(interface="mdd-vpn", ifindex=44, address="100.127.0.1")
    (root / "sing-box.json").write_text(json.dumps(_singbox_config(
        ("gb", "mdd-gb", "172.29.21.1/30"))))
    (root / "network-inventory.json").write_text(json.dumps(_inventory([candidate])))

    status = media_ingress.status("100.127.0.1:8443")
    assert status["candidate"]["interface"] == "mdd-vpn"


def test_host_inventory_excludes_only_exact_mdd_owned_tun_endpoints(monkeypatch):
    address_rows = [
        {"ifname": "wlp4s0", "ifindex": 2, "flags": ["UP"], "operstate": "UP",
         "addr_info": [{"family": "inet", "scope": "global", "local": "192.0.2.10"}]},
        {"ifname": "tun0", "ifindex": 7, "flags": ["UP"], "operstate": "UNKNOWN",
         "addr_info": [{"family": "inet", "scope": "global", "local": "10.44.0.23"}]},
        {"ifname": "mdd-gb", "ifindex": 21, "flags": ["UP"], "operstate": "UNKNOWN",
         "addr_info": [{"family": "inet", "scope": "global", "local": "172.29.21.1"}]},
        {"ifname": "mdd-vpn", "ifindex": 44, "flags": ["UP"], "operstate": "UNKNOWN",
         "addr_info": [{"family": "inet", "scope": "global", "local": "100.127.0.1"}]},
    ]
    link_rows = [
        {"ifindex": 2, "linkinfo": {"info_kind": "ether"}},
        {"ifindex": 7, "linkinfo": {"info_kind": "tun"}},
        {"ifindex": 21, "linkinfo": {"info_kind": "tun"}},
        {"ifindex": 44, "linkinfo": {"info_kind": "tun"}},
    ]

    def fake_run(args, **_kwargs):
        if args[:4] == ["ip", "-j", "-4", "address"]:
            return SimpleNamespace(returncode=0, stdout=json.dumps(address_rows))
        if args[:3] == ["ip", "-j", "-details"]:
            return SimpleNamespace(returncode=0, stdout=json.dumps(link_rows))
        raise AssertionError(args)

    monkeypatch.setattr(mdd_orchestrator, "run", fake_run)

    inventory = mdd_orchestrator.host_network_inventory({("mdd-gb", "172.29.21.1")})
    assert [item["interface"] for item in inventory["candidates"]] == [
        "wlp4s0", "tun0", "mdd-vpn"]


def test_failed_singbox_apply_does_not_publish_planned_media_egress(tmp_path, monkeypatch):
    app = mdd_orchestrator.Orchestrator(tmp_path, Path.cwd(), dry_run=False)
    config = _singbox_config(("gb", "mdd-gb", "172.29.21.1/30"))
    config.update({"outbounds": [], "route": {"rules": []}})

    class ExitedProcess:
        def poll(self):
            return 1

    monkeypatch.setattr(mdd_orchestrator.shutil, "which", lambda _binary: "/bin/sing-box")
    monkeypatch.setattr(mdd_orchestrator, "run",
                        lambda *_args, **_kwargs: SimpleNamespace(returncode=0, stdout="", stderr=""))
    monkeypatch.setattr(mdd_orchestrator.subprocess, "Popen", lambda *_args, **_kwargs: ExitedProcess())
    monkeypatch.setattr(mdd_orchestrator.time, "sleep", lambda _seconds: None)
    monkeypatch.setattr(mdd_orchestrator, "wait_tun_endpoints_gone",
                        lambda _endpoints, **_kwargs: True)

    with pytest.raises(RuntimeError, match="sing-box exited during startup"):
        app.apply_singbox(config)

    assert not app.generated.exists()
    assert not mdd_orchestrator.singbox_tun_endpoints(mdd_orchestrator.read_json(app.generated))


def test_disabled_proxy_clears_confirmed_singbox_ownership(tmp_path, monkeypatch):
    app = mdd_orchestrator.Orchestrator(tmp_path, Path.cwd(), dry_run=True)
    app.root.mkdir(parents=True, exist_ok=True)
    app.generated.write_text(json.dumps(_singbox_config(
        ("gb", "mdd-gb", "172.29.21.1/30"))))

    class RunningProcess:
        def __init__(self):
            self.running = True

        def poll(self):
            return None if self.running else 0

        def terminate(self):
            self.running = False

        def wait(self, _timeout=None):
            self.running = False
            return 0

        def kill(self):
            self.running = False

    app.singbox = RunningProcess()
    monkeypatch.setattr(mdd_orchestrator, "wait_tun_endpoints_gone",
                        lambda _endpoints, **_kwargs: True)
    app.reconcile_proxy({"proxy": {"enabled": False}, "lines": [], "generation": "off"})
    assert not app.generated.exists()

    monkeypatch.setattr(media_ingress.cfg, "DATA_DIR", str(tmp_path))
    candidate = _candidate(interface="mdd-gb", ifindex=21, address="172.29.21.1")
    (app.root / "network-inventory.json").write_text(json.dumps(_inventory([candidate])))
    status = media_ingress.status("172.29.21.1:8443")
    assert status["candidate"]["interface"] == "mdd-gb"


def test_disabled_proxy_keeps_ownership_until_endpoint_disappears(tmp_path, monkeypatch):
    app = mdd_orchestrator.Orchestrator(tmp_path, Path.cwd(), dry_run=True)
    app.root.mkdir(parents=True, exist_ok=True)
    app.generated.write_text(json.dumps(_singbox_config(
        ("gb", "mdd-gb", "172.29.21.1/30"))))

    class StoppedProcess:
        def poll(self):
            return 0

    app.singbox = StoppedProcess()
    monkeypatch.setattr(mdd_orchestrator, "wait_tun_endpoints_gone",
                        lambda _endpoints, **_kwargs: False)
    app.reconcile_proxy({"proxy": {"enabled": False}, "lines": [], "generation": "off"})
    assert app.generated.exists()

    monkeypatch.setattr(media_ingress.cfg, "DATA_DIR", str(tmp_path))
    candidate = _candidate(interface="mdd-gb", ifindex=21, address="172.29.21.1")
    (app.root / "network-inventory.json").write_text(json.dumps(_inventory([candidate])))
    assert media_ingress.status("172.29.21.1:8443")["candidate"] is None


def test_failed_old_singbox_restore_keeps_old_internal_endpoint_filtered(tmp_path, monkeypatch):
    app = mdd_orchestrator.Orchestrator(tmp_path, Path.cwd(), dry_run=False)
    app.root.mkdir(parents=True, exist_ok=True)
    old_config = _singbox_config(("gb", "mdd-gb", "172.29.21.1/30"))
    old_config.update({"outbounds": [], "route": {"rules": []}})
    app.generated.write_text(json.dumps(old_config))
    new_config = _singbox_config(("fr", "mdd-fr", "172.29.22.1/30"))
    new_config.update({"outbounds": [], "route": {"rules": []}})

    class ExitedProcess:
        def poll(self):
            return 1

    def fake_wait(endpoints, **_kwargs):
        if ("mdd-fr", "172.29.22.1") in endpoints:
            return True
        if ("mdd-gb", "172.29.21.1") in endpoints:
            return False
        return True

    monkeypatch.setattr(mdd_orchestrator.shutil, "which", lambda _binary: "/bin/sing-box")
    monkeypatch.setattr(mdd_orchestrator, "run",
                        lambda *_args, **_kwargs: SimpleNamespace(returncode=0, stdout="", stderr=""))
    monkeypatch.setattr(mdd_orchestrator.subprocess, "Popen", lambda *_args, **_kwargs: ExitedProcess())
    monkeypatch.setattr(mdd_orchestrator.time, "sleep", lambda _seconds: None)
    monkeypatch.setattr(mdd_orchestrator, "wait_tun_endpoints_gone", fake_wait)

    with pytest.raises(RuntimeError, match="sing-box exited during startup"):
        app.apply_singbox(new_config)

    assert mdd_orchestrator.singbox_tun_endpoints(mdd_orchestrator.read_json(
        app.generated)) == {("mdd-gb", "172.29.21.1")}
    monkeypatch.setattr(media_ingress.cfg, "DATA_DIR", str(tmp_path))
    candidate = _candidate(interface="mdd-gb", ifindex=21, address="172.29.21.1")
    (app.root / "network-inventory.json").write_text(json.dumps(_inventory([candidate])))
    assert media_ingress.status("172.29.21.1:8443")["candidate"] is None


def test_malformed_endpoint_probe_keeps_generated_ownership(tmp_path, monkeypatch):
    def fake_run(_args, **_kwargs):
        return SimpleNamespace(returncode=0, stdout="{}")

    monkeypatch.setattr(mdd_orchestrator, "run", fake_run)
    assert mdd_orchestrator.host_tun_endpoint_present("mdd-gb", "172.29.21.1")

    monkeypatch.setattr(mdd_orchestrator, "run", lambda *_args, **_kwargs: SimpleNamespace(
        returncode=0,
        stdout=json.dumps([{"ifname": "mdd-gb", "addr_info": {"local": "172.29.21.1"}}]),
    ))
    assert mdd_orchestrator.host_tun_endpoint_present("mdd-gb", "172.29.21.1")

    monkeypatch.setattr(mdd_orchestrator, "run", lambda *_args, **_kwargs: SimpleNamespace(
        returncode=0,
        stdout=json.dumps([{"ifname": "mdd-gb", "addr_info": [
            {"family": "inet", "local": "not-an-ip"}]}]),
    ))
    assert mdd_orchestrator.host_tun_endpoint_present("mdd-gb", "172.29.21.1")

    app = mdd_orchestrator.Orchestrator(tmp_path, Path.cwd(), dry_run=True)
    app.root.mkdir(parents=True, exist_ok=True)
    app.generated.write_text(json.dumps(_singbox_config(
        ("gb", "mdd-gb", "172.29.21.1/30"))))
    app.singbox = None
    ticks = iter([0.0, 4.0])
    monkeypatch.setattr(mdd_orchestrator, "run", fake_run)
    monkeypatch.setattr(mdd_orchestrator.time, "monotonic",
                        lambda: next(ticks, 4.0))
    monkeypatch.setattr(mdd_orchestrator.time, "sleep", lambda _seconds: None)

    app.stop_singbox()
    assert app.generated.exists()


def test_media_route_rejects_dns_forwarded_and_bridge_candidates(tmp_path, monkeypatch):
    monkeypatch.setattr(media_ingress.cfg, "DATA_DIR", str(tmp_path))
    candidate = _candidate(interface="docker0", ifindex=4, address="172.17.0.1")
    candidate["bridge"] = True
    path = Path(tmp_path) / "orchestrator" / "network-inventory.json"
    path.parent.mkdir()
    path.write_text(json.dumps(_inventory([candidate])))
    assert media_ingress.resolve("gateway.example.com")["candidate"] is None
    assert media_ingress.resolve("172.17.0.1:8443")["candidate"] is None
    assert media_ingress.same_origin("https://10.44.0.23:8443", "10.44.0.23:8443")
    assert media_ingress.same_origin("https://10.44.0.23", "10.44.0.23:443")
    assert not media_ingress.same_origin("https://evil.invalid", "10.44.0.23:8443")


def test_stale_orchestrator_inventory_is_not_authoritative(tmp_path, monkeypatch):
    monkeypatch.setattr(media_ingress.cfg, "DATA_DIR", str(tmp_path))
    candidate = _candidate()
    document = _inventory([candidate])
    document["updated_at"] = int(time.time()) - media_ingress.INVENTORY_FRESH_SECONDS - 1
    path = Path(tmp_path) / "orchestrator" / "network-inventory.json"
    path.parent.mkdir()
    path.write_text(json.dumps(document))
    assert media_ingress.resolve("10.44.0.23:8443")["candidate"] is None


SDP = ("SIP/2.0 200 OK\r\n"
       "Content-Type: application/sdp\r\n"
       "Content-Length: 257\r\n\r\n"
       "v=0\r\n"
       "o=- 1 1 IN IP4 172.18.0.5\r\n"
       "s=-\r\n"
       "c=IN IP4 172.18.0.5\r\n"
       "t=0 0\r\n"
       "m=audio 12000 UDP/TLS/RTP/SAVPF 111\r\n"
       "a=rtcp:12001 IN IP4 172.18.0.5\r\n"
       "a=candidate:old 1 UDP 2130706431 172.18.0.5 12000 typ host generation 0\r\n"
       "a=candidate:relay 1 UDP 1 198.51.100.9 40000 typ relay\r\n")


@pytest.mark.parametrize("binary", [False, True])
def test_sip_proxy_rewrites_only_engine_host_candidates_and_preserves_opcode(binary):
    source = SDP.encode() if binary else SDP
    result = rewrite_engine_sdp(
        source, engine_ip="172.18.0.5", advertised_ip="10.44.0.23",
        route_id="route-a", rtp_start=12000, rtp_end=12031)
    assert isinstance(result, bytes if binary else str)
    text = result.decode() if binary else result
    assert "c=IN IP4 10.44.0.23" in text
    assert "a=rtcp:12001 IN IP4 10.44.0.23" in text
    assert "typ host" in text and " 10.44.0.23 12000 typ host" in text
    assert "o=- 1 1 IN IP4 172.18.0.5" in text
    assert "198.51.100.9 40000 typ relay" in text
    head, body = text.split("\r\n\r\n", 1)
    assert f"Content-Length: {len(body.encode())}" in head


def test_sip_proxy_fails_closed_for_claimed_sdp_without_engine_address():
    with pytest.raises(SipMediaRewriteError):
        rewrite_engine_sdp(
            SDP.replace("172.18.0.5", "172.18.0.6"),
            engine_ip="172.18.0.5", advertised_ip="10.44.0.23", route_id="route",
            rtp_start=12000, rtp_end=12031)


def test_sip_proxy_leaves_non_sdp_frame_unchanged():
    register = "REGISTER sip:example SIP/2.0\r\nContent-Length: 0\r\n\r\n"
    assert rewrite_engine_sdp(
        register, engine_ip="172.18.0.5", advertised_ip="10.44.0.23",
        route_id="route", rtp_start=12000, rtp_end=12031) == register


def test_sip_proxy_rejects_unpublished_ports_tcp_and_duplicate_framing():
    for invalid in (
        SDP.replace("m=audio 12000", "m=audio 13000"),
        SDP.replace("a=rtcp:12001", "a=rtcp:13001"),
        SDP.replace(" 12000 typ host", " 13000 typ host"),
        SDP.replace(" UDP 2130706431", " TCP 2130706431"),
        SDP.replace("Content-Type: application/sdp",
                    "Content-Type: application/sdp\r\nc: application/sdp"),
        SDP.replace("Content-Length: 257", "Content-Length: 257\r\n folded"),
        SDP.replace("Content-Length: 257", "Content-Length: 257\r\nl: 257"),
    ):
        with pytest.raises(SipMediaRewriteError):
            rewrite_engine_sdp(
                invalid, engine_ip="172.18.0.5", advertised_ip="10.44.0.23",
                route_id="route", rtp_start=12000, rtp_end=12031)


def test_ice_components_share_foundation_after_route_rewrite():
    two_components = SDP.replace(
        "a=candidate:relay 1 UDP 1 198.51.100.9 40000 typ relay",
        "a=candidate:other 2 UDP 2130706430 172.18.0.5 12001 typ host")
    rewritten = rewrite_engine_sdp(
        two_components, engine_ip="172.18.0.5", advertised_ip="10.44.0.23",
        route_id="route", rtp_start=12000, rtp_end=12031)
    foundations = [line.split()[0].split(":", 1)[1]
                   for line in rewritten.splitlines() if line.startswith("a=candidate:")]
    assert len(foundations) == 2
    assert len(set(foundations)) == 1
