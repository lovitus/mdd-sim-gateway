#!/usr/bin/env python3
"""No-network Asterisk 20.7 E2E for the native browser outbound dialplan.

Run inside an Engine candidate with Asterisk already running, the test fixture PJSIP and
websocket_client configs installed, and /logs isolated.  All SIP stays on loopback; the Docker
container itself must use ``--network none``.
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
import queue
import re
import socket
import struct
import threading
import time
import uuid


ADMISSION_SOCKET = "/run/mdd-sim-gateway/test-admission.sock"


def wait_for(predicate, timeout=5.0, detail="condition"):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(0.03)
    raise AssertionError(f"timed out waiting for {detail}")


class AdmissionServer:
    def __init__(self):
        self.stop = threading.Event()
        self.thread = None
        self.allowed = True
        self.decisions = queue.Queue()

    def start(self):
        try:
            os.unlink(ADMISSION_SOCKET)
        except FileNotFoundError:
            pass
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        server.bind(ADMISSION_SOCKET)
        os.chmod(ADMISSION_SOCKET, 0o600)
        server.listen(8)
        server.settimeout(0.2)

        def run():
            while not self.stop.is_set():
                try:
                    client, _ = server.accept()
                except socket.timeout:
                    continue
                with client:
                    raw = b""
                    while not raw.endswith(b"\n") and len(raw) < 128:
                        chunk = client.recv(128 - len(raw))
                        if not chunk:
                            break
                        raw += chunk
                    match = re.fullmatch(rb"MDD1 ([0-9a-f]{16}) ([a-z_]+)\n", raw)
                    if match:
                        try:
                            allowed = self.decisions.get_nowait()
                        except queue.Empty:
                            allowed = self.allowed
                        if allowed:
                            client.sendall(
                                b"MDD1 " + match.group(1) + b" ALLOW "
                                + b"a" * 32 + b" 1 1\n")
            server.close()

        self.thread = threading.Thread(target=run, daemon=True)
        self.thread.start()

    def close(self):
        self.stop.set()
        self.thread.join(1)
        try:
            os.unlink(ADMISSION_SOCKET)
        except FileNotFoundError:
            pass


def recv_exact(sock, count):
    data = b""
    while len(data) < count:
        chunk = sock.recv(count - len(data))
        if not chunk:
            raise EOFError("WebSocket closed")
        data += chunk
    return data


def read_ws_frame(sock):
    first, second = recv_exact(sock, 2)
    opcode = first & 0x0F
    masked = bool(second & 0x80)
    size = second & 0x7F
    if size == 126:
        size = struct.unpack("!H", recv_exact(sock, 2))[0]
    elif size == 127:
        size = struct.unpack("!Q", recv_exact(sock, 8))[0]
    mask = recv_exact(sock, 4) if masked else b""
    payload = recv_exact(sock, size)
    if masked:
        payload = bytes(value ^ mask[index % 4] for index, value in enumerate(payload))
    return opcode, payload


def write_ws_frame(sock, opcode, payload):
    payload = bytes(payload)
    header = bytes([0x80 | opcode])
    if len(payload) < 126:
        header += bytes([len(payload)])
    elif len(payload) < 65536:
        header += bytes([126]) + struct.pack("!H", len(payload))
    else:
        header += bytes([127]) + struct.pack("!Q", len(payload))
    sock.sendall(header + payload)


class MediaConnection:
    def __init__(self, sock, headers):
        self.sock = sock
        self.headers = headers
        self.channel = ""
        self.channel_id = ""
        self.started = threading.Event()
        self.closed = threading.Event()
        self.binary = queue.Queue()
        self.control = queue.Queue()
        self.send_lock = threading.Lock()
        threading.Thread(target=self._read, daemon=True).start()

    def _read(self):
        try:
            while True:
                opcode, payload = read_ws_frame(self.sock)
                if opcode == 1:
                    value = json.loads(payload)
                    if value.get("event") == "MEDIA_START":
                        self.channel = str(value.get("channel") or "")
                        self.channel_id = str(value.get("channel_id") or "")
                        self.started.set()
                        self.send_json({"command": "ANSWER"})
                    else:
                        self.control.put(value)
                elif opcode == 2:
                    self.binary.put(payload)
                elif opcode == 8:
                    return
                elif opcode == 9:
                    with self.send_lock:
                        write_ws_frame(self.sock, 10, payload)
        except (EOFError, OSError, ValueError):
            pass
        finally:
            self.closed.set()
            try:
                self.sock.close()
            except OSError:
                pass

    def send_json(self, value):
        with self.send_lock:
            write_ws_frame(self.sock, 1, json.dumps(value, separators=(",", ":")).encode())

    def send_pcm_echo(self, marker):
        payload = bytes([marker]) * 320
        with self.send_lock:
            write_ws_frame(self.sock, 2, payload)
        echoed = self.binary.get(timeout=3)
        if echoed != payload:
            raise AssertionError("PCM echo mismatch")


class MediaServer:
    def __init__(self):
        self.connections = queue.Queue()
        self.stop = threading.Event()

    def start(self):
        server = socket.socket()
        server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        server.bind(("127.0.0.1", 18080))
        server.listen(8)
        server.settimeout(0.2)

        def run():
            while not self.stop.is_set():
                try:
                    client, _ = server.accept()
                except socket.timeout:
                    continue
                try:
                    client.settimeout(5)
                    raw = b""
                    while b"\r\n\r\n" not in raw and len(raw) < 16384:
                        chunk = client.recv(4096)
                        if not chunk:
                            raise EOFError("client closed during handshake")
                        raw += chunk
                    lines = raw.decode("latin1").split("\r\n")
                    headers = {}
                    for line in lines[1:]:
                        name, separator, value = line.partition(":")
                        if separator:
                            headers[name.strip().lower()] = value.strip()
                    key = headers.get("sec-websocket-key", "")
                    if not key:
                        raise ValueError("missing WebSocket key")
                    accept = base64.b64encode(hashlib.sha1(
                        (key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
                    client.sendall((
                        "HTTP/1.1 101 Switching Protocols\r\n"
                        "Upgrade: websocket\r\nConnection: Upgrade\r\n"
                        f"Sec-WebSocket-Accept: {accept}\r\n"
                        "Sec-WebSocket-Protocol: media\r\n\r\n").encode())
                    connection = MediaConnection(client, headers)
                    self.connections.put(connection)
                except (EOFError, OSError, ValueError):
                    client.close()
                    continue
            server.close()

        self.thread = threading.Thread(target=run, daemon=True)
        self.thread.start()

    def next(self, timeout=5):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            remaining = deadline - time.monotonic()
            value = self.connections.get(timeout=remaining)
            if value.started.wait(min(0.5, remaining)):
                return value
        raise AssertionError("MEDIA_START not received")

    def close(self):
        self.stop.set()
        self.thread.join(1)


def sip_header(message, name):
    match = re.search(rf"^{re.escape(name)}:\s*(.+)\r?$", message, re.I | re.M)
    if not match:
        raise AssertionError(f"missing SIP header {name}")
    return match.group(1).strip()


class FakeSipServer:
    def __init__(self):
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.bind(("127.0.0.1", 15060))
        self.stop = threading.Event()
        self.invite = None
        self.invite_addr = None
        self.invite_event = threading.Event()
        self.info_bodies = queue.Queue()
        self.rtp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.rtp.bind(("127.0.0.1", 16000))

    def _response(self, request, status, body="", content_type=""):
        to = sip_header(request, "To")
        if ";tag=" not in to:
            to += ";tag=mdd-isolated"
        lines = [
            f"SIP/2.0 {status}", f"Via: {sip_header(request, 'Via')}",
            f"From: {sip_header(request, 'From')}", f"To: {to}",
            f"Call-ID: {sip_header(request, 'Call-ID')}",
            f"CSeq: {sip_header(request, 'CSeq')}",
            "Contact: <sip:fake@127.0.0.1:15060>",
        ]
        if content_type:
            lines.append(f"Content-Type: {content_type}")
        lines.append(f"Content-Length: {len(body.encode())}")
        return ("\r\n".join(lines) + "\r\n\r\n" + body).encode()

    def start(self):
        self.sock.settimeout(0.2)

        def run():
            while not self.stop.is_set():
                try:
                    raw, addr = self.sock.recvfrom(65535)
                except socket.timeout:
                    continue
                message = raw.decode("utf-8", errors="replace")
                method = message.split(" ", 1)[0]
                if method == "INVITE":
                    if self.invite is None:
                        self.invite, self.invite_addr = message, addr
                        self.invite_event.set()
                    self.sock.sendto(self._response(message, "100 Trying"), addr)
                    self.sock.sendto(self._response(message, "180 Ringing"), addr)
                elif method == "INFO":
                    self.info_bodies.put(message.split("\r\n\r\n", 1)[-1])
                    self.sock.sendto(self._response(message, "200 OK"), addr)
                elif method in {"BYE", "CANCEL"}:
                    self.sock.sendto(self._response(message, "200 OK"), addr)

        self.thread = threading.Thread(target=run, daemon=True)
        self.thread.start()

    def answer(self):
        if not self.invite_event.wait(4):
            raise AssertionError("fake endpoint did not receive INVITE")
        body = (
            "v=0\r\no=mdd 1 1 IN IP4 127.0.0.1\r\ns=mdd\r\n"
            "c=IN IP4 127.0.0.1\r\nt=0 0\r\n"
            "m=audio 16000 RTP/AVP 0 101\r\n"
            "a=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\n")
        self.sock.sendto(self._response(
            self.invite, "200 OK", body, "application/sdp"), self.invite_addr)

    def close(self):
        self.stop.set()
        self.thread.join(1)
        self.sock.close()
        self.rtp.close()


class Ami:
    def __init__(self, username, secret):
        self.sock = socket.create_connection(("127.0.0.1", 5038), timeout=3)
        self.file = self.sock.makefile("rb")
        self.events = queue.Queue()
        self.file.readline()
        self.action({"Action": "Login", "Username": username, "Secret": secret})

    def _message(self):
        value = {}
        while True:
            line = self.file.readline()
            if not line:
                raise EOFError("AMI closed")
            if line in {b"\r\n", b"\n"}:
                return value
            name, separator, raw = line.decode(errors="replace").rstrip("\r\n").partition(":")
            if separator:
                value[name] = raw.strip()

    def action(self, values, complete_event=""):
        action_id = uuid.uuid4().hex
        payload = {**values, "ActionID": action_id}
        raw = "".join(f"{key}: {value}\r\n" for key, value in payload.items()) + "\r\n"
        self.sock.sendall(raw.encode())
        messages = []
        while True:
            item = self._message()
            if item.get("ActionID") != action_id:
                self.events.put(item)
                continue
            messages.append(item)
            if complete_event:
                if item.get("Event") == complete_event:
                    return messages
            elif "Response" in item:
                return messages

    def success(self, values):
        messages = self.action(values)
        if messages[0].get("Response") != "Success":
            raise AssertionError(messages[0].get("Message") or "AMI action failed")
        return messages[0]

    def channels(self):
        return [item for item in self.action(
            {"Action": "CoreShowChannels"}, "CoreShowChannelsComplete")
                if item.get("Event") == "CoreShowChannel"]

    def wait_event(self, event_name, predicate=lambda _item: True, timeout=5.0):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                item = self.events.get_nowait()
            except queue.Empty:
                self.sock.settimeout(max(0.1, deadline - time.monotonic()))
                item = self._message()
            if item.get("Event") == event_name and predicate(item):
                return item
        raise AssertionError(f"timed out waiting for AMI event {event_name}")

    def close(self):
        try:
            self.success({"Action": "Logoff"})
        except Exception:
            pass
        self.file.close()
        self.sock.close()


def main():
    config = json.load(open("/config/instance.json", encoding="utf-8"))
    events_path = "/logs/events.jsonl"
    try:
        os.unlink(events_path)
    except FileNotFoundError:
        pass
    admission, media, sip = AdmissionServer(), MediaServer(), FakeSipServer()
    admission.start(); media.start(); sip.start()
    ami = Ami(config.get("ami_user", "vowifi"), config["ami_secret"])
    first_id = "mddcanary-00000000-0000-4000-8000-000000000001"
    try:
        admission.allowed = False
        denied_id = "mddcanary-00000000-0000-4000-8000-000000000000"
        ami.success({
            "Action": "Originate",
            "Channel": "WebSocket/mdd_control_media/c(slin)nf(json)v(sid=deny)",
            "Context": "browser-media-outbound-warmup", "Exten": "echo",
            "Priority": "1", "ChannelId": denied_id, "Async": "true",
        })
        denied = media.next()
        if not denied.closed.wait(3):
            raise AssertionError("Engine-local media admission DENY did not close warmup")
        if any(item.get("Uniqueid") == denied_id for item in ami.channels()):
            raise AssertionError("admission-denied warmup remained active")
        admission.allowed = True

        ami.success({
            "Action": "Originate",
            "Channel": "WebSocket/mdd_control_media/c(slin)nf(json)v(sid=e2e1)",
            "Context": "browser-media-outbound-warmup", "Exten": "echo",
            "Priority": "1", "ChannelId": first_id, "Async": "true",
        })
        first = media.next()
        if first.channel_id != first_id:
            raise AssertionError("first warmup ChannelId changed")
        first.send_pcm_echo(1); first.send_pcm_echo(2)
        exact = wait_for(lambda: next((item for item in ami.channels()
                                      if item.get("Uniqueid") == first_id), None),
                         detail="first warmup channel")
        if exact.get("Context") != "browser-media-outbound-warmup" \
                or exact.get("Application") != "Echo":
            raise AssertionError("warmup channel is not in Echo")
        channel = exact["Channel"]

        second_id = "mddcanary-00000000-0000-4000-8000-000000000002"
        ami.success({
            "Action": "Originate",
            "Channel": "WebSocket/mdd_control_media/c(slin)nf(json)v(sid=e2e2)",
            "Context": "browser-media-outbound-warmup", "Exten": "echo",
            "Priority": "1", "ChannelId": second_id, "Async": "true",
        })
        second = media.next()
        if not second.closed.wait(3):
            raise AssertionError("second warmup was not rejected by the channel group claim")
        if any(item.get("Uniqueid") == second_id for item in ami.channels()):
            raise AssertionError("second warmup remained active")

        variables = {
            "MDD_NATIVE_CALL": "1", "MDD_MEDIA_TOKEN": "A" * 43,
            "MDD_MEDIA_EPOCH": "B" * 24, "MDD_OPERATION_ID": "c" * 32,
            "MDD_DESTINATION": "555",
        }
        for name, value in variables.items():
            ami.success({"Action": "Setvar", "Channel": channel,
                         "Variable": name, "Value": value})
            observed = ami.success({"Action": "Getvar", "Channel": channel,
                                    "Variable": name}).get("Value")
            if observed != value:
                raise AssertionError(f"Set/Get mismatch for {name}")
        ami.success({
            "Action": "Redirect", "Channel": channel,
            "Context": "browser-media-outbound", "Exten": "call", "Priority": "1",
        })
        if not sip.invite_event.wait(4):
            raise AssertionError("fake loopback endpoint did not receive native Dial")
        ami.success({"Action": "Setvar", "Channel": channel,
                     "Variable": "TIMEOUT(absolute)", "Value": "10"})

        third_id = "mddcanary-00000000-0000-4000-8000-000000000003"
        ami.success({
            "Action": "Originate",
            "Channel": "WebSocket/mdd_control_media/c(slin)nf(json)v(sid=e2e3)",
            "Context": "browser-media-outbound-warmup", "Exten": "echo",
            "Priority": "1", "ChannelId": third_id, "Async": "true",
        })
        third = media.next()
        if not third.closed.wait(3):
            raise AssertionError("lock was lost across Redirect")

        sip.answer()
        def active_event():
            try:
                rows = [json.loads(line) for line in open(events_path, encoding="utf-8")]
            except (FileNotFoundError, ValueError):
                return None
            return next((row for row in rows if row.get("event") == "call_active"), None)
        active = wait_for(active_event, timeout=4, detail="U(mdd-call-active) event")
        if active.get("args") != ["555", "<redacted>"] \
                or not str(active.get("source_call_id") or "").endswith(":" + first_id):
            raise AssertionError("call_active U arguments changed")

        ami.success({"Action": "PlayDTMF", "Channel": channel, "Digit": "5",
                     "Duration": "160", "Receive": "true"})
        info = sip.info_bodies.get(timeout=4)
        if "Signal=5" not in info:
            raise AssertionError("PlayDTMF Receive did not reach fake peer")
        ami.success({"Action": "Hangup", "Channel": channel, "Cause": "16"})
        wait_for(lambda: not ami.channels(), timeout=4, detail="terminal channel cleanup")

        fourth_id = "mddcanary-00000000-0000-4000-8000-000000000004"
        ami.success({
            "Action": "Originate",
            "Channel": "WebSocket/mdd_control_media/c(slin)nf(json)v(sid=e2e4)",
            "Context": "browser-media-outbound-warmup", "Exten": "echo",
            "Priority": "1", "ChannelId": fourth_id, "Async": "true",
        })
        fourth = media.next()
        fourth.send_pcm_echo(4)
        fourth_channel = wait_for(lambda: next((item.get("Channel") for item in ami.channels()
                                                if item.get("Uniqueid") == fourth_id), ""),
                                  detail="post-destroy warmup")
        ami.success({"Action": "Hangup", "Channel": fourth_channel, "Cause": "16"})
        wait_for(lambda: not ami.channels(), timeout=4, detail="final cleanup")
        print(json.dumps({
            "network": "none", "fake_sip_invites": 1,
            "real_ims_dial": 0, "warmup_pcm_echo": 3,
            "engine_media_gate_denied": True,
            "second_warmup_rejected": True, "lock_held_across_redirect": True,
            "lock_released_on_destroy": True, "set_get_variables": 5,
            "call_active_u_args": True, "play_dtmf_receive": True,
            "active_channels_final": 0,
        }, sort_keys=True))
    finally:
        ami.close(); sip.close(); media.close(); admission.close()


if __name__ == "__main__":
    main()
