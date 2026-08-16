#!/usr/bin/env python3
"""
mdd-card-agent - Cross-platform PC/SC Smartcard Forwarding Agent.

Runs on Linux, Windows, or macOS with physically attached smartcard readers.
Bridges local PC/SC smartcard access to the central MDD VoWiFi Gateway VPCD socket.

Features:
- Full support for Linux (pcscd), macOS (PCSC.framework), Windows (WinSCard).
- Auto-enumeration of plugged-in smartcard readers (CCID, ESTKme, SIM readers).
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
import time
from typing import Optional

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
# SGP.22 ES10c.DeleteProfile: Tag 0xBF33 in STORE DATA or proprietary DELETE APDU
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
    def __init__(self, reader_pattern: Optional[str] = None):
        self.reader_pattern = reader_pattern
        self.connection: Optional[CardConnection] = None
        self.reader_name: Optional[str] = None
        self.atr: bytes = b""

    def find_and_connect(self) -> bool:
        r_list = readers()
        if not r_list:
            log.warning("No PC/SC smartcard readers found on this system.")
            return False

        selected_reader = None
        if self.reader_pattern:
            for r in r_list:
                if self.reader_pattern.lower() in str(r).lower():
                    selected_reader = r
                    break
        else:
            selected_reader = r_list[0]

        if not selected_reader:
            log.warning("No matching reader found for pattern: %s (available: %s)",
                        self.reader_pattern, [str(r) for r in r_list])
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
            # Return SW=0x6985 (Conditions of use not satisfied / Command blocked)
            return bytes.fromhex("6985")
        try:
            apdu_list = list(apdu)
            data, sw1, sw2 = self.connection.transmit(apdu_list)
            return bytes(data) + bytes([sw1, sw2])
        except Exception as exc:
            log.warning("Card APDU transmit error: %s", exc)
            return bytes.fromhex("6F00")

    def reset(self):
        if self.connection:
            try:
                self.connection.disconnect()
            except Exception:
                pass
            self.connection = None
        self.find_and_connect()


def run_agent(gateway_host: str, gateway_port: int, reader_pattern: Optional[str] = None, retry_delay: float = 3.0):
    log.info("Starting MDD Card Agent -> Gateway %s:%d", gateway_host, gateway_port)
    card_client = PhysicalCardClient(reader_pattern)

    while True:
        # 1. Connect to card reader
        while not card_client.connection:
            if card_client.find_and_connect():
                break
            time.sleep(retry_delay)

        # 2. Connect to gateway VPCD socket
        sock = None
        try:
            log.info("Connecting to gateway socket %s:%d...", gateway_host, gateway_port)
            sock = socket.create_connection((gateway_host, gateway_port), timeout=10)
            sock.settimeout(None)
            log.info("Bridge established! Forwarding APDU between [%s] and gateway.", card_client.reader_name)

            while True:
                header = recv_exact(sock, 2)
                if not header:
                    log.warning("Gateway disconnected.")
                    break
                (length,) = struct.unpack(">H", header)
                if length == 0:
                    continue

                payload = recv_exact(sock, length)
                if not payload:
                    log.warning("Gateway stream closed unexpectedly.")
                    break

                if length == 1:
                    ctrl = payload[0]
                    if ctrl == VPCD_CTRL_ATR:
                        # Return ATR
                        atr = card_client.atr
                        sock.sendall(struct.pack(">H", len(atr)) + atr)
                    elif ctrl in (VPCD_CTRL_OFF, VPCD_CTRL_ON, VPCD_CTRL_RESET):
                        card_client.reset()
                    continue

                # Normal APDU command
                resp = card_client.transmit(payload)
                sock.sendall(struct.pack(">H", len(resp)) + resp)

        except (ConnectionRefusedError, ConnectionResetError, socket.timeout, OSError) as exc:
            log.warning("Gateway connection failed (%s). Retrying in %.1fs...", exc, retry_delay)
        finally:
            if sock:
                try:
                    sock.close()
                except Exception:
                    pass

        time.sleep(retry_delay)


def main():
    parser = argparse.ArgumentParser(description="MDD Card Agent - Smartcard Forwarding Client")
    parser.add_argument("--gateway", "-g", default="127.0.0.1", help="Gateway IP/hostname (default: 127.0.0.1)")
    parser.add_argument("--port", "-p", type=int, default=35963, help="VPCD socket port (default: 35963)")
    parser.add_argument("--reader", "-r", default=None, help="Substring filter for PC/SC reader name")
    parser.add_argument("--retry", type=float, default=3.0, help="Retry interval in seconds (default: 3.0)")
    args = parser.parse_args()

    try:
        run_agent(args.gateway, args.port, reader_pattern=args.reader, retry_delay=args.retry)
    except KeyboardInterrupt:
        log.info("Agent stopped by user.")
        sys.exit(0)


if __name__ == "__main__":
    main()
