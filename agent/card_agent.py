#!/usr/bin/env python3
"""
mdd-card-agent - Cross-platform PC/SC Smartcard Forwarding Agent.

Runs on Linux, Windows, or macOS with physically attached smartcard readers.
Bridges local PC/SC smartcard access to the central MDD VoWiFi Gateway VPCD socket.

Features:
- Full support for Linux (pcscd), macOS (PCSC.framework), Windows (WinSCard).
- Multi-reader support: forward all connected readers or specific readers.
- Automatic reconnection on network drop or smartcard replug.
- Built-in APDU Safety Guard: strictly blocks physical profile deletion APDUs.
"""

from __future__ import annotations

import argparse
import logging
import os
import signal
import socket
import struct
import sys
import threading
import time
from typing import Optional, List

try:
    from smartcard.System import readers
    from smartcard.CardConnection import CardConnection
    from smartcard.Exceptions import NoCardException, CardConnectionException
except ImportError:
    print("[ERROR] pyscard is required. Install via: pip install pyscard", file=sys.stderr)
    sys.exit(1)

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
    cla, ins, p1, p2 = apdu_bytes[0], apdu_bytes[1], apdu_bytes[2], apdu_bytes[3]
    # SGP.22 ES10c / ES10b Delete Profile tag: 0xBF33 or 0xBF30
    if b"\xBF\x33" in apdu_bytes:
        log.warning("APDU Safety Guard: Blocked ES10c.DeleteProfile (tag 0xBF33)!")
        return True
    # ISO 7816-4 DELETE FILE: CLA=0x00/0x80, INS=0xE4
    if ins == 0xE4:
        log.warning("APDU Safety Guard: Blocked ISO 7816 DELETE FILE APDU (INS=0xE4)!")
        return True
    return False


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


def run_reader_bridge(gateway_host: str, gateway_port: int, reader_name: str, retry_delay: float = 3.0):
    log.info("Starting Worker for [%s] -> Gateway %s:%d", reader_name, gateway_host, gateway_port)
    card_client = PhysicalCardClient(reader_name)

    while True:
        # 1. Connect to card reader
        while not card_client.connection:
            if card_client.find_and_connect():
                break
            time.sleep(retry_delay)

        # 2. Connect to gateway VPCD socket
        sock = None
        try:
            log.info("[%s] Connecting to gateway socket %s:%d...", card_client.reader_name, gateway_host, gateway_port)
            sock = socket.create_connection((gateway_host, gateway_port), timeout=10)
            sock.settimeout(None)
            log.info("[%s] Bridge established! Forwarding APDU commands.", card_client.reader_name)

            while True:
                header = recv_exact(sock, 2)
                if not header:
                    log.warning("[%s] Gateway disconnected.", card_client.reader_name)
                    break
                (length,) = struct.unpack(">H", header)
                if length == 0:
                    continue

                payload = recv_exact(sock, length)
                if not payload:
                    log.warning("[%s] Gateway stream closed unexpectedly.", card_client.reader_name)
                    break

                if length == 1:
                    ctrl = payload[0]
                    if ctrl == VPCD_CTRL_ATR:
                        atr = card_client.atr
                        sock.sendall(struct.pack(">H", len(atr)) + atr)
                    elif ctrl in (VPCD_CTRL_OFF, VPCD_CTRL_ON, VPCD_CTRL_RESET):
                        card_client.reset()
                    continue

                # Normal APDU command
                resp = card_client.transmit(payload)
                sock.sendall(struct.pack(">H", len(resp)) + resp)

        except (ConnectionRefusedError, ConnectionResetError, socket.timeout, OSError) as exc:
            log.warning("[%s] Gateway connection failed (%s). Retrying in %.1fs...", reader_name, exc, retry_delay)
        finally:
            if sock:
                try:
                    sock.close()
                except Exception:
                    pass

        time.sleep(retry_delay)


def list_connected_readers():
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
    parser = argparse.ArgumentParser(description="MDD Card Agent - Smartcard Forwarding Client")
    parser.add_argument("--gateway", "-g", default="127.0.0.1", help="Gateway IP/hostname (default: 127.0.0.1)")
    parser.add_argument("--port", "-p", type=int, default=35963, help="Base VPCD socket port (default: 35963)")
    parser.add_argument("--reader", "-r", default=None, help="Specific reader name substring to forward")
    parser.add_argument("--reader-index", "-i", type=int, default=None, help="Specific reader index to forward")
    parser.add_argument("--all", "-a", action="store_true", help="Forward all connected readers to ports base, base+1, ...")
    parser.add_argument("--list", "-l", action="store_true", help="List all connected readers and exit")
    parser.add_argument("--retry", type=float, default=3.0, help="Retry interval in seconds (default: 3.0)")
    args = parser.parse_args()

    if args.list:
        list_connected_readers()
        return

    r_list = readers()
    if not r_list:
        log.error("No PC/SC smartcard readers found on this system.")
        sys.exit(1)

    threads = []

    if args.reader_index is not None:
        if args.reader_index >= len(r_list) or args.reader_index < 0:
            log.error("Invalid reader index %d. Available: 0..%d", args.reader_index, len(r_list) - 1)
            sys.exit(1)
        r_name = str(r_list[args.reader_index])
        run_reader_bridge(args.gateway, args.port, r_name, retry_delay=args.retry)
    elif args.reader is not None:
        matched = [str(r) for r in r_list if args.reader.lower() in str(r).lower()]
        if not matched:
            log.error("No reader matched pattern '%s'", args.reader)
            sys.exit(1)
        run_reader_bridge(args.gateway, args.port, matched[0], retry_delay=args.retry)
    elif args.all or len(r_list) > 1:
        log.info("Multi-reader mode: forwarding %d readers to gateway ports starting at %d", len(r_list), args.port)
        for idx, r in enumerate(r_list):
            port = args.port + idx
            r_name = str(r)
            t = threading.Thread(
                target=run_reader_bridge,
                args=(args.gateway, port, r_name, args.retry),
                name=f"Bridge-{idx}",
                daemon=True,
            )
            t.start()
            threads.append(t)
        try:
            while True:
                time.sleep(1)
        except KeyboardInterrupt:
            log.info("Stopping all bridges.")
            sys.exit(0)
    else:
        r_name = str(r_list[0])
        run_reader_bridge(args.gateway, args.port, r_name, retry_delay=args.retry)


if __name__ == "__main__":
    main()
