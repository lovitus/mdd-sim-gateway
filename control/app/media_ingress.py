"""Per-browser WebRTC media ingress selection for multi-homed gateway hosts.

The host orchestrator is the only authority for addresses assigned to the Linux host.  A
browser never submits a raw address: its same-origin HTTP/WSS Host is matched to one opaque
candidate in the current inventory.  Confirmations intentionally expire when Control restarts,
the host inventory changes, or the media protocol changes.  This is a direct-IPv4 fast path;
NAT, remote reverse proxies and IPv6 require a managed TURN relay.
"""

from __future__ import annotations

import hashlib
import hmac
import ipaddress
import json
import os
import secrets
import threading
import time
from pathlib import Path
from urllib.parse import urlsplit

from . import config as cfg


PROTOCOL_VERSION = 1
INVENTORY_FRESH_SECONDS = 45
CONTROL_EPOCH = secrets.token_hex(16)
_lock = threading.RLock()


def _inventory_path() -> Path:
    return Path(cfg.DATA_DIR) / "orchestrator" / "network-inventory.json"


def _singbox_config_path() -> Path:
    return Path(cfg.DATA_DIR) / "orchestrator" / "sing-box.json"


def _confirmation_path() -> Path:
    return Path(cfg.DATA_DIR) / "media-ingress-confirmations.json"


def _read_json(path: Path) -> dict:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
        return value if isinstance(value, dict) else {}
    except (OSError, ValueError, TypeError):
        return {}


def _write_json(path: Path, value: dict) -> None:
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    if os.name != "nt":
        os.chmod(path.parent, 0o700)
    temporary = path.with_suffix(path.suffix + f".{os.getpid()}.tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n",
                         encoding="utf-8")
    if os.name != "nt":
        os.chmod(temporary, 0o600)
    os.replace(temporary, path)


def candidate_id(interface: str, ifindex: int, address: str) -> str:
    material = f"v{PROTOCOL_VERSION}\0{interface}\0{ifindex}\0{address}".encode()
    return hashlib.sha256(material).hexdigest()[:32]


def _inventory_generation(values: list[dict]) -> str:
    canonical = json.dumps(values, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(canonical.encode()).hexdigest()


def _singbox_tun_endpoints(config: dict) -> set[tuple[str, str]]:
    """Return exact host-side TUN endpoints owned by our generated sing-box config."""
    endpoints: set[tuple[str, str]] = set()
    for inbound in config.get("inbounds") or []:
        if not isinstance(inbound, dict) or inbound.get("type") != "tun":
            continue
        interface = str(inbound.get("interface_name") or "")
        if not interface:
            continue
        addresses = inbound.get("address") or []
        if isinstance(addresses, str):
            addresses = [addresses]
        for item in addresses:
            try:
                parsed = ipaddress.ip_interface(str(item)).ip
            except ValueError:
                continue
            if parsed.version == 4:
                endpoints.add((interface, str(parsed)))
    return endpoints


def _managed_media_egress() -> set[tuple[str, str]]:
    return _singbox_tun_endpoints(_read_json(_singbox_config_path()))


def inventory() -> dict:
    raw = _read_json(_inventory_path())
    generation = str(raw.get("generation") or "")
    raw_values = []
    seen = set()
    for item in raw.get("candidates") or []:
        if not isinstance(item, dict):
            continue
        interface = str(item.get("interface") or "")
        address = str(item.get("address") or "")
        ifindex = item.get("ifindex")
        try:
            parsed = ipaddress.ip_address(address)
        except ValueError:
            continue
        # The first implementation is deliberately IPv4-only.  An IPv6 candidate is not safe
        # until Docker publishes the RTP range on :: and the no-cost Echo gate proves it.
        if (parsed.version != 4 or parsed.is_loopback or parsed.is_link_local
                or parsed.is_multicast or not interface or type(ifindex) is not int
                or ifindex <= 0 or item.get("up") is not True
                or item.get("bridge") is True):
            continue
        cid = str(item.get("id") or candidate_id(interface, ifindex, str(parsed)))
        expected = candidate_id(interface, ifindex, str(parsed))
        if not hmac.compare_digest(cid, expected) or cid in seen:
            continue
        seen.add(cid)
        raw_values.append({
            "id": cid,
            "interface": interface[:64],
            "ifindex": ifindex,
            "address": str(parsed),
            "family": "ipv4",
            "kind": str(item.get("kind") or "network")[:32],
            "up": True,
        })
    expected_generation = _inventory_generation(raw_values)
    raw_updated_at = raw.get("updated_at")
    updated_at = raw_updated_at if type(raw_updated_at) is int else 0
    age = time.time() - updated_at if updated_at else float("inf")
    if (not generation or not hmac.compare_digest(generation, expected_generation)
            or age < -5 or age > INVENTORY_FRESH_SECONDS):
        return {"generation": "", "candidates": [], "updated_at": 0}

    managed_egress = _managed_media_egress()
    values = [
        item for item in raw_values
        if (item["interface"], item["address"]) not in managed_egress
    ]
    return {"generation": _inventory_generation(values), "candidates": values,
            "updated_at": updated_at}


def _literal_host(host_header: str) -> str:
    text = str(host_header or "").strip()
    if not text or len(text) > 512 or any(ch in text for ch in "\r\n/\\"):
        return ""
    # urlsplit handles bracketed literals and an optional port without treating the first IPv6
    # colon as a separator.  IPv6 is still rejected by the protocol gate below.
    try:
        host = urlsplit(f"//{text}").hostname or ""
        parsed = ipaddress.ip_address(host)
    except (ValueError, TypeError):
        return ""
    return str(parsed) if parsed.version == 4 else ""


def resolve(host_header: str) -> dict:
    current = inventory()
    literal = _literal_host(host_header)
    matches = [item for item in current["candidates"] if item["address"] == literal]
    selected = matches[0] if len(matches) == 1 else None
    return {"inventory_generation": current["generation"],
            "candidate": selected, "candidates": current["candidates"],
            "reason": ("ready" if selected else
                       "host_inventory_unavailable" if not current["generation"] else
                       "access_host_not_a_managed_ipv4")}


def same_origin(origin: str, host_header: str) -> bool:
    try:
        parsed = urlsplit(str(origin or ""))
        request = urlsplit(f"//{str(host_header or '').strip()}")
    except ValueError:
        return False
    if (parsed.scheme not in {"http", "https"} or parsed.username or parsed.password
            or request.username or request.password or not parsed.hostname
            or not request.hostname):
        return False
    origin_host = parsed.hostname.casefold().rstrip(".")
    request_host = request.hostname.casefold().rstrip(".")
    default_port = 443 if parsed.scheme == "https" else 80
    try:
        origin_port = parsed.port or default_port
        request_port = request.port or default_port
    except ValueError:
        return False
    return bool(origin_host and hmac.compare_digest(origin_host, request_host)
                and origin_port == request_port)


def _confirmations() -> dict:
    value = _read_json(_confirmation_path())
    return value if value.get("version") == 1 else {"version": 1, "entries": {}}


def status(host_header: str) -> dict:
    route = resolve(host_header)
    candidate = route.get("candidate") or {}
    confirmation = (_confirmations().get("entries") or {}).get(candidate.get("id"), {})
    confirmed = bool(candidate and confirmation
                     and confirmation.get("control_epoch") == CONTROL_EPOCH
                     and confirmation.get("inventory_generation")
                     == route["inventory_generation"]
                     and confirmation.get("protocol_version") == PROTOCOL_VERSION)
    return {**route, "confirmed": confirmed,
            "confirmation_required": not confirmed,
            "confirmed_at": int(confirmation.get("confirmed_at") or 0) if confirmed else 0,
            "protocol_version": PROTOCOL_VERSION}


def binding_id(route_status: dict) -> str:
    """Internal identity for one confirmed route generation.

    Candidate IDs remain stable so the UI can explain a previous choice.  Call admission is
    stricter: an already-open SIP WebSocket must not consume proof issued after a host-network
    inventory change, even when its individual interface still exists.
    """
    candidate = (route_status or {}).get("candidate") or {}
    cid = str(candidate.get("id") or "")
    generation = str((route_status or {}).get("inventory_generation") or "")
    if not (route_status or {}).get("confirmed") or not cid or not generation:
        return ""
    material = (f"v{PROTOCOL_VERSION}\0{CONTROL_EPOCH}\0{generation}\0{cid}"
                .encode("utf-8"))
    return hashlib.sha256(material).hexdigest()


def confirm(candidate: str, generation: str, host_header: str) -> dict:
    route = resolve(host_header)
    actual = route.get("candidate") or {}
    if (not actual or not candidate or not generation
            or not hmac.compare_digest(str(candidate), str(actual.get("id") or ""))
            or not hmac.compare_digest(str(generation), route["inventory_generation"])):
        raise ValueError("the selected media route is stale or does not match this browser")
    with _lock:
        document = _confirmations()
        entries = document.setdefault("entries", {})
        # Keep only current-inventory entries; old interface IDs are neither useful nor safe.
        valid_ids = {item["id"] for item in route["candidates"]}
        document["entries"] = {key: value for key, value in entries.items()
                               if key in valid_ids}
        document["entries"][actual["id"]] = {
            "inventory_generation": route["inventory_generation"],
            "control_epoch": CONTROL_EPOCH,
            "protocol_version": PROTOCOL_VERSION,
            "confirmed_at": int(time.time()),
        }
        _write_json(_confirmation_path(), document)
    return status(host_header)
