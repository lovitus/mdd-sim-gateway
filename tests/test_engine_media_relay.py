import pathlib
import queue
import socket
import struct
import sys
import time
import uuid
import inspect

import pytest

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "engine"))

import media_relay  # noqa: E402


def test_relay_listener_and_pcm_protocol_are_fixed():
    assert media_relay.LISTEN_HOST == "127.0.0.1"
    assert media_relay.LISTEN_PORT == 9073
    assert media_relay.PCM_FRAME_BYTES == 320


def test_manager_transport_uses_header_auth_and_ca_plus_exact_spki_pin():
    source = inspect.getsource(media_relay.connect_wss)
    assert 'X-MDD-Engine-Token' in source
    assert 'urllib.parse.urlencode({"iid": iid, "engine_run_id": run_id})' in source
    assert 'ssl.CERT_REQUIRED' in source
    assert 'ssl.CERT_NONE' not in source
    assert '_spki_digest(peer)' in source and '_expected_spki(cert_path)' in source
    assert 'token' not in source.split('query =', 1)[1].split('path =', 1)[0]


def test_reservations_reject_duplicate_expired_and_seventeenth_uuid():
    relay = media_relay.MediaRelay("7", "run-7")
    values = [uuid.uuid4() for _ in range(17)]
    for value in values[:16]:
        assert relay.reserve({"audio_uuid": str(value), "ttl_ms": 12000})
    assert not relay.reserve({"audio_uuid": str(values[0]), "ttl_ms": 12000})
    assert not relay.reserve({"audio_uuid": str(values[16]), "ttl_ms": 12000})
    relay.reservations[values[0]].expires_at = time.monotonic() - 1
    assert relay.reserve({"audio_uuid": str(values[16]), "ttl_ms": 12000})


def test_pcm_jitter_queue_is_bounded_and_overflow_closes_only_reservation():
    relay = media_relay.MediaRelay("7", "run-7")
    audio_uuid = uuid.uuid4()
    assert relay.reserve({"audio_uuid": str(audio_uuid), "ttl_ms": 12000})
    reservation = relay.reservations[audio_uuid]
    for _ in range(media_relay.MAX_QUEUE_FRAMES):
        relay.receive_pcm(audio_uuid.bytes + b"\0" * 320)
    relay.receive_pcm(audio_uuid.bytes + b"\0" * 320)
    assert reservation.closed.is_set()
    assert audio_uuid not in relay.reservations


def test_late_valid_pcm_does_not_disconnect_other_reservations():
    relay = media_relay.MediaRelay("7", "run-7")
    active = uuid.uuid4()
    assert relay.reserve({"audio_uuid": str(active), "ttl_ms": 12000})
    assert relay.receive_pcm(uuid.uuid4().bytes + b"\0" * 320) is False
    assert active in relay.reservations
    with pytest.raises(media_relay.RelayError, match="invalid multiplexed"):
        relay.receive_pcm(b"malformed")


def test_retry_backoff_resets_only_after_application_hello_ack():
    backoff = media_relay.RetryBackoff()
    assert [backoff.next_delay() for _ in range(7)] == [1, 2, 4, 8, 16, 30, 30]
    backoff.ready()
    assert backoff.next_delay() == 1
    # Another failure before a new acknowledged handshake continues the exponential sequence.
    assert backoff.next_delay() == 2


def test_serve_wss_marks_ready_on_hello_ack_not_transport_connect(monkeypatch):
    class FakeTransport:
        def __init__(self, frames):
            self.frames = iter(frames)

        def send_json(self, _payload):
            pass

        def receive(self):
            value = next(self.frames)
            if isinstance(value, Exception):
                raise value
            return value

        def close(self):
            pass

    ack = (0x01, b'{"type":"engine.media.hello.ack","version":1}')
    connected = FakeTransport([ConnectionError("before ack")])
    monkeypatch.setattr(media_relay, "connect_wss", lambda *_args: connected)
    ready = []
    with pytest.raises(ConnectionError, match="before ack"):
        media_relay.MediaRelay("7", "run-7").serve_wss(
            "wss://manager", "token", "cert", on_ready=lambda: ready.append(True))
    assert ready == []

    acknowledged = FakeTransport([ack, ConnectionError("after ack")])
    monkeypatch.setattr(media_relay, "connect_wss", lambda *_args: acknowledged)
    with pytest.raises(ConnectionError, match="after ack"):
        media_relay.MediaRelay("7", "run-7").serve_wss(
            "wss://manager", "token", "cert", on_ready=lambda: ready.append(True))
    assert ready == [True]


def test_server_websocket_frames_must_be_unmasked_complete_and_bounded():
    client, server = socket.socketpair()
    transport = media_relay.WebSocketTransport(client)
    try:
        server.sendall(bytes([0x82, 3]) + b"pcm")
        assert transport.receive() == (0x02, b"pcm")
        server.sendall(bytes([0x02, 0]))
        with pytest.raises(media_relay.RelayError, match="fragmented"):
            transport.receive()
    finally:
        client.close()
        server.close()


def test_audiosocket_unknown_uuid_is_closed_without_claiming_a_slot():
    relay = media_relay.MediaRelay("7", "run-7")
    client, server = socket.socketpair()
    try:
        server.sendall(struct.pack("!BH", 0x01, 16) + uuid.uuid4().bytes)
        relay._accept_audio(client)
        assert not relay.reservations
        assert server.recv(1) == b""
    finally:
        server.close()
