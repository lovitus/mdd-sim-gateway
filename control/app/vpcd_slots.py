"""Persistent, card-aware allocation for remote VPCD transport slots.

A VPCD slot is only a socket/PCSC transport address.  It is deliberately not a
SIM, eUICC, line, or hardware identity.  This registry remembers enough remote
endpoint and card metadata to make reconnects predictable and to retain an
offline (grey) view, while allowing the oldest offline slot to be reused when
the configured capacity is exhausted.
"""
from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import math
import os
import re
import secrets
import stat
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


@dataclass(frozen=True)
class RecoveryReservation:
    slot: int
    token: str
    campaign_epoch: str
    expected_session_generation: str
    current_identity_digest: str
    deadline: float


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
        stem, _extension = os.path.splitext(path)
        self.reservation_path = stem + ".recovery-reservations.json"
        self.max_slots = max(1, min(MAX_SLOTS, int(max_slots)))
        self.clock = clock
        self._lock = threading.RLock()
        self._active: dict[int, dict] = {}
        self._records: dict[int, dict] = {}
        self._reservations: dict[int, dict] = {}
        self._invalid_reservations: dict[int, object] = {}
        self._reservation_load_failed = False
        self._load()
        self._load_reservations()

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
                self._records[slot] = self._migrate_record(slot, raw_record)

    @staticmethod
    def _migrate_record(slot: int, raw_record: dict) -> dict:
        """Migrate flat v1 identity to history; restart never restores current identity."""
        raw = dict(raw_record)
        identity_keys = ("card_id", "eid", "iccid", "imsi", "matched", "spn",
                         "profile_name", "carrier")
        route_keys = ("endpoint_key", "agent_id", "reader_id", "reader_name", "peer",
                      "agent_run_id", "session_generation", "imei", "connected_at",
                      "last_seen", "ready_at", "card_hint")
        route = dict(raw.get("route") or {})
        for key in route_keys:
            if key in raw and key not in route:
                route[key] = raw[key]
        historical = dict(raw.get("last_known_identity") or {})
        source = dict(raw.get("current_identity") or {}) or raw
        for key in identity_keys:
            if source.get(key) and key not in historical:
                historical[key] = source[key]
        return {"slot": slot, "route": {**route, "online": False, "ready_at": None},
                "current_identity": None,
                "last_known_identity": historical or None,
                "legacy_observed": False}

    def _write(self) -> None:
        parent = os.path.dirname(self.path)
        if parent:
            os.makedirs(parent, mode=0o700, exist_ok=True)
        temporary = self.path + ".tmp"
        document = {
            "version": 2,
            "max_slots": self.max_slots,
            "updated_at": int(self.clock()),
            # Duplicate route scalars for read-only v1 tooling; identity remains nested.
            "slots": {str(slot): {**record, **dict(record.get("route") or {})}
                      for slot, record in sorted(self._records.items())},
        }
        with open(temporary, "w", encoding="utf-8") as handle:
            json.dump(document, handle, ensure_ascii=False, indent=2, sort_keys=True)
            handle.write("\n")
        os.chmod(temporary, 0o600)
        os.replace(temporary, self.path)

    @staticmethod
    def current_identity_digest(identity: dict | None) -> str:
        """Digest only authoritative current-card fields shared with Control topology."""
        value = identity if isinstance(identity, dict) else {}
        normalized = {
            key: _clean(value.get(key)) for key in (
                "eid", "iccid", "imsi", "matched", "session_generation")
            if _clean(value.get(key))
        }
        if (not normalized.get("session_generation")
                or not any(normalized.get(key) for key in ("eid", "iccid", "imsi"))):
            return ""
        return hashlib.sha256(json.dumps(
            normalized, sort_keys=True, separators=(",", ":")).encode()).hexdigest()

    @staticmethod
    def _valid_reservation(slot: int, value: object) -> dict | None:
        required = {
            "token", "campaign_epoch", "expected_session_generation",
            "current_identity_digest", "deadline",
        }
        if (not isinstance(value, dict) or set(value) != required
                or not re.fullmatch(r"[0-9a-f]{32}", str(value.get("token") or ""))
                or not re.fullmatch(r"[0-9a-f]{64}", str(
                    value.get("campaign_epoch") or ""))
                or not _clean(value.get("expected_session_generation"), 256)
                or not re.fullmatch(r"[0-9a-f]{64}", str(
                    value.get("current_identity_digest") or ""))
                or not isinstance(value.get("deadline"), (int, float))
                or isinstance(value.get("deadline"), bool)
                or not math.isfinite(float(value["deadline"]))
                or float(value["deadline"]) <= 0):
            return None
        return {
            "token": str(value["token"]),
            "campaign_epoch": str(value["campaign_epoch"]),
            "expected_session_generation": _clean(
                value["expected_session_generation"], 256),
            "current_identity_digest": str(value["current_identity_digest"]),
            "deadline": float(value["deadline"]),
        }

    def _load_reservations(self) -> None:
        try:
            metadata = os.lstat(self.reservation_path)
            if (not stat.S_ISREG(metadata.st_mode)
                    or metadata.st_mode & 0o777 != 0o600):
                self._reservation_load_failed = True
                return
            with open(self.reservation_path, encoding="utf-8") as handle:
                document = json.load(handle)
        except FileNotFoundError:
            return
        except (OSError, ValueError, TypeError):
            self._reservation_load_failed = True
            return
        if (not isinstance(document, dict) or set(document) != {
                "version", "updated_at", "reservations"}
                or document.get("version") != 1
                or not isinstance(document.get("updated_at"), (int, float))
                or isinstance(document.get("updated_at"), bool)
                or not math.isfinite(float(document.get("updated_at")))
                or float(document.get("updated_at")) < 0
                or not isinstance(document.get("reservations"), dict)):
            self._reservation_load_failed = True
            return
        for raw_slot, raw_value in document["reservations"].items():
            try:
                slot = int(raw_slot)
            except (TypeError, ValueError):
                self._reservation_load_failed = True
                return
            if not 0 <= slot < self.max_slots or str(slot) != str(raw_slot):
                self._reservation_load_failed = True
                return
            checked = self._valid_reservation(slot, raw_value)
            if checked is None:
                self._invalid_reservations[slot] = raw_value
            else:
                self._reservations[slot] = checked

    def _write_reservations(self) -> None:
        if self._reservation_load_failed:
            raise SlotBusy("VPCD recovery reservation state is unreadable")
        parent = os.path.dirname(self.reservation_path)
        if parent:
            os.makedirs(parent, mode=0o700, exist_ok=True)
        temporary = self.reservation_path + ".tmp"
        values = {str(slot): value for slot, value in self._invalid_reservations.items()}
        values.update({str(slot): value for slot, value in self._reservations.items()})
        document = {"version": 1, "updated_at": float(self.clock()),
                    "reservations": values}
        descriptor = os.open(
            temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            json.dump(document, handle, ensure_ascii=True, indent=2, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, self.reservation_path)
        directory = os.open(parent or ".", os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)

    def begin_recovery_reservation(
            self, slot: int, *, campaign_epoch: str,
            expected_session_generation: str, current_identity_digest: str,
            deadline: float) -> RecoveryReservation:
        """Durably reserve one exact live route; an existing lease never extends."""
        slot = int(slot)
        campaign_epoch = str(campaign_epoch or "")
        generation = _clean(expected_session_generation, 256)
        identity_digest = str(current_identity_digest or "")
        if (not 0 <= slot < self.max_slots
                or not re.fullmatch(r"[0-9a-f]{64}", campaign_epoch)
                or not generation or not re.fullmatch(r"[0-9a-f]{64}", identity_digest)
                or not isinstance(deadline, (int, float)) or isinstance(deadline, bool)
                or not math.isfinite(float(deadline)) or float(deadline) <= 0):
            raise SlotError("invalid VPCD recovery reservation")
        with self._lock:
            if self._reservation_load_failed or slot in self._invalid_reservations:
                raise SlotBusy("VPCD recovery reservation state is unreadable")
            existing = self._reservations.get(slot)
            active = self._active.get(slot)
            record = self._records.get(slot) or {}
            current = dict(record.get("current_identity") or {})
            exact_current = bool(
                active and active.get("ready_at") is not None
                and secrets.compare_digest(
                    str(active.get("session_generation") or ""), generation)
                and secrets.compare_digest(
                    self.current_identity_digest(current), identity_digest))
            if existing is not None:
                if (secrets.compare_digest(existing["campaign_epoch"], campaign_epoch)
                        and secrets.compare_digest(
                            existing["expected_session_generation"], generation)
                        and secrets.compare_digest(
                            existing["current_identity_digest"], identity_digest)
                        and exact_current):
                    return RecoveryReservation(slot=slot, **existing)
                raise SlotBusy(f"VPCD slot {slot} has a recovery reservation")
            if not exact_current:
                raise SlotBusy("VPCD recovery route is no longer exact and current")
            value = {
                "token": secrets.token_hex(16), "campaign_epoch": campaign_epoch,
                "expected_session_generation": generation,
                "current_identity_digest": identity_digest, "deadline": float(deadline),
            }
            self._reservations[slot] = value
            try:
                self._write_reservations()
            except Exception as exc:
                self._reservation_load_failed = True
                raise SlotBusy("VPCD recovery reservation could not be persisted") from exc
            return RecoveryReservation(slot=slot, **value)

    def validate_recovery_reservation(self, reservation: RecoveryReservation) -> bool:
        with self._lock:
            if (type(reservation) is not RecoveryReservation
                    or self._reservation_load_failed
                    or reservation.slot in self._invalid_reservations):
                return False
            stored = self._reservations.get(reservation.slot)
            active = self._active.get(reservation.slot)
            record = self._records.get(reservation.slot) or {}
            if (stored is None or active is None or active.get("ready_at") is None
                    or float(self.clock()) >= stored["deadline"]):
                return False
            expected = {
                "token": reservation.token, "campaign_epoch": reservation.campaign_epoch,
                "expected_session_generation": reservation.expected_session_generation,
                "current_identity_digest": reservation.current_identity_digest,
                "deadline": float(reservation.deadline),
            }
            return bool(stored == expected
                        and secrets.compare_digest(
                            str(active.get("session_generation") or ""),
                            stored["expected_session_generation"])
                        and secrets.compare_digest(
                            self.current_identity_digest(record.get("current_identity")),
                            stored["current_identity_digest"]))

    def clear_recovery_reservation(self, reservation: RecoveryReservation) -> bool:
        """Clear only the exact terminal campaign capability; deadline alone is insufficient."""
        with self._lock:
            if type(reservation) is not RecoveryReservation:
                return False
            stored = self._reservations.get(reservation.slot)
            if (stored is None or not secrets.compare_digest(
                    stored["token"], str(reservation.token or ""))
                    or not secrets.compare_digest(
                        stored["campaign_epoch"], str(reservation.campaign_epoch or ""))):
                return False
            removed = self._reservations.pop(reservation.slot)
            try:
                self._write_reservations()
            except Exception as exc:
                self._reservations[reservation.slot] = removed
                self._reservation_load_failed = True
                raise SlotBusy("VPCD recovery reservation clear could not be persisted") from exc
            record = self._records.get(reservation.slot) or {}
            if reservation.slot not in self._active:
                current = dict(record.get("current_identity") or {})
                if current:
                    current["card_id"] = current.get("eid") or current.get("iccid") or ""
                    record["last_known_identity"] = current
                record["current_identity"] = None
                record["legacy_observed"] = False
                self._records[reservation.slot] = record
                self._write()
            return True

    def recovery_reservation(self, slot: int) -> RecoveryReservation | None:
        with self._lock:
            if self._reservation_load_failed or int(slot) in self._invalid_reservations:
                raise SlotBusy("VPCD recovery reservation state is unreadable")
            value = self._reservations.get(int(slot))
            return RecoveryReservation(slot=int(slot), **value) if value else None

    def recovery_reservation_for_campaign(
            self, campaign_epoch: str) -> RecoveryReservation | None:
        """Return one exact campaign capability; ambiguity and corruption fail closed."""
        campaign = str(campaign_epoch or "")
        with self._lock:
            if self._reservation_load_failed:
                raise SlotBusy("VPCD recovery reservation state is unreadable")
            matches = [RecoveryReservation(slot=slot, **value)
                       for slot, value in self._reservations.items()
                       if secrets.compare_digest(value["campaign_epoch"], campaign)]
            if len(matches) > 1:
                raise SlotBusy("VPCD recovery campaign has ambiguous reservations")
            return matches[0] if matches else None

    @staticmethod
    def endpoint_key(agent_id: str, reader_id: str) -> str:
        agent_id, reader_id = _clean(agent_id), _clean(reader_id)
        return f"{agent_id}/{reader_id}" if agent_id and reader_id else ""

    def _candidate_slots(self, endpoint_key: str, card_id: str,
                         unavailable: set[int]) -> list[int]:
        preferred = []
        if endpoint_key:
            preferred.extend(slot for slot, record in self._records.items()
                             if (record.get("route") or {}).get("endpoint_key") == endpoint_key)
        if card_id:
            preferred.extend(slot for slot, record in self._records.items()
                             if ((record.get("last_known_identity") or {}).get("card_id") == card_id
                                 or (record.get("route") or {}).get("card_hint") == card_id))
        unused = [slot for slot in range(self.max_slots) if slot not in self._records]
        reusable = sorted(
            (slot for slot in self._records if slot not in self._active),
            key=lambda slot: float(
                (self._records[slot].get("route") or {}).get("last_seen") or 0),
        )
        # Old Android/legacy agents did not send endpoint metadata. Reusing the most
        # recently disconnected anonymous slot prevents every reconnect from appearing as
        # a brand-new reader and consuming the whole slot range. Metadata-aware agents keep
        # the stronger endpoint/card affinity above; multiple simultaneously-online legacy
        # clients are still isolated because active slots are never candidates.
        recent_anonymous = sorted(
            (slot for slot in self._records
             if slot not in self._active
             and not (self._records[slot].get("route") or {}).get("endpoint_key")),
            key=lambda slot: float(
                (self._records[slot].get("route") or {}).get("last_seen") or 0),
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
            if self._reservation_load_failed:
                raise SlotBusy("VPCD recovery reservation state is unreadable")
            unavailable.update(self._reservations)
            unavailable.update(self._invalid_reservations)
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
            session_generation = token
            previous = self._records.get(slot) or {}
            route = {
                "endpoint_key": endpoint_key,
                "agent_id": _clean(agent_id),
                "reader_id": _clean(reader_id),
                "reader_name": _clean(reader_name),
                "peer": _clean(peer),
                "agent_run_id": _clean(agent_run_id, 64),
                "session_generation": session_generation,
                "connected_at": now,
                "last_seen": now,
                "online": True,
                "ready_at": None,
                "card_hint": card_id,
            }
            if imei:
                route["imei"] = imei
            # Card hints affect only slot choice. Every route starts without current identity.
            record = {
                "slot": slot, "route": route, "current_identity": None,
                "last_known_identity": dict(previous.get("last_known_identity") or {}) or None,
                "legacy_observed": False,
            }
            self._records[slot] = record
            self._active[slot] = {**route, "token": token}
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
            self._records[claim.slot]["route"]["last_seen"] = now
            return True

    def mark_ready(self, claim: SlotClaim) -> bool:
        """Admit probes only after the claim's local VPCD TCP transport is open."""
        with self._lock:
            active = self._active.get(claim.slot)
            if not active or not secrets.compare_digest(
                    str(active.get("token") or ""), str(claim.token or "")):
                return False
            ready_at = float(self.clock())
            active["ready_at"] = ready_at
            self._records[claim.slot]["route"]["ready_at"] = ready_at
            self._write()
            return True

    def release(self, claim: SlotClaim) -> bool:
        with self._lock:
            active = self._active.get(claim.slot)
            if not active or active.get("token") != claim.token:
                return False
            self._active.pop(claim.slot, None)
            record = self._records.get(claim.slot) or {}
            if claim.slot in self._reservations or claim.slot in self._invalid_reservations:
                # The route is offline, but its exact current identity remains the durable
                # recovery owner until Engine terminal evidence authorizes token-bound clear.
                record["route"].update(
                    online=False, ready_at=None, last_seen=int(self.clock()))
                self._records[claim.slot] = record
                self._write()
                return True
            current = dict(record.get("current_identity") or {})
            if current:
                current["card_id"] = current.get("eid") or current.get("iccid") or ""
                record["last_known_identity"] = current
            record["current_identity"] = None
            record["legacy_observed"] = False
            record["route"].update(
                online=False, ready_at=None, last_seen=int(self.clock()))
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
            if not active or active.get("ready_at") is None:
                return None
            generation = str(active.get("session_generation") or "")
            record = self._records.get(slot) or {}
            reservation = self._reservations.get(slot)
            if reservation is not None:
                if (not secrets.compare_digest(
                        generation, reservation["expected_session_generation"])
                        or not secrets.compare_digest(
                            self.current_identity_digest(record.get("current_identity")),
                            reservation["current_identity_digest"])):
                    return None
                return generation or None
            if slot in self._invalid_reservations or self._reservation_load_failed:
                return None
            record["current_identity"] = None
            record["legacy_observed"] = False
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
            active = self._active.get(slot)
            if not active:
                return False
            if expected_generation is not None:
                if (active.get("ready_at") is None or not secrets.compare_digest(
                        str(active.get("session_generation") or ""),
                        str(expected_generation or ""))):
                    return False
            iccid = _clean(card.get("iccid"))
            eid = _clean(eid or card.get("eid"))
            historical = dict(record.get("last_known_identity") or {})
            observed = {}
            if card.get("identity_placeholder"):
                eid = eid or _clean(historical.get("eid"))
            if iccid:
                observed["iccid"] = iccid
            if eid:
                observed["eid"] = eid
            for key in ("imsi", "matched", "spn", "profile_name", "carrier"):
                if card.get(key):
                    observed[key] = card[key]
            reservation = self._reservations.get(slot)
            if reservation is not None:
                observed["session_generation"] = str(expected_generation or "")
                return bool(expected_generation is not None
                            and secrets.compare_digest(
                                str(expected_generation),
                                reservation["expected_session_generation"])
                            and secrets.compare_digest(
                                self.current_identity_digest(observed),
                                reservation["current_identity_digest"])
                            and secrets.compare_digest(
                                self.current_identity_digest(record.get("current_identity")),
                                reservation["current_identity_digest"]))
            if slot in self._invalid_reservations or self._reservation_load_failed:
                return False
            # A card may move to a different remote reader/slot. Retire its stale offline
            # location before attaching the new one, otherwise the UI shows the same eUICC
            # twice. Never rewrite another live slot: that would hide a genuine conflict.
            for other_slot, other in self._records.items():
                if other_slot == slot or other_slot in self._active:
                    continue
                other_identity = dict(other.get("last_known_identity") or {})
                same_eid = bool(eid and other_identity.get("eid") == eid)
                same_iccid = bool(iccid and other_identity.get("iccid") == iccid)
                if not (same_eid or same_iccid):
                    continue
                for key in ("card_id", "eid", "iccid", "imsi", "matched", "spn",
                            "profile_name", "carrier"):
                    other_identity.pop(key, None)
                other["last_known_identity"] = other_identity or None
            record["route"]["last_seen"] = int(self.clock())
            if expected_generation is not None:
                observed["session_generation"] = str(expected_generation)
                record["current_identity"] = observed
                record["legacy_observed"] = False
            else:
                history = ({**historical, **observed}
                           if not card.get("identity_placeholder") else dict(observed))
                card_key = eid or iccid
                if card_key:
                    history["card_id"] = card_key
                record["last_known_identity"] = history or None
                record["legacy_observed"] = True
            self._write()
            return True

    def confirm_card_absent(self, reader_name: str, expected_generation: str) -> bool:
        """Retire current identity only after the server's authoritative evidence join."""
        slot = slot_from_reader_name(reader_name)
        if slot is None:
            return False
        with self._lock:
            record = self._records.get(slot)
            route = (record or {}).get("route") or {}
            if (record is None or not secrets.compare_digest(
                    str(route.get("session_generation") or ""),
                    str(expected_generation or ""))):
                return False
            if slot in self._reservations or slot in self._invalid_reservations:
                return False
            current = dict(record.get("current_identity") or {})
            if current:
                current["card_id"] = current.get("eid") or current.get("iccid") or ""
                record["last_known_identity"] = current
            record["current_identity"] = None
            record["legacy_observed"] = False
            self._write()
            return True

    def snapshot(self) -> list[dict]:
        with self._lock:
            result = []
            for slot, stored in sorted(self._records.items()):
                route = dict(stored.get("route") or {})
                current = dict(stored.get("current_identity") or {})
                historical = dict(stored.get("last_known_identity") or {})
                active = self._active.get(slot)
                identity_current = bool(
                    active and active.get("ready_at") is not None and current
                    and current.get("session_generation") == active.get("session_generation"))
                if active:
                    route.update(last_seen=active.get("last_seen"), ready_at=active.get("ready_at"),
                                 online=True)
                record = {"slot": slot, "route": route,
                          "current_identity": current if identity_current else None,
                          "last_known_identity": historical or None,
                          **route, "online": bool(active),
                          "identity_current": identity_current,
                          "identity_session_generation": (
                              current.get("session_generation") if identity_current else None),
                          "port": BASE_PORT + slot}
                if identity_current:
                    record.update(current)
                elif stored.get("legacy_observed"):
                    # Compatibility display only. It is never accompanied by identity_current.
                    record.update(historical)
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
                historical = dict(record.get("last_known_identity") or {})
                visible_identity = (dict(record.get("current_identity") or {})
                                    if record.get("identity_current") else
                                    historical if not record.get("online") else {})
                visible = {**record, **visible_identity}
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
                    if visible.get(key) and not item.get(key):
                        item[key] = visible[key]
                item.update(remote=True, vpcd_slot=slot,
                            connection_online=bool(record.get("online")),
                            session_generation=record.get("session_generation"),
                            identity_session_generation=record.get("identity_session_generation"),
                            identity_current=record.get("identity_current") is True)
                if item["identity_current"]:
                    for key in ("iccid", "imsi", "matched"):
                        item[key] = visible.get(key)
                # False is authoritative too. A Hub row learned from a previous transport
                # or a running Engine's configuration cannot override this generation fence.
                if quarantined_unknown or (record.get("online") and not item["identity_current"]):
                    item.update(last_known_iccid=str(historical.get("iccid") or ""),
                                last_known_matched=str(historical.get("matched") or ""))
                    item.update(identity_current=False, matched=None, iccid=None, imsi=None)
            output.append(item)
        # Usually pcscd exposes all compiled slots even when offline.  Synthesize a row as a
        # restart-safe fallback when its enumeration is temporarily missing.
        for slot, record in by_slot.items():
            historical = dict(record.get("last_known_identity") or {})
            identity = dict(record.get("current_identity") or {}) or historical
            if slot in seen or not (identity.get("iccid") or identity.get("eid")):
                continue
            output.append({
                "index": None,
                "name": f"Virtual PCD 00 {slot:02X}",
                "present": False,
                "hardware_kind": "reader",
                "remote": True,
                "vpcd_slot": slot,
                "connection_online": bool(record.get("online")),
                **{key: ({**record, **identity}).get(key) for key in (
                    "agent_id", "reader_id", "reader_name", "endpoint_key", "eid",
                    "agent_run_id", "session_generation", "identity_current",
                    "identity_session_generation", "iccid", "imsi", "imei", "matched",
                    "spn", "profile_name", "carrier", "last_seen")
                   if ({**record, **identity}).get(key)},
            })
        return output
