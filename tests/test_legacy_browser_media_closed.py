import asyncio
import sys
import types
from unittest.mock import AsyncMock, Mock

import httpx
import pytest

from control.app import main


@pytest.mark.asyncio
@pytest.mark.parametrize("prefix", ["", "/mdd"])
async def test_retired_media_http_routes_are_gone_without_minting_admission_or_reading_nics(monkeypatch, prefix):
    monkeypatch.setattr(main.auth, "session", lambda token: {"csrf": "test-csrf"})
    monkeypatch.setattr(main.engine, "global_maintenance_pending", lambda: False)
    monkeypatch.setattr(main.engine, "engine_maintenance_pending", lambda _iid: False)
    forbidden = Mock(side_effect=AssertionError("legacy route must have no side effect"))
    monkeypatch.setattr(main.media_ingress, "status", forbidden)
    monkeypatch.setattr(main.media_ingress, "confirm", forbidden)
    monkeypatch.setattr(main.media_admission, "issue", forbidden)
    monkeypatch.setattr(main.media_admission, "mark_browser", forbidden)
    monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(side_effect=forbidden))
    routes = [
        ("GET", "/api/system/media-ingress"),
        ("POST", "/api/system/media-ingress/confirm"),
        ("POST", "/api/instances/5/softphone/media-admission/new"),
        ("POST", "/api/instances/5/softphone/media-evidence"),
        ("POST", "/api/instances/5/softphone/media-admission"),
    ]
    async with httpx.AsyncClient(transport=httpx.ASGITransport(app=main.app),
                                base_url="https://forwarded.example",
                                headers={"authorization": "Bearer cached-session",
                                         "x-mdd-csrf-token": "test-csrf"}) as client:
        for method, path in routes:
            response = await client.request(method, prefix + path, **(
                {"json": {"token": "old-token", "candidate_id": "old-nic"}}
                if method == "POST" else {}))
            assert response.status_code == 410, (path, response.text)
            assert response.json()["detail"]["code"] == "legacy_browser_media_removed"
    forbidden.assert_not_called()


@pytest.mark.asyncio
@pytest.mark.parametrize("prefix", ["", "/mdd"])
async def test_old_sip_ws_route_rejects_cached_authenticated_client_before_any_upstream(monkeypatch, prefix):
    forbidden = Mock(side_effect=AssertionError("old SIP proxy must never open upstream"))
    # The retired route must not even need its former optional client dependency. Install a
    # tripwire module for any accidental legacy import/connection, not a working proxy stub.
    monkeypatch.setitem(sys.modules, "websockets", types.SimpleNamespace(connect=forbidden))
    monkeypatch.setattr(main.media_ingress, "status", forbidden)
    monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(side_effect=forbidden))
    monkeypatch.setattr(main.auth, "session", lambda _token: {"csrf": "test-csrf"})
    sent = []
    events = asyncio.Queue()
    await events.put({"type": "websocket.connect"})
    scope = {"type": "websocket", "asgi": {"version": "3.0", "spec_version": "2.4"},
             "scheme": "wss", "path": prefix + "/api/instances/5/ws", "root_path": "",
             "query_string": b"generation=old-generation", "server": ("forwarded.example", 443),
             "client": ("127.0.0.1", 10000), "subprotocols": ["sip"],
             "headers": [(b"host", b"forwarded.example"),
                         (b"origin", b"https://forwarded.example"),
                         (b"sec-websocket-protocol", b"sip"),
                         (b"cookie", f"{main.auth.SESSION_COOKIE}=cached-session".encode())]}

    async def send(message):
        sent.append(message)

    await main.app(scope, events.get, send)
    assert len(sent) == 1 and sent[0]["type"] == "websocket.close"
    assert sent[0]["code"] == 4410
    assert "disabled" in sent[0]["reason"]
    forbidden.assert_not_called()


@pytest.mark.asyncio
@pytest.mark.parametrize("host", ["localhost:38443", "vpn.example:8443", "[::1]:8443",
                                  "reverse-proxy.example"])
async def test_provisioning_is_native_only_for_arbitrary_same_origin_access(monkeypatch, host):
    monkeypatch.setattr(main.auth, "session", lambda _token: {"csrf": "test-csrf"})
    monkeypatch.setattr(main.cfg, "get_instance", lambda _iid: {
        "id": "5", "sip": {"webrtc": {"username": "old-user", "password": "old-secret"}},
        "ports": {"webrtc": 8089}})
    monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(return_value={
        "running": True, "container_id": "engine-generation",
        "media_websocket": True, "browser_outbound": True, "browser_inbound": True,
        "webrtc_host_port": None, "rtp_mapping_exact": False}))
    monkeypatch.setattr(main, "_line_admission_blocked", AsyncMock(return_value=False))
    forbidden = Mock(side_effect=AssertionError("no interface/IP selection in native mode"))
    monkeypatch.setattr(main.media_ingress, "status", forbidden)
    async with httpx.AsyncClient(transport=httpx.ASGITransport(app=main.app),
                                base_url="https://gateway.example") as client:
        response = await client.get("/mdd/api/instances/5/softphone", headers={"host": host})
    assert response.status_code == 200
    result = response.json()
    assert result["enabled"] and result["state"] == "running"
    assert result["generation"] == "engine-generation"
    assert result["browser_media"]["outbound"] and result["browser_media"]["inbound"]
    assert not {"username", "password", "realm", "host", "ws_url", "ws_port", "ice_servers",
                "media_ingress", "media_ready", "media_test_target"}.intersection(result)
    assert "old-secret" not in response.text
    forbidden.assert_not_called()


@pytest.mark.asyncio
@pytest.mark.parametrize("runtime,blocked,state", [
    ({"running": False}, False, "stopped"),
    ({"running": True, "container_id": "generation"}, False, "native_media_unavailable"),
    ({"running": True, "container_id": "generation", "media_websocket": True},
     False, "native_calls_unavailable"),
    ({"running": True, "container_id": "generation", "media_websocket": True,
      "browser_inbound": True, "browser_outbound": True}, True, "rebind_pending"),
])
async def test_native_provisioning_explains_unavailable_capability_without_ip_confirmation(monkeypatch, runtime, blocked, state):
    monkeypatch.setattr(main.cfg, "get_instance", lambda _iid: {"id": "5"})
    monkeypatch.setattr(main, "_line_admission_blocked", AsyncMock(return_value=blocked))
    result = await main._softphone_provisioning("5", None, runtime=runtime)
    assert not result["enabled"] and result["state"] == state
    assert result["media_error"] and "Confirm" not in result["media_error"]
    assert not result["browser_media"]["inbound"]
    assert not result["browser_media"]["outbound"]
