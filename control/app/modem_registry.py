"""Session-scoped registry and RPC transport for remote cellular modems.

Configuration refers to a SIM by ICCID.  Agent and modem identifiers only describe the
current attachment and are deliberately never persisted as the logical identity of an exit.
"""
from __future__ import annotations

import asyncio
import hashlib
import json
import os
import socket
import struct
import time
import uuid
from dataclasses import dataclass, field

from . import config as cfg


class ModemUnavailable(RuntimeError):
    pass


class ModemConflict(ModemUnavailable):
    pass


class ModemTimeout(ModemUnavailable):
    pass


async def _read_socks_address(reader: asyncio.StreamReader, atyp: int) -> tuple[str, int]:
    if atyp == 1:
        host = socket.inet_ntoa(await reader.readexactly(4))
    elif atyp == 3:
        length = (await reader.readexactly(1))[0]
        host = (await reader.readexactly(length)).decode("idna")
    elif atyp == 4:
        host = socket.inet_ntop(socket.AF_INET6, await reader.readexactly(16))
    else:
        raise ValueError("unsupported SOCKS address type")
    return host, struct.unpack("!H", await reader.readexactly(2))[0]


def _socks_address(host: str, port: int) -> bytes:
    try:
        return b"\x01" + socket.inet_aton(host) + struct.pack("!H", port)
    except OSError:
        encoded = host.encode("idna")
        return b"\x03" + bytes([len(encoded)]) + encoded + struct.pack("!H", port)


class _UdpQueue(asyncio.DatagramProtocol):
    def __init__(self, client_host: str):
        self.queue: asyncio.Queue = asyncio.Queue(maxsize=256)
        self.client_host = client_host
        self.client = None

    def datagram_received(self, data: bytes, address):
        if address[0] != self.client_host or (self.client and address != self.client):
            return
        self.client = address
        try:
            self.queue.put_nowait(data)
        except asyncio.QueueFull:
            pass


@dataclass
class Attachment:
    iccid: str
    agent_id: str
    modem_id: str
    session_id: str
    websocket: object
    imei: str = ""
    model: str = ""
    # The exact revision string, kept separate from the model name because only the revision
    # identifies the hardware branch and baseline a compatibility check may act on.
    firmware: str = ""
    phone: str = ""
    capabilities: dict = field(default_factory=dict)
    status: dict = field(default_factory=dict)
    connected_at: float = field(default_factory=time.time)
    seen_at: float = field(default_factory=time.time)
    pending: dict[str, asyncio.Future] = field(default_factory=dict)
    reverse_server: object | None = None
    reverse_port: int = 0
    reverse_agent_port: int = 0
    tunnel_waiters: dict[str, tuple[asyncio.Future, asyncio.Event]] = field(default_factory=dict)

    def public(self) -> dict:
        return {
            "iccid": self.iccid, "agent_id": self.agent_id, "modem_id": self.modem_id,
            "session_id": self.session_id, "imei": self.imei, "model": self.model,
            "firmware": self.firmware, "phone": self.phone,
            "capabilities": dict(self.capabilities), "status": dict(self.status),
            "online": True, "connected_at": self.connected_at, "seen_at": self.seen_at,
        }


class ModemRegistry:
    def __init__(self):
        self._by_iccid: dict[str, Attachment] = {}
        self._known: dict[str, dict] = {}
        self._ports: dict[str, int] = {}
        self._conflicts: set[str] = set()
        self._lock = asyncio.Lock()
        self._load()

    @staticmethod
    def _state_path() -> str:
        return os.path.join(cfg.DATA_DIR, "orchestrator", "remote-modems.json")

    @staticmethod
    def _offline_record(item: dict) -> dict:
        known = dict(item)
        status = dict(known.get("status") or {})
        previous_cellular = dict(status.get("cellular") or {})
        previous_cellular.pop("isolation", None)
        previous_cellular.pop("ip", None)
        status.update({"data": "disconnected", "data_active": False,
                       "proxy": {"ready": False},
                       "agent_proxy": {"ready": False}})
        status.pop("ip", None)
        status["cellular"] = {
            **previous_cellular, "ok": False, "status": "unavailable",
            "data": "disconnected", "proxy": {"ready": False},
            "error": "The modem Agent is offline.",
        }
        known.update({"online": False, "conflict": False, "status": status})
        return known

    def _load(self) -> None:
        try:
            with open(self._state_path(), encoding="utf-8") as handle:
                document = json.load(handle)
        except (OSError, ValueError, TypeError):
            return
        for iccid, item in (document.get("sims") or {}).items():
            if isinstance(item, dict):
                self._known[str(iccid)] = self._offline_record(item)
        for iccid, port in (document.get("ports") or {}).items():
            try:
                value = int(port)
            except (TypeError, ValueError):
                continue
            if 1024 <= value <= 65535:
                self._ports[str(iccid)] = value

    def _persist(self) -> None:
        path = self._state_path()
        os.makedirs(os.path.dirname(path), exist_ok=True)
        temporary = path + ".tmp"
        values = {item["iccid"]: item for item in self.list()}
        with open(temporary, "w", encoding="utf-8") as handle:
            json.dump({"version": 2, "updated_at": time.time(), "sims": values,
                       "ports": self._ports}, handle,
                      indent=2, sort_keys=True)
            handle.write("\n")
        os.replace(temporary, path)

    async def attach(self, hello: dict, websocket) -> Attachment:
        iccid = str(hello.get("iccid") or "").strip()
        agent_id = str(hello.get("agent_id") or "").strip()
        modem_id = str(hello.get("modem_id") or "").strip()
        if not iccid or not agent_id or not modem_id:
            raise ValueError("hello requires iccid, agent_id and modem_id")
        initial_status = dict(hello.get("status") or {})
        # The Agent can only attest its local SOCKS listener. Until this registry has created
        # the reverse listener, the gateway-facing proxy is deliberately not ready.
        initial_status["agent_proxy"] = dict(initial_status.get("proxy") or {})
        initial_status["proxy"] = {"ready": False}
        attachment = Attachment(
            iccid=iccid, agent_id=agent_id, modem_id=modem_id,
            session_id=uuid.uuid4().hex, websocket=websocket,
            imei=str(hello.get("imei") or ""), model=str(hello.get("model") or ""),
            firmware=str(hello.get("firmware") or "")[:100],
            phone=str(hello.get("phone") or ""),
            capabilities=dict(hello.get("capabilities") or {}), status=initial_status,
        )
        async with self._lock:
            previous = self._by_iccid.get(iccid)
            if previous:
                self._known[iccid] = {**previous.public(), "conflict": True}
                self._conflicts.add(iccid)
                self._persist()
                raise ModemConflict("this ICCID already has a live modem attachment")
            self._by_iccid[iccid] = attachment
            self._known[iccid] = attachment.public()
            self._persist()
        return attachment

    async def detach(self, attachment: Attachment) -> None:
        async with self._lock:
            if self._by_iccid.get(attachment.iccid) is not attachment:
                return
            self._by_iccid.pop(attachment.iccid, None)
            self._conflicts.discard(attachment.iccid)
            known = self._offline_record(attachment.public())
            known.update({"online": False, "seen_at": time.time()})
            self._known[attachment.iccid] = known
            self._persist()
        for future in attachment.pending.values():
            if not future.done():
                future.set_exception(ModemUnavailable("modem disconnected"))
        await self._close_reverse(attachment)

    async def receive(self, attachment: Attachment, message: dict) -> None:
        if self._by_iccid.get(attachment.iccid) is not attachment:
            return
        attachment.seen_at = time.time()
        kind = message.get("type")
        if kind == "status":
            reported = dict(message.get("status") or {})
            agent_proxy = reported.pop("proxy", None)
            attachment.status.update(reported)
            if isinstance(agent_proxy, dict):
                attachment.status["agent_proxy"] = agent_proxy
                reverse_ready = bool(attachment.reverse_server and attachment.reverse_port)
                if agent_proxy.get("ready") is False or not reverse_ready:
                    attachment.status["proxy"] = {"ready": False}
            # Status messages are snapshots, not patches, for ephemeral network fields.  An
            # address from a previous PDP context must disappear as soon as the Agent reports
            # the context inactive; otherwise the device page displays an impossible
            # "disconnected" state alongside a live-looking stale IP address.
            if reported.get("data_active") is False:
                attachment.status.pop("ip", None)
            proxy = agent_proxy
            if isinstance(proxy, dict) and proxy.get("ready") is False:
                previous = attachment.status.get("cellular") or {}
                attachment.status["cellular"] = {
                    **previous, "ok": False, "status": "unavailable",
                    "data": reported.get("data") or "disconnected",
                    "proxy": {"ready": False},
                    "error": reported.get("error") or previous.get("error") or
                    "Cellular proxy stopped on the Agent.",
                }
            elif isinstance(proxy, dict) and proxy.get("ready") is True:
                # A healthy snapshot supersedes a failure from the preceding PDP context.
                # During recovery Windows can publish proxy-ready just before its MBN
                # data_active field settles; that interval is "starting", never the old red
                # error. Preserve other diagnostics but clear the stale failure immediately.
                previous = attachment.status.get("cellular") or {}
                end_to_end_proxy = attachment.status.get("proxy") or {"ready": False}
                end_to_end_ready = bool(end_to_end_proxy.get("ready"))
                attachment.status["cellular"] = {
                    **previous,
                    "ok": True if reported.get("data_active") and end_to_end_ready
                               else previous.get("ok"),
                    "status": "ready" if reported.get("data_active") and end_to_end_ready
                              else "starting",
                    "data": reported.get("data") or previous.get("data"),
                    "proxy": end_to_end_proxy,
                    "error": None,
                }
            if message.get("phone"):
                attachment.phone = str(message["phone"])
            self._known[attachment.iccid] = attachment.public()
            self._persist()
            if isinstance(proxy, dict) and proxy.get("ready") is False:
                await self._close_reverse(attachment)
        elif kind == "rpc.result":
            future = attachment.pending.pop(str(message.get("id") or ""), None)
            if future and not future.done():
                if message.get("ok", True):
                    future.set_result(message.get("result") or {})
                else:
                    future.set_exception(RuntimeError(str(message.get("error") or "remote operation failed")))

    def resolve(self, iccid: str) -> Attachment | None:
        return self._by_iccid.get(str(iccid or "").strip())

    async def _close_reverse(self, attachment: Attachment) -> None:
        server, attachment.reverse_server = attachment.reverse_server, None
        attachment.reverse_port = 0
        attachment.reverse_agent_port = 0
        if server:
            server.close()
            await server.wait_closed()
        for future, closed in tuple(attachment.tunnel_waiters.values()):
            if not future.done():
                future.set_exception(ModemUnavailable("reverse proxy stopped"))
            closed.set()
        attachment.tunnel_waiters.clear()

    async def _reverse_endpoint(self, attachment: Attachment, agent_port: int) -> dict:
        if attachment.reverse_server:
            return {"ready": True, "host": cfg.advertise_address(cfg.get_settings()),
                    "port": attachment.reverse_port, "udp": True, "reverse": True}
        await self._close_reverse(attachment)

        async def accept(reader: asyncio.StreamReader, writer: asyncio.StreamWriter):
            tunnel_id = uuid.uuid4().hex
            loop = asyncio.get_running_loop()
            ready, closed = loop.create_future(), asyncio.Event()
            attachment.tunnel_waiters[tunnel_id] = (ready, closed)
            udp_transport = None
            try:
                version, count = await reader.readexactly(2)
                methods = await reader.readexactly(count)
                if version != 5 or 0 not in methods:
                    writer.write(b"\x05\xff")
                    await writer.drain()
                    return
                writer.write(b"\x05\x00")
                await writer.drain()
                version, command, _, atyp = await reader.readexactly(4)
                host, port = await _read_socks_address(reader, atyp)
                if version != 5 or command not in (1, 3):
                    writer.write(b"\x05\x07\x00\x01\x00\x00\x00\x00\x00\x00")
                    await writer.drain()
                    return
                await attachment.websocket.send_json({
                    "version": 1, "type": "tunnel.open", "id": tunnel_id,
                    "session_id": attachment.session_id, "modem_id": attachment.modem_id,
                    "mode": "tcp" if command == 1 else "udp", "host": host, "port": port,
                })
                tunnel = await asyncio.wait_for(ready, 15)
                if command == 1:
                    writer.write(b"\x05\x00\x00\x01\x00\x00\x00\x00\x00\x00")
                    await writer.drain()

                    async def client_to_agent():
                        while True:
                            data = await reader.read(65536)
                            if not data:
                                break
                            await tunnel.send_bytes(data)

                    async def agent_to_client():
                        while True:
                            data = await tunnel.receive_bytes()
                            writer.write(data)
                            await writer.drain()

                    tasks = [asyncio.create_task(client_to_agent()),
                             asyncio.create_task(agent_to_client())]
                else:
                    peer = writer.get_extra_info("peername") or ("", 0)
                    protocol = _UdpQueue(str(peer[0]))
                    udp_transport, _ = await loop.create_datagram_endpoint(
                        lambda: protocol, local_addr=("0.0.0.0", 0))
                    relay_port = udp_transport.get_extra_info("sockname")[1]
                    advertised = cfg.advertise_address(cfg.get_settings())
                    writer.write(b"\x05\x00\x00" + _socks_address(advertised, relay_port))
                    await writer.drain()

                    async def udp_to_agent():
                        while True:
                            await tunnel.send_bytes(await protocol.queue.get())

                    async def agent_to_udp():
                        while True:
                            packet = await tunnel.receive_bytes()
                            if protocol.client:
                                udp_transport.sendto(packet, protocol.client)

                    async def control_lifetime():
                        while await reader.read(1024):
                            pass

                    tasks = [asyncio.create_task(udp_to_agent()),
                             asyncio.create_task(agent_to_udp()),
                             asyncio.create_task(control_lifetime())]
                done, pending = await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
                for task in pending:
                    task.cancel()
                await asyncio.gather(*done, *pending, return_exceptions=True)
            except Exception:
                pass
            finally:
                attachment.tunnel_waiters.pop(tunnel_id, None)
                closed.set()
                if udp_transport:
                    udp_transport.close()
                writer.close()
                try:
                    await writer.wait_closed()
                except Exception:
                    pass

        minimum = max(1024, int(os.environ.get("MDD_REMOTE_MODEM_PORT_MIN", "37000")))
        maximum = min(65535, int(os.environ.get("MDD_REMOTE_MODEM_PORT_MAX", "37999")))
        if maximum < minimum:
            raise ModemUnavailable("remote modem SOCKS port range is invalid")
        size = maximum - minimum + 1
        preferred = self._ports.get(attachment.iccid)
        start = int.from_bytes(hashlib.sha256(attachment.iccid.encode()).digest()[:4], "big") % size
        candidates = ([preferred] if preferred and minimum <= preferred <= maximum else [])
        candidates += [minimum + ((start + offset) % size) for offset in range(size)
                       if minimum + ((start + offset) % size) != preferred]
        server = None
        for candidate in candidates:
            try:
                server = await asyncio.start_server(accept, "0.0.0.0", candidate)
                break
            except OSError:
                continue
        if not server:
            raise ModemUnavailable("no free remote modem SOCKS port is available")
        attachment.reverse_server = server
        attachment.reverse_port = int(server.sockets[0].getsockname()[1])
        attachment.reverse_agent_port = agent_port
        self._ports[attachment.iccid] = attachment.reverse_port
        self._persist()
        return {"ready": True, "host": cfg.advertise_address(cfg.get_settings()),
                "port": attachment.reverse_port, "udp": True, "reverse": True}

    async def accept_tunnel(self, session_id: str, tunnel_id: str, websocket) -> None:
        attachment = next((item for item in self._by_iccid.values()
                           if item.session_id == session_id), None)
        waiter = attachment.tunnel_waiters.get(tunnel_id) if attachment else None
        if not attachment or not waiter:
            raise ModemUnavailable("unknown or expired reverse tunnel")
        ready, closed = waiter
        await websocket.accept()
        if not ready.done():
            ready.set_result(websocket)
        await closed.wait()

    def list(self) -> list[dict]:
        result = dict(self._known)
        for iccid, attachment in self._by_iccid.items():
            result[iccid] = {**attachment.public(),
                             "conflict": iccid in self._conflicts}
        return sorted(result.values(), key=lambda item: item.get("iccid", ""))

    async def rpc(self, iccid: str, method: str, params: dict | None = None,
                  timeout: float = 20.0, operation_id: str = "") -> dict:
        attachment = self.resolve(iccid)
        if not attachment:
            raise ModemUnavailable("SIM is not attached to an online modem")
        request_id = uuid.uuid4().hex
        future = asyncio.get_running_loop().create_future()
        attachment.pending[request_id] = future
        try:
            await attachment.websocket.send_json({
                "version": 1, "type": "rpc.request", "id": request_id,
                "session_id": attachment.session_id, "method": method,
                "modem_id": attachment.modem_id,
                "operation_id": operation_id or request_id, "params": params or {},
            })
            result = await asyncio.wait_for(future, timeout)
            if method == "cellular.ensure" and (result.get("proxy") or {}).get("ready"):
                result = {**result, "proxy": await self._reverse_endpoint(
                    attachment, int(result["proxy"].get("port") or 0))}
            elif method == "cellular.disable":
                await self._close_reverse(attachment)
            attachment.status["last_rpc"] = {"method": method, "at": time.time(),
                                              "ok": bool(result.get("ok", True))}
            self._known[attachment.iccid] = attachment.public()
            self._persist()
            if method.startswith("cellular."):
                attachment.status.update({"cellular": result})
                if "roaming_allowed" in result:
                    attachment.status["roaming_allowed"] = bool(result["roaming_allowed"])
                if result.get("data"):
                    attachment.status["data"] = result["data"]
                if result.get("proxy") is not None:
                    attachment.status["proxy"] = result["proxy"]
                self._known[attachment.iccid] = attachment.public()
                self._persist()
            elif method == "radio.set":
                attachment.status["radio_enabled"] = bool(result.get("radio_enabled"))
                self._known[attachment.iccid] = attachment.public()
                self._persist()
            return result
        except asyncio.TimeoutError as exc:
            raise ModemTimeout(f"remote modem timed out during {method}") from exc
        finally:
            attachment.pending.pop(request_id, None)


registry = ModemRegistry()
