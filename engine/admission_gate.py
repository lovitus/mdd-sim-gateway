#!/usr/bin/env python3
"""Fail-closed, monotonic Engine admission gate.

The host authority atomically renews ``admission-authority.json`` in the per-line run
directory.  This process observes strictly increasing sequence numbers with a monotonic
clock and serves a deliberately tiny Unix-socket protocol to the patched Asterisk module.
File timestamps and wall-clock expiry are never trusted.  Missing, malformed, replayed or
stalled authority therefore denies *new* work while Asterisk remains free to terminate an
existing dialog.
"""

from __future__ import annotations

import argparse
import dataclasses
import fcntl
import grp
import hashlib
import json
import os
import pwd
import re
import selectors
import signal
import socket
import stat
import subprocess
import sys
import threading
import time
import uuid
from pathlib import Path
from typing import Callable, Sequence


PROTOCOL = "mdd-admission-v1"
DEFAULT_RUNDIR = Path("/run/mdd-sim-gateway")
DEFAULT_TTL = 3.0
WATCH_INTERVAL = 0.10
IO_TIMEOUT = 0.20
MAX_FRAME = 512

_HEX32 = re.compile(r"[0-9a-f]{32}\Z")
_HEX64 = re.compile(r"[0-9a-f]{64}\Z")
_IID = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}\Z")
_TXID = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:-]{7,127}\Z")
_STARTED_AT = re.compile(
    r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})\Z")
_IMAGE_ID = re.compile(r"sha256:[0-9a-f]{64}\Z")
_NONCE = re.compile(r"[0-9a-f]{16,64}\Z")
_KINDS = frozenset({"call_in", "call_out", "media_check", "sms_in", "sms_out"})
REGISTRATION_PERMIT_NAME = "usim-registration-permit.json"
REGISTRATION_DISPATCH_NAME = "usim-registration-dispatch.json"
REGISTRATION_DISPATCH_LOCK = ".usim-registration-dispatch.lock"


class AuthorityError(ValueError):
    pass


def _durable_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.{uuid.uuid4().hex}.tmp")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(value, handle, sort_keys=True, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def consume_registration_permit(rundir: Path, permit_nonce: str, *,
                                engine_run_id: str, now: float | None = None,
                                peer_holds_dispatch: bool = False) -> dict:
    """Consume one exact recovery permit after a durable dispatch receipt is written."""
    root = Path(rundir)
    current_time = time.time() if now is None else float(now)
    if (not _HEX32.fullmatch(str(permit_nonce or ""))
            or not isinstance(engine_run_id, str) or not engine_run_id
            or not isinstance(current_time, (int, float))):
        return {"allowed": False, "status": "invalid_request"}
    root.mkdir(parents=True, exist_ok=True)
    lock_path = root / REGISTRATION_DISPATCH_LOCK
    with lock_path.open("a+", encoding="utf-8") as lock:
        os.chmod(lock_path, 0o600)
        if peer_holds_dispatch:
            try:
                fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                pass  # The authenticated Asterisk peer owns dispatch across this exchange.
            else:
                fcntl.flock(lock.fileno(), fcntl.LOCK_UN)
                return {"allowed": False, "status": "dispatch_lock_not_held"}
        else:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
        try:
            receipt_path = root / REGISTRATION_DISPATCH_NAME
            if os.path.lexists(receipt_path):
                return {"allowed": False, "status": "already_consumed"}
            permit_path = root / REGISTRATION_PERMIT_NAME
            try:
                permit = json.loads(permit_path.read_text(encoding="utf-8"))
            except (OSError, ValueError, TypeError):
                return {"allowed": False, "status": "permit_missing_or_invalid"}
            if (not isinstance(permit, dict) or set(permit) != {
                    "version", "phase", "permit_nonce", "campaign_epoch", "engine_run_id",
                    "auth_seq_baseline", "issued_at", "deadline"}
                    or permit.get("version") != 2 or permit.get("phase") != "permit_issued"
                    or permit.get("permit_nonce") != permit_nonce
                    or permit.get("engine_run_id") != engine_run_id
                    or not _HEX64.fullmatch(str(permit.get("campaign_epoch") or ""))
                    or type(permit.get("auth_seq_baseline")) is not int
                    or permit["auth_seq_baseline"] <= 0
                    or not isinstance(permit.get("deadline"), (int, float))
                    or isinstance(permit.get("deadline"), bool)
                    or current_time > float(permit["deadline"])):
                return {"allowed": False, "status": "permit_mismatch"}
            receipt = {**permit, "phase": "submitted_unknown", "dispatch_count": 1,
                       "dispatch_recorded_at": current_time,
                       "result_class": "dispatch_recorded_send_unknown"}
            _durable_json(receipt_path, receipt)
            try:
                permit_path.unlink()
            except FileNotFoundError:
                pass
            directory = os.open(root, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
            return {"allowed": True, "status": "dispatch_recorded", "receipt": receipt}
        finally:
            if not peer_holds_dispatch:
                fcntl.flock(lock.fileno(), fcntl.LOCK_UN)


def _plain_int(value: object, *, minimum: int = 0) -> int:
    if type(value) is not int or value < minimum:  # bool is intentionally rejected
        raise AuthorityError("invalid_integer")
    return value


def _string(value: object, pattern: re.Pattern[str], reason: str) -> str:
    if not isinstance(value, str) or not pattern.fullmatch(value):
        raise AuthorityError(reason)
    return value


def _exact_dict(value: object, keys: set[str], reason: str) -> dict:
    if not isinstance(value, dict) or set(value) != keys:
        raise AuthorityError(reason)
    return value


def _canonical_uuid(value: object, reason: str) -> str:
    if not isinstance(value, str):
        raise AuthorityError(reason)
    try:
        parsed = uuid.UUID(value)
    except (ValueError, AttributeError):
        raise AuthorityError(reason) from None
    if str(parsed) != value:
        raise AuthorityError(reason)
    return value


def _engine_digest(engine: dict) -> str:
    raw = json.dumps(engine, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(raw).hexdigest()


@dataclasses.dataclass(frozen=True)
class Authority:
    mode: str
    iid: str
    issuer_boot_id: str
    authority_epoch: int
    lease_seq: int
    engine_generation_digest: str
    normal_commit_id: str
    normal_state_digest: str
    identity: tuple[object, ...]


def parse_authority(value: object, *, iid: str, engine_run_id: str) -> Authority:
    root = _exact_dict(value, {
        "version", "protocol", "mode", "iid", "issuer_boot_id", "authority_epoch",
        "lease_seq", "engine", "engine_generation_digest", "maintenance", "normal",
    }, "authority_schema")
    if _plain_int(root["version"], minimum=1) != 1 or root["protocol"] != PROTOCOL:
        raise AuthorityError("authority_version")
    mode = root["mode"]
    if not isinstance(mode, str) or mode not in {"maintenance", "normal_committed"}:
        raise AuthorityError("authority_mode")
    actual_iid = _string(root["iid"], _IID, "authority_iid")
    if actual_iid != iid:
        raise AuthorityError("authority_iid_mismatch")
    issuer = _string(root["issuer_boot_id"], _HEX32, "authority_issuer")
    epoch = _plain_int(root["authority_epoch"], minimum=1)
    seq = _plain_int(root["lease_seq"], minimum=1)

    engine = _exact_dict(root["engine"], {
        "container_id", "image_id", "started_at", "restart_count", "run_id",
    }, "engine_schema")
    _string(engine["container_id"], _HEX64, "engine_container")
    _string(engine["image_id"], _IMAGE_ID, "engine_image")
    _string(engine["started_at"], _STARTED_AT, "engine_started_at")
    _plain_int(engine["restart_count"])
    run_id = _canonical_uuid(engine["run_id"], "engine_run_id")
    if run_id != engine_run_id:
        raise AuthorityError("engine_run_id_mismatch")
    digest = _string(root["engine_generation_digest"], _HEX64, "engine_digest")
    if digest != _engine_digest(engine):
        raise AuthorityError("engine_digest_mismatch")

    normal_commit_id = ""
    normal_state_digest = ""
    if mode == "maintenance":
        maintenance = _exact_dict(root["maintenance"], {
            "txid", "manifest_digest", "proxy_process_boot_id",
            "supervisor_boot_id", "proxy_mode_epoch",
        }, "maintenance_schema")
        if root["normal"] is not None:
            raise AuthorityError("normal_unexpected")
        txid = _string(maintenance["txid"], _TXID, "maintenance_txid")
        manifest = _string(maintenance["manifest_digest"], _HEX64,
                           "maintenance_manifest")
        proxy_boot = _string(maintenance["proxy_process_boot_id"], _HEX32,
                             "maintenance_proxy_boot")
        supervisor = _string(maintenance["supervisor_boot_id"], _HEX32,
                             "maintenance_supervisor")
        if supervisor != issuer:
            raise AuthorityError("maintenance_issuer_mismatch")
        proxy_epoch = _plain_int(maintenance["proxy_mode_epoch"], minimum=1)
        mode_identity: tuple[object, ...] = (
            "maintenance", txid, manifest, proxy_boot, supervisor, proxy_epoch,
        )
    else:
        normal = _exact_dict(root["normal"], {"commit_id", "state_digest"},
                             "normal_schema")
        if root["maintenance"] is not None:
            raise AuthorityError("maintenance_unexpected")
        commit_id = _string(normal["commit_id"], _HEX32, "normal_commit")
        state_digest = _string(normal["state_digest"], _HEX64, "normal_state")
        normal_commit_id = commit_id
        normal_state_digest = state_digest
        mode_identity = ("normal_committed", commit_id, state_digest)

    return Authority(mode, actual_iid, issuer, epoch, seq, digest,
                     normal_commit_id, normal_state_digest,
                     (actual_iid, issuer, epoch, digest, *mode_identity))


@dataclasses.dataclass(frozen=True)
class Decision:
    allowed: bool
    reason: str
    gate_boot_id: str
    authority_epoch: int = 0
    lease_seq: int = 0


class GateState:
    """Thread-safe local observer; only forward sequence progress refreshes its TTL."""

    def __init__(self, iid: str, engine_run_id: str, *, ttl: float = DEFAULT_TTL,
                 monotonic: Callable[[], float] = time.monotonic) -> None:
        if not _IID.fullmatch(iid):
            raise ValueError("invalid local Engine identity")
        try:
            _canonical_uuid(engine_run_id, "engine_run_id")
        except AuthorityError as exc:
            raise ValueError("invalid local Engine identity") from exc
        if not (0.25 <= ttl < 5.0):
            raise ValueError("gate TTL must be positive and shorter than the proxy lease")
        self.iid = iid
        self.engine_run_id = engine_run_id
        self.ttl = float(ttl)
        self.monotonic = monotonic
        self.gate_boot_id = uuid.uuid4().hex
        self._lock = threading.Lock()
        self._identity: tuple[object, ...] | None = None
        self._last_seq = 0
        self._progress_steps = 0
        self._last_progress = 0.0
        self._authority: Authority | None = None
        self._reason = "start_deny"

    def deny(self, reason: str) -> None:
        with self._lock:
            self._progress_steps = 0
            self._authority = None
            self._reason = reason

    def observe(self, value: object) -> None:
        try:
            authority = parse_authority(value, iid=self.iid,
                                        engine_run_id=self.engine_run_id)
        except AuthorityError as exc:
            self.deny(str(exc))
            return
        now = self.monotonic()
        with self._lock:
            if authority.identity != self._identity:
                self._identity = authority.identity
                self._last_seq = authority.lease_seq
                self._progress_steps = 1
                self._last_progress = now
                self._authority = authority
                self._reason = "warmup"
                return
            if authority.lease_seq < self._last_seq:
                self._progress_steps = 0
                self._authority = None
                self._reason = "lease_replay"
                return
            if authority.lease_seq == self._last_seq:
                return
            self._last_seq = authority.lease_seq
            self._last_progress = now
            self._progress_steps += 1
            self._authority = authority
            self._reason = "allow" if self._progress_steps >= 2 else "warmup"

    def check(self, kind: str) -> Decision:
        if kind not in _KINDS:
            return Decision(False, "unknown_operation", self.gate_boot_id)
        now = self.monotonic()
        with self._lock:
            authority = self._authority
            if authority is None or self._progress_steps < 2:
                return Decision(False, self._reason, self.gate_boot_id)
            if now - self._last_progress > self.ttl:
                self._progress_steps = 0
                self._authority = None
                self._reason = "lease_expired"
                return Decision(False, "lease_expired", self.gate_boot_id)
            return Decision(True, "allow", self.gate_boot_id,
                            authority.authority_epoch, authority.lease_seq)

    def status(self) -> dict:
        decision = self.check("media_check")
        now_ns = time.time_ns()
        with self._lock:
            authority = self._authority
            identity_digest = ""
            authority_mode = ""
            engine_digest = ""
            normal_commit_id = ""
            normal_state_digest = ""
            if authority is not None:
                identity_digest = hashlib.sha256(json.dumps(
                    authority.identity, sort_keys=True,
                    separators=(",", ":")).encode("utf-8")).hexdigest()
                authority_mode = authority.mode
                engine_digest = authority.engine_generation_digest
                normal_commit_id = authority.normal_commit_id
                normal_state_digest = authority.normal_state_digest
        return {
            "version": 1, "protocol": PROTOCOL, "iid": self.iid,
            "engine_run_id": self.engine_run_id, "gate_boot_id": self.gate_boot_id,
            "updated_at": now_ns // 1_000_000_000, "updated_at_ns": now_ns,
            "state": "allow" if decision.allowed else "deny", "reason": decision.reason,
            "authority_epoch": decision.authority_epoch, "lease_seq": decision.lease_seq,
            "authority_mode": authority_mode,
            "authority_identity_digest": identity_digest,
            "engine_generation_digest": engine_digest,
            "normal_commit_id": normal_commit_id,
            "normal_state_digest": normal_state_digest,
        }


class GateService:
    def __init__(self, state: GateState, authority_path: Path, socket_path: Path,
                 status_path: Path, *, interval: float = WATCH_INTERVAL,
                 fence_paths: Sequence[tuple[Path, str]] = ()) -> None:
        self.state = state
        self.authority_path = authority_path
        self.socket_path = socket_path
        self.status_path = status_path
        self.fence_paths = tuple((Path(path), str(reason)) for path, reason in fence_paths)
        self.interval = interval
        self.stop_event = threading.Event()
        self.failed_event = threading.Event()
        self.ready_event = threading.Event()
        self._threads: list[threading.Thread] = []
        self._listener: socket.socket | None = None

    def _read_authority(self) -> object:
        flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
        fd = os.open(self.authority_path, flags)
        try:
            metadata = os.fstat(fd)
            if (not stat.S_ISREG(metadata.st_mode) or metadata.st_uid not in {0, os.geteuid()}
                    or metadata.st_mode & 0o022 or metadata.st_size > 8192):
                raise AuthorityError("authority_file_unsafe")
            raw = os.read(fd, 8193)
            if len(raw) > 8192:
                raise AuthorityError("authority_too_large")
        finally:
            os.close(fd)
        return json.loads(raw)

    def _publish_status(self) -> None:
        self._local_fence_reason()
        value = json.dumps(self.state.status(), sort_keys=True,
                           separators=(",", ":")) + "\n"
        tmp = self.status_path.with_name(f".{self.status_path.name}.{os.getpid()}.tmp")
        try:
            self.status_path.parent.mkdir(parents=True, exist_ok=True)
            tmp.write_text(value, encoding="utf-8")
            os.chmod(tmp, 0o644)
            os.replace(tmp, self.status_path)
        except OSError:
            try:
                tmp.unlink()
            except OSError:
                pass

    def _watch(self) -> None:
        try:
            while not self.stop_event.is_set():
                try:
                    value = self._read_authority()
                except FileNotFoundError:
                    self.state.deny("authority_missing")
                except (OSError, UnicodeDecodeError, json.JSONDecodeError, AuthorityError):
                    self.state.deny("authority_invalid")
                else:
                    self.state.observe(value)
                self._publish_status()
                self.stop_event.wait(self.interval)
        except BaseException:
            self.state.deny("watcher_failed")
            self.failed_event.set()
        finally:
            self._publish_status()

    def _sync_authority_for_request(self) -> str:
        try:
            value = self._read_authority()
        except FileNotFoundError:
            self.state.deny("authority_missing")
            return "authority_missing"
        except (OSError, UnicodeDecodeError, json.JSONDecodeError, AuthorityError):
            self.state.deny("authority_invalid")
            return "authority_invalid"
        self.state.observe(value)
        return ""

    @staticmethod
    def _peer_allowed(conn: socket.socket) -> bool:
        if hasattr(socket, "SO_PEERCRED"):
            try:
                import struct
                _pid, uid, _gid = struct.unpack("3i", conn.getsockopt(
                    socket.SOL_SOCKET, socket.SO_PEERCRED, struct.calcsize("3i")))
            except (OSError, ValueError):
                return False
        elif hasattr(conn, "getpeereid"):
            try:
                uid, _gid = conn.getpeereid()
            except OSError:
                return False
        elif hasattr(socket, "LOCAL_PEERCRED"):
            # Darwin exposes struct xucred through SOL_LOCAL (numeric level 0) but Python does
            # not export SOL_LOCAL. The first fields are version, effective uid, group count.
            try:
                import struct
                credentials = conn.getsockopt(0, socket.LOCAL_PEERCRED, 128)
                _version, uid, _groups = struct.unpack_from("=IIh", credentials)
            except (OSError, ValueError, struct.error):
                return False
        else:
            return False
        allowed = {0, os.geteuid()}
        try:
            allowed.add(pwd.getpwnam("asterisk").pw_uid)
        except KeyError:
            pass
        return uid in allowed

    def _handle(self, conn: socket.socket) -> None:
        conn.settimeout(IO_TIMEOUT)
        if not self._peer_allowed(conn):
            return
        data = bytearray()
        while len(data) <= MAX_FRAME:
            chunk = conn.recv(MAX_FRAME + 1 - len(data))
            if not chunk:
                return
            data.extend(chunk)
            if b"\n" in data:
                break
        if len(data) > MAX_FRAME or data.count(b"\n") != 1 or not data.endswith(b"\n"):
            return
        try:
            line = data.decode("ascii").rstrip("\n")
            version, nonce, kind = line.split(" ")
        except (UnicodeDecodeError, ValueError):
            return
        registration_nonce = (kind.removeprefix("registration_")
                              if kind.startswith("registration_") else "")
        if (version != "MDD1" or not _NONCE.fullmatch(nonce)
                or (kind not in _KINDS and not _HEX32.fullmatch(registration_nonce))):
            return
        if registration_nonce:
            consumed = consume_registration_permit(
                self.socket_path.parent, registration_nonce,
                engine_run_id=self.state.engine_run_id, peer_holds_dispatch=True)
            if consumed["allowed"]:
                response = f"MDD1 {nonce} ALLOW {self.state.gate_boot_id} 1 1\n"
            else:
                reason = re.sub(r"[^a-z0-9_]", "_", consumed["status"].casefold())[:48]
                response = f"MDD1 {nonce} DENY {reason}\n"
            conn.sendall(response.encode("ascii"))
            return
        local_fence = self._local_fence_reason()
        if local_fence:
            decision = Decision(False, local_fence, self.state.gate_boot_id)
        else:
            authority_error = self._sync_authority_for_request()
            decision = (Decision(False, authority_error, self.state.gate_boot_id)
                        if authority_error else self.state.check(kind))
        if decision.allowed:
            response = (f"MDD1 {nonce} ALLOW {decision.gate_boot_id} "
                        f"{decision.authority_epoch} {decision.lease_seq}\n")
        else:
            reason = re.sub(r"[^a-z0-9_]", "_", decision.reason.casefold())[:48] or "deny"
            response = f"MDD1 {nonce} DENY {reason}\n"
        conn.sendall(response.encode("ascii"))

    def _local_fence_reason(self) -> str:
        for path, reason in self.fence_paths:
            if os.path.lexists(path):
                self.state.deny(reason)
                return reason
        return ""

    def _serve(self) -> None:
        listener: socket.socket | None = None
        try:
            self.socket_path.parent.mkdir(parents=True, exist_ok=True)
            if self.socket_path.exists() or self.socket_path.is_symlink():
                mode = self.socket_path.lstat().st_mode
                if not stat.S_ISSOCK(mode):
                    raise RuntimeError("admission socket path is not a socket")
                self.socket_path.unlink()
            listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self._listener = listener
            listener.bind(str(self.socket_path))
            os.chmod(self.socket_path, 0o660)
            try:
                os.chown(self.socket_path, -1, grp.getgrnam("asterisk").gr_gid)
            except (KeyError, PermissionError):
                pass
            listener.listen(32)
            listener.settimeout(0.20)
            self.ready_event.set()
            while not self.stop_event.is_set():
                try:
                    conn, _ = listener.accept()
                except socket.timeout:
                    continue
                with conn:
                    try:
                        self._handle(conn)
                    except (OSError, TimeoutError):
                        pass
        except BaseException:
            self.state.deny("server_failed")
            self.failed_event.set()
        finally:
            self.ready_event.set()
            if listener is not None:
                listener.close()
            try:
                if self.socket_path.exists() and stat.S_ISSOCK(self.socket_path.lstat().st_mode):
                    self.socket_path.unlink()
            except OSError:
                pass

    def start(self, timeout: float = 2.0) -> None:
        self._threads = [
            threading.Thread(target=self._watch, name="mdd-admission-watch", daemon=True),
            threading.Thread(target=self._serve, name="mdd-admission-socket", daemon=True),
        ]
        for thread in self._threads:
            thread.start()
        if not self.ready_event.wait(timeout) or self.failed_event.is_set():
            self.stop()
            raise RuntimeError("admission gate failed to start")

    def healthy(self) -> bool:
        return (not self.failed_event.is_set()
                and all(thread.is_alive() for thread in self._threads))

    def request_stop(self) -> None:
        self.stop_event.set()
        listener = self._listener
        if listener is not None:
            try:
                listener.close()
            except OSError:
                pass

    def stop(self, *, deadline: float | None = None, timeout: float = 2.0) -> None:
        self.request_stop()
        if deadline is None:
            deadline = time.monotonic() + timeout
        for thread in self._threads:
            thread.join(timeout=max(0.0, deadline - time.monotonic()))


def probe(socket_path: Path, kind: str, timeout: float = IO_TIMEOUT) -> dict:
    nonce = os.urandom(16).hex()
    request = f"MDD1 {nonce} {kind}\n".encode("ascii")
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(timeout)
        client.connect(str(socket_path))
        client.sendall(request)
        data = bytearray()
        while len(data) <= MAX_FRAME:
            chunk = client.recv(MAX_FRAME + 1 - len(data))
            if not chunk:
                break
            data.extend(chunk)
    if len(data) > MAX_FRAME or data.count(b"\n") != 1 or not data.endswith(b"\n"):
        raise RuntimeError("invalid gate response framing")
    parts = data.decode("ascii").rstrip("\n").split(" ")
    if len(parts) < 4 or parts[0:2] != ["MDD1", nonce]:
        raise RuntimeError("invalid gate response identity")
    if parts[2] == "ALLOW" and len(parts) == 6 and _HEX32.fullmatch(parts[3]):
        epoch = int(parts[4])
        seq = int(parts[5])
        if epoch < 1 or seq < 1:
            raise RuntimeError("invalid gate response generation")
        return {"allowed": True, "gate_boot_id": parts[3],
                "authority_epoch": epoch, "lease_seq": seq}
    if parts[2] == "DENY" and len(parts) == 4:
        return {"allowed": False, "reason": parts[3]}
    raise RuntimeError("invalid gate response")


def _signal_group(pgid: int, signum: int) -> None:
    """Signal a saved process group even after its original leader has exited."""
    try:
        os.killpg(pgid, signum)
    except ProcessLookupError:
        pass


def _group_alive(pgid: int) -> bool:
    try:
        os.killpg(pgid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


def _reap_orphans() -> None:
    """Reap adopted descendants only when this supervisor is the container init.

    The direct runtime child is always reaped through ``Popen``. Calling waitpid(-1) from a
    library invocation could steal an unrelated child owned by the embedding process; PID 1 is
    the only process that adopts the runtime's orphaned helpers in production.
    """
    if os.getpid() != 1:
        return
    while True:
        try:
            pid, _status = os.waitpid(-1, os.WNOHANG)
        except ChildProcessError:
            return
        if pid <= 0:
            return


def _poll_runtime(process: subprocess.Popen | None) -> int | None:
    if process is None:
        return None
    result = process.poll()
    if result is not None:
        _reap_orphans()
    return result


def _wait_group_until(pgid: int | None, process: subprocess.Popen | None,
                      deadline: float, interval: float) -> int | None:
    result = _poll_runtime(process)
    while pgid is not None and _group_alive(pgid) and time.monotonic() < deadline:
        current = _poll_runtime(process)
        if result is None and current is not None:
            result = current
        remaining = deadline - time.monotonic()
        if remaining > 0:
            time.sleep(min(interval, remaining))
    current = _poll_runtime(process)
    if result is None:
        result = current
    return result


def supervise(service: GateService, command: Sequence[str], *, stop_timeout: float = 8.0) -> int:
    if not command:
        raise ValueError("missing supervised command")
    if stop_timeout <= 0:
        raise ValueError("stop timeout must be positive")
    process: subprocess.Popen | None = None
    pgid: int | None = None
    started = False
    # First stop intent wins. Its timestamp is the one absolute deadline origin; duplicate
    # TERM/INT cannot extend shutdown or replay a signal into the child group.
    stop_intent: list[tuple[int, float] | None] = [None]

    def on_signal(signum, _frame) -> None:
        if stop_intent[0] is None:
            stop_intent[0] = (signum, time.monotonic())

    previous = {sig: signal.signal(sig, on_signal)
                for sig in (signal.SIGTERM, signal.SIGINT)}
    gate_failed = False
    natural_rc: int | None = None
    shutdown_deadline: float | None = None
    cleanup_complete = False
    try:
        # Handlers are installed before either the gate threads or runtime child exists.
        if stop_intent[0] is None:
            service.start()
            started = True
        if stop_intent[0] is None:
            process = subprocess.Popen(list(command), start_new_session=True)
            pgid = process.pid
        while process is not None and _poll_runtime(process) is None and stop_intent[0] is None:
            if not service.healthy():
                gate_failed = True
                stop_intent[0] = (signal.SIGTERM, time.monotonic())
                break
            time.sleep(0.05)
        if process is not None:
            natural_rc = _poll_runtime(process)

        # A natural leader exit is also a stop transaction: background initialization helpers
        # may still own the saved PGID and must not outlive the container.
        intent = stop_intent[0]
        if intent is None:
            intent = (signal.SIGTERM, time.monotonic())
        requested_signal, requested_at = intent
        deadline = requested_at + stop_timeout
        shutdown_deadline = deadline
        reserve = min(0.5, max(0.05, stop_timeout * 0.25))
        force_at = max(requested_at, deadline - reserve)

        if started:
            service.request_stop()
        if pgid is not None:
            _signal_group(pgid, requested_signal if stop_intent[0] is not None
                          else signal.SIGTERM)

        waited_rc = _wait_group_until(pgid, process, force_at, 0.02)
        if natural_rc is None:
            natural_rc = waited_rc
        if pgid is not None and _group_alive(pgid):
            _signal_group(pgid, signal.SIGKILL)

        if started:
            service.stop(deadline=deadline)
        waited_rc = _wait_group_until(pgid, process, deadline, 0.01)
        if natural_rc is None:
            natural_rc = waited_rc
        _reap_orphans()
        if process is not None and natural_rc is None:
            natural_rc = _poll_runtime(process)
        if process is not None and natural_rc is None:
            try:
                natural_rc = process.wait(timeout=max(0.0, deadline - time.monotonic()))
            except subprocess.TimeoutExpired:
                natural_rc = None
        cleanup_complete = True
        if gate_failed:
            return 70
        if natural_rc is not None:
            return natural_rc
        return 128 + requested_signal
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)
        if not cleanup_complete:
            # Exception paths keep the original stop deadline when one already exists. Only an
            # exception before any stop intent creates a deadline, so cleanup can never receive
            # a second full budget after a partially completed shutdown.
            if shutdown_deadline is None:
                shutdown_deadline = time.monotonic() + stop_timeout
            if started:
                service.request_stop()
            if pgid is not None and _group_alive(pgid):
                _signal_group(pgid, signal.SIGKILL)
            if started:
                service.stop(deadline=shutdown_deadline)
            _wait_group_until(pgid, process, shutdown_deadline, 0.01)
            _reap_orphans()


def _service_from_args(args) -> GateService:
    state = GateState(args.iid, args.engine_run_id, ttl=args.ttl)
    rundir = Path(args.rundir)
    return GateService(
        state, Path(args.authority), Path(args.socket), Path(args.status),
        fence_paths=(
            (rundir / "engine-maintenance.json", "local_fence_engine_maintenance"),
            (rundir / "pcscf-rebind.json", "local_fence_pcscf_rebind"),
            (rundir / "usim-auth-recovery.fence", "local_fence_usim_auth_recovery"),
            (rundir / "engine-replacement-postflight.fence",
             "local_fence_engine_replacement_postflight"),
            (rundir / "admission-deny", "local_fence_admission_deny"),
        ))


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser()
    parser.add_argument("--rundir", default=str(DEFAULT_RUNDIR))
    parser.add_argument("--iid", default=os.environ.get("MDD_ID", ""))
    parser.add_argument("--engine-run-id", default=os.environ.get("MDD_ENGINE_RUN_ID", ""))
    parser.add_argument("--ttl", type=float, default=DEFAULT_TTL)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("serve")
    supervise_parser = sub.add_parser("supervise")
    supervise_parser.add_argument("argv", nargs=argparse.REMAINDER)
    probe_parser = sub.add_parser("probe")
    probe_parser.add_argument("kind", choices=sorted(_KINDS))
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    rundir = Path(args.rundir)
    args.authority = str(rundir / "admission-authority.json")
    args.socket = str(rundir / "admission-gate.sock")
    args.status = str(rundir / "admission-gate-status.json")
    if args.command == "probe":
        result = probe(Path(args.socket), args.kind)
        print(json.dumps(result, sort_keys=True))
        return 0 if result["allowed"] else 3
    service = _service_from_args(args)
    if args.command == "serve":
        service.start()
        try:
            while service.healthy():
                time.sleep(0.2)
        except KeyboardInterrupt:
            pass
        finally:
            service.stop()
        return 0 if not service.failed_event.is_set() else 70
    command = list(args.argv)
    if command and command[0] == "--":
        command.pop(0)
    return supervise(service, command)


if __name__ == "__main__":
    raise SystemExit(main())
