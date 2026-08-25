"""Pure durable contract for an absent-Engine start quarantine.

This module deliberately uses only the Python standard library.  Control, the
host-side CLI and the admission authority all consume the same path, schema,
digest and filesystem rules; none may maintain a second interpretation.
"""
from __future__ import annotations

from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import threading
import time
from typing import Iterator


QUARANTINE_NAME = "engine-start-quarantine.json"
LOCK_ROOT_NAME = "engine-start-quarantine-locks"
RELEASE_ROOT_NAME = "engine-start-quarantine-releases"
GLOBAL_LIFECYCLE_LOCK_NAME = ".engine-replacement.lock"

_IID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$")
_TXID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.:-]{7,127}$")
_MAX_RECORD_BYTES = 4096


class QuarantineContractError(RuntimeError):
    """A malformed, unsafe or non-durable quarantine state."""


def canonical_iid(value: object) -> str:
    iid = str(value or "")
    if not _IID_RE.fullmatch(iid):
        raise QuarantineContractError("invalid Engine instance id")
    return iid


def active_path(data: str | os.PathLike[str], iid: object) -> Path:
    return Path(data) / "instances" / canonical_iid(iid) / "run" / QUARANTINE_NAME


def stable_lock_path(data: str | os.PathLike[str], iid: object) -> Path:
    return Path(data) / "orchestrator" / LOCK_ROOT_NAME / f"{canonical_iid(iid)}.lock"


def global_lifecycle_lock_path(data: str | os.PathLike[str]) -> Path:
    return Path(data) / "orchestrator" / GLOBAL_LIFECYCLE_LOCK_NAME


def canonical_bytes(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"),
                      ensure_ascii=False).encode("utf-8")


def record_digest(value: object) -> str:
    return hashlib.sha256(canonical_bytes(value)).hexdigest()


def validate_record(value: object, iid: object) -> dict:
    expected_iid = canonical_iid(iid)
    if not isinstance(value, dict) or set(value) != {
            "version", "instance", "owner", "reason", "created_at"}:
        raise QuarantineContractError("invalid Engine start quarantine schema")
    if type(value.get("version")) is not int or value["version"] != 1:
        raise QuarantineContractError("invalid Engine start quarantine version")
    if value.get("instance") != expected_iid:
        raise QuarantineContractError("Engine start quarantine instance mismatch")
    owner = value.get("owner")
    if (not isinstance(owner, dict) or set(owner) != {"type", "txid"}
            or owner.get("type") != "deployment"
            or not isinstance(owner.get("txid"), str)
            or not _TXID_RE.fullmatch(owner["txid"])):
        raise QuarantineContractError("invalid Engine start quarantine owner")
    reason = value.get("reason")
    if (not isinstance(reason, str) or not 1 <= len(reason) <= 240
            or any(ord(char) < 32 or ord(char) == 127 for char in reason)):
        raise QuarantineContractError("invalid Engine start quarantine reason")
    created_at = value.get("created_at")
    if type(created_at) is not int or created_at < 1:
        raise QuarantineContractError("invalid Engine start quarantine timestamp")
    return {
        "version": 1,
        "instance": expected_iid,
        "owner": {"type": "deployment", "txid": owner["txid"]},
        "reason": reason,
        "created_at": created_at,
    }


def _lstat_regular_private(path: Path) -> os.stat_result:
    try:
        value = path.lstat()
    except OSError as exc:
        raise QuarantineContractError("Engine start quarantine is unreadable") from exc
    if (not stat.S_ISREG(value.st_mode) or stat.S_IMODE(value.st_mode) != 0o600
            or value.st_uid != os.geteuid() or value.st_size > _MAX_RECORD_BYTES):
        raise QuarantineContractError("Engine start quarantine is unsafe")
    return value


def read_active(data: str | os.PathLike[str], iid: object) -> tuple[dict, str] | None:
    """Read one strict active record; any present malformed object fails closed."""
    path = active_path(data, iid)
    if not os.path.lexists(path):
        return None
    before = _lstat_regular_private(path)
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        fd = os.open(path, flags)
    except OSError as exc:
        raise QuarantineContractError("Engine start quarantine is unreadable") from exc
    try:
        current = os.fstat(fd)
        if (current.st_dev, current.st_ino) != (before.st_dev, before.st_ino):
            raise QuarantineContractError("Engine start quarantine changed while reading")
        raw = b""
        while len(raw) <= _MAX_RECORD_BYTES:
            chunk = os.read(fd, _MAX_RECORD_BYTES + 1 - len(raw))
            if not chunk:
                break
            raw += chunk
        if len(raw) > _MAX_RECORD_BYTES:
            raise QuarantineContractError("Engine start quarantine is too large")
    finally:
        os.close(fd)
    try:
        decoded = json.loads(raw.decode("utf-8"))
    except Exception as exc:
        raise QuarantineContractError("Engine start quarantine is invalid") from exc
    record = validate_record(decoded, iid)
    return record, record_digest(record)


def is_pending(data: str | os.PathLike[str], iid: object) -> bool:
    """Existence is fail-closed, including a corrupt/symlink/special marker."""
    return os.path.lexists(active_path(data, iid))


def active_iids(data: str | os.PathLike[str]) -> list[str]:
    """Enumerate marker presence while the caller owns the global lifecycle lock.

    The result is intentionally based on presence, not validity: an unsafe marker is still a
    fence. Unsafe instance-root topology makes the whole enumeration untrustworthy and fails
    closed instead of guessing that no quarantines exist.
    """
    root = Path(data) / "instances"
    if not os.path.lexists(root):
        return []
    try:
        value = root.lstat()
        if (not stat.S_ISDIR(value.st_mode) or stat.S_ISLNK(value.st_mode)
                or value.st_uid != os.geteuid()):
            raise QuarantineContractError("Engine instance root is unsafe")
        entries = list(os.scandir(root))
    except QuarantineContractError:
        raise
    except OSError as exc:
        raise QuarantineContractError("Engine start quarantines cannot be enumerated") from exc
    present = []
    for entry in entries:
        if not _IID_RE.fullmatch(entry.name):
            continue
        try:
            if not entry.is_dir(follow_symlinks=False):
                raise QuarantineContractError("Engine instance directory is unsafe")
        except OSError as exc:
            raise QuarantineContractError("Engine instance directory is unsafe") from exc
        if os.path.lexists(active_path(data, entry.name)):
            present.append(entry.name)
    return sorted(present)


def _ensure_private_directory(path: Path) -> None:
    try:
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
        value = path.lstat()
    except OSError as exc:
        raise QuarantineContractError("quarantine directory is unavailable") from exc
    if (not stat.S_ISDIR(value.st_mode) or stat.S_ISLNK(value.st_mode)
            or value.st_uid != os.geteuid()):
        raise QuarantineContractError("quarantine directory is unsafe")
    try:
        os.chmod(path, 0o700)
    except OSError as exc:
        raise QuarantineContractError("quarantine directory permissions failed") from exc


def _fsync_directory(path: Path) -> None:
    try:
        fd = os.open(path, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0)
                     | getattr(os, "O_CLOEXEC", 0))
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    except OSError as exc:
        raise QuarantineContractError("quarantine directory fsync failed") from exc


def _flock(handle, operation: int, *, blocking: bool,
           timeout_seconds: float | None, busy_message: str) -> None:
    if blocking and timeout_seconds is None:
        fcntl.flock(handle.fileno(), operation)
        return
    deadline = (time.monotonic() + max(0.0, float(timeout_seconds))
                if timeout_seconds is not None else None)
    while True:
        try:
            fcntl.flock(handle.fileno(), operation | fcntl.LOCK_NB)
            return
        except BlockingIOError as exc:
            if deadline is None or time.monotonic() >= deadline:
                raise QuarantineContractError(busy_message) from exc
            time.sleep(min(0.05, max(0.0, deadline - time.monotonic())))


@contextmanager
def locked_lines(data: str | os.PathLike[str], iids: list[object] | tuple[object, ...],
                 *, exclusive: bool, blocking: bool = False,
                 timeout_seconds: float | None = None) -> Iterator[tuple[object, ...]]:
    """Lock stable per-line inodes in canonical order and keep them after instance deletion."""
    canonical = sorted({canonical_iid(iid) for iid in iids})
    if not canonical:
        raise QuarantineContractError("at least one Engine instance lock is required")
    root = Path(data) / "orchestrator" / LOCK_ROOT_NAME
    _ensure_private_directory(root)
    handles = []
    operation = fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH
    try:
        for iid in canonical:
            path = stable_lock_path(data, iid)
            flags = (os.O_RDWR | os.O_CREAT | getattr(os, "O_CLOEXEC", 0)
                     | getattr(os, "O_NOFOLLOW", 0))
            try:
                fd = os.open(path, flags, 0o600)
            except OSError as exc:
                raise QuarantineContractError("Engine start quarantine lock is unsafe") from exc
            handle = os.fdopen(fd, "r+")
            value = os.fstat(handle.fileno())
            if (not stat.S_ISREG(value.st_mode) or stat.S_IMODE(value.st_mode) != 0o600
                    or value.st_uid != os.geteuid()):
                handle.close()
                raise QuarantineContractError("Engine start quarantine lock is unsafe")
            try:
                _flock(handle, operation, blocking=blocking,
                       timeout_seconds=timeout_seconds,
                       busy_message="Engine start quarantine lock is busy")
            except Exception:
                handle.close()
                raise
            handles.append(handle)
        yield tuple(handles)
    finally:
        for handle in reversed(handles):
            try:
                fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
            finally:
                handle.close()


@contextmanager
def global_lifecycle_locked(data: str | os.PathLike[str], *, exclusive: bool,
                            blocking: bool = False,
                            timeout_seconds: float | None = None) -> Iterator[object]:
    root = Path(data) / "orchestrator"
    _ensure_private_directory(root)
    path = global_lifecycle_lock_path(data)
    flags = (os.O_RDWR | os.O_CREAT | getattr(os, "O_CLOEXEC", 0)
             | getattr(os, "O_NOFOLLOW", 0))
    try:
        fd = os.open(path, flags, 0o600)
    except OSError as exc:
        raise QuarantineContractError("global Engine lifecycle lock is unsafe") from exc
    handle = os.fdopen(fd, "r+")
    operation = fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH
    try:
        value = os.fstat(handle.fileno())
        if not stat.S_ISREG(value.st_mode) or value.st_uid != os.geteuid():
            raise QuarantineContractError("global Engine lifecycle lock is unsafe")
        _flock(handle, operation, blocking=blocking, timeout_seconds=timeout_seconds,
               busy_message="global Engine lifecycle lock is busy")
        yield handle
    finally:
        try:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
        finally:
            handle.close()


def write_active(data: str | os.PathLike[str], record: object) -> tuple[dict, str]:
    checked = validate_record(record, (record or {}).get("instance")
                              if isinstance(record, dict) else "")
    path = active_path(data, checked["instance"])
    _ensure_private_directory(path.parent)
    if os.path.lexists(path):
        raise QuarantineContractError("Engine start quarantine already exists")
    payload = canonical_bytes(checked) + b"\n"
    tmp = path.with_name(f".{path.name}.{os.getpid()}.{threading.get_ident()}.tmp")
    flags = (os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0)
             | getattr(os, "O_NOFOLLOW", 0))
    try:
        fd = os.open(tmp, flags, 0o600)
        with os.fdopen(fd, "wb") as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp, path)
        _fsync_directory(path.parent)
    except Exception:
        try:
            tmp.unlink()
        except FileNotFoundError:
            pass
        raise
    reread = read_active(data, checked["instance"])
    if reread is None or reread[0] != checked:
        raise QuarantineContractError("Engine start quarantine readback mismatch")
    return reread


def release_to_tombstone(data: str | os.PathLike[str], iid: object, *,
                         owner_type: str, owner_txid: str,
                         acquisition_digest: str) -> Path:
    canonical = canonical_iid(iid)
    current = read_active(data, canonical)
    if current is None:
        raise QuarantineContractError("Engine start quarantine is absent")
    record, digest = current
    if (owner_type != record["owner"]["type"]
            or owner_txid != record["owner"]["txid"]
            or acquisition_digest != digest
            or not re.fullmatch(r"[0-9a-f]{64}", acquisition_digest or "")):
        raise QuarantineContractError("Engine start quarantine ownership mismatch")
    source = active_path(data, canonical)
    release_root = Path(data) / "orchestrator" / RELEASE_ROOT_NAME / canonical
    _ensure_private_directory(release_root)
    target = release_root / f"{owner_txid}.{digest}.json"
    if os.path.lexists(target):
        raise QuarantineContractError("Engine start quarantine release already recorded")
    try:
        if source.parent.stat().st_dev != release_root.stat().st_dev:
            raise QuarantineContractError("quarantine release crosses filesystems")
        os.rename(source, target)
        _fsync_directory(source.parent)
        _fsync_directory(release_root)
    except QuarantineContractError:
        raise
    except OSError as exc:
        raise QuarantineContractError("Engine start quarantine release failed") from exc
    return target
