"""Strict SIP-over-WebSocket SDP rewriting for the direct media fast path.

Only Engine-to-browser SDP is rewritten.  SIP message boundaries and WebSocket opcodes are
preserved by the caller; this module accepts and returns the same ``str``/``bytes`` type.
TURN candidates and browser-originated SDP are never touched.
"""
from __future__ import annotations

import hashlib
import ipaddress
import re


class SipMediaRewriteError(ValueError):
    """The frame claimed to contain SDP but was unsafe to transform."""


def _foundation(route_id: str, base: str, protocol: str, candidate_type: str) -> str:
    # RFC 8445 foundations group candidates with the same type, base and transport.  RTP and
    # RTCP components therefore deliberately share this value.
    material = f"mdd-v1\0{route_id}\0{base}\0{protocol}\0{candidate_type}".encode()
    return hashlib.sha256(material).hexdigest()[:20]


def _split_message(text: str) -> tuple[list[str], str, str]:
    if "\r\n\r\n" in text:
        head, body = text.split("\r\n\r\n", 1)
        newline = "\r\n"
    elif "\n\n" in text:
        head, body = text.split("\n\n", 1)
        newline = "\n"
    else:
        return text.splitlines(), "", "\r\n" if "\r\n" in text else "\n"
    return head.split(newline), body, newline


def _header_value(lines: list[str], names: set[str]) -> str:
    # Folded Content-Type/Length is rejected rather than normalized: accepting two physical
    # representations of a framing header creates request-smuggling ambiguity.
    found = []
    for index, line in enumerate(lines[1:], 1):
        name, separator, value = line.partition(":")
        if separator and name.strip().casefold() in names:
            if index + 1 < len(lines) and lines[index + 1].startswith((" ", "\t")):
                raise SipMediaRewriteError("folded SIP framing header")
            found.append(value.strip())
    if len(found) > 1:
        raise SipMediaRewriteError("duplicate SIP framing header")
    return found[0] if found else ""


def rewrite_engine_sdp(frame: str | bytes, *, engine_ip: str,
                       advertised_ip: str, route_id: str,
                       rtp_start: int, rtp_end: int) -> str | bytes:
    """Rewrite exact Engine IPv4 host candidates to one browser-reachable host address.

    SDP origin lines, server-reflexive/relay candidates, ports, credentials and all
    browser-to-Engine frames remain untouched.  Malformed SDP fails closed.
    """
    binary = isinstance(frame, bytes)
    if not binary and not isinstance(frame, str):
        raise TypeError("SIP frame must be text or bytes")
    try:
        text = frame.decode("utf-8", errors="strict") if binary else frame
        source = str(ipaddress.IPv4Address(engine_ip))
        target = str(ipaddress.IPv4Address(advertised_ip))
    except (UnicodeError, ipaddress.AddressValueError) as exc:
        raise SipMediaRewriteError("invalid SIP text or IPv4 media address") from exc
    if not route_id or len(route_id) > 128:
        raise SipMediaRewriteError("invalid media route identity")
    if (type(rtp_start) is not int or type(rtp_end) is not int
            or not 1 <= rtp_start <= rtp_end <= 65535):
        raise SipMediaRewriteError("invalid published RTP range")

    lines, body, newline = _split_message(text)
    if not lines:
        raise SipMediaRewriteError("empty SIP message")
    content_type = _header_value(lines, {"content-type", "c"}).casefold()
    content_length = _header_value(lines, {"content-length", "l"})
    if content_length and not content_length.isdigit():
        raise SipMediaRewriteError("invalid SIP Content-Length")
    if not content_type:
        return frame
    media_type = content_type.split(";", 1)[0].strip()
    if media_type != "application/sdp":
        return frame
    if not body or not body.replace("\r\n", "\n").startswith("v=0\n"):
        raise SipMediaRewriteError("invalid SDP body")

    body_newline = "\r\n" if "\r\n" in body else "\n"
    trailing = body.endswith(("\n", "\r"))
    body_lines = body.replace("\r\n", "\n").split("\n")
    if trailing and body_lines and body_lines[-1] == "":
        body_lines.pop()
    rewritten = 0
    has_media = False
    for index, line in enumerate(body_lines):
        if line.startswith("m="):
            has_media = True
            media = line[2:].split()
            if len(media) < 3 or not media[1].isdigit():
                raise SipMediaRewriteError("malformed SDP media line")
            media_port = int(media[1])
            if media[0].casefold() == "audio" and not rtp_start <= media_port <= rtp_end:
                raise SipMediaRewriteError("SDP audio port is outside the published RTP range")
        if line == f"c=IN IP4 {source}":
            body_lines[index] = f"c=IN IP4 {target}"
            rewritten += 1
            continue
        rtcp = re.fullmatch(r"a=rtcp:(\d{1,5}) IN IP4 (\S+)", line)
        if rtcp and rtcp.group(2) == source:
            rtcp_port = int(rtcp.group(1))
            if not rtp_start <= rtcp_port <= rtp_end:
                raise SipMediaRewriteError("SDP RTCP port is outside the published RTP range")
            body_lines[index] = f"a=rtcp:{rtcp.group(1)} IN IP4 {target}"
            rewritten += 1
            continue
        if not line.startswith("a=candidate:"):
            continue
        fields = line[len("a=candidate:"):].split()
        # foundation component transport priority address port typ type [extensions]
        if len(fields) < 8 or fields[6].casefold() != "typ":
            raise SipMediaRewriteError("malformed ICE candidate")
        if fields[7].casefold() != "host" or fields[4] != source:
            continue
        if fields[2].casefold() != "udp":
            raise SipMediaRewriteError("only the proven UDP RTP mapping may be advertised")
        if (not fields[1].isdigit() or not fields[3].isdigit()
                or not fields[5].isdigit() or not 1 <= int(fields[5]) <= 65535
                or not rtp_start <= int(fields[5]) <= rtp_end):
            raise SipMediaRewriteError("invalid or unpublished ICE candidate port")
        fields[0] = _foundation(route_id, target, "udp", "host")
        fields[4] = target
        body_lines[index] = "a=candidate:" + " ".join(fields)
        rewritten += 1
    if not has_media or not rewritten:
        raise SipMediaRewriteError("SDP does not contain the expected Engine media address")
    rewritten_body = body_newline.join(body_lines) + (body_newline if trailing else "")
    length = len(rewritten_body.encode("utf-8"))
    found_length = False
    for index, line in enumerate(lines[1:], 1):
        name, separator, _value = line.partition(":")
        if separator and name.strip().casefold() in {"content-length", "l"}:
            if found_length:
                raise SipMediaRewriteError("duplicate SIP Content-Length")
            lines[index] = f"{name}:{' ' if not name.endswith(' ') else ''}{length}"
            found_length = True
    if not found_length:
        lines.append(f"Content-Length: {length}")
    result = newline.join(lines) + newline + newline + rewritten_body
    return result.encode("utf-8") if binary else result
