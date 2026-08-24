"""Host-scoped Agent health sessions, independent from SIM and reader transports."""

from __future__ import annotations

import asyncio
import json
import logging
import math
import os
import time
import uuid
from dataclasses import dataclass, field

from . import config as cfg


FRESH_SECONDS = 25.0
OFFLINE_SECONDS = 40.0
log = logging.getLogger("vowifi.agent_health")


class AgentHealthError(RuntimeError):
    pass


def _text(value, *, limit: int, field: str) -> str:
    if not isinstance(value, str):
        raise ValueError(f"{field} must be a string")
    text = value.strip()
    if len(text) > limit:
        raise ValueError(f"{field} is too long")
    return text


def _enum(value, allowed: set[str], *, field: str) -> str:
    text = _text(value, limit=60, field=field)
    if text not in allowed:
        raise ValueError(f"invalid {field}")
    return text


def _bounded_int(value, *, field: str, low: int = 0, high: int = 1_000_000) -> int:
    if type(value) is not int:
        raise ValueError(f"{field} must be an integer")
    if not low <= value <= high:
        raise ValueError(f"{field} is outside the supported range")
    return value


def validate_meta(value) -> dict:
    if not isinstance(value, dict):
        raise ValueError("Agent health meta must be an object")
    allowed = {"platform", "arch", "agent_version", "manager", "collector", "support"}
    if set(value) - allowed:
        raise ValueError("Agent health meta contains unsupported fields")
    platform = _enum(value.get("platform"), {"windows", "macos", "linux"}, field="platform")
    support = _enum(value.get("support"), {"supported", "unsupported"}, field="support")
    normalized = {
        "platform": platform,
        "arch": _text(value.get("arch"), limit=40, field="arch"),
        "agent_version": _text(value.get("agent_version"), limit=40, field="agent_version"),
        "manager": _enum(value.get("manager"), {"scm", "user-process"}, field="manager"),
        "collector": _enum(value.get("collector"), {"native-v1", "unsupported"},
                           field="collector"),
        "support": support,
    }
    if support == "supported" and normalized["collector"] != "native-v1":
        raise ValueError("supported Agent health requires the native-v1 collector")
    if support == "unsupported" and normalized["collector"] != "unsupported":
        raise ValueError("unsupported Agent health requires the unsupported collector")
    if platform == "linux" and support != "unsupported":
        raise ValueError("Linux Agent health collection is not implemented in protocol v1")
    if platform in {"windows", "macos"} and support != "supported":
        raise ValueError("Windows and macOS require native Agent health collection")
    if ((platform == "windows" and normalized["manager"] != "scm") or
            (platform != "windows" and normalized["manager"] != "user-process")):
        raise ValueError("Agent health manager is inconsistent with the platform")
    return normalized


def validate_snapshot(value) -> dict:
    if not isinstance(value, dict):
        raise ValueError("Agent health snapshot must be an object")
    allowed = {"support", "overall", "runtime", "manager", "config", "isolation",
               "inventory", "resources", "started_at"}
    if set(value) - allowed:
        raise ValueError("Agent health snapshot contains unsupported fields")
    support = _enum(value.get("support"), {"supported", "unsupported"}, field="support")
    overall = _enum(
        value.get("overall"),
        {"healthy", "starting", "degraded", "failed", "unsupported", "stopping", "stopped"},
        field="overall")
    runtime = value.get("runtime") or {}
    manager = value.get("manager") or {}
    config = value.get("config") or {}
    isolation = value.get("isolation") or {}
    inventory = value.get("inventory") or {}
    resources = value.get("resources") or {}
    storage = resources.get("storage") or {}
    for name, item in (("runtime", runtime), ("manager", manager), ("config", config),
                       ("isolation", isolation), ("inventory", inventory),
                       ("resources", resources), ("storage", storage)):
        if not isinstance(item, dict):
            raise ValueError(f"{name} must be an object")
    if set(runtime) - {"state", "last_error_code"}:
        raise ValueError("runtime contains unsupported fields")
    if set(manager) - {"kind", "host_mode", "autostart", "session_scope"}:
        raise ValueError("manager contains unsupported fields")
    if set(config) - {"state", "token_configured"}:
        raise ValueError("config contains unsupported fields")
    if set(isolation) - {"state", "backend", "reason_code"}:
        raise ValueError("isolation contains unsupported fields")
    if set(inventory) - {"modems_total", "modems_connected"}:
        raise ValueError("inventory contains unsupported fields")
    if set(resources) - {"storage"} or set(storage) - {"state", "used_percent", "free_mb"}:
        raise ValueError("resources contains unsupported fields")
    token_configured = config.get("token_configured")
    autostart = manager.get("autostart")
    if not isinstance(token_configured, bool) or not isinstance(autostart, bool):
        raise ValueError("Agent health boolean fields must be boolean")
    started_at = value.get("started_at")
    if started_at is not None and (isinstance(started_at, bool) or
                                   not isinstance(started_at, (int, float)) or
                                   not math.isfinite(started_at) or started_at < 0):
        raise ValueError("started_at must be a number or null")
    normalized_storage = {
        "state": _enum(storage.get("state"), {"ok", "warning", "critical", "unknown"},
                       field="storage.state")}
    if "used_percent" in storage:
        normalized_storage["used_percent"] = _bounded_int(
            storage["used_percent"], field="storage.used_percent", high=100)
    if "free_mb" in storage:
        normalized_storage["free_mb"] = _bounded_int(
            storage["free_mb"], field="storage.free_mb", high=100_000_000)
    normalized = {
        "support": support,
        "overall": overall,
        "runtime": {
            "state": _enum(runtime.get("state"),
                           {"starting", "ready", "online", "stopping", "stopped", "failed",
                            "cleanup_blocked"}, field="runtime.state"),
            "last_error_code": _text(runtime.get("last_error_code", ""), limit=80,
                                     field="runtime.last_error_code"),
        },
        "manager": {
            "kind": _enum(manager.get("kind"), {"scm", "gui", "cli"}, field="manager.kind"),
            "host_mode": _enum(manager.get("host_mode"), {"service", "gui", "cli"},
                               field="manager.host_mode"),
            "autostart": autostart,
            "session_scope": _enum(manager.get("session_scope"), {"machine", "user"},
                                   field="manager.session_scope"),
        },
        "config": {
            "state": _enum(config.get("state"), {"ok", "error"}, field="config.state"),
            "token_configured": token_configured,
        },
        "isolation": {
            "state": _enum(isolation.get("state"), {"ok", "error", "unsupported"},
                           field="isolation.state"),
            "backend": _text(isolation.get("backend", ""), limit=60,
                             field="isolation.backend"),
            "reason_code": _text(isolation.get("reason_code", ""), limit=80,
                                 field="isolation.reason_code"),
        },
        "inventory": {
            "modems_total": _bounded_int(inventory.get("modems_total", 0),
                                         field="inventory.modems_total", high=256),
            "modems_connected": _bounded_int(inventory.get("modems_connected", 0),
                                             field="inventory.modems_connected", high=256),
        },
        "resources": {"storage": normalized_storage},
        "started_at": float(started_at) if started_at is not None else None,
    }
    if normalized["inventory"]["modems_connected"] > normalized["inventory"]["modems_total"]:
        raise ValueError("connected modem count exceeds total modem count")
    if support == "unsupported":
        if overall != "unsupported" or normalized["isolation"]["state"] != "unsupported":
            raise ValueError("unsupported collection has inconsistent health state")
    elif overall == "unsupported" or normalized["isolation"]["state"] == "unsupported":
        raise ValueError("supported collection has inconsistent health state")
    return normalized


@dataclass
class AgentHealthAttachment:
    agent_id: str
    run_id: str
    session_id: str
    websocket: object
    meta: dict
    snapshot: dict
    revision: int
    sequence: int
    connected_at: float = field(default_factory=time.time)
    seen_at: float = field(default_factory=time.time)
    monotonic_seen_at: float = field(default_factory=time.monotonic)
    transport_closed: bool = False
    emitted_connection: str = "fresh"
    announce_attach: bool = True

    def connection_state(self, now: float | None = None) -> str:
        age = (now if now is not None else time.monotonic()) - self.monotonic_seen_at
        if age <= FRESH_SECONDS:
            return "fresh"
        if age <= OFFLINE_SECONDS:
            return "delayed"
        return "offline"

    def public(self, now: float | None = None) -> dict:
        connection = self.connection_state(now)
        return {
            "agent_id": self.agent_id,
            "run_id": self.run_id,
            "session_id": self.session_id,
            "meta": dict(self.meta),
            "snapshot": dict(self.snapshot),
            "revision": self.revision,
            "connected_at": self.connected_at,
            "seen_at": self.seen_at,
            "connection": connection,
            "online": connection != "offline",
            "reporting": True,
        }


class AgentHealthRegistry:
    def __init__(self):
        self._active: dict[str, AgentHealthAttachment] = {}
        self._known: dict[str, dict] = {}
        self._lock = asyncio.Lock()
        self._load()

    @staticmethod
    def _state_path() -> str:
        return os.path.join(cfg.DATA_DIR, "orchestrator", "agent-health.json")

    def _load(self) -> None:
        try:
            with open(self._state_path(), encoding="utf-8") as handle:
                value = json.load(handle)
        except (OSError, ValueError, TypeError):
            return
        for agent_id, item in (value.get("agents") or {}).items():
            if not isinstance(item, dict):
                continue
            record = dict(item)
            record.update({"agent_id": str(agent_id), "online": False,
                           "connection": "offline", "reporting": True})
            self._known[str(agent_id)] = record

    def _persist(self) -> None:
        path = self._state_path()
        os.makedirs(os.path.dirname(path), exist_ok=True)
        temporary = path + ".tmp"
        agents = {agent_id: dict(item) for agent_id, item in self._known.items()}
        agents.update({agent_id: attachment.public()
                       for agent_id, attachment in self._active.items()})
        for item in agents.values():
            item.pop("session_id", None)
        with open(temporary, "w", encoding="utf-8") as handle:
            json.dump({"version": 1, "updated_at": time.time(), "agents": agents}, handle,
                      indent=2, sort_keys=True)
            handle.write("\n")
        os.replace(temporary, path)

    def _safe_persist(self) -> None:
        try:
            self._persist()
        except OSError as exc:
            # Health history is diagnostic only. A full disk must be reported by the Agent,
            # but it must never tear down health transport or any hardware business path.
            log.warning("could not persist Agent health history: %s", exc)

    async def attach(self, hello: dict, websocket) -> AgentHealthAttachment:
        if not isinstance(hello, dict):
            raise ValueError("Agent health hello must be an object")
        allowed = {"version", "type", "agent_id", "run_id", "session_id", "seq",
                   "revision", "meta", "snapshot"}
        if set(hello) != allowed:
            raise ValueError("Agent health hello has an invalid envelope")
        if (type(hello.get("version")) is not int or hello.get("version") != 1 or
                hello.get("type") != "agent.health.hello"):
            raise ValueError("Agent health protocol v1 hello required")
        if hello.get("session_id") != "":
            raise ValueError("Agent health hello session_id must be empty")
        agent_id = _text(hello.get("agent_id"), limit=128, field="agent_id")
        run_id = _text(hello.get("run_id"), limit=64, field="run_id")
        if not agent_id or not run_id:
            raise ValueError("Agent health hello requires agent_id and run_id")
        revision = _bounded_int(hello.get("revision", 0), field="revision",
                                low=1, high=2_147_483_647)
        sequence = _bounded_int(hello.get("seq", 0), field="seq",
                                low=1, high=2_147_483_647)
        meta = validate_meta(hello.get("meta"))
        snapshot = validate_snapshot(hello.get("snapshot"))
        if snapshot["support"] != meta["support"]:
            raise ValueError("Agent health support metadata is inconsistent")
        manager = snapshot["manager"]
        if ((meta["platform"] == "windows" and
             (manager["kind"] != "scm" or manager["host_mode"] != "service" or
              manager["session_scope"] != "machine")) or
                (meta["platform"] != "windows" and
                 (manager["kind"] not in {"cli", "gui"} or
                  manager["session_scope"] != "user"))):
            raise ValueError("Agent health runtime manager is inconsistent with the platform")
        attachment = AgentHealthAttachment(
            agent_id=agent_id, run_id=run_id, session_id=uuid.uuid4().hex,
            websocket=websocket, meta=meta, snapshot=snapshot, revision=revision,
            sequence=sequence)
        previous = None
        async with self._lock:
            previous = self._active.get(agent_id)
            baseline = previous.public() if previous is not None else self._known.get(agent_id)
            attachment.announce_attach = bool(
                baseline is None or baseline.get("connection") != "fresh" or
                baseline.get("meta") != meta or baseline.get("snapshot") != snapshot)
            self._active[agent_id] = attachment
            self._known[agent_id] = attachment.public()
            if attachment.announce_attach:
                self._safe_persist()
        if previous is not None and previous.websocket is not websocket:
            try:
                await previous.websocket.close(code=4409, reason="newer Agent health session")
            except Exception:
                pass
        return attachment

    async def receive_result(
            self, attachment: AgentHealthAttachment, message: dict) -> tuple[bool, bool]:
        """Apply one message and return ``(accepted, semantic_changed)``.

        A heartbeat that was accepted is intentionally distinct from a stale or fenced
        frame. The endpoint may acknowledge only the former without turning receipts into
        an oracle that keeps an obsolete Agent session alive.
        """
        if self._active.get(attachment.agent_id) is not attachment:
            return False, False
        if not isinstance(message, dict):
            raise ValueError("Agent health message must be an object")
        kind = message.get("type")
        common = {"version", "type", "agent_id", "run_id", "session_id", "seq",
                  "revision"}
        expected = common if kind == "agent.health.heartbeat" else common | {"meta", "snapshot"}
        if set(message) != expected:
            raise ValueError("Agent health message has an invalid envelope")
        if type(message.get("version")) is not int or message.get("version") != 1:
            raise ValueError("Agent health protocol version is unsupported")
        if (_text(message.get("agent_id"), limit=128, field="agent_id") !=
                attachment.agent_id or
                _text(message.get("run_id"), limit=64, field="run_id") !=
                attachment.run_id or
                _text(message.get("session_id"), limit=64, field="session_id") !=
                attachment.session_id):
            return False, False
        sequence = _bounded_int(message.get("seq", 0), field="seq", low=1,
                                high=2_147_483_647)
        if sequence <= attachment.sequence:
            return False, False
        if kind == "agent.health.heartbeat":
            revision = _bounded_int(message.get("revision", 0), field="revision",
                                    low=1, high=2_147_483_647)
            if revision != attachment.revision:
                raise ValueError("heartbeat revision does not match the last full status")
            attachment.sequence = sequence
            attachment.seen_at = time.time()
            attachment.monotonic_seen_at = time.monotonic()
            attachment.transport_closed = False
            return True, False
        if kind not in {"agent.health.status", "agent.health.shutdown"}:
            raise ValueError("unsupported Agent health message")
        if validate_meta(message.get("meta")) != attachment.meta:
            raise ValueError("Agent health metadata changed within one session")
        revision = _bounded_int(message.get("revision", 0), field="revision",
                                low=1, high=2_147_483_647)
        snapshot = validate_snapshot(message.get("snapshot"))
        if snapshot["support"] != attachment.meta["support"]:
            raise ValueError("Agent health support metadata is inconsistent")
        changed = snapshot != attachment.snapshot
        if kind == "agent.health.status":
            if not changed or revision != attachment.revision + 1:
                raise ValueError("Agent health status requires one new semantic revision")
        elif changed:
            if revision != attachment.revision + 1:
                raise ValueError("changed Agent health shutdown requires one new revision")
        elif revision != attachment.revision:
            raise ValueError("unchanged Agent health shutdown must retain its revision")
        attachment.snapshot = snapshot
        attachment.revision = revision
        attachment.sequence = sequence
        attachment.seen_at = time.time()
        attachment.monotonic_seen_at = time.monotonic()
        attachment.transport_closed = False
        if changed:
            self._known[attachment.agent_id] = attachment.public()
            self._safe_persist()
        return True, changed

    async def receive(self, attachment: AgentHealthAttachment, message: dict) -> bool:
        """Compatibility view returning only whether accepted semantics changed."""
        _accepted, changed = await self.receive_result(attachment, message)
        return changed

    async def shutdown(self, attachment: AgentHealthAttachment) -> bool:
        async with self._lock:
            if self._active.get(attachment.agent_id) is not attachment:
                return False
            self._active.pop(attachment.agent_id, None)
            record = attachment.public()
            record.update({"online": False, "connection": "stopped",
                           "seen_at": time.time()})
            self._known[attachment.agent_id] = record
            self._safe_persist()
            return True

    async def transport_closed(self, attachment: AgentHealthAttachment) -> None:
        async with self._lock:
            if self._active.get(attachment.agent_id) is attachment:
                attachment.transport_closed = True

    async def sweep(self) -> list[dict]:
        transitions = []
        close_transports = []
        async with self._lock:
            monotonic_now = time.monotonic()
            persist = False
            for agent_id, attachment in list(self._active.items()):
                connection = attachment.connection_state(monotonic_now)
                if connection != attachment.emitted_connection:
                    attachment.emitted_connection = connection
                    transitions.append(attachment.public(monotonic_now))
                if connection == "offline":
                    self._active.pop(agent_id, None)
                    close_transports.append(attachment.websocket)
                    record = attachment.public(monotonic_now)
                    record.update({"online": False, "connection": "offline"})
                    self._known[agent_id] = record
                    persist = True
            if persist:
                self._safe_persist()
        for websocket in close_transports:
            try:
                # The attachment is already fenced. Closing forces the Agent to create a new
                # hello/session instead of leaving a live socket whose later frames are ignored.
                await websocket.close(code=4410, reason="Agent health heartbeat timed out")
            except Exception:
                pass
        return transitions

    def list(self) -> list[dict]:
        now = time.monotonic()
        values = dict(self._known)
        values.update({agent_id: attachment.public(now)
                       for agent_id, attachment in self._active.items()})
        return [values[key] for key in sorted(values)]


registry = AgentHealthRegistry()
