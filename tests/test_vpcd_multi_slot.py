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
                # libifdvpcd only allows 1 active connection per port
                writer.close()
                try:
                    await writer.wait_closed()
                except Exception:
                    pass
                return

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

