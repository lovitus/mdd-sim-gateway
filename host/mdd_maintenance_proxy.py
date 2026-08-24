#!/usr/bin/env python3
"""Fail-closed rollback proxy used only by the reviewed Control upgrade transaction.

The process is deliberately independent of both old and candidate Control containers. During
maintenance it admits only Agent/VPCD WebSockets and authenticated Engine callback traffic;
after a durable rollback commit it can proxy the full Control surface. Every process restart
starts in deny mode and must re-prove the exact transaction before opening full mode.
"""
from __future__ import annotations

import argparse
import asyncio
import contextlib
import fcntl
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import socket
import ssl
import time
import uuid


MAX_HEADER = 65536
MAX_BODY = 4 * 1024 * 1024
READ_TIMEOUT = 10.0
SUPERVISOR_LEASE_TIMEOUT = 5.0
FULL_DRAIN_TIMEOUT = 15.0
_HEX64 = re.compile(r"^[0-9a-f]{64}$")
_BOOT_ID = re.compile(r"^[0-9a-f]{32}$")
_TXID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.:-]{7,127}$")
_AGENT_WS_PATHS = {
    "/api/vpcd/ws",
    "/api/agent/modem/tunnel",
    "/api/agent/health/ws",
    "/api/agent/modem/ws",
    "/api/agent/modem/media",
}
_ENGINE_EVENT_PATH = "/api/engine/event"
_TERMINATION_PATH = re.compile(
    r"^/api/instances/[^/]+/(?:hangup|cellular-call/hangup|"
    r"cellular-call/[^/]+/release)$")
_SINGLE_REQUEST_HEADERS = {"host", "content-length", "transfer-encoding", "connection", "upgrade"}
_SINGLE_RESPONSE_HEADERS = {"content-length", "transfer-encoding", "connection", "upgrade"}


class ProxyStateError(RuntimeError):
    pass


def host_boot_id(*, required: bool = False) -> str:
    """Return one stable host-boot token; production requires Linux's kernel token."""
    try:
        text = Path("/proc/sys/kernel/random/boot_id").read_text(
            encoding="ascii").strip().lower().replace("-", "")
        if _BOOT_ID.fullmatch(text):
            return text
    except OSError:
        pass
    if required:
        raise ProxyStateError("Linux host boot identity is unavailable")
    # Unit tests also run on macOS.  This fallback is never accepted by run(), but gives all
    # Authorization instances in the same test host boot a deterministic token.
    started = int(time.time() - time.monotonic())
    return hashlib.sha256(f"{socket.gethostname()}:{started}".encode()).hexdigest()[:32]


def _digest(value: object) -> str:
    return hashlib.sha256(json.dumps(
        value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def _image_id(value: object, label: str) -> str:
    text = str(value or "")
    if not text.startswith("sha256:") or not _HEX64.fullmatch(text[7:]):
        raise ProxyStateError(f"invalid {label} image id")
    return text


def _container_id(value: object, label: str) -> str:
    text = str(value or "")
    if not _HEX64.fullmatch(text):
        raise ProxyStateError(f"invalid {label} container id")
    return text


def validate_manifest(value: object) -> dict:
    """Validate the complete rollback authorization record; unknown fields are rejected."""
    if not isinstance(value, dict) or set(value) != {
            "version", "txid", "phase", "owner", "source_control", "rollback_control", "proxy",
            "rollback_upstream", "lines"}:
        raise ProxyStateError("invalid control-upgrade manifest schema")
    if value.get("version") != 1 or value.get("phase") not in {
            "prepared", "entry_fenced", "engines_removed", "candidate_started",
            "rollback_starting", "rollback_committed", "committed", "manual_required"}:
        raise ProxyStateError("invalid control-upgrade phase")
    txid = value.get("txid")
    if not isinstance(txid, str) or not _TXID.fullmatch(txid):
        raise ProxyStateError("invalid control-upgrade transaction")
    owner = value.get("owner")
    if not isinstance(owner, dict) or set(owner) != {"id", "epoch"}:
        raise ProxyStateError("invalid transaction owner")
    if (not isinstance(owner.get("id"), str) or not owner["id"]
            or type(owner.get("epoch")) is not int or owner["epoch"] < 1):
        raise ProxyStateError("invalid transaction owner generation")
    source = value.get("source_control")
    if not isinstance(source, dict) or set(source) != {
            "container_id", "image_id", "started_at", "network_mode"}:
        raise ProxyStateError("invalid source Control schema")
    _container_id(source.get("container_id"), "source Control")
    _image_id(source.get("image_id"), "source Control")
    if (not isinstance(source.get("started_at"), str) or not source["started_at"]
            or source.get("network_mode") not in {"host", "bridge"}):
        raise ProxyStateError("invalid source Control generation")
    rollback_control = value.get("rollback_control")
    if rollback_control is not None:
        if not isinstance(rollback_control, dict) or set(rollback_control) != {
                "container_id", "image_id", "started_at", "pid", "restart_count",
                "network_mode", "create_spec_hash"}:
            raise ProxyStateError("invalid rollback Control schema")
        _container_id(rollback_control.get("container_id"), "rollback Control")
        _image_id(rollback_control.get("image_id"), "rollback Control")
        if (not isinstance(rollback_control.get("started_at"), str)
                or not rollback_control["started_at"]
                or type(rollback_control.get("pid")) is not int
                or rollback_control["pid"] < 1
                or type(rollback_control.get("restart_count")) is not int
                or rollback_control["restart_count"] < 0
                or rollback_control.get("network_mode") not in {"host", "bridge"}
                or not isinstance(rollback_control.get("create_spec_hash"), str)
                or not _HEX64.fullmatch(rollback_control["create_spec_hash"])):
            raise ProxyStateError("invalid rollback Control generation")
    if value.get("phase") == "rollback_committed" and rollback_control is None:
        raise ProxyStateError("rollback Control generation is required")
    proxy = value.get("proxy")
    if not isinstance(proxy, dict) or set(proxy) != {"container_id", "image_id"}:
        raise ProxyStateError("invalid proxy schema")
    _container_id(proxy.get("container_id"), "proxy")
    _image_id(proxy.get("image_id"), "proxy")
    upstream = value.get("rollback_upstream")
    if not isinstance(upstream, dict) or set(upstream) != {
            "tls_host", "tls_port", "plain_host", "plain_port", "engine_peers"}:
        raise ProxyStateError("invalid rollback upstream schema")
    for key in ("tls_host", "plain_host"):
        try:
            ipaddress.ip_address(str(upstream.get(key) or ""))
        except ValueError as exc:
            raise ProxyStateError(f"invalid rollback {key}") from exc
    for key in ("tls_port", "plain_port"):
        if type(upstream.get(key)) is not int or not 1 <= upstream[key] <= 65535:
            raise ProxyStateError(f"invalid rollback {key}")
    peers = upstream.get("engine_peers")
    if not isinstance(peers, list) or len(peers) > 256:
        raise ProxyStateError("invalid Engine peer list")
    try:
        normalized_peers = [str(ipaddress.ip_address(str(item))) for item in peers]
    except ValueError as exc:
        raise ProxyStateError("invalid Engine callback peer") from exc
    if len(normalized_peers) != len(set(normalized_peers)):
        raise ProxyStateError("duplicate Engine callback peer")
    lines = value.get("lines")
    if not isinstance(lines, list) or len(lines) > 256:
        raise ProxyStateError("invalid maintenance line list")
    seen = set()
    for line in lines:
        if not isinstance(line, dict) or set(line) != {
                "instance", "source_container_id", "source_image_id",
                "target_image_digest", "phase"}:
            raise ProxyStateError("invalid maintenance line schema")
        iid = str(line.get("instance") or "")
        if not iid or iid in seen:
            raise ProxyStateError("invalid or duplicate maintenance line")
        seen.add(iid)
        _container_id(line.get("source_container_id"), "line source")
        _image_id(line.get("source_image_id"), "line source")
        _image_id(line.get("target_image_digest"), "line target")
        if line.get("phase") not in {
                "prepared", "source_quiescing", "source_removed", "target_starting",
                "target_started", "verified", "rollback_required", "rollback_starting",
                "rollback_started", "rollback_verified", "aborted", "manual_required"}:
            raise ProxyStateError("invalid maintenance line phase")
    return json.loads(json.dumps(value))


def read_manifest(path: Path) -> dict:
    try:
        with path.open(encoding="utf-8") as handle:
            return validate_manifest(json.load(handle))
    except ProxyStateError:
        raise
    except Exception as exc:
        raise ProxyStateError("unreadable control-upgrade manifest") from exc


@contextlib.contextmanager
def manifest_locked(path: Path):
    """Shared transaction lock used by manifest writer, supervisor and proxy."""
    lock_path = path.with_suffix(path.suffix + ".lock")
    lock_path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with lock_path.open("a+") as handle:
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        yield


def normalized_path(raw_target: str) -> str:
    """Return one canonical origin-form path; ambiguity is rejected instead of decoded."""
    if (not raw_target.startswith("/") or raw_target.startswith("//")
            or "#" in raw_target):
        raise ProxyStateError("invalid request target")
    raw_path = raw_target.split("?", 1)[0]
    if ("%" in raw_path or "\\" in raw_path or "//" in raw_path
            or any(part in {".", "..", ""} for part in raw_path.split("/")[1:])):
        raise ProxyStateError("ambiguous request path")
    if raw_path == "/mdd" or raw_path.startswith("/mdd/mdd/"):
        raise ProxyStateError("invalid context prefix")
    if raw_path.startswith("/mdd/"):
        raw_path = raw_path[4:]
    if not raw_path.startswith("/api/"):
        raise ProxyStateError("path is outside the API")
    return raw_path


def maintenance_allows(method: str, raw_target: str, headers: dict[str, str],
                       peer: str, engine_peers: set[str]) -> bool:
    try:
        path = normalized_path(raw_target)
        peer = str(ipaddress.ip_address(peer))
    except (ProxyStateError, ValueError):
        return False
    method = method.upper()
    connection = {part.strip().lower() for part in
                  headers.get("connection", "").split(",")}
    websocket = headers.get("upgrade", "").casefold() == "websocket" and "upgrade" in connection
    if path in _AGENT_WS_PATHS:
        return method == "GET" and websocket
    if path == _ENGINE_EVENT_PATH:
        return method == "POST" and not websocket and peer in engine_peers
    # Revocation must never make an existing charged call impossible to stop.  These exact
    # termination endpoints still pass through Control's normal session, CSRF and call-owner
    # checks; no answer, commit, dial, SMS or generic mutation is admitted here.
    if _TERMINATION_PATH.fullmatch(path):
        return method == "POST" and not websocket
    return False


def _atomic_json(path: Path, value: dict) -> None:
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    tmp = path.with_name(f"{path.name}.tmp.{os.getpid()}.{uuid.uuid4().hex}")
    payload = json.dumps(value, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp, path)
        dirfd = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(dirfd)
        finally:
            os.close(dirfd)
    finally:
        with contextlib.suppress(FileNotFoundError):
            os.unlink(tmp)


def read_self_facts(path: Path, txid: str,
                    container_id_path: Path | None = None) -> tuple[str, str]:
    """Read facts written after `docker create` and before first process start."""
    try:
        with path.open(encoding="utf-8") as handle:
            value = json.load(handle)
    except Exception as exc:
        raise ProxyStateError("unreadable proxy self facts") from exc
    if not isinstance(value, dict) or set(value) != {
            "version", "txid", "container_id", "image_id"}:
        raise ProxyStateError("invalid proxy self facts schema")
    if value.get("version") != 1 or value.get("txid") != txid:
        raise ProxyStateError("proxy self facts transaction mismatch")
    container_id = _container_id(value.get("container_id"), "self")
    image_id = _image_id(value.get("image_id"), "self")
    if container_id_path is not None:
        try:
            cid = container_id_path.read_text(encoding="ascii").strip().casefold()
        except OSError as exc:
            raise ProxyStateError("unreadable proxy container id file") from exc
        if not _HEX64.fullmatch(cid) or cid != container_id:
            raise ProxyStateError("proxy self facts do not match container id file")
    else:
        # Compatibility for older wrappers. New Docker deployment must use --cidfile and pass
        # it explicitly; image-defined/custom hostnames are valid and cannot be an identity.
        hostname = socket.gethostname().casefold()
        if (not re.fullmatch(r"[0-9a-f]{12,64}", hostname)
                or not container_id.startswith(hostname)):
            raise ProxyStateError("proxy self facts do not match this container")
    return container_id, image_id


class Authorization:
    def __init__(self, manifest_path: Path, mode_path: Path, txid: str,
                 container_id: str, image_id: str, *, host_boot: str | None = None,
                 lease_timeout: float = SUPERVISOR_LEASE_TIMEOUT,
                 entry_facts: dict | None = None):
        if not _TXID.fullmatch(txid):
            raise ProxyStateError("invalid configured transaction")
        self.manifest_path = manifest_path
        self.mode_path = mode_path
        self.txid = txid
        self.container_id = _container_id(container_id, "configured proxy")
        self.image_id = _image_id(image_id, "configured proxy")
        self.process_boot_id = uuid.uuid4().hex
        self.host_boot_id = str(host_boot or host_boot_id())
        if not _BOOT_ID.fullmatch(self.host_boot_id):
            raise ProxyStateError("invalid host boot identity")
        if not 0.25 <= float(lease_timeout) <= 60.0:
            raise ProxyStateError("invalid supervisor lease timeout")
        self.lease_timeout = float(lease_timeout)
        self._lease_generation: tuple[str, int, int] | None = None
        self._lease_deadline = 0.0
        self.entry_facts = entry_facts or {
            "bind": "127.0.0.1", "tls_port": 8443, "plain_port": 8000,
            "admin_bind": "127.0.0.1", "admin_port": 19090,
        }
        if (not isinstance(self.entry_facts, dict) or set(self.entry_facts) != {
                "bind", "tls_port", "plain_port", "admin_bind", "admin_port"}):
            raise ProxyStateError("invalid proxy entry facts")
        try:
            ipaddress.ip_address(str(self.entry_facts["bind"]))
            ipaddress.ip_address(str(self.entry_facts["admin_bind"]))
        except ValueError as exc:
            raise ProxyStateError("invalid proxy entry bind") from exc
        if any(type(self.entry_facts[key]) is not int
               or not 1 <= self.entry_facts[key] <= 65535
               for key in ("tls_port", "plain_port", "admin_port")):
            raise ProxyStateError("invalid proxy entry port")
        self.manifest: dict | None = None
        self.ready_path: Path | None = None
        self._ready_generation: tuple[str, int] | None = None
        self.revoke_event = asyncio.Event()
        # Integrity loss is irreversible for this process boot. A restored/cached JSON file
        # must not resurrect an authorization that this process can no longer authenticate.
        self.authorization_lost = False

    @contextlib.contextmanager
    def _mode_locked(self):
        lock_path = self.mode_path.with_suffix(self.mode_path.suffix + ".lock")
        lock_path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        with lock_path.open("a+") as handle:
            fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
            yield

    def _read_mode(self) -> dict:
        try:
            with self.mode_path.open(encoding="utf-8") as handle:
                value = json.load(handle)
        except Exception as exc:
            raise ProxyStateError("unreadable proxy mode") from exc
        required = {"version", "txid", "container_id", "image_id", "process_boot_id",
                    "host_boot_id", "supervisor_boot_id", "lease_seq",
                    "epoch", "state", "active_full", "forwarding_full",
                    "manifest_digest", "updated_at"}
        if not isinstance(value, dict) or set(value) != required:
            raise ProxyStateError("invalid proxy mode schema")
        if (value.get("version") != 1 or value.get("txid") != self.txid
                or value.get("container_id") != self.container_id
                or value.get("image_id") != self.image_id
                or not isinstance(value.get("process_boot_id"), str)
                or not _BOOT_ID.fullmatch(value["process_boot_id"])
                or value.get("host_boot_id") != self.host_boot_id
                or not isinstance(value.get("supervisor_boot_id"), str)
                or (value["supervisor_boot_id"] and
                    not _BOOT_ID.fullmatch(value["supervisor_boot_id"]))
                or type(value.get("lease_seq")) is not int or value["lease_seq"] < 0
                or type(value.get("epoch")) is not int or value["epoch"] < 1
                or value.get("state") not in {"deny", "full", "revoking", "deny_applied"}
                or type(value.get("active_full")) is not int or value["active_full"] < 0
                or type(value.get("forwarding_full")) is not int
                or not 0 <= value["forwarding_full"] <= value["active_full"]
                or not isinstance(value.get("manifest_digest"), str)
                or (value["manifest_digest"] and
                    not _HEX64.fullmatch(value["manifest_digest"]))
                or type(value.get("updated_at")) is not int or value["updated_at"] < 1):
            raise ProxyStateError("invalid proxy mode")
        leased_state = value["state"] in {"full", "revoking", "deny_applied"}
        if (leased_state != bool(value["supervisor_boot_id"])
                or leased_state != (value["lease_seq"] > 0)):
            raise ProxyStateError("invalid proxy supervisor lease")
        return value

    def _record(self, *, epoch: int, state: str, active_full: int,
                manifest_digest: str = "", forwarding_full: int = 0,
                supervisor_boot_id: str = "", lease_seq: int = 0) -> dict:
        return {
            "version": 1, "txid": self.txid,
            "container_id": self.container_id, "image_id": self.image_id,
            "process_boot_id": self.process_boot_id,
            "host_boot_id": self.host_boot_id,
            "supervisor_boot_id": str(supervisor_boot_id),
            "lease_seq": int(lease_seq), "epoch": int(epoch),
            "state": state, "active_full": int(active_full),
            "forwarding_full": int(forwarding_full),
            "manifest_digest": str(manifest_digest), "updated_at": int(time.time()),
        }

    def initialize_deny(self, ready_path: Path) -> dict:
        """Invalidate every prior process boot before publishing this process as ready."""
        self.ready_path = Path(ready_path)
        with manifest_locked(self.manifest_path), self._mode_locked():
            epoch = 1
            try:
                epoch = self._read_mode()["epoch"] + 1
            except ProxyStateError:
                pass
            record = self._record(epoch=epoch, state="deny", active_full=0)
            _atomic_json(self.mode_path, record)
            self._publish_ready(record)
        self.authorization_lost = False
        self._lease_generation = None
        self._lease_deadline = 0.0
        self.revoke_event.set()
        return record

    def _publish_ready(self, mode: dict) -> None:
        if self.ready_path is None:
            return
        generation = (self.process_boot_id, int(mode["epoch"]))
        if self._ready_generation == generation:
            return
        _atomic_json(self.ready_path, {
            "version": 1, "txid": self.txid,
            "container_id": self.container_id, "image_id": self.image_id,
            "process_boot_id": self.process_boot_id,
            "mode_epoch": int(mode["epoch"]), "entry": self.entry_facts,
            "ready_at": int(time.time()),
        })
        self._ready_generation = generation

    def admit_maintenance_manifest(self, manifest: dict) -> bool:
        """Use exact rollback facts for the narrow whitelist without opening full mode."""
        if (manifest["txid"] != self.txid
                or manifest["proxy"] != {
                    "container_id": self.container_id, "image_id": self.image_id}
                or manifest["phase"] not in {"rollback_starting", "rollback_committed"}):
            return False
        self.manifest = manifest
        return True

    def _full_claim_matches(self, mode: dict, manifest: dict) -> bool:
        return bool(
            mode["state"] == "full"
            and mode["process_boot_id"] == self.process_boot_id
            and mode["host_boot_id"] == self.host_boot_id
            and bool(mode["supervisor_boot_id"])
            and mode["lease_seq"] > 0
            and manifest["txid"] == self.txid
            and manifest["phase"] == "rollback_committed"
            and manifest["proxy"] == {
                "container_id": self.container_id, "image_id": self.image_id}
            and mode["manifest_digest"] == _digest(manifest))

    def _lease_valid(self, mode: dict) -> bool:
        """Trust progress observed by this proxy's monotonic clock, never wall time."""
        if not self._full_claim_shape(mode):
            return False
        generation = (mode["supervisor_boot_id"], int(mode["epoch"]),
                      int(mode["lease_seq"]))
        now = time.monotonic()
        if self._lease_generation != generation:
            previous = self._lease_generation
            # Within one supervisor+epoch generation the sequence may only advance. Replays
            # or rollback of the durable mode file cannot refresh the lease.
            if (previous is not None and previous[:2] == generation[:2]
                    and generation[2] <= previous[2]):
                return False
            self._lease_generation = generation
            self._lease_deadline = now + self.lease_timeout
        return now <= self._lease_deadline

    def _full_claim_shape(self, mode: dict) -> bool:
        return bool(mode["state"] == "full"
                    and mode["process_boot_id"] == self.process_boot_id
                    and mode["host_boot_id"] == self.host_boot_id
                    and bool(mode["supervisor_boot_id"])
                    and mode["lease_seq"] > 0)

    def _full_matches(self, mode: dict, manifest: dict) -> bool:
        return self._full_claim_matches(mode, manifest) and self._lease_valid(mode)

    def _revoke_lease_locked(self, mode: dict) -> None:
        """Revoke an expired supervisor lease without poisoning a future proven epoch."""
        self.revoke_event.set()
        if self._full_claim_shape(mode):
            mode["state"] = ("deny_applied" if mode["active_full"] == 0
                             else "revoking")
            mode["updated_at"] = int(time.time())
            _atomic_json(self.mode_path, mode)
        self._lease_generation = None
        self._lease_deadline = 0.0

    def _latch_full_mismatch_locked(self, mode: dict) -> None:
        """Irreversibly revoke a current-boot full claim after authentication mismatch."""
        self.authorization_lost = True
        self.revoke_event.set()
        if (mode["process_boot_id"] == self.process_boot_id
                and mode["state"] == "full"):
            mode["state"] = ("deny_applied" if mode["active_full"] == 0
                             else "revoking")
            mode["updated_at"] = int(time.time())
            _atomic_json(self.mode_path, mode)

    def begin_full(self) -> int | None:
        """Durably register one full request before it can write to the upstream."""
        if self.authorization_lost:
            return None
        try:
            with manifest_locked(self.manifest_path), self._mode_locked():
                if self.authorization_lost:
                    return None
                manifest = read_manifest(self.manifest_path)
                mode = self._read_mode()
                claim_matches = self._full_claim_matches(mode, manifest)
                matches = claim_matches and self._lease_valid(mode)
                if not matches:
                    if claim_matches:
                        self._revoke_lease_locked(mode)
                    elif (mode["process_boot_id"] == self.process_boot_id
                          and mode["state"] == "full"):
                        self._latch_full_mismatch_locked(mode)
                    return None
                mode["active_full"] += 1
                mode["updated_at"] = int(time.time())
                _atomic_json(self.mode_path, mode)
                self.manifest = manifest
                self.revoke_event.clear()
                return int(mode["epoch"])
        except ProxyStateError:
            self.authorization_lost = True
            self.revoke_event.set()
            return None

    def recheck_full(self, epoch: int) -> bool:
        if self.authorization_lost:
            return False
        try:
            with manifest_locked(self.manifest_path), self._mode_locked():
                manifest = read_manifest(self.manifest_path)
                mode = self._read_mode()
                claim_matches = self._full_claim_matches(mode, manifest)
                matches = claim_matches and self._lease_valid(mode)
                if claim_matches and not matches:
                    self._revoke_lease_locked(mode)
                elif (not matches and mode["process_boot_id"] == self.process_boot_id
                      and mode["state"] == "full"):
                    self._latch_full_mismatch_locked(mode)
                return mode["epoch"] == int(epoch) and matches
        except ProxyStateError:
            self.authorization_lost = True
            self.revoke_event.set()
            return False

    def commit_forward(self, epoch: int) -> bool:
        """Linearization point: requests committed here may drain after revocation starts."""
        if self.authorization_lost:
            return False
        try:
            with manifest_locked(self.manifest_path), self._mode_locked():
                if self.authorization_lost:
                    return False
                manifest = read_manifest(self.manifest_path)
                mode = self._read_mode()
                claim_matches = self._full_claim_matches(mode, manifest)
                matches = claim_matches and self._lease_valid(mode)
                if claim_matches and not matches:
                    self._revoke_lease_locked(mode)
                elif (not matches and mode["process_boot_id"] == self.process_boot_id
                      and mode["state"] == "full"):
                    self._latch_full_mismatch_locked(mode)
                if (mode["epoch"] != int(epoch) or not matches
                        or mode["forwarding_full"] >= mode["active_full"]):
                    return False
                mode["forwarding_full"] += 1
                mode["updated_at"] = int(time.time())
                _atomic_json(self.mode_path, mode)
                return True
        except ProxyStateError:
            self.authorization_lost = True
            self.revoke_event.set()
            return False

    def finish_full(self, epoch: int, forwarding: bool = False) -> None:
        """Release one request; the last request durably acknowledges revocation."""
        try:
            with manifest_locked(self.manifest_path), self._mode_locked():
                mode = self._read_mode()
                if (mode["process_boot_id"] != self.process_boot_id
                        or mode["epoch"] != int(epoch) or mode["active_full"] < 1):
                    raise ProxyStateError("full request ownership changed")
                mode["active_full"] -= 1
                if forwarding:
                    if mode["forwarding_full"] < 1:
                        raise ProxyStateError("forwarding request ownership changed")
                    mode["forwarding_full"] -= 1
                if mode["state"] == "revoking" and mode["active_full"] == 0:
                    mode["state"] = "deny_applied"
                mode["updated_at"] = int(time.time())
                _atomic_json(self.mode_path, mode)
        except Exception:
            self.authorization_lost = True
            self.revoke_event.set()
            raise

    def observe_mode(self) -> dict | None:
        try:
            with manifest_locked(self.manifest_path), self._mode_locked():
                mode = self._read_mode()
                if (mode["process_boot_id"] == self.process_boot_id
                        and mode["state"] == "revoking"
                        and mode["active_full"] == 0):
                    mode["state"] = "deny_applied"
                    mode["updated_at"] = int(time.time())
                    _atomic_json(self.mode_path, mode)
                full_valid = False
                if (mode["process_boot_id"] == self.process_boot_id
                        and mode["state"] == "full"):
                    try:
                        manifest = read_manifest(self.manifest_path)
                        claim_matches = self._full_claim_matches(mode, manifest)
                        full_valid = claim_matches and self._lease_valid(mode)
                    except ProxyStateError:
                        claim_matches = False
                        full_valid = False
                    if self.authorization_lost:
                        full_valid = False
                    if not full_valid:
                        # Authorization failure is latched durably for this process boot. Merely
                        # restoring an older manifest must never resurrect full mode without the
                        # host supervisor issuing a new epoch after exact-generation proof.
                        if claim_matches and not self.authorization_lost:
                            self._revoke_lease_locked(mode)
                        else:
                            self._latch_full_mismatch_locked(mode)
            if not full_valid:
                self.revoke_event.set()
            else:
                self.revoke_event.clear()
            self._publish_ready(mode)
            return mode
        except Exception:
            self.authorization_lost = True
            self.revoke_event.set()
            return None


async def _read_headers(reader: asyncio.StreamReader) -> tuple[bytes, str, str, dict[str, str]]:
    try:
        raw = await asyncio.wait_for(reader.readuntil(b"\r\n\r\n"), READ_TIMEOUT)
    except (asyncio.TimeoutError, asyncio.IncompleteReadError, asyncio.LimitOverrunError) as exc:
        raise ProxyStateError("invalid HTTP header") from exc
    if len(raw) > MAX_HEADER:
        raise ProxyStateError("HTTP header too large")
    try:
        lines = raw.decode("iso-8859-1").split("\r\n")
        method, target, version = lines[0].split(" ")
    except ValueError as exc:
        raise ProxyStateError("invalid HTTP request line") from exc
    if version not in {"HTTP/1.0", "HTTP/1.1"} or not method.isalpha():
        raise ProxyStateError("invalid HTTP request")
    headers: dict[str, str] = {}
    for line in lines[1:]:
        if not line:
            continue
        if line[0] in " \t" or ":" not in line:
            raise ProxyStateError("ambiguous HTTP header")
        key, value = line.split(":", 1)
        key = key.strip().lower()
        if key in headers and key in _SINGLE_REQUEST_HEADERS:
            raise ProxyStateError("duplicate framing or routing header")
        value = value.strip()
        headers[key] = f"{headers[key]}, {value}" if key in headers else value
    if "transfer-encoding" in headers:
        raise ProxyStateError("streaming request body is unsupported")
    return raw, method.upper(), target, headers


async def _request_body(reader: asyncio.StreamReader, headers: dict[str, str]) -> bytes:
    raw = headers.get("content-length", "0")
    if not raw.isdigit() or int(raw) > MAX_BODY:
        raise ProxyStateError("invalid request body length")
    size = int(raw)
    return await asyncio.wait_for(reader.readexactly(size), READ_TIMEOUT) if size else b""


async def _pipe(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
    try:
        while True:
            data = await reader.read(65536)
            if not data:
                return
            writer.write(data)
            await writer.drain()
    except (OSError, asyncio.CancelledError):
        return


async def _tunnel(client_reader, client_writer, upstream_reader, upstream_writer,
                  revoke_event: asyncio.Event | None = None):
    tasks = {
        asyncio.create_task(_pipe(client_reader, upstream_writer)),
        asyncio.create_task(_pipe(upstream_reader, client_writer)),
    }
    if revoke_event is not None:
        tasks.add(asyncio.create_task(revoke_event.wait()))
    try:
        await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
    finally:
        for task in tasks:
            if not task.done():
                task.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)


async def _read_response_headers(reader: asyncio.StreamReader) -> tuple[bytes, int, dict[str, str]]:
    try:
        raw = await asyncio.wait_for(reader.readuntil(b"\r\n\r\n"), READ_TIMEOUT)
    except (asyncio.TimeoutError, asyncio.IncompleteReadError, asyncio.LimitOverrunError) as exc:
        raise ProxyStateError("invalid upstream response") from exc
    if len(raw) > MAX_HEADER:
        raise ProxyStateError("upstream header too large")
    lines = raw.decode("iso-8859-1").split("\r\n")
    parts = lines[0].split(" ", 2)
    if len(parts) < 2 or not parts[0].startswith("HTTP/1.") or not parts[1].isdigit():
        raise ProxyStateError("invalid upstream status")
    headers: dict[str, str] = {}
    for line in lines[1:]:
        if not line:
            continue
        if line[0] in " \t" or ":" not in line:
            raise ProxyStateError("ambiguous upstream header")
        key, value = line.split(":", 1)
        key = key.strip().lower()
        if key in headers and key in _SINGLE_RESPONSE_HEADERS:
            raise ProxyStateError("duplicate upstream framing header")
        value = value.strip()
        headers[key] = f"{headers[key]}, {value}" if key in headers else value
    return raw, int(parts[1]), headers


async def _relay_chunked(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
    total = 0
    while True:
        line = await asyncio.wait_for(reader.readline(), READ_TIMEOUT)
        if not line or len(line) > 128 or not line.endswith(b"\r\n"):
            raise ProxyStateError("invalid chunked upstream response")
        try:
            size = int(line.split(b";", 1)[0].strip(), 16)
        except ValueError as exc:
            raise ProxyStateError("invalid upstream chunk size") from exc
        if size < 0 or total + size > 32 * 1024 * 1024:
            raise ProxyStateError("upstream response too large")
        writer.write(line)
        if size:
            payload = await asyncio.wait_for(reader.readexactly(size + 2), READ_TIMEOUT)
            if not payload.endswith(b"\r\n"):
                raise ProxyStateError("invalid upstream chunk terminator")
            writer.write(payload)
            total += size
            await writer.drain()
            continue
        # Forward bounded trailers through the terminating blank line.
        trailer_bytes = 0
        while True:
            trailer = await asyncio.wait_for(reader.readline(), READ_TIMEOUT)
            trailer_bytes += len(trailer)
            if not trailer or trailer_bytes > MAX_HEADER:
                raise ProxyStateError("invalid upstream trailers")
            writer.write(trailer)
            if trailer == b"\r\n":
                await writer.drain()
                return


async def _write_error(writer: asyncio.StreamWriter, status: int, message: str) -> None:
    body = (json.dumps({"detail": message}, separators=(",", ":")) + "\n").encode()
    reason = {400: "Bad Request", 502: "Bad Gateway", 503: "Service Unavailable"}.get(
        status, "Error")
    writer.write(
        f"HTTP/1.1 {status} {reason}\r\nContent-Type: application/json\r\n"
        f"Content-Length: {len(body)}\r\nConnection: close\r\n\r\n".encode() + body)
    with contextlib.suppress(OSError):
        await writer.drain()


async def _forward_exchange(*, method: str, request_headers: dict[str, str], raw: bytes,
                            body: bytes, client_reader: asyncio.StreamReader,
                            client_writer: asyncio.StreamWriter,
                            upstream_reader: asyncio.StreamReader,
                            upstream_writer: asyncio.StreamWriter,
                            revoke_event: asyncio.Event | None) -> None:
    """Forward one request. Its caller owns the absolute full-mode drain deadline."""
    upstream_writer.write(raw + body)
    await upstream_writer.drain()
    response, status, response_headers = await _read_response_headers(upstream_reader)
    client_writer.write(response)
    await client_writer.drain()
    websocket = status == 101 and request_headers.get("upgrade", "").casefold() == "websocket"
    if websocket:
        await _tunnel(client_reader, client_writer, upstream_reader, upstream_writer,
                      revoke_event)
        return
    length = response_headers.get("content-length")
    if method == "HEAD" or status in {204, 304} or 100 <= status < 200:
        return
    if length is not None:
        if not length.isdigit() or int(length) > MAX_BODY:
            raise ProxyStateError("invalid upstream body length")
        payload = await asyncio.wait_for(
            upstream_reader.readexactly(int(length)), READ_TIMEOUT) if int(length) else b""
        client_writer.write(payload)
        await client_writer.drain()
        return
    if "chunked" in response_headers.get("transfer-encoding", "").casefold():
        await _relay_chunked(upstream_reader, client_writer)
        return
    # Bounded close-delimited response. The per-read timeout protects idle connections and the
    # outer full-mode timeout prevents a peer from extending an authorized request forever.
    while True:
        chunk = await asyncio.wait_for(upstream_reader.read(65536), READ_TIMEOUT)
        if not chunk:
            return
        client_writer.write(chunk)
        await client_writer.drain()


async def _forward_until_revoked(exchange, revoke_event: asyncio.Event) -> None:
    """Run indefinitely while authorized; start one absolute deadline only on revoke."""
    exchange_task = asyncio.create_task(exchange)
    revoke_task = asyncio.create_task(revoke_event.wait())
    try:
        done, _pending = await asyncio.wait(
            {exchange_task, revoke_task}, return_when=asyncio.FIRST_COMPLETED)
        if exchange_task in done:
            await exchange_task
            return
        try:
            async with asyncio.timeout(FULL_DRAIN_TIMEOUT):
                await asyncio.shield(exchange_task)
        except TimeoutError:
            exchange_task.cancel()
            await asyncio.gather(exchange_task, return_exceptions=True)
            raise
    finally:
        revoke_task.cancel()
        if not exchange_task.done():
            exchange_task.cancel()
        await asyncio.gather(revoke_task, exchange_task, return_exceptions=True)


class MaintenanceProxy:
    def __init__(self, auth: Authorization, tls_upstream: tuple[str, int],
                 plain_upstream: tuple[str, int]):
        self.auth = auth
        self.tls_upstream = tls_upstream
        self.plain_upstream = plain_upstream

    async def authorization_monitor(self) -> None:
        while True:
            self.auth.observe_mode()
            await asyncio.sleep(0.5)

    async def handle(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter,
                     tls: bool) -> None:
        upstream_writer = None
        full_epoch = None
        full_forwarding = False
        try:
            raw, method, target, headers = await _read_headers(reader)
            body = await _request_body(reader, headers)
            peer_info = writer.get_extra_info("peername") or ("", 0)
            peer = str(peer_info[0])
            try:
                manifest = read_manifest(self.auth.manifest_path)
                if not self.auth.admit_maintenance_manifest(manifest):
                    manifest = None
            except ProxyStateError:
                manifest = None
            engine_peers = set((manifest or {}).get("rollback_upstream", {}).get(
                "engine_peers", []))
            full_epoch = self.auth.begin_full()
            if full_epoch is None and not maintenance_allows(
                    method, target, headers, peer, engine_peers):
                await _write_error(writer, 503, "maintenance in progress")
                return
            upstream_ssl = None
            address = self.tls_upstream if tls else self.plain_upstream
            if tls:
                upstream_ssl = ssl.create_default_context()
                upstream_ssl.check_hostname = False
                upstream_ssl.verify_mode = ssl.CERT_NONE
            upstream_reader, upstream_writer = await asyncio.wait_for(
                asyncio.open_connection(*address, ssl=upstream_ssl), READ_TIMEOUT)
            if full_epoch is not None:
                full_forwarding = self.auth.commit_forward(full_epoch)
                if not full_forwarding:
                    await _write_error(writer, 503, "full proxy authorization was revoked")
                    return
            exchange = _forward_exchange(
                method=method, request_headers=headers, raw=raw, body=body,
                client_reader=reader, client_writer=writer,
                upstream_reader=upstream_reader, upstream_writer=upstream_writer,
                revoke_event=(self.auth.revoke_event if full_epoch is not None else None))
            if full_epoch is not None:
                await _forward_until_revoked(exchange, self.auth.revoke_event)
            else:
                await exchange
        except TimeoutError:
            # A committed full request has one absolute lifetime. Closing both sides is the
            # fail-closed response; writing a second HTTP response after partial bytes is unsafe.
            if full_epoch is None:
                await _write_error(writer, 502, "rollback upstream timed out")
        except ProxyStateError as exc:
            await _write_error(writer, 400, str(exc))
        except Exception:
            await _write_error(writer, 502, "rollback upstream unavailable")
        finally:
            if full_epoch is not None:
                with contextlib.suppress(Exception):
                    self.auth.finish_full(full_epoch, full_forwarding)
            if upstream_writer is not None:
                upstream_writer.close()
                with contextlib.suppress(Exception):
                    await asyncio.wait_for(upstream_writer.wait_closed(), 1.0)
            writer.close()
            with contextlib.suppress(Exception):
                await asyncio.wait_for(writer.wait_closed(), 1.0)


def _server_ssl(cert: str, key: str) -> ssl.SSLContext:
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.load_cert_chain(cert, key)
    return context


async def _admin_health(auth: Authorization, reader: asyncio.StreamReader,
                        writer: asyncio.StreamWriter) -> None:
    try:
        _raw, method, target, _headers = await _read_headers(reader)
        if method != "GET" or target != "/health":
            await _write_error(writer, 400, "unknown admin request")
            return
        mode = auth.observe_mode()
        state_ok = bool(mode and mode.get("process_boot_id") == auth.process_boot_id
                        and int(mode.get("epoch") or 0) > 0)
        eligible = bool(state_ok and not auth.authorization_lost
                        and mode.get("state") in {"deny", "deny_applied"}
                        and int(mode.get("active_full") or 0) == 0
                        and int(mode.get("forwarding_full") or 0) == 0)
        payload = json.dumps({
            "ok": state_ok, "txid": auth.txid,
            "container_id": auth.container_id, "image_id": auth.image_id,
            "process_boot_id": auth.process_boot_id,
            "host_boot_id": auth.host_boot_id,
            "supervisor_boot_id": str((mode or {}).get("supervisor_boot_id") or ""),
            "lease_seq": int((mode or {}).get("lease_seq") or 0),
            "epoch": int((mode or {}).get("epoch") or 0),
            "state": str((mode or {}).get("state") or "deny"),
            "active_full": int((mode or {}).get("active_full") or 0),
            "forwarding_full": int((mode or {}).get("forwarding_full") or 0),
            "authorization_lost": bool(auth.authorization_lost),
            "authorization_eligible": eligible,
        }, sort_keys=True, separators=(",", ":")).encode() + b"\n"
        writer.write(
            b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n"
            + f"Content-Length: {len(payload)}\r\nConnection: close\r\n\r\n".encode()
            + payload)
        await writer.drain()
    except Exception:
        with contextlib.suppress(Exception):
            await _write_error(writer, 503, "proxy state unavailable")
    finally:
        writer.close()
        with contextlib.suppress(Exception):
            await writer.wait_closed()


async def run(args) -> None:
    if args.self_facts:
        container_id, image_id = read_self_facts(
            Path(args.self_facts), args.txid,
            Path(args.container_id_file) if args.container_id_file else None)
    elif args.container_id and args.image_id:
        container_id, image_id = args.container_id, args.image_id
    else:
        raise ProxyStateError("proxy self facts are required")
    auth = Authorization(
        Path(args.manifest), Path(args.mode_state), args.txid,
        container_id, image_id, host_boot=host_boot_id(required=True), entry_facts={
            "bind": args.bind, "tls_port": args.tls_port,
            "plain_port": args.plain_port, "admin_bind": args.admin_bind,
            "admin_port": args.admin_port,
        })
    auth.initialize_deny(Path(args.ready_state))
    proxy = MaintenanceProxy(
        auth, (args.upstream_tls_host, args.upstream_tls_port),
        (args.upstream_plain_host, args.upstream_plain_port))
    # Start listening in maintenance-deny before attempting recovery. The host nft handoff is
    # a later gate and may only point external standard ports here after the admin health probe.
    tls_server = await asyncio.start_server(
        lambda r, w: proxy.handle(r, w, True), args.bind, args.tls_port,
        ssl=_server_ssl(args.cert, args.key), limit=MAX_HEADER + 1)
    plain_server = await asyncio.start_server(
        lambda r, w: proxy.handle(r, w, False), args.bind, args.plain_port,
        limit=MAX_HEADER + 1)
    admin_server = await asyncio.start_server(
        lambda r, w: _admin_health(auth, r, w), args.admin_bind, args.admin_port,
        limit=MAX_HEADER + 1)
    monitor = asyncio.create_task(proxy.authorization_monitor())
    try:
        async with tls_server, plain_server, admin_server:
            await asyncio.gather(tls_server.serve_forever(), plain_server.serve_forever(),
                                 admin_server.serve_forever())
    finally:
        monitor.cancel()
        await asyncio.gather(monitor, return_exceptions=True)


def parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser()
    p.add_argument("--manifest", required=True)
    p.add_argument("--mode-state", required=True)
    p.add_argument("--ready-state", required=True)
    p.add_argument("--txid", required=True)
    p.add_argument("--self-facts")
    p.add_argument("--container-id-file")  # Docker --cidfile, mounted read-only into proxy
    p.add_argument("--container-id")  # test/native-only; Docker deployment uses --self-facts
    p.add_argument("--image-id")
    p.add_argument("--cert", required=True)
    p.add_argument("--key", required=True)
    p.add_argument("--bind", default="0.0.0.0")
    p.add_argument("--tls-port", type=int, default=8443)
    p.add_argument("--plain-port", type=int, default=8000)
    p.add_argument("--admin-bind", default="127.0.0.1")
    p.add_argument("--admin-port", type=int, default=19090)
    p.add_argument("--upstream-tls-host", default="127.0.0.1")
    p.add_argument("--upstream-tls-port", type=int, default=18443)
    p.add_argument("--upstream-plain-host", default="127.0.0.1")
    p.add_argument("--upstream-plain-port", type=int, default=18000)
    return p


def main(argv=None) -> int:
    args = parser().parse_args(argv)
    try:
        asyncio.run(run(args))
        return 0
    except (ProxyStateError, OSError, ssl.SSLError) as exc:
        print(f"maintenance proxy failed closed: {exc}", flush=True)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
