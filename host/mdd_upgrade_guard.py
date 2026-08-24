"""Linux-only fail-closed evidence gates for a live Control/Engine upgrade."""
from __future__ import annotations

from dataclasses import dataclass
import ctypes
import errno
import os
from pathlib import Path
import select
import sqlite3
import struct
import time
import uuid


class UpgradeGuardError(RuntimeError):
    pass


@dataclass(frozen=True)
class FileFact:
    exists: bool
    device: int = 0
    inode: int = 0
    size: int = 0
    mtime_ns: int = 0


def file_fact(path: Path) -> FileFact:
    try:
        st = path.stat(follow_symlinks=False)
        return FileFact(True, st.st_dev, st.st_ino, st.st_size, st.st_mtime_ns)
    except FileNotFoundError:
        return FileFact(False)


class LinuxInotify:
    IN_MODIFY = 0x00000002
    IN_ATTRIB = 0x00000004
    IN_CLOSE_WRITE = 0x00000008
    IN_MOVED_FROM = 0x00000040
    IN_MOVED_TO = 0x00000080
    IN_CREATE = 0x00000100
    IN_DELETE = 0x00000200
    IN_DELETE_SELF = 0x00000400
    IN_MOVE_SELF = 0x00000800
    IN_Q_OVERFLOW = 0x00004000
    MASK = (IN_MODIFY | IN_ATTRIB | IN_CLOSE_WRITE | IN_MOVED_FROM | IN_MOVED_TO |
            IN_CREATE | IN_DELETE | IN_DELETE_SELF | IN_MOVE_SELF | IN_Q_OVERFLOW)
    _EVENT = struct.Struct("iIII")

    def __init__(self):
        if os.name != "posix" or not Path("/proc").exists():
            raise UpgradeGuardError("Linux inotify is required")
        libc = ctypes.CDLL(None, use_errno=True)
        init = libc.inotify_init1
        init.argtypes = [ctypes.c_int]
        init.restype = ctypes.c_int
        self._add = libc.inotify_add_watch
        self._add.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_uint32]
        self._add.restype = ctypes.c_int
        self.fd = init(os.O_NONBLOCK | os.O_CLOEXEC)
        if self.fd < 0:
            err = ctypes.get_errno()
            raise UpgradeGuardError(f"inotify_init1 failed: {os.strerror(err)}")
        self.watches: dict[int, Path] = {}

    def watch(self, path: Path) -> None:
        wd = self._add(self.fd, os.fsencode(path), self.MASK)
        if wd < 0:
            err = ctypes.get_errno()
            self.close()
            raise UpgradeGuardError(f"inotify_add_watch failed: {os.strerror(err)}")
        self.watches[wd] = Path(path)

    def drain(self) -> list[tuple[int, int, str]]:
        events = []
        while True:
            try:
                raw = os.read(self.fd, 1024 * 1024)
            except BlockingIOError:
                return events
            except OSError as exc:
                if exc.errno == errno.EINTR:
                    continue
                raise UpgradeGuardError(f"inotify read failed: {exc}") from exc
            if not raw:
                raise UpgradeGuardError("inotify descriptor closed")
            offset = 0
            while offset < len(raw):
                if len(raw) - offset < self._EVENT.size:
                    raise UpgradeGuardError("truncated inotify event")
                wd, mask, _cookie, length = self._EVENT.unpack_from(raw, offset)
                offset += self._EVENT.size
                if len(raw) - offset < length:
                    raise UpgradeGuardError("truncated inotify name")
                name = raw[offset:offset + length].split(b"\0", 1)[0].decode(
                    errors="replace")
                offset += length
                if mask & self.IN_Q_OVERFLOW:
                    raise UpgradeGuardError("inotify queue overflow")
                if wd not in self.watches:
                    raise UpgradeGuardError("unknown inotify watch descriptor")
                events.append((wd, mask, name))

    def wait(self, timeout: float) -> bool:
        ready, _, _ = select.select([self.fd], [], [], max(0.0, timeout))
        return bool(ready)

    def close(self) -> None:
        if getattr(self, "fd", -1) >= 0:
            os.close(self.fd)
            self.fd = -1


class MessageFileGuard:
    """Continuously rejects append/create/replace/truncate from registration through drain."""
    def __init__(self, paths: list[Path], backend_factory=LinuxInotify):
        if not paths:
            raise UpgradeGuardError("at least one message evidence path is required")
        self.paths = tuple(Path(path) for path in paths)
        self.backend_factory = backend_factory
        self.backend = None
        self.baseline: dict[Path, FileFact] = {}

    def _relevant_events(self, events: list[tuple[int, int, str]]) -> list[tuple[int, int, str]]:
        """Keep direct-file events and parent events naming one watched evidence file."""
        names = {path.name for path in self.paths}
        return [event for event in events if not event[2] or event[2] in names]

    def arm(self) -> None:
        if self.backend is not None:
            raise UpgradeGuardError("message watcher is already armed")
        backend = self.backend_factory()
        try:
            # Registration must precede baseline. Parents observe absent-file creation and
            # rename replacement; existing files add direct modify/truncate/delete evidence.
            for parent in sorted({path.parent for path in self.paths}, key=str):
                backend.watch(parent)
            for path in self.paths:
                if file_fact(path).exists:
                    backend.watch(path)
            backend.drain()
            first = {path: file_fact(path) for path in self.paths}
            registration_events = self._relevant_events(backend.drain())
            second = {path: file_fact(path) for path in self.paths}
            if registration_events or first != second:
                raise UpgradeGuardError("message evidence changed while watcher armed")
            self.backend = backend
            self.baseline = second
        except Exception:
            backend.close()
            raise

    def check(self) -> None:
        if self.backend is None:
            raise UpgradeGuardError("message watcher is not armed")
        events = self._relevant_events(self.backend.drain())
        current = {path: file_fact(path) for path in self.paths}
        if events or current != self.baseline:
            raise UpgradeGuardError("message evidence changed during maintenance")

    def wait_quiet(self, seconds: float, poll_seconds: float = 0.1) -> None:
        if seconds < 0 or not 0 < poll_seconds <= 0.5:
            raise UpgradeGuardError("invalid quiet interval")
        deadline = time.monotonic() + seconds
        while True:
            self.check()
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return
            self.backend.wait(min(poll_seconds, remaining))

    def close(self) -> None:
        if self.backend is not None:
            self.backend.close()
            self.backend = None

    def __enter__(self):
        self.arm()
        return self

    def __exit__(self, _kind, _value, _traceback):
        self.close()


def filesystem_durability_probe(directory: Path, txid: str) -> None:
    """Prove create/fsync/replace/dir-fsync/delete in the actual Engine logs directory."""
    directory = Path(directory)
    if not directory.is_dir() or directory.is_symlink():
        raise UpgradeGuardError("Engine logs directory is unavailable or unsafe")
    token = uuid.uuid4().hex
    first = directory / f".mdd-maintenance-probe.{txid}.{token}.tmp"
    final = directory / f".mdd-maintenance-probe.{txid}.{token}"
    dirfd = os.open(directory, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        fd = os.open(first, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as handle:
            handle.write(token.encode())
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(first, final)
        os.fsync(dirfd)
        if final.read_text(encoding="ascii") != token:
            raise UpgradeGuardError("filesystem durability probe readback mismatch")
        os.unlink(final)
        os.fsync(dirfd)
        if final.exists():
            raise UpgradeGuardError("filesystem durability probe delete failed")
    except Exception:
        for path in (first, final):
            try:
                os.unlink(path)
            except FileNotFoundError:
                pass
        raise
    finally:
        os.close(dirfd)


def sqlite_durability_probe(database: Path, txid: str) -> None:
    """Commit/readback/delete-commit through independent production-database connections."""
    database = Path(database)
    if not database.is_file() or database.is_symlink():
        raise UpgradeGuardError("production message database is unavailable or unsafe")
    try:
        first = sqlite3.connect(database, timeout=5)
        first.execute(
            "CREATE TABLE IF NOT EXISTS maintenance_durability_probes ("
            "txid TEXT PRIMARY KEY, created_at INTEGER NOT NULL)")
        first.execute(
            "INSERT INTO maintenance_durability_probes(txid,created_at) VALUES(?,?)",
            (txid, int(time.time())))
        first.commit()
        first.close()

        second = sqlite3.connect(database, timeout=5)
        row = second.execute(
            "SELECT txid FROM maintenance_durability_probes WHERE txid=?", (txid,)).fetchone()
        if row != (txid,):
            raise UpgradeGuardError("SQLite commit readback failed")
        second.execute("DELETE FROM maintenance_durability_probes WHERE txid=?", (txid,))
        second.commit()
        second.close()

        third = sqlite3.connect(database, timeout=5)
        row = third.execute(
            "SELECT 1 FROM maintenance_durability_probes WHERE txid=?", (txid,)).fetchone()
        third.close()
        if row is not None:
            raise UpgradeGuardError("SQLite delete commit readback failed")
    except UpgradeGuardError:
        raise
    except Exception as exc:
        raise UpgradeGuardError(f"SQLite durability probe failed: {exc}") from exc


def pending_paid_work(database: Path) -> dict[str, int]:
    """Read only the durable paid-work states required by the maintenance gate."""
    try:
        connection = sqlite3.connect(f"file:{Path(database)}?mode=ro", uri=True, timeout=5)
        leases = connection.execute(
            "SELECT COUNT(*) FROM cellular_call_leases "
            "WHERE state NOT IN ('terminal_confirmed','cancelled')").fetchone()[0]
        messages = connection.execute(
            "SELECT COUNT(*) FROM messages WHERE direction='out' AND status='pending'").fetchone()[0]
        sms_states = {
            str(state): int(count) for state, count in connection.execute(
                "SELECT state,COUNT(*) FROM sms_submission_guards GROUP BY state").fetchall()
        }
        if set(sms_states) - {"active", "completed", "orphaned"}:
            raise UpgradeGuardError("paid-work state is unknown: invalid SMS submission state")
        sms_guards = sms_states.get("active", 0)
        allowances = connection.execute(
            "SELECT COUNT(*) FROM allowance_queries WHERE status='pending'").fetchone()[0]
        connection.close()
        return {"open_call_leases": int(leases),
                "pending_messages": int(messages) + int(sms_guards),
                "pending_allowance_queries": int(allowances)}
    except Exception as exc:
        raise UpgradeGuardError(f"paid-work state is unknown: {exc}") from exc
