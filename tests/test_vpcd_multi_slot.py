"""
tests/test_vpcd_multi_slot.py - Comprehensive automated tests for multi-slot VPCD
WebSocket bridging, token authentication, dynamic slot pool allocation, and protocol negotiation.
"""
import asyncio
import struct
import pytest
from starlette.websockets import WebSocketDisconnect
from fastapi.testclient import TestClient

from control.app import auth, main, sim
from smartcard.Exceptions import CardConnectionException
from smartcard.CardConnection import CardConnection


class DummyTCPVPCDServer:
    """Mock libifdvpcd local TCP server running on 127.0.0.1."""
    def __init__(self, port: int):
        self.port = port
        self.server = None
        self.connected_client_reader = None
        self.connected_client_writer = None
        self.received_frames = []

    async def start(self):
        async def handle_client(reader, writer):
            if self.connected_client_writer is not None:
                try:
                    self.connected_client_writer.close()
                except Exception:
                    pass
                self.connected_client_writer = None

            self.connected_client_reader = reader
            self.connected_client_writer = writer
            try:
                while True:
                    hdr = await reader.readexactly(2)
                    (length,) = struct.unpack(">H", hdr)
                    if length == 0:
                        continue
                    payload = await reader.readexactly(length)
                    self.received_frames.append(payload)
            except (asyncio.IncompleteReadError, asyncio.CancelledError, ConnectionResetError):
                pass
            finally:
                writer.close()
                self.connected_client_writer = None

        self.server = await asyncio.start_server(handle_client, "127.0.0.1", self.port)

    async def send_frame(self, data: bytes):
        if self.connected_client_writer:
            hdr = struct.pack(">H", len(data))
            self.connected_client_writer.write(hdr + data)
            await self.connected_client_writer.drain()

    async def stop(self):
        if self.server:
            self.server.close()
            await self.server.wait_closed()


@pytest.fixture(autouse=True)
def setup_auth(monkeypatch, tmp_path):
    monkeypatch.setattr(auth, "AUTH_PATH", str(tmp_path / "auth.json"))
    auth.set_agent_token("test-secret-token")


def test_token_auth_invalid():
    client = TestClient(main.app)
    with pytest.raises(WebSocketDisconnect) as exc_info:
        with client.websocket_connect("/api/vpcd/ws?token=wrong-token") as ws:
            ws.receive_bytes()
    assert exc_info.value.code == 4401


def test_token_auth_missing():
    client = TestClient(main.app)
    with pytest.raises(WebSocketDisconnect) as exc_info:
        with client.websocket_connect("/api/vpcd/ws") as ws:
            ws.receive_bytes()
    assert exc_info.value.code == 4401


@pytest.mark.asyncio
async def test_all_slots_busy_returns_4503():
    """When no local VPCD ports are open, returns close code 4503 (NOT 401/403)."""
    client = TestClient(main.app)
    with pytest.raises(WebSocketDisconnect) as exc_info:
        with client.websocket_connect("/api/vpcd/ws?token=test-secret-token") as ws:
            ws.receive_bytes()
    assert exc_info.value.code == 4503


@pytest.mark.asyncio
async def test_dynamic_slot_allocation_and_binary_framing():
    """When Slot 1 (port 35964) is open, agent connects to it and forwards frames."""
    mock_vpcd = DummyTCPVPCDServer(35964)
    await mock_vpcd.start()

    try:
        client = TestClient(main.app)
        with client.websocket_connect("/api/vpcd/ws?token=test-secret-token") as ws:
            # Client sends simulated APDU over WebSocket
            test_apdu = bytes.fromhex("00A40004023F0000")
            ws.send_bytes(test_apdu)

            # Wait briefly for TCP receiver
            for _ in range(20):
                if mock_vpcd.received_frames:
                    break
                await asyncio.sleep(0.05)

            assert len(mock_vpcd.received_frames) == 1
            assert mock_vpcd.received_frames[0] == test_apdu

            # Mock VPCD sends ATR response over TCP (VPCD protocol: 2-byte header + data)
            mock_atr = bytes.fromhex("3B9F96803FC7828031E073FE211B57AA8660F0020008F4")
            await mock_vpcd.send_frame(mock_atr)

            # Client receives binary frame over WebSocket without 2-byte header
            received_ws = ws.receive_bytes()
            assert received_ws == mock_atr
    finally:
        await mock_vpcd.stop()


@pytest.mark.asyncio
async def test_explicit_slot_selection():
    """Requesting slot=3 connects to port 35966 specifically."""
    mock_vpcd_slot3 = DummyTCPVPCDServer(35966)
    await mock_vpcd_slot3.start()

    try:
        client = TestClient(main.app)
        with client.websocket_connect("/api/vpcd/ws?slot=3&token=test-secret-token") as ws:
            ws.send_bytes(b"\x04")
            for _ in range(20):
                if mock_vpcd_slot3.received_frames:
                    break
                await asyncio.sleep(0.05)
            assert mock_vpcd_slot3.received_frames[0] == b"\x04"
    finally:
        await mock_vpcd_slot3.stop()


def test_t0_t1_smartcard_protocol_fallback(monkeypatch):
    """sim._safe_transmit automatically falls back to T1 if T0 raises protocol mismatch."""
    calls = []

    class MockConn:
        def __init__(self):
            self.current_proto = None

        def getProtocol(self):
            return CardConnection.T0_protocol

    def mock_orig_transmit(self, bytes_data, protocol=None):
        calls.append(protocol)
        if protocol == CardConnection.T0_protocol:
            raise CardConnectionException("Failed to transmit with protocol T0. Card protocol mismatch.")
        return ([0x90, 0x00], 0x90, 0x00)

    monkeypatch.setattr(sim, "_orig_transmit", mock_orig_transmit)

    conn = MockConn()
    res = sim._safe_transmit(conn, [0x00, 0xA4, 0x00, 0x04], protocol=CardConnection.T0_protocol)
    assert res == ([0x90, 0x00], 0x90, 0x00)
    assert calls == [CardConnection.T0_protocol, CardConnection.T1_protocol]


def test_token_auth_headers():
    """Agents can pass token via Authorization: Bearer or X-Agent-Token."""
    client = TestClient(main.app)
    # Bearer header
    with pytest.raises(WebSocketDisconnect) as exc_info1:
        with client.websocket_connect("/api/vpcd/ws", headers={"Authorization": "Bearer test-secret-token"}) as ws:
            ws.receive_bytes()
    # 4503 means auth passed and it proceeded to slot check (which failed because no VPCD daemon is up)
    assert exc_info1.value.code == 4503

    # X-Agent-Token header
    with pytest.raises(WebSocketDisconnect) as exc_info2:
        with client.websocket_connect("/api/vpcd/ws", headers={"X-Agent-Token": "test-secret-token"}) as ws:
            ws.receive_bytes()
    assert exc_info2.value.code == 4503


@pytest.mark.asyncio
async def test_concurrent_multi_agents_distinct_slots():
    """Multiple agents connect concurrently and are automatically allocated distinct slots."""
    slot0_server = DummyTCPVPCDServer(35963)
    slot1_server = DummyTCPVPCDServer(35964)
    slot2_server = DummyTCPVPCDServer(35965)

    await slot0_server.start()
    await slot1_server.start()
    await slot2_server.start()

    try:
        client = TestClient(main.app)
        with client.websocket_connect("/api/vpcd/ws?token=test-secret-token") as ws0:
            ws0.send_bytes(b"\xAA\x00")

            with client.websocket_connect("/api/vpcd/ws?token=test-secret-token") as ws1:
                ws1.send_bytes(b"\xBB\x01")

                with client.websocket_connect("/api/vpcd/ws?token=test-secret-token") as ws2:
                    ws2.send_bytes(b"\xCC\x02")

                    for _ in range(20):
                        if (slot0_server.received_frames and
                                slot1_server.received_frames and
                                slot2_server.received_frames):
                            break
                        await asyncio.sleep(0.05)

                    assert slot0_server.received_frames[0] == b"\xAA\x00"
                    assert slot1_server.received_frames[0] == b"\xBB\x01"
                    assert slot2_server.received_frames[0] == b"\xCC\x02"
    finally:
        await slot0_server.stop()
        await slot1_server.stop()
        await slot2_server.stop()


@pytest.mark.asyncio
async def test_scenario1_slots_full_rejects_17th_reader():
    """场景1：16个卡槽全部占满时，新读卡器接入明确返回 4503（槽位繁忙），不崩溃、不卡死。"""
    # 启动 16 个模拟 VPCD 监听端口
    servers = [DummyTCPVPCDServer(35963 + i) for i in range(16)]
    for s in servers:
        await s.start()

    client = TestClient(main.app)
    ws_contexts = []
    try:
        # 连接前 16 个客户端占满所有槽位
        for i in range(16):
            ctx = client.websocket_connect("/api/vpcd/ws?token=test-secret-token")
            ws = ctx.__enter__()
            ws_contexts.append((ctx, ws))
            ws.send_bytes(b"\x3B\x00")

        for _ in range(30):
            if len(main.active_vpcd_slots) == 16:
                break
            await asyncio.sleep(0.05)

        assert len(main.active_vpcd_slots) == 16

        # 第 17 个读卡器尝试接入 -> 必须被优雅拒绝并返回 4503
        with pytest.raises(WebSocketDisconnect) as exc_info:
            with client.websocket_connect("/api/vpcd/ws?token=test-secret-token") as ws17:
                ws17.receive_bytes()
        assert exc_info.value.code == 4503
    finally:
        for ctx, ws in ws_contexts:
            try:
                ctx.__exit__(None, None, None)
            except Exception:
                pass
        for s in servers:
            await s.stop()


@pytest.mark.asyncio
async def test_scenario2_slot_release_and_reconnection():
    """场景2：槽位满后拔出一个读卡器，卡槽自动释放并从列表移除；新读卡器插入后自动显现并分配该空闲槽位。"""
    s0 = DummyTCPVPCDServer(35963)
    s1 = DummyTCPVPCDServer(35964)
    s2 = DummyTCPVPCDServer(35965)
    await s0.start()
    await s1.start()
    await s2.start()

    client = TestClient(main.app)
    try:
        with client.websocket_connect("/api/vpcd/ws?token=test-secret-token") as ws0:
            ws0.send_bytes(b"\x3B\x00")
            with client.websocket_connect("/api/vpcd/ws?token=test-secret-token") as ws1:
                ws1.send_bytes(b"\x3B\x01")
                # 第3个动态槽位接入（Slot 2）
                ws2_context = client.websocket_connect("/api/vpcd/ws?token=test-secret-token")
                ws2 = ws2_context.__enter__()
                ws2.send_bytes(b"\x3B\x02")

                for _ in range(20):
                    if 2 in main.active_vpcd_slots:
                        break
                    await asyncio.sleep(0.05)

                assert 2 in main.active_vpcd_slots
                # 检查 client_cards 列表包含动态槽位 2
                cards = main._client_cards([
                    {"name": "Virtual PCD 00 00", "index": 0, "present": False},
                    {"name": "Virtual PCD 00 01", "index": 1, "present": False},
                    {"name": "Virtual PCD 1 00 00", "index": 2, "present": False},
                    {"name": "Virtual PCD 1 00 01", "index": 3, "present": False},
                ])
                card_names = [c["name"] for c in cards]
                assert "Virtual PCD 1 00 00" in card_names
                assert "Virtual PCD 1 00 01" not in card_names  # 未连接的 Slot 3 不展示

                # 模拟拔出读卡器 2 (关闭 ws2)
                ws2.close()
                ws2_context.__exit__(None, None, None)
                for _ in range(20):
                    if 2 not in main.active_vpcd_slots:
                        break
                    await asyncio.sleep(0.05)

                # 验证 Slot 2 已被回收
                assert 2 not in main.active_vpcd_slots

                # 拔出后，未连接的 Slot 2 不在 client_cards 列表中
                cards_unplugged = main._client_cards([
                    {"name": "Virtual PCD 00 00", "index": 0, "present": False},
                    {"name": "Virtual PCD 00 01", "index": 1, "present": False},
                    {"name": "Virtual PCD 1 00 00", "index": 2, "present": False},
                ])
                card_names_unplugged = [c["name"] for c in cards_unplugged]
                assert "Virtual PCD 1 00 00" not in card_names_unplugged

                # 新读卡器接入 -> 应该能重新获取到 Slot 2 并重新显现
                with client.websocket_connect("/api/vpcd/ws?token=test-secret-token") as ws_new:
                    ws_new.send_bytes(b"\x3B\x99")
                    for _ in range(30):
                        if 2 in main.active_vpcd_slots:
                            break
                        await asyncio.sleep(0.05)

                    assert 2 in main.active_vpcd_slots
                    cards_reconnected = main._client_cards([
                        {"name": "Virtual PCD 00 00", "index": 0, "present": False},
                        {"name": "Virtual PCD 00 01", "index": 1, "present": False},
                        {"name": "Virtual PCD 1 00 00", "index": 2, "present": False},
                    ])
                    card_names_reconnected = [c["name"] for c in cards_reconnected]
                    assert "Virtual PCD 1 00 00" in card_names_reconnected
    finally:
        await s0.stop()
        await s1.stop()
        await s2.stop()


def test_scenario3_blank_writable_esim(tmp_path, monkeypatch):
    """场景3：读卡器内插入一张空白可写的全新 eSIM（有 EID、剩余空间，但无 Profile），正常缓存并支持下载。"""
    cache_path = str(tmp_path / "esim-cache.json")
    monkeypatch.setattr(main, "_ESIM_CACHE_PATH", cache_path)

    blank_eUICC_ses = [{
        "id": "default",
        "label": "eUICC",
        "eid": "89049032000001000000999999999999",
        "freeSpace": 512000,
        "defaultDpAddress": "lpa.ds.gsma.com",
        "rootDsAddress": "lpa.ds.gsma.com",
        "profiles": [],  # 空白卡，无任何 profile
        "notifications": [],
        "error": None,
        "chip": {
            "eid": "89049032000001000000999999999999",
            "freeNonVolatileMemory": 512000,
        }
    }]

    # 1. 空白 eUICC 成功保存至缓存
    main._esim_cache_store(blank_eUICC_ses, "356839119784073")
    cached_data = main._esim_cache_load()
    assert "89049032000001000000999999999999" in cached_data

    entry = cached_data["89049032000001000000999999999999"]
    assert entry["ses"][0]["eid"] == "89049032000001000000999999999999"
    assert entry["ses"][0]["profiles"] == []
    assert entry["ses"][0]["freeSpace"] == 512000


def test_scenario4_non_esim_physical_usim_handling(tmp_path, monkeypatch):
    """场景4：普通实体非 eSIM 卡接入，读取基本 ICCID/IMSI 正常，eSIM 模块安全识别非 eUICC 且不破坏缓存。"""
    cache_path = str(tmp_path / "esim-cache.json")
    monkeypatch.setattr(main, "_ESIM_CACHE_PATH", cache_path)

    # 写入既有的有效缓存
    existing_cache = {
        "89049032000001000000111111111111": {
            "ses": [{"id": "default", "eid": "89049032000001000000111111111111", "profiles": [{"iccid": "8944110000000000001"}]}],
            "imei": "356839119784073",
            "ts": 1786933000
        }
    }
    main._esim_cache_write(existing_cache)

    # 模拟普通物理 SIM 卡（非 eSIM）返回错误
    non_esim_error_ses = [{
        "id": "default",
        "error": "This card does not appear to be an eUICC / eSIM. Ordinary USIM cards cannot be managed here.",
        "profiles": []
    }]

    # 尝试将非 eSIM 错误写入缓存 -> 必须被拒绝，绝不能抹除已有缓存
    main._esim_cache_store(non_esim_error_ses, "")
    current_cache = main._esim_cache_load()
    assert "89049032000001000000111111111111" in current_cache
    assert len(current_cache) == 1

    # 查询不存在的 ICCID 缓存 -> 返回 None
    assert main._esim_cache_for_iccid("89860012345678901234") is None


