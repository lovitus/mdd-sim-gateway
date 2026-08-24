"""Process-wide transactional JSON state used by independent modem contexts."""

from __future__ import annotations

import json
import os
import tempfile
import threading
from pathlib import Path
from typing import Callable


_LOCKS_GUARD = threading.Lock()
_LOCKS: dict[str, threading.RLock] = {}


class StateCorruptError(RuntimeError):
    """A durable action guard cannot be trusted and must fail closed."""


def _normalized(path: Path) -> str:
    return os.path.normcase(str(path.expanduser().resolve()))


def _lock_for(path: Path) -> threading.RLock:
    key = _normalized(path)
    with _LOCKS_GUARD:
        return _LOCKS.setdefault(key, threading.RLock())


class TransactionalJsonState:
    """Merge updates from all contexts sharing one normalized state path."""

    def __init__(self, path: Path | str | None):
        self.path = Path(path) if path else None
        self.lock = _lock_for(self.path) if self.path else threading.RLock()

    def _read_unlocked(self) -> dict:
        if not self.path:
            return {}
        try:
            value = json.loads(self.path.read_text(encoding="utf-8"))
        except FileNotFoundError:
            return {}
        except (OSError, TypeError, ValueError) as exc:
            raise StateCorruptError(
                f"durable modem action state is unreadable: {self.path}") from exc
        if not isinstance(value, dict):
            raise StateCorruptError(
                f"durable modem action state is not an object: {self.path}")
        return value

    def load(self) -> dict:
        with self.lock:
            return dict(self._read_unlocked())

    def update(self, mutate: Callable[[dict], None]) -> dict:
        if not self.path:
            value = {}
            mutate(value)
            return value
        with self.lock:
            value = self._read_unlocked()
            mutate(value)
            self.path.parent.mkdir(parents=True, exist_ok=True)
            fd, temporary = tempfile.mkstemp(
                prefix=self.path.name + ".", suffix=".tmp", dir=self.path.parent)
            try:
                with os.fdopen(fd, "w", encoding="utf-8") as handle:
                    json.dump(value, handle, sort_keys=True)
                    handle.write("\n")
                    handle.flush()
                    os.fsync(handle.fileno())
                if os.name != "nt":
                    os.chmod(temporary, 0o600)
                os.replace(temporary, self.path)
                if os.name != "nt":
                    directory = os.open(self.path.parent, os.O_RDONLY)
                    try:
                        os.fsync(directory)
                    finally:
                        os.close(directory)
            finally:
                try:
                    os.unlink(temporary)
                except FileNotFoundError:
                    pass
            return dict(value)
