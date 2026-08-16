#!/usr/bin/env python3
"""
mdd-card-agent - Cross-platform PC/SC Smartcard Forwarding Agent.

Runs on Linux, Windows, or macOS with physically attached smartcard readers.
Bridges local PC/SC smartcard access to the central MDD VoWiFi Gateway VPCD socket.

Features:
- Encrypted WebSocket over TLS (WSS) support with TOFU certificate pinning.
- Full support for Linux (pcscd), macOS (PCSC.framework), Windows (WinSCard).
- Multi-reader support: forward all connected readers or specific readers.
- Automatic reconnection on network drop or smartcard replug.
- Built-in APDU Safety Guard: strictly blocks physical profile deletion APDUs.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import logging
import os
import secrets
import socket
import ssl
import struct
import sys
import threading
import time
import urllib.parse
from typing import Optional, List, Dict

try:
    from smartcard.System import readers
    from smartcard.CardConnection import CardConnection
    from smartcard.Exceptions import NoCardException, CardConnectionException
except ImportError:
    # Allow running without pyscard if only imported as module or unit tests
    readers = None
    CardConnection = None
    NoCardException = Exception
    CardConnectionException = Exception

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] [card-agent] %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("mdd-card-agent")

# VPCD control commands
VPCD_CTRL_OFF = 0x01
VPCD_CTRL_ON = 0x02
VPCD_CTRL_RESET = 0x03
VPCD_CTRL_ATR = 0x04

# Safety: Check if APDU is an eUICC Delete Profile command or raw delete
def is_forbidden_apdu(apdu_bytes: bytes) -> bool:
    if len(apdu_bytes) < 4:
        return False
    # SGP.22 ES10c / ES10b Delete Profile tag: 0xBF33 or 0xBF30
    if b"\xBF\x33" in apdu_bytes:
        log.warning("APDU Safety Guard: Blocked ES10c.DeleteProfile (tag 0xBF33)!")
        return True
    # ISO 7816-4 DELETE FILE: CLA=0x00/0x80, INS=0xE4
    if apdu_bytes[1] == 0xE4:
        log.warning("APDU Safety Guard: Blocked ISO 7816 DELETE FILE APDU (INS=0xE4)!")
        return True
    return False


def format_fingerprint(der_bytes: bytes) -> str:
    sha = hashlib.sha256(der_bytes).hexdigest().upper()
    return ":".join(sha[i:i+2] for i in range(0, len(sha), 2))


def get_pin_store_path() -> str:
    pin_dir = os.path.expanduser("~/.mdd-agent")
    os.makedirs(pin_dir, exist_ok=True)
    return os.path.join(pin_dir, "known_fingerprints.json")


def load_pin_store() -> Dict[str, str]:
    path = get_pin_store_path()
    if os.path.exists(path):
        try:
            with open(path, "r", encoding="utf-8") as f:
                return json.load(f)
        except Exception:
            return {}
    return {}


def save_pin_store(store: Dict[str, str]):
    path = get_pin_store_path()
    try:
        with open(path, "w", encoding="utf-8") as f:
            json.dump(store, f, indent=2)
    except Exception as exc:
        log.warning("Failed to save fingerprint store: %s", exc)


def verify_or_pin_fingerprint(target_host: str, cert_der: bytes, explicit_pin: str = "", reset_pin: bool = False) -> None:
    current_fp = format_fingerprint(cert_der)

    if explicit_pin:
        clean_exp = explicit_pin.upper().replace(":", "")
        clean_act = current_fp.upper().replace(":", "")
        if clean_exp != clean_act:
            raise ssl.SSLError(f"[SECURITY ALERT] ⚠️ Certificate fingerprint mismatch!\n  Expected: {explicit_pin}\n  Actual:   {current_fp}")
        log.info("[SECURITY] ✅ Verified against explicit certificate pin (%s)", current_fp)
        return

    store = load_pin_store()
    pinned_fp = store.get(target_host)

    if reset_pin:
        store[target_host] = current_fp
        save_pin_store(store)
        log.info("[SECURITY] 🔄 Reset and updated pinned fingerprint for %s -> %s", target_host, current_fp)
        return

    if not pinned_fp:
        # Trust On First Use (TOFU)
        store[target_host] = current_fp
        save_pin_store(store)
        log.info("[SECURITY] 🔒 First time connecting to %s. Pinned server certificate fingerprint (SHA-256):\n           %s", target_host, current_fp)
        return

    if pinned_fp != current_fp:
        raise ssl.SSLError(
            f"[SECURITY ALERT] ⚠️ Server TLS certificate fingerprint MISMATCH for {target_host}!\n"
            f"  Previous Pinned: {pinned_fp}\n"
            f"  Current Server:  {current_fp}\n"
            f"  Possible Man-In-The-Middle (MITM) attack or certificate renewal!\n"
            f"  Connection ABORTED. To trust this new certificate, rerun with '--reset-pin'"
        )

    log.info("[SECURITY] ✅ TLS certificate fingerprint verified: %s", current_fp)


class WebSocketClientTransport:
    def __init__(self, raw_ssl_sock: ssl.SSLSocket):
        self.sock = raw_ssl_sock

    def send_frame(self, data: bytes, opcode: int = 0x02):
        header = bytearray()
        header.append(0x80 | opcode)  # FIN + opcode
        length = len(data)
        if length < 126:
            header.append(0x80 | length)  # Mask bit set
        elif length <= 65535:
            header.append(0x80 | 126)
            header.extend(struct.pack(">H", length))
        else:
            header.append(0x80 | 127)
            header.extend(struct.pack(">Q", length))

        mask_key = secrets.token_bytes(4)
        header.extend(mask_key)

        masked_data = bytearray(length)
        for i in range(length):
            masked_data[i] = data[i] ^ mask_key[i % 4]

        self.sock.sendall(bytes(header) + bytes(masked_data))

    def recv_frame(self) -> Optional[bytes]:
        while True:
            b1_b2 = self._recv_exact(2)
            if not b1_b2:
                return None
            b1, b2 = b1_b2[0], b1_b2[1]
            opcode = b1 & 0x0F
            is_masked = (b2 & 0x80) != 0
            length = b2 & 0x7F

            if length == 126:
                ext = self._recv_exact(2)
                if not ext:
                    return None
                (length,) = struct.unpack(">H", ext)
            elif length == 127:
                ext = self._recv_exact(8)
                if not ext:
                    return None
                (length,) = struct.unpack(">Q", ext)

            mask_key = None
            if is_masked:
                mask_key = self._recv_exact(4)
                if not mask_key:
                    return None

            payload = bytearray(self._recv_exact(length) or b"")
            if len(payload) < length:
                return None

            if is_masked and mask_key:
                for i in range(length):
                    payload[i] ^= mask_key[i % 4]

            if opcode == 0x08:  # Close
                return None
            if opcode == 0x09:  # Ping -> reply Pong
                self.send_frame(bytes(payload), opcode=0x0A)
                continue
            if opcode == 0x0A:  # Pong
                continue
            if opcode in (0x01, 0x02):  # Text or Binary
                return bytes(payload)

    def _recv_exact(self, size: int) -> Optional[bytes]:
        buf = bytearray()
        while len(buf) < size:
            chunk = self.sock.recv(size - len(buf))
            if not chunk:
                return None
            buf.extend(chunk)
        return bytes(buf)

    def close(self):
        try:
            self.send_frame(b"", opcode=0x08)
        except Exception:
            pass
        try:
            self.sock.close()
        except Exception:
            pass


def connect_wss(host: str, port: int, path: str = "/mdd/api/vpcd/ws", token: str = "", explicit_pin: str = "", reset_pin: bool = False) -> WebSocketClientTransport:
    raw_sock = socket.create_connection((host, port), timeout=10)
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE  # We perform TOFU pinning on peer cert

    ssl_sock = ctx.wrap_socket(raw_sock, server_hostname=host)
    cert_der = ssl_sock.getpeercert(binary_form=True)
    if not cert_der:
        ssl_sock.close()
        raise ssl.SSLError("Server presented no TLS certificate")

    verify_or_pin_fingerprint(host, cert_der, explicit_pin=explicit_pin, reset_pin=reset_pin)

    # HTTP WebSocket Upgrade
    sec_key = base64.b64encode(secrets.token_bytes(16)).decode("ascii")
    ws_path = path if path.startswith("/") else f"/{path}"
    if token:
        sep = "&" if "?" in ws_path else "?"
        ws_path += f"{sep}token={urllib.parse.quote(token)}"

    headers = [
        f"GET {ws_path} HTTP/1.1",
        f"Host: {host}:{port}",
        "Upgrade: websocket",
        "Connection: Upgrade",
        f"Sec-WebSocket-Key: {sec_key}",
        "Sec-WebSocket-Version: 13",
    ]
    if token:
        headers.append(f"X-Agent-Token: {token}")
    headers.append("\r\n")

    ssl_sock.sendall("\r\n".join(headers).encode("utf-8"))

    # Read response status
    resp_header = bytearray()
    while b"\r\n\r\n" not in resp_header:
        chunk = ssl_sock.recv(1024)
        if not chunk:
            break
        resp_header.extend(chunk)

    status_line = resp_header.split(b"\r\n")[0].decode("utf-8", errors="ignore")
    if "101" not in status_line:
        ssl_sock.close()
        if "401" in status_line or "403" in status_line:
            raise PermissionError(f"Gateway rejected connection: {status_line} (Check your --token)")
        raise ConnectionError(f"WebSocket upgrade rejected: {status_line}")

    log.info("✅ WSS handshake established on https://%s:%d%s", host, port, ws_path)
    return WebSocketClientTransport(ssl_sock)


def recv_exact(sock: socket.socket, size: int) -> Optional[bytes]:
    buf = bytearray()
    while len(buf) < size:
        chunk = sock.recv(size - len(buf))
        if not chunk:
            return None
        buf.extend(chunk)
    return bytes(buf)


class PhysicalCardClient:
    def __init__(self, reader_name: str):
        self.target_reader_name = reader_name
        self.connection: Optional[CardConnection] = None
        self.reader_name: Optional[str] = None
        self.atr: bytes = b""

    def find_and_connect(self) -> bool:
        if readers is None:
            log.error("pyscard is not installed")
            return False
        r_list = readers()
        if not r_list:
            log.warning("No PC/SC smartcard readers found on this system.")
            return False

        selected_reader = None
        for r in r_list:
            if str(r) == self.target_reader_name or self.target_reader_name.lower() in str(r).lower():
                selected_reader = r
                break

        if not selected_reader:
            log.warning("Reader '%s' not found among available: %s",
                        self.target_reader_name, [str(r) for r in r_list])
            return False

        try:
            self.reader_name = str(selected_reader)
            conn = selected_reader.createConnection()
            conn.connect()
            self.connection = conn
            raw_atr = conn.getATR()
            self.atr = bytes(raw_atr) if raw_atr else b"\x3B\x9F\x95\x80\x1F\xC7\x80\x31\xE0\x73\xFE\x21\x1B\x66\xD0\x01\x77\x97\x02\x0C\x00\x0B"
            log.info("Connected to smartcard on reader '%s' (ATR: %s)", self.reader_name, self.atr.hex())
            return True
        except (NoCardException, CardConnectionException) as exc:
            log.warning("Card not ready on reader '%s': %s", selected_reader, exc)
            self.connection = None
            return False
        except Exception as exc:
            log.error("Failed to connect to reader '%s': %s", selected_reader, exc)
            self.connection = None
            return False

    def transmit(self, apdu: bytes) -> bytes:
        if not self.connection:
            return bytes.fromhex("6F00")
        if is_forbidden_apdu(apdu):
            return bytes.fromhex("6985")
        try:
            apdu_list = list(apdu)
            data, sw1, sw2 = self.connection.transmit(apdu_list)
            return bytes(data) + bytes([sw1, sw2])
        except Exception as exc:
            log.warning("Card APDU transmit error on [%s]: %s", self.reader_name, exc)
            return bytes.fromhex("6F00")

    def reset(self):
        if self.connection:
            try:
                self.connection.disconnect()
            except Exception:
                pass
            self.connection = None
        self.find_and_connect()


def run_reader_bridge(
    gateway_host: str,
    gateway_port: int,
    reader_name: str,
    token: str = "",
    use_wss: bool = True,
    ws_path: str = "/mdd/api/vpcd/ws",
    explicit_pin: str = "",
    reset_pin: bool = False,
    retry_delay: float = 3.0,
):
    proto_label = "WSS (Encrypted + TOFU)" if use_wss else "Raw TCP"
    log.info("Starting Worker [%s] for [%s] -> Gateway %s:%d", proto_label, reader_name, gateway_host, gateway_port)
    card_client = PhysicalCardClient(reader_name)

    while True:
        # 1. Connect to card reader
        while not card_client.connection:
            if card_client.find_and_connect():
                break
            time.sleep(retry_delay)

        # 2. Connect to gateway
        ws_client = None
        raw_sock = None
        try:
            if use_wss:
                ws_client = connect_wss(
                    gateway_host, gateway_port, ws_path, token=token,
                    explicit_pin=explicit_pin, reset_pin=reset_pin
                )
            else:
                raw_sock = socket.create_connection((gateway_host, gateway_port), timeout=10)
                raw_sock.settimeout(None)

            # Clear reset_pin after first successful connection
            reset_pin = False
            log.info("[%s] Bridge established! Forwarding APDU commands.", card_client.reader_name)

            while True:
                if use_wss:
                    payload = ws_client.recv_frame()
                    if payload is None:
                        log.warning("[%s] Gateway WebSocket closed.", card_client.reader_name)
                        break
                else:
                    header = recv_exact(raw_sock, 2)
                    if not header:
                        log.warning("[%s] Gateway disconnected.", card_client.reader_name)
                        break
                    (length,) = struct.unpack(">H", header)
                    if length == 0:
                        continue
                    payload = recv_exact(raw_sock, length)
                    if not payload:
                        break

                if len(payload) == 1:
                    ctrl = payload[0]
                    if ctrl == VPCD_CTRL_ATR:
                        atr = card_client.atr
                        if use_wss:
                            ws_client.send_frame(atr)
                        else:
                            raw_sock.sendall(struct.pack(">H", len(atr)) + atr)
                    elif ctrl in (VPCD_CTRL_OFF, VPCD_CTRL_ON, VPCD_CTRL_RESET):
                        card_client.reset()
                    continue

                # Normal APDU command
                resp = card_client.transmit(payload)
                if use_wss:
                    ws_client.send_frame(resp)
                else:
                    raw_sock.sendall(struct.pack(">H", len(resp)) + resp)

        except Exception as exc:
            log.warning("[%s] Gateway connection error (%s). Retrying in %.1fs...", reader_name, exc, retry_delay)
        finally:
            if ws_client:
                ws_client.close()
            if raw_sock:
                try:
                    raw_sock.close()
                except Exception:
                    pass

        time.sleep(retry_delay)


def list_connected_readers():
    if readers is None:
        print("pyscard is not installed")
        return
    r_list = readers()
    if not r_list:
        print("No PC/SC smartcard readers found.")
        return
    print(f"Found {len(r_list)} connected PC/SC smartcard reader(s):")
    for i, r in enumerate(r_list):
        conn = None
        atr_str = "No card inserted"
        try:
            conn = r.createConnection()
            conn.connect()
            raw_atr = conn.getATR()
            if raw_atr:
                atr_str = "ATR: " + bytes(raw_atr).hex()
        except Exception:
            pass
        finally:
            if conn:
                try:
                    conn.disconnect()
                except Exception:
                    pass
        print(f"  [{i}] {r} ({atr_str})")


def main():
    parser = argparse.ArgumentParser(
        description="MDD Card Agent - Smartcard Forwarding Client",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # 1. Connect via WSS with Token (Auto TOFU Pinning):
  python3 agent/card_agent.py --gateway 10.44.0.14 --port 8443 --token "<AGENT_TOKEN>"

  # 2. Connect specifying server shorthand (host:port):
  python3 agent/card_agent.py --server 10.44.0.14:8443 --token "<AGENT_TOKEN>"

  # 3. Connect filtering a specific smartcard reader:
  python3 agent/card_agent.py --server 10.44.0.14:8443 --token "<AGENT_TOKEN>" --reader "ESTKme"

  # 4. Reset and trust a newly rotated server certificate:
  python3 agent/card_agent.py --server 10.44.0.14:8443 --token "<AGENT_TOKEN>" --reset-pin

  # 5. Connect with explicit certificate fingerprint pinning:
  python3 agent/card_agent.py --server 10.44.0.14:8443 --token "<AGENT_TOKEN>" --pin "75:9E:08:73:9F:..."
        """,
    )
    parser.add_argument("--server", "-s", default="", help="Gateway address in host:port format (e.g. 10.44.0.14:8443)")
    parser.add_argument("--gateway", "-g", default="127.0.0.1", help="Gateway IP/hostname (default: 127.0.0.1)")
    parser.add_argument("--port", "-p", type=int, default=8443, help="Gateway port (8443 for WSS, 35963 for raw TCP)")
    parser.add_argument("--token", "-t", default=os.getenv("MDD_AGENT_TOKEN", ""), help="Agent security token (shared across devices)")
    parser.add_argument("--wss", action="store_true", default=None, help="Force WSS encrypted tunnel (default true for port 8443)")
    parser.add_argument("--raw-tcp", action="store_true", help="Force raw unencrypted TCP tunnel (legacy)")
    parser.add_argument("--pin", default="", help="Explicit expected SHA-256 certificate fingerprint")
    parser.add_argument("--reset-pin", action="store_true", help="Reset and trust the current server certificate fingerprint")
    parser.add_argument("--reader", "-r", default=None, help="Specific reader name substring to forward")
    parser.add_argument("--reader-index", "-i", type=int, default=None, help="Specific reader index to forward")
    parser.add_argument("--all", "-a", action="store_true", help="Forward all connected readers to ports base, base+1, ...")
    parser.add_argument("--list", "-l", action="store_true", help="List all connected readers and exit")
    parser.add_argument("--retry", type=float, default=3.0, help="Retry interval in seconds (default: 3.0)")
    args = parser.parse_args()

    if args.server:
        if ":" in args.server:
            h, p = args.server.rsplit(":", 1)
            args.gateway = h
            try:
                args.port = int(p)
            except ValueError:
                pass
        else:
            args.gateway = args.server

    if args.list:
        list_connected_readers()
        return

    use_wss = True
    if args.raw_tcp:
        use_wss = False
    elif args.wss is False or args.port == 35963:
        use_wss = False

    if readers is None:
        log.error("pyscard is required. Run: pip install pyscard")
        sys.exit(1)

    r_list = readers()
    if not r_list:
        log.error("No PC/SC smartcard readers found on this system.")
        sys.exit(1)

    if args.reader_index is not None:
        if args.reader_index >= len(r_list) or args.reader_index < 0:
            log.error("Invalid reader index %d. Available: 0..%d", args.reader_index, len(r_list) - 1)
            sys.exit(1)
        r_name = str(r_list[args.reader_index])
        run_reader_bridge(args.gateway, args.port, r_name, token=args.token, use_wss=use_wss, explicit_pin=args.pin, reset_pin=args.reset_pin, retry_delay=args.retry)
    elif args.reader is not None:
        matched = [str(r) for r in r_list if args.reader.lower() in str(r).lower()]
        if not matched:
            log.error("No reader matched pattern '%s'", args.reader)
            sys.exit(1)
        run_reader_bridge(args.gateway, args.port, matched[0], token=args.token, use_wss=use_wss, explicit_pin=args.pin, reset_pin=args.reset_pin, retry_delay=args.retry)
    elif args.all or len(r_list) > 1:
        log.info("Multi-reader mode: forwarding %d readers starting at %d", len(r_list), args.port)
        for idx, r in enumerate(r_list):
            port = args.port if use_wss else args.port + idx
            ws_path = f"/mdd/api/vpcd/ws?slot={idx}" if use_wss else "/mdd/api/vpcd/ws"
            r_name = str(r)
            t = threading.Thread(
                target=run_reader_bridge,
                args=(args.gateway, port, r_name, args.token, use_wss, ws_path, args.pin, args.reset_pin, args.retry),
                name=f"Bridge-{idx}",
                daemon=True,
            )
            t.start()
        try:
            while True:
                time.sleep(1)
        except KeyboardInterrupt:
            log.info("Stopping all bridges.")
            sys.exit(0)
    else:
        r_name = str(r_list[0])
        run_reader_bridge(args.gateway, args.port, r_name, token=args.token, use_wss=use_wss, explicit_pin=args.pin, reset_pin=args.reset_pin, retry_delay=args.retry)


if __name__ == "__main__":
    main()
