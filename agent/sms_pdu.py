"""Minimal 3GPP SMS PDU codec used by platform providers.

Outbound messages deliberately use UCS-2 so encoding is deterministic across platforms.  The
decoder supports UCS-2 and the GSM 7-bit default alphabet used by ordinary inbound messages.
Multipart concatenation stays a business-layer concern and is not guessed here.
"""
from __future__ import annotations

import hashlib
import re
import time


_GSM7 = (
    "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ\x1bÆæßÉ !\"#¤%&'()*+,-./"
    "0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿"
    "abcdefghijklmnopqrstuvwxyzäöñüà"
)
_GSM7_EXT = {10: "\f", 20: "^", 40: "{", 41: "}", 47: "\\", 60: "[",
             61: "~", 62: "]", 64: "|", 101: "€"}


def _semi_octets(number: str) -> str:
    value = number + ("F" if len(number) % 2 else "")
    return "".join(value[index + 1] + value[index] for index in range(0, len(value), 2))


def encode_submit(recipient: str, body: str) -> tuple[str, int]:
    if not re.fullmatch(r"\+?\d{1,32}", str(recipient or "")):
        raise ValueError("invalid SMS recipient")
    digits = recipient.lstrip("+")
    content = str(body or "").encode("utf-16-be")
    if not content or len(content) > 140:
        raise ValueError("SMS body must contain 1-70 UCS-2 characters")
    toa = "91" if recipient.startswith("+") else "81"
    # SMSC=default, SMS-SUBMIT without validity period, MR=0, PID=0, DCS=UCS-2.
    pdu = "00" + "01" + "00" + f"{len(digits):02X}" + toa + _semi_octets(digits)
    pdu += "00" + "08" + f"{len(content):02X}" + content.hex().upper()
    return pdu, len(bytes.fromhex(pdu)) - 1


def _number(raw: bytes, digits: int, international: bool) -> str:
    value = "".join(f"{byte:02X}"[1] + f"{byte:02X}"[0] for byte in raw)[:digits]
    return ("+" if international else "") + value.rstrip("F")


def _gsm7(raw: bytes, septets: int) -> str:
    values = []
    accumulator = 0
    bits = 0
    for byte in raw:
        accumulator |= byte << bits
        bits += 8
        while bits >= 7 and len(values) < septets:
            values.append(accumulator & 0x7F)
            accumulator >>= 7
            bits -= 7
    chars, escaped = [], False
    for value in values:
        if escaped:
            chars.append(_GSM7_EXT.get(value, "?"))
            escaped = False
        elif value == 27:
            escaped = True
        else:
            chars.append(_GSM7[value] if value < len(_GSM7) else "?")
    return "".join(chars)


def decode_deliver(pdu: str, index: int | str = 0, status: str = "") -> dict:
    raw = bytes.fromhex(str(pdu or ""))
    offset = 0
    smsc_length = raw[offset]
    offset += 1 + smsc_length
    first = raw[offset]
    offset += 1
    if first & 3 != 0:
        raise ValueError("only SMS-DELIVER PDUs can be listed")
    address_digits, toa = raw[offset], raw[offset + 1]
    offset += 2
    address_bytes = (address_digits + 1) // 2
    peer = _number(raw[offset:offset + address_bytes], address_digits, toa == 0x91)
    offset += address_bytes
    offset += 1  # PID
    dcs = raw[offset]
    offset += 1
    offset += 7  # SCTS
    length = raw[offset]
    offset += 1
    user_data = raw[offset:]
    if dcs & 0x0C == 0x08:
        body = user_data[:length].decode("utf-16-be", "replace")
    elif dcs & 0x0C == 0:
        body = _gsm7(user_data, length)
    else:
        body = user_data[:length].decode("latin-1", "replace")
    fingerprint = hashlib.sha256(raw).hexdigest()
    return {"id": str(index), "fingerprint": fingerprint, "direction": "in",
            "peer": peer, "body": body, "ts": int(time.time()), "status": status}
