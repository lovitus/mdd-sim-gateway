"""Persistent, card-aware allocation for remote VPCD transport slots.

A VPCD slot is only a socket/PCSC transport address.  It is deliberately not a
SIM, eUICC, line, or hardware identity.  This registry remembers enough remote
endpoint and card metadata to make reconnects predictable and to retain an
offline (grey) view, while allowing the oldest offline slot to be reused when
the configured capacity is exhausted.
"""
from __future__ import annotations

from dataclasses import dataclass
import json
import os
import re
import secrets
import threading
import time
from typing import Callable


BASE_PORT = 35963
MAX_SLOTS = 16
_VIRTUAL_READER_RE = re.compile(
    r"^Virtual PCD(?:\s+[0-9A-Fa-f]{2})?\s+([0-9A-Fa-f]{2})$"
)


class SlotError(RuntimeError):
    """Base class for a rejected VPCD slot claim."""


class SlotBusy(SlotError):
    """The requested slot or stable remote endpoint already has a live session."""


class SlotFull(SlotError):
    """All configured transport slots have live sessions."""


@dataclass(frozen=True)
class SlotClaim:
    slot: int
    port: int
    token: str
    endpoint_key: str
    session_generation: str


def slot_from_reader_name(name: str | None) -> int | None:
    """Translate pcsc-lite's ``Virtual PCD 00 NN`` name into a transport slot."""
    match = _VIRTUAL_READER_RE.fullmatch(str(name or "").strip())
    if not match:
        return None
    # pcsc-lite renders the channel byte in hexadecimal: slots 10..15 are 0A..0F.
    value = int(match.group(1), 16)
    return value if 0 <= value < MAX_SLOTS else None


def _clean(value: object, limit: int = 160) -> str:
    return str(value or "").strip()[:limit]


class VpcdSlotRegistry:
    """Thread-safe persistent slot history plus process-local live claims."""

    def __init__(self, path: str, *, max_slots: int = MAX_SLOTS,
                 clock: Callable[[], float] = time.time):
        self.path = path
        self.max_slots = max(1, min(MAX_SLOTS, int(max_slots)))
        self.clock = clock
        self._lock = threading.RLock()
        self._active: dict[int, dict] = {}
        self._records: dict[int, dict] = {}
        self._load()

    def _load(self) -> None:
        try:
            with open(self.path, encoding="utf-8") as handle:
                document = json.load(handle)
        except (OSError, ValueError, TypeError):
            document = {}
        records = document.get("slots") if isinstance(document, dict) else {}
        if not isinstance(records, dict):
            records = {}
        for raw_slot, raw_record in records.items():
            try:
                slot = int(raw_slot)
            except (TypeError, ValueError):
                continue
            if 0 <= slot < self.max_slots and isinstance(raw_record, dict):
                record = dict(raw_record)
                record["slot"] = slot
                record["online"] = False
                record["identity_current"] = False
                self._records[slot] = record

    def _write(self) -> None:
        parent = os.path.dirname(self.path)
        if parent:
            os.makedirs(parent, mode=0o700, exist_ok=True)
        temporary = self.path + ".tmp"
        document = {
            "version": 1,
            "max_slots": self.max_slots,
            "updated_at": int(self.clock()),
            "slots": {str(slot): {key: value for key, value in record.items()
                                   if key != "online"}
                      for slot, record in sorted(self._records.items())},
        }
        with open(temporary, "w", encoding="utf-8") as handle:
            json.dump(document, handle, ensure_ascii=False, indent=2, sort_keys=True)
            handle.write("\n")
        os.chmod(temporary, 0o600)
        os.replace(temporary, self.path)

    @staticmethod
    def endpoint_key(agent_id: str, reader_id: str) -> str:
        agent_id, reader_id = _clean(agent_id), _clean(reader_id)
        return f"{agent_id}/{reader_id}" if agent_id and reader_id else ""

    def _candidate_slots(self, endpoint_key: str, card_id: str,
                         unavailable: set[int]) -> list[int]:
        preferred = []
        if endpoint_key:
            preferred.extend(slot for slot, record in self._records.items()
                             if record.get("endpoint_key") == endpoint_key)
        if card_id:
            preferred.extend(slot for slot, record in self._records.items()
                             if record.get("card_id") == card_id)
        unused = [slot for slot in range(self.max_slots) if slot not in self._records]
        reusable = sorted(
            (slot for slot in self._records if slot not in self._active),
            key=lambda slot: float(self._records[slot].get("last_seen") or 0),
        )
        # Old Android/legacy agents did not send endpoint metadata. Reusing the most
        # recently disconnected anonymous slot prevents every reconnect from appearing as
        # a brand-new reader and consuming the whole slot range. Metadata-aware agents keep
        # the stronger endpoint/card affinity above; multiple simultaneously-online legacy
        # clients are still isolated because active slots are never candidates.
        recent_anonymous = sorted(
            (slot for slot in self._records
             if slot not in self._active and not self._records[slot].get("endpoint_key")),
            key=lambda slot: float(self._records[slot].get("last_seen") or 0),
            reverse=True,
        )
        ordered = (preferred + unused + reusable) if (endpoint_key or card_id) else (
            recent_anonymous + unused + reusable)
        return list(dict.fromkeys(
            slot for slot in ordered
            if slot not in self._active and slot not in unavailable
        ))

    def claim(self, *, agent_id: str = "", reader_id: str = "",
              reader_name: str = "", requested_slot: str | int | None = "auto",
              card_id: str = "", imei: str = "", peer: str = "",
              agent_run_id: str = "",
              unavailable_slots: set[int] | None = None) -> SlotClaim:
        """Claim a transport slot.

        Stable endpoints reuse their previous free slot.  A supplied card hint (EID or
        ICCID) is the next preference.  Otherwise an unused slot wins, followed by the
        oldest offline history record.  A live slot is never overwritten.
        """
        endpoint_key = self.endpoint_key(agent_id, reader_id)
        card_id = _clean(card_id)
        imei = "".join(ch for ch in str(imei or "") if ch.isdigit())
        if len(imei) != 15:
            imei = ""
        unavailable = {
            int(slot) for slot in (unavailable_slots or set())
            if isinstance(slot, int) and 0 <= slot < self.max_slots
        }
        with self._lock:
            if endpoint_key and any(active.get("endpoint_key") == endpoint_key
                                    for active in self._active.values()):
                raise SlotBusy("this reader already has a live VPCD session")

            explicit = str(requested_slot if requested_slot is not None else "auto").strip()
            if explicit and explicit.lower() != "auto":
                try:
                    slot = int(explicit)
                except ValueError as exc:
                    raise SlotError("slot must be 'auto' or an integer") from exc
                if not 0 <= slot < self.max_slots:
                    raise SlotError(f"slot must be between 0 and {self.max_slots - 1}")
                if slot in self._active or slot in unavailable:
                    raise SlotBusy(f"VPCD slot {slot} is already online")
            else:
                candidates = self._candidate_slots(endpoint_key, card_id, unavailable)
                if not candidates:
                    raise SlotFull("all VPCD slots are online")
                slot = candidates[0]

            now = int(self.clock())
            token = secrets.token_hex(16)
            session_generation = secrets.token_hex(16)
            previous = self._records.get(slot) or {}
            # Reusing an offline slot for a different endpoint/card intentionally replaces
            # only that slot's transport history.  SIM/line/eSIM configuration is stored by
            # ICCID/EID elsewhere and is therefore not deleted.
            same_identity = (not endpoint_key or previous.get("endpoint_key") == endpoint_key
                             or card_id and previous.get("card_id") == card_id)
            record = dict(previous) if same_identity else {}
            record.update({
                "slot": slot,
                "endpoint_key": endpoint_key,
                "agent_id": _clean(agent_id),
                "reader_id": _clean(reader_id),
                "reader_name": _clean(reader_name),
                "peer": _clean(peer),
                "agent_run_id": _clean(agent_run_id, 64),
                "session_generation": session_generation,
                "identity_current": False,
                "connected_at": now,
                "last_seen": now,
                "online": True,
            })
            if card_id:
                record["card_id"] = card_id
            if imei:
                record["imei"] = imei
            self._records[slot] = record
            self._active[slot] = {**record, "token": token}
            self._write()
            return SlotClaim(slot=slot, port=BASE_PORT + slot, token=token,
                             endpoint_key=endpoint_key,
                             session_generation=session_generation)

    def touch(self, claim: SlotClaim) -> bool:
        with self._lock:
            active = self._active.get(claim.slot)
            if not active or active.get("token") != claim.token:
                return False
            now = int(self.clock())
            active["last_seen"] = now
            self._records[claim.slot]["last_seen"] = now
            return True

    def release(self, claim: SlotClaim) -> bool:
        with self._lock:
            active = self._active.get(claim.slot)
            if not active or active.get("token") != claim.token:
                return False
            self._active.pop(claim.slot, None)
            record = self._records.get(claim.slot) or {}
            record["online"] = False
            record["identity_current"] = False
            record["last_seen"] = int(self.clock())
            self._records[claim.slot] = record
            self._write()
            return True

    def begin_observation(self, reader_name: str) -> str | None:
        """Fence a real card probe to the currently active transport generation."""
        slot = slot_from_reader_name(reader_name)
        if slot is None:
            return None
        with self._lock:
            active = self._active.get(slot)
            if not active:
                return None
            generation = str(active.get("session_generation") or "")
            record = self._records.get(slot) or {}
            record["identity_current"] = False
            self._records[slot] = record
            self._write()
            return generation or None

    def observe_card(self, reader_name: str, card: dict, *, eid: str = "",
                     expected_generation: str | None = None) -> bool:
        """Attach last-known card identity to the transport slot serving this PC/SC reader."""
        slot = slot_from_reader_name(reader_name)
        if slot is None:
            return False
        with self._lock:
            record = self._records.get(slot)
            if record is None:
                return False
            if expected_generation is not None:
                active = self._active.get(slot)
                if (not active or not secrets.compare_digest(
                        str(active.get("session_generation") or ""),
                        str(expected_generation or ""))):
                    return False
            iccid = _clean(card.get("iccid"))
            eid = _clean(eid or card.get("eid"))
            if card.get("identity_placeholder"):
                for key in ("card_id", "iccid", "imsi", "matched", "spn", "profile_name", "carrier"):
                    record.pop(key, None)
                eid = eid or _clean(record.get("eid"))
            # A card may move to a different remote reader/slot. Retire its stale offline
            # location before attaching the new one, otherwise the UI shows the same eUICC
            # twice. Never rewrite another live slot: that would hide a genuine conflict.
            for other_slot, other in self._records.items():
                if other_slot == slot or other_slot in self._active:
                    continue
                same_eid = bool(eid and other.get("eid") == eid)
                same_iccid = bool(iccid and other.get("iccid") == iccid)
                if not (same_eid or same_iccid):
                    continue
                for key in ("card_id", "eid", "iccid", "imsi", "matched", "spn",
                            "profile_name", "carrier"):
                    other.pop(key, None)
            if iccid:
                record["iccid"] = iccid
            if eid:
                record["eid"] = eid
            record["card_id"] = eid or iccid or record.get("card_id") or ""
            for key in ("imsi", "matched", "spn", "profile_name", "carrier"):
                if card.get(key):
                    record[key] = card[key]
            record["last_seen"] = int(self.clock())
            if expected_generation is not None:
                record["identity_current"] = True
                record["identity_session_generation"] = str(expected_generation)
            self._write()
            return True

    def confirm_card_absent(self, reader_name: str, expected_generation: str) -> bool:
        """Retire current identity only after the server's authoritative evidence join."""
        slot = slot_from_reader_name(reader_name)
        if slot is None:
            return False
        with self._lock:
            record = self._records.get(slot)
            if (record is None or not secrets.compare_digest(
                    str(record.get("session_generation") or ""),
                    str(expected_generation or ""))):
                return False
            record["identity_current"] = False
            self._write()
            return True

    def snapshot(self) -> list[dict]:
        with self._lock:
            result = []
            for slot, stored in sorted(self._records.items()):
                record = dict(stored)
                active = self._active.get(slot)
                record["online"] = bool(active)
                if active:
                    record["last_seen"] = active.get("last_seen")
                record["port"] = BASE_PORT + slot
                result.append(record)
            return result

    def enrich_cards(self, cards: list[dict]) -> list[dict]:
        """Merge remote metadata and offline identity into card-monitor rows."""
        by_slot = {item["slot"]: item for item in self.snapshot()}
        output = []
        seen = set()
        for original in cards:
            item = dict(original)
            slot = slot_from_reader_name(item.get("name"))
            record = by_slot.get(slot) if slot is not None else None
            if record:
                seen.add(slot)
                quarantined_unknown = bool(
                    item.get("quarantined") or item.get("probe_deferred"))
                for key in ("agent_id", "reader_id", "reader_name", "endpoint_key",
                            "agent_run_id", "session_generation", "identity_current",
                            "identity_session_generation", "eid", "iccid", "imsi", "imei",
                            "matched", "spn", "profile_name", "carrier", "last_seen"):
                    if quarantined_unknown and key in {
                            "identity_session_generation", "eid", "iccid", "imsi",
                            "matched", "spn", "profile_name", "carrier"}:
                        continue
                    if record.get(key) and not item.get(key):
                        item[key] = record[key]
                item.update(remote=True, vpcd_slot=slot,
                            connection_online=bool(record.get("online")))
                if quarantined_unknown:
                    item.update(identity_current=False, matched=None, iccid=None, imsi=None)
            output.append(item)
        # Usually pcscd exposes all compiled slots even when offline.  Synthesize a row as a
        # restart-safe fallback when its enumeration is temporarily missing.
        for slot, record in by_slot.items():
            if slot in seen or not (record.get("iccid") or record.get("eid")):
                continue
            output.append({
                "index": None,
                "name": f"Virtual PCD 00 {slot:02X}",
                "present": False,
                "hardware_kind": "reader",
                "remote": True,
                "vpcd_slot": slot,
                "connection_online": bool(record.get("online")),
                **{key: record.get(key) for key in (
                    "agent_id", "reader_id", "reader_name", "endpoint_key", "eid",
                    "agent_run_id", "session_generation", "identity_current",
                    "identity_session_generation", "iccid", "imsi", "imei", "matched",
                    "spn", "profile_name", "carrier", "last_seen") if record.get(key)},
            })
        return output
