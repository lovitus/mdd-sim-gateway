"""One installation-scoped hardware host shared by the macOS CLI and GUI."""

from __future__ import annotations

import os
import signal
import stat
import sys
import threading
from pathlib import Path

try:
    from config_store import ConfigStore
    from local_control import ControlServer
    from managed_runtime import ManagedAgentRuntime, configure_file_logging
except ModuleNotFoundError:
    from .config_store import ConfigStore
    from .local_control import ControlServer
    from .managed_runtime import ManagedAgentRuntime, configure_file_logging


class HostConflictError(RuntimeError):
    pass


class InstallationLease:
    """Advisory process lease stored only in the Agent's private state directory."""

    def __init__(self, state_dir: Path):
        self.state_dir = Path(state_dir)
        self.path = self.state_dir / "host.lock"
        self._file = None

    def acquire(self) -> None:
        if os.name == "nt":
            return
        if self._file is not None:
            return
        import fcntl

        self.state_dir.mkdir(parents=True, exist_ok=True)
        for directory in (self.state_dir.parent, self.state_dir):
            metadata = os.lstat(directory)
            if not stat.S_ISDIR(metadata.st_mode):
                raise RuntimeError(f"Agent private path is not a directory: {directory}")
            if metadata.st_uid != os.geteuid():
                raise PermissionError(f"Agent private path is owned by uid {metadata.st_uid}")
            os.chmod(directory, 0o700)
        flags = os.O_RDWR | os.O_CREAT
        flags |= getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
        fd = os.open(self.path, flags, 0o600)
        handle = os.fdopen(fd, "r+", encoding="ascii")
        try:
            metadata = os.fstat(handle.fileno())
            if not stat.S_ISREG(metadata.st_mode):
                raise RuntimeError("Agent host lease is not a regular file")
            if metadata.st_uid != os.geteuid():
                raise PermissionError(f"Agent host lease is owned by uid {metadata.st_uid}")
            os.fchmod(handle.fileno(), 0o600)
            try:
                fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise HostConflictError("another MDD Agent host is already running") from exc
            handle.seek(0)
            handle.truncate()
            handle.write(f"{os.getpid()}\n")
            handle.flush()
            os.fsync(handle.fileno())
            self._file = handle
        except Exception:
            handle.close()
            raise

    def release(self) -> None:
        if self._file is None:
            return
        try:
            import fcntl
            fcntl.flock(self._file.fileno(), fcntl.LOCK_UN)
        finally:
            self._file.close()
            self._file = None


class AgentHost:
    """Own hardware, runtime and local control for either a CLI or GUI process."""

    def __init__(self, store: ConfigStore | None = None, runtime=None, control=None,
                 host_mode: str = "cli", lease=None):
        self.store = store or ConfigStore()
        self.runtime = runtime
        self.control = control
        self.host_mode = host_mode
        # Configuration may use an explicit test/headless root, but hardware ownership is
        # installation-scoped.  A CLI and GUI for the same macOS login must still contend on
        # one fixed lease before either touches PC/SC or raw USB.
        lease_dir = (Path.home() / "Library" / "Application Support" / "MDD Agent" / "state"
                     if sys.platform == "darwin" else self.store.state_dir)
        self.lease = lease or InstallationLease(lease_dir)
        self.stopped = threading.Event()
        self._started = False
        self._cleanup_blocked = False

    def _stop_components(self) -> tuple[bool, bool]:
        """Stop both owners independently; exceptions are unsafe, never success."""
        control_stopped = self.control is None
        runtime_stopped = self.runtime is None
        if self.control is not None:
            try:
                control_stopped = self.control.stop() is not False
            except Exception:
                control_stopped = False
        if self.runtime is not None:
            try:
                runtime_stopped = self.runtime.stop() is not False
            except Exception:
                runtime_stopped = False
        return control_stopped, runtime_stopped

    def start(self) -> None:
        if self._started:
            return
        if self._cleanup_blocked:
            raise RuntimeError(
                "a previous MDD Agent startup did not stop cleanly; lease retained")
        self.lease.acquire()
        try:
            self.store.ensure_dirs()
            if self.runtime is None:
                self.runtime = ManagedAgentRuntime(self.store)
            self.runtime.host_mode = self.host_mode
            if self.control is None:
                self.control = ControlServer(self.runtime, self.store.root)
            configure_file_logging(self.store)
            self.control.start()
            self.runtime.start()
            self._started = True
        except Exception:
            control_stopped, runtime_stopped = self._stop_components()
            if control_stopped and runtime_stopped:
                self._cleanup_blocked = False
                self.lease.release()
            else:
                self._cleanup_blocked = True
            raise

    def release_lease_if_idle(self) -> bool:
        """Release a pre-start GUI lease only when no component may survive."""
        if self._started or self._cleanup_blocked:
            return False
        self.lease.release()
        return True

    def stop(self) -> None:
        if not self._started:
            if self._cleanup_blocked:
                control_stopped, runtime_stopped = self._stop_components()
                if not control_stopped or not runtime_stopped:
                    raise RuntimeError(
                        "MDD Agent cleanup is still blocked; installation lease retained")
                self._cleanup_blocked = False
            self.lease.release()
            self.stopped.set()
            return
        control_stopped, runtime_stopped = self._stop_components()
        if not control_stopped or not runtime_stopped:
            # Keep the installation lease held. A surviving runtime or control handler may
            # still own hardware or request reconnect and must never overlap a new host.
            self._cleanup_blocked = True
            raise RuntimeError("MDD Agent did not stop cleanly; installation lease retained")
        self._started = False
        self._cleanup_blocked = False
        self.lease.release()
        self.stopped.set()

    def run_forever(self, after_start=None) -> None:
        self.start()
        previous = {}

        def request_stop(_signum, _frame):
            self.stopped.set()

        if threading.current_thread() is threading.main_thread():
            for signum in (signal.SIGINT, signal.SIGTERM):
                previous[signum] = signal.signal(signum, request_stop)
        try:
            # Interactive host work (for example a macOS TCC request) must happen only after
            # readers, modems and the local control endpoint are already available. Keep it
            # inside the cleanup boundary so even an unexpected UI failure releases hardware.
            if after_start is not None:
                after_start(self)
            self.stopped.wait()
        finally:
            self.stop()
            for signum, handler in previous.items():
                signal.signal(signum, handler)
