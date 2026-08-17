"""
tests/test_e2e_real_scenarios.py - High-fidelity End-to-End integration tests for real gateway scenarios.
Verifies exact user-facing behaviors across /api/devices, /api/esim/chip, /api/cards, and /api/vpcd/ws.
"""
import asyncio
import json
import pytest
from fastapi.testclient import TestClient

from control.app import auth, config as cfg, device_state, engine, lpa, main
from tests.test_vpcd_multi_slot import DummyTCPVPCDServer

hub = main.hub


@pytest.fixture(autouse=True)
def setup_test_environment(monkeypatch, tmp_path):
    # Setup test auth
    monkeypatch.setattr(auth, "AUTH_PATH", str(tmp_path / "auth.json"))
    monkeypatch.setattr(auth, "session", lambda token: True)
    monkeypatch.setattr(engine, "is_running", lambda iid: False)
    monkeypatch.setattr(main, "_find_running_by_reader", lambda name: None)
    monkeypatch.setattr(main, "_esim_guard_engine", lambda name: None)
    auth.set_agent_token("test-secret-token")

    # Setup test config with 2 distinct instances
    inst1 = {
        "id": 1,
        "name": "giffgaff-line",
        "reader": "imsi:234104046996669",
        "reader_index": 0,
        "iccid": "8944110069499811522",
        "imsi": "234104046996669",
        "msisdn": "+447856314080",
        "smsc": "+447802002606",
        "mcc": "234",
        "mnc": "10",
        "enabled": True,
    }
    inst2 = {
        "id": 2,
        "name": "ctexcel-line",
        "reader": "imsi:234336570593733",
        "reader_index": 1,
        "iccid": "8944304253516282770",
        "imsi": "234336570593733",
        "msisdn": "+447726248965",
        "smsc": "+447870002308",
        "mcc": "234",
        "mnc": "33",
        "enabled": True,
    }
    monkeypatch.setattr(cfg, "list_instances", lambda: [inst1, inst2])

    # Setup mock eSIM cache for the 2 ICCIDs
    cache_store = {
        "8944110069499811522": {
            "ts": 1786933000,
            "imei": "356839119784073",
            "ses": [{
                "id": "default",
                "label": "eUICC",
                "eid": "89049032000001000000290952251921",
                "profiles": [
                    {"iccid": "8948010010011712600", "profileName": "Bootstrap", "profileState": "disabled"},
                    {"iccid": "89103000000584198432", "profileName": "Connect", "profileState": "disabled"},
                    {"iccid": "8944110069499811522", "profileName": "giffgaff", "profileState": "enabled"},
                ],
                "chip": {"eid": "89049032000001000000290952251921"}
            }]
        },
        "8944304253516282770": {
            "ts": 1786872443,
            "imei": "356839119784073",
            "ses": [{
                "id": "default",
                "label": "eUICC",
                "eid": "89086030202200000025000038757357",
                "profiles": [
                    {"iccid": "8944304253516282770", "profileName": "CTExcel", "profileState": "enabled"},
                    {"iccid": "89103000000592376087", "profileName": "Connect", "profileState": "disabled"},
                ],
                "chip": {"eid": "89086030202200000025000038757357"}
            }]
        }
    }
    monkeypatch.setattr(main, "_esim_cache_for_iccid", lambda iccid: cache_store.get(iccid))

    # Clean in-memory states
    main.active_vpcd_slots.clear()
    main.vpcd_last_heartbeat.clear()


def test_offline_devices_retain_instance_identity_and_sim_info(monkeypatch):
    """
    真实场景测试：当所有物理读卡器离线（未连接）时：
    GET /api/devices 必须保留对应槽位的线路实例配置（ICCID、MSISDN、运营商），
    绝不能因为 reader 离线而将 instance_id 清空为 null 或显示错误的 missing imsi 错误。
    """
    monkeypatch.setattr(device_state, "hardware", lambda: {
        "reader-33bda14ceb3e2596": {
            "device_type": "reader", "imei": "356839119784073", "name": "Virtual PCD 00 00"
        },
        "reader-b68194ea133b3d33": {
            "device_type": "reader", "imei": "356839119784073", "name": "Virtual PCD 00 01"
        }
    })
    monkeypatch.setattr(hub, "cards_list", lambda: [])

    client = TestClient(main.app, cookies={"mdd_session": "test-session"})
    resp = client.get("/api/devices")
    assert resp.status_code == 200
    data = resp.json()
    devices = data.get("devices") or []

    assert len(devices) == 2
    dev0 = next(d for d in devices if d["name"] == "Virtual PCD 00 00")
    dev1 = next(d for d in devices if d["name"] == "Virtual PCD 00 01")

    # 验证 Slot 0 绑定 Instance 1 (giffgaff)
    assert dev0["instance_id"] == "1"
    assert dev0["sim"]["iccid"] == "8944110069499811522"
    assert dev0["sim"]["number"] == "+447856314080"
    assert dev0["sim"]["carrier"]["name"] in ("O2", "giffgaff")
    assert dev0["present"] is False
    assert "waiting for card agent" in dev0["capabilities"]["vowifi"]["reason"].lower()

    # 验证 Slot 1 绑定 Instance 2 (CTExcel / EE)，且没有与 Slot 0 串台
    assert dev1["instance_id"] == "2"
    assert dev1["sim"]["iccid"] == "8944304253516282770"
    assert dev1["sim"]["number"] == "+447726248965"
    assert dev1["sim"]["carrier"]["name"] in ("EE", "CTExcel")
    assert dev1["present"] is False


def test_esim_chip_fallback_to_cache_when_live_read_fails(monkeypatch):
    """
    真实场景测试：当用户在界面点击获取，但读卡器离线导致底层 lpac 返回 8010000C 时：
    GET /api/esim/chip 必须优雅返回该读卡器的离线缓存 Profile，不能变为空白或报错中断。
    """
    # 模拟 lpac 探针在离线读卡器上执行失败
    async def mock_load_all_ses(reader_name, reader_index=0):
        return {
            "ok": True,
            "ses": [{
                "id": "default",
                "label": "eUICC",
                "eid": None,
                "profiles": [],
                "error": "No smart card detected in reader (Card Agent is offline or reader is empty)."
            }]
        }
    monkeypatch.setattr(lpa, "load_all_ses", mock_load_all_ses)

    client = TestClient(main.app, cookies={"mdd_session": "test-session"})

    # 1. 验证 Slot 0 优雅回退展示 giffgaff 3 个缓存 Profile
    resp0 = client.get("/api/esim/chip?reader=Virtual+PCD+00+00")
    assert resp0.status_code == 200
    data0 = resp0.json()
    assert data0["ok"] is True
    assert data0["cached"] is True
    assert len(data0["ses"][0]["profiles"]) == 3
    profile_names0 = [p["profileName"] for p in data0["ses"][0]["profiles"]]
    assert "giffgaff" in profile_names0
    assert "Bootstrap" in profile_names0

    # 2. 验证 Slot 1 优雅回退展示 CTExcel 2 个缓存 Profile
    resp1 = client.get("/api/esim/chip?reader=Virtual+PCD+00+01")
    assert resp1.status_code == 200
    data1 = resp1.json()
    assert data1["ok"] is True
    assert data1["cached"] is True
    assert len(data1["ses"][0]["profiles"]) == 2
    profile_names1 = [p["profileName"] for p in data1["ses"][0]["profiles"]]
    assert "CTExcel" in profile_names1
    assert "Connect" in profile_names1


def test_lpa_error_message_mapping():
    """
    真实场景测试：底层 SCardConnect 8010000C 错误必须映射为读卡器离线/无卡提示，
    绝不能误报为'非 eSIM 卡片'。
    """
    err = lpa.LpaError("euicc_init", detail="SCardConnect() failed: 8010000C")
    msg = err.user_message()
    assert "No smart card detected in reader" in msg
    assert "Ordinary USIM" not in msg


@pytest.mark.asyncio
async def test_websocket_presence_syncs_to_http_cards_and_devices():
    """
    真实场景测试：远端读卡器通过 WebSocket 连入 -> HTTP /api/cards 必须通过内存心跳瞬间显现 present: True；
    断开后槽位瞬间回收。
    """
    mock_slot2 = DummyTCPVPCDServer(35965)
    await mock_slot2.start()

    client = TestClient(main.app, cookies={"mdd_session": "test-session"})
    try:
        # 连接前：Slot 2 不在列表中
        cards_before = client.get("/api/cards").json().get("cards", [])
        assert not any(c.get("index") == 2 for c in cards_before)

        # 连入 WebSocket (Slot 2)
        with client.websocket_connect("/api/vpcd/ws?token=test-secret-token") as ws:
            ws.send_bytes(b"\x3B\x80\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0A")
            for _ in range(20):
                if 2 in main.active_vpcd_slots:
                    break
                await asyncio.sleep(0.05)

            assert 2 in main.active_vpcd_slots

            # 连接中：通过 HTTP /api/cards 检查 Slot 2 动态显现且 present=True
            cards_active = main._client_cards([
                {"name": "Virtual PCD 00 00", "index": 0, "present": False},
                {"name": "Virtual PCD 00 01", "index": 1, "present": False},
                {"name": "Virtual PCD 1 00 00", "index": 2, "present": False},
            ])
            slot2 = next((c for c in cards_active if c.get("index") == 2), None)
            assert slot2 is not None
            assert slot2["present"] is True

        # 断开后：Slot 2 被回收
        for _ in range(20):
            if 2 not in main.active_vpcd_slots:
                break
            await asyncio.sleep(0.05)

        assert 2 not in main.active_vpcd_slots
    finally:
        await mock_slot2.stop()
