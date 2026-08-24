"""Local-only control transport used identically by the Agent CLI and GUI."""

from __future__ import annotations

import json
import hashlib
import os
import socket
import stat
import sys
import threading
import time
from pathlib import Path

try:
    from control_contract import (
        ADMIN, METHOD_PERMISSIONS, OPERATE, ProtocolError, decode_request, encode_message,
        request, response,
    )
except ModuleNotFoundError:
    from .control_contract import (
        ADMIN, METHOD_PERMISSIONS, OPERATE, ProtocolError, decode_request, encode_message,
        request, response,
    )


PIPE_NAME = r"\\.\pipe\MddAgent.Control.v1"
ROLE_ORDER = {"read": 0, "operate": 1, "admin": 2}


def _request_secrets(value: dict | None) -> set[str]:
    """Collect only explicitly secret request fields for error/result scrubbing."""
    found = set()

    def visit(item, key: str = ""):
        if isinstance(item, dict):
            for name, member in item.items():
                visit(member, str(name))
        elif ("token" in key.casefold() or "password" in key.casefold() or
              "secret" in key.casefold()):
            text = str(item or "")
            if text:
                found.add(text)

    visit(value or {})
    return found


def _unix_socket_path(root: Path) -> str:
    preferred = str(root / "state" / "control.sock")
    if len(preferred.encode()) < 96:
        return preferred
    identity = hashlib.sha256(str(root.resolve()).encode("utf-8")).hexdigest()[:20]
    if sys.platform == "darwin":
        base = Path.home() / "Library" / "Application Support" / "MDD Agent" / "sockets"
    else:
        runtime = os.environ.get("XDG_RUNTIME_DIR")
        base = (Path(runtime) / "mdd-agent" if runtime else
                Path.home() / ".local" / "state" / "mdd-agent" / "sockets")
    fallback = str(base / f"{identity}.sock")
    if len(fallback.encode()) >= 96:
        raise ValueError(
            "MDD Agent data directory is too long for a secure local control socket")
    return fallback


def _safe_unlink_socket(path: str) -> None:
    try:
        metadata = os.lstat(path)
    except FileNotFoundError:
        return
    if not stat.S_ISSOCK(metadata.st_mode):
        raise RuntimeError(f"refusing to replace non-socket local control path: {path}")
    if hasattr(os, "geteuid") and metadata.st_uid != os.geteuid():
        raise PermissionError(f"local control socket is owned by uid {metadata.st_uid}")
    os.unlink(path)


class ControlServer:
    def __init__(self, runtime, root: Path | str):
        self.runtime = runtime
        self.root = Path(root)
        self._stop = threading.Event()
        self._thread = None
        self._socket = None
        self._active_handles = set()
        self._active_clients = set()
        self._handlers = set()
        self._active_lock = threading.Lock()
        self._pipe_slots = threading.BoundedSemaphore(16)
        self._ready = threading.Event()
        self._start_error = None

    def start(self) -> None:
        if self._thread and self._thread.is_alive():
            return
        self._stop.clear()
        self._ready.clear()
        self._start_error = None
        target = self._run_windows if os.name == "nt" else self._run_unix

        def run():
            try:
                target()
            except BaseException as exc:
                self._start_error = exc
            finally:
                self._ready.set()

        self._thread = threading.Thread(target=run, name="mdd-agent-control", daemon=True)
        self._thread.start()
        if not self._ready.wait(5):
            raise RuntimeError("local control listener did not become ready")
        if self._start_error is not None:
            raise self._start_error

    def stop(self, timeout: float = 5.0) -> bool:
        self._stop.set()
        if self._socket:
            try:
                self._socket.close()
            except OSError:
                pass
        with self._active_lock:
            clients = list(self._active_clients)
        for client in clients:
            try:
                client.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                client.close()
            except OSError:
                pass
        if self._thread:
            self._thread.join(timeout)
        if os.name == "nt":
            try:
                import win32file
                with self._active_lock:
                    handles = list(self._active_handles)
                for handle in handles:
                    try:
                        win32file.CloseHandle(handle)
                    except Exception:
                        pass
            except ImportError:
                pass
        deadline = time.monotonic() + max(0.0, timeout)
        while True:
            with self._active_lock:
                handlers = list(self._handlers)
            if not handlers or time.monotonic() >= deadline:
                break
            for handler in handlers:
                handler.join(max(0.0, min(0.25, deadline - time.monotonic())))
        with self._active_lock:
            handlers_alive = any(handler.is_alive() for handler in self._handlers)
        return not bool(self._thread and self._thread.is_alive()) and not handlers_alive

    def _dispatch(self, payload: bytes, role: str) -> bytes:
        request_id = None
        secrets = set()

        def redact(message):
            store = getattr(self.runtime, "store", None)
            if store is not None and hasattr(store, "redact"):
                return store.redact(message, *secrets)
            text = str(message)
            for secret in secrets:
                text = text.replace(secret, "<redacted>")
            return text

        try:
            value = decode_request(payload)
            request_id = value["id"]
            secrets = _request_secrets(value.get("params") or {})
            needed = METHOD_PERMISSIONS[value["method"]]
            if self._stop.is_set() and needed != "read":
                raise ProtocolError(
                    "shutting_down", "Agent is shutting down; mutating requests are rejected")
            if ROLE_ORDER.get(role, -1) < ROLE_ORDER[needed]:
                raise ProtocolError("permission_denied", f"{needed} permission is required")
            result = self.runtime.execute(value["method"], value.get("params") or {}, role)
            store = getattr(self.runtime, "store", None)
            if store is not None and hasattr(store, "redact"):
                result = store.redact(result, *secrets)
            return encode_message(response(request_id, result=result))
        except ProtocolError as exc:
            message = redact(str(exc))
            return encode_message(response(
                request_id, error=ProtocolError(exc.code, message)))
        except PermissionError as exc:
            message = redact(str(exc) or "permission denied")
            error = ProtocolError("permission_denied", message)
            return encode_message(response(request_id, error=error))
        except Exception as exc:
            message = redact(str(exc) or type(exc).__name__)
            error = ProtocolError("action_failed", message)
            return encode_message(response(request_id, error=error))

    def _run_unix(self) -> None:
        path = _unix_socket_path(self.root)
        parent = Path(path).parent
        parent.mkdir(parents=True, exist_ok=True)
        metadata = os.lstat(parent)
        if not stat.S_ISDIR(metadata.st_mode):
            raise RuntimeError(f"local control parent is not a directory: {parent}")
        if hasattr(os, "geteuid") and metadata.st_uid != os.geteuid():
            raise PermissionError(f"local control parent is owned by uid {metadata.st_uid}")
        os.chmod(parent, 0o700)
        _safe_unlink_socket(path)
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self._socket = server
        server.bind(path)
        os.chmod(path, 0o600)
        socket_inode = os.lstat(path).st_ino
        server.listen(8)
        server.settimeout(0.5)
        self._ready.set()
        while not self._stop.is_set():
            try:
                client, _ = server.accept()
            except socket.timeout:
                continue
            except OSError:
                if self._stop.is_set():
                    break
                raise
            with self._active_lock:
                self._active_clients.add(client)
            handler = threading.Thread(
                target=self._serve_unix_client, args=(client,), daemon=True)
            with self._active_lock:
                self._handlers.add(handler)
            handler.start()
        try:
            if os.lstat(path).st_ino == socket_inode:
                _safe_unlink_socket(path)
        except FileNotFoundError:
            pass

    def _serve_unix_client(self, client: socket.socket) -> None:
        try:
            with client:
                client.settimeout(15)
                header = _recv_exact(client, 4)
                if not header:
                    return
                length = int.from_bytes(header, "big")
                if length > 1024 * 1024:
                    return
                payload = _recv_exact(client, length)
                reply = self._dispatch(payload, "admin")
                client.sendall(len(reply).to_bytes(4, "big") + reply)
        finally:
            with self._active_lock:
                self._active_clients.discard(client)
                self._handlers.discard(threading.current_thread())

    def _windows_role(self, handle) -> str:  # pragma: no cover - exercised on Windows
        import win32api
        import win32con
        import win32security
        role = "read"
        win32security.ImpersonateNamedPipeClient(handle)
        try:
            token = win32security.OpenThreadToken(
                win32api.GetCurrentThread(), win32con.TOKEN_QUERY, True)
            admin_sid = win32security.CreateWellKnownSid(
                win32security.WinBuiltinAdministratorsSid, None)
            if win32security.CheckTokenMembership(token, admin_sid):
                return "admin"
            try:
                operator_sid, _domain, _kind = win32security.LookupAccountName(
                    None, "MDD Agent Operators")
                if win32security.CheckTokenMembership(token, operator_sid):
                    role = "operate"
            except Exception:
                pass
            return role
        finally:
            win32security.RevertToSelf()

    def _run_windows(self) -> None:  # pragma: no cover - exercised on Windows
        import pywintypes
        import win32con
        import win32file
        import win32pipe
        import win32security
        descriptor = win32security.ConvertStringSecurityDescriptorToSecurityDescriptor(
            # AU may connect and exchange messages but is not granted FILE_CREATE_PIPE_INSTANCE
            # (which aliases FILE_APPEND_DATA and is included by GENERIC_WRITE).
            "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x120183;;;AU)",
            win32security.SDDL_REVISION_1)
        attributes = pywintypes.SECURITY_ATTRIBUTES()
        attributes.SECURITY_DESCRIPTOR = descriptor
        flags = (win32pipe.PIPE_TYPE_MESSAGE | win32pipe.PIPE_READMODE_MESSAGE |
                 win32pipe.PIPE_WAIT | getattr(win32pipe, "PIPE_REJECT_REMOTE_CLIENTS", 0x8))
        first_instance = True
        while not self._stop.is_set():
            if not self._pipe_slots.acquire(timeout=0.5):
                continue
            access = win32pipe.PIPE_ACCESS_DUPLEX
            if first_instance:
                access |= getattr(win32file, "FILE_FLAG_FIRST_PIPE_INSTANCE", 0x00080000)
            try:
                handle = win32pipe.CreateNamedPipe(
                    PIPE_NAME, access, flags,
                    16, 1024 * 1024, 1024 * 1024, 5000,
                    attributes)
            except Exception:
                self._pipe_slots.release()
                raise
            if first_instance:
                self._ready.set()
            first_instance = False
            try:
                try:
                    win32pipe.ConnectNamedPipe(handle, None)
                except pywintypes.error as exc:
                    if exc.winerror != 535:  # ERROR_PIPE_CONNECTED
                        raise
                if self._stop.is_set():
                    break
                threading.Thread(target=self._serve_windows_handle,
                                 args=(handle,), daemon=True).start()
                handle = None
            finally:
                if handle is not None:
                    win32file.CloseHandle(handle)
                    self._pipe_slots.release()

    def _serve_windows_handle(self, handle) -> None:  # pragma: no cover - Windows integration
        import win32file
        import win32pipe
        with self._active_lock:
            self._active_handles.add(handle)
        try:
            payload = _read_win_message(handle, win32file, timeout_ms=5000)
            reply = self._dispatch(payload, self._windows_role(handle))
            win32file.WriteFile(handle, reply)
            win32file.FlushFileBuffers(handle)
        finally:
            try:
                win32pipe.DisconnectNamedPipe(handle)
            except Exception:
                pass
            try:
                win32file.CloseHandle(handle)
            except Exception:
                pass
            with self._active_lock:
                self._active_handles.discard(handle)
            self._pipe_slots.release()


class ControlClient:
    def __init__(self, root: Path | str):
        self.root = Path(root)

    def call(self, method: str, params: dict | None = None, deadline_ms: int = 15_000):
        envelope = request(method, params, deadline_ms)
        payload = encode_message(envelope)
        raw = self._call_windows(payload, deadline_ms) if os.name == "nt" else self._call_unix(
            payload, deadline_ms)
        try:
            value = json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ProtocolError("invalid_response", "Agent returned invalid JSON") from exc
        if value.get("version") != 1 or value.get("id") != envelope["id"]:
            raise ProtocolError("invalid_response", "Agent response does not match request")
        if not value.get("ok"):
            error = value.get("error") or {}
            raise ProtocolError(str(error.get("code") or "action_failed"),
                                str(error.get("message") or "Agent action failed"))
        return value.get("result")

    def _call_unix(self, payload: bytes, deadline_ms: int) -> bytes:
        client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        client.settimeout(deadline_ms / 1000)
        try:
            client.connect(_unix_socket_path(self.root))
            client.sendall(len(payload).to_bytes(4, "big") + payload)
            header = _recv_exact(client, 4)
            if not header:
                raise ConnectionError("Agent control socket closed without a response")
            length = int.from_bytes(header, "big")
            if length > 1024 * 1024:
                raise ProtocolError("message_too_large", "Agent response exceeds 1 MiB")
            return _recv_exact(client, length)
        finally:
            client.close()

    @staticmethod
    def _call_windows(payload: bytes, deadline_ms: int) -> bytes:  # pragma: no cover
        import pywintypes
        import win32file
        import win32pipe
        # Match the pipe DACL without requesting FILE_APPEND_DATA, which aliases the server-only
        # FILE_CREATE_PIPE_INSTANCE right and is included by GENERIC_WRITE.
        handle = _open_windows_pipe(win32file, win32pipe, pywintypes, deadline_ms)
        try:
            win32pipe.SetNamedPipeHandleState(handle, win32pipe.PIPE_READMODE_MESSAGE, None, None)
            _verify_windows_pipe_server(handle)
            win32file.WriteFile(handle, payload)
            return _read_win_message(handle, win32file, timeout_ms=deadline_ms)
        finally:
            win32file.CloseHandle(handle)


def _open_windows_pipe(win32file, win32pipe, pywintypes, deadline_ms: int):
    """Open the shared control pipe without exposing its instance-rotation race to clients."""
    deadline = time.monotonic() + max(0.001, deadline_ms / 1000)
    while True:
        remaining_ms = max(1, int((deadline - time.monotonic()) * 1000))
        try:
            win32pipe.WaitNamedPipe(PIPE_NAME, remaining_ms)
            return win32file.CreateFile(
                PIPE_NAME, 0x00120183, 0, None, win32file.OPEN_EXISTING, 0, None)
        except pywintypes.error as exc:
            # A waiter can lose the race for the instance that just became available.  The
            # server creates another instance immediately, so retry only these transient states
            # and keep the caller's original deadline authoritative.
            if getattr(exc, "winerror", None) not in {2, 231, 233}:
                raise
            if time.monotonic() >= deadline:
                raise TimeoutError("local control pipe remained busy until the deadline") from exc
            time.sleep(0.01)


def _recv_exact(sock: socket.socket, length: int) -> bytes:
    value = bytearray()
    while len(value) < length:
        chunk = sock.recv(length - len(value))
        if not chunk:
            break
        value.extend(chunk)
    return bytes(value)


def _read_win_message(handle, win32file, timeout_ms=15000) -> bytes:  # pragma: no cover
    import pywintypes
    import win32pipe
    chunks = []
    deadline = time.monotonic() + max(0.001, timeout_ms / 1000)
    while True:
        while True:
            try:
                _peek, available, _left = win32pipe.PeekNamedPipe(handle, 0)
            except pywintypes.error:
                raise
            if available:
                break
            if time.monotonic() >= deadline:
                raise TimeoutError("local control pipe read deadline elapsed")
            time.sleep(0.01)
        try:
            status, chunk = win32file.ReadFile(handle, 64 * 1024)
            chunks.append(chunk)
            if sum(map(len, chunks)) > 1024 * 1024:
                raise ProtocolError("message_too_large", "local control message exceeds 1 MiB")
            if status != 234:  # ERROR_MORE_DATA
                break
        except pywintypes.error as exc:
            if exc.winerror != 234:  # ERROR_MORE_DATA
                raise
            if len(exc.args) >= 3 and isinstance(exc.args[2], (bytes, bytearray)):
                chunks.append(bytes(exc.args[2]))
            if sum(map(len, chunks)) > 1024 * 1024:
                raise ProtocolError("message_too_large", "local control message exceeds 1 MiB")
    payload = b"".join(chunks)
    if len(payload) > 1024 * 1024:
        raise ProtocolError("message_too_large", "local control message exceeds 1 MiB")
    return payload


def _verify_windows_pipe_server(handle) -> None:  # pragma: no cover - Windows integration
    """Bind the fixed pipe name to the PID currently registered for the SCM service."""
    import win32pipe
    import win32service
    server_pid = int(win32pipe.GetNamedPipeServerProcessId(handle))
    manager = win32service.OpenSCManager(None, None, win32service.SC_MANAGER_CONNECT)
    service = None
    try:
        service = win32service.OpenService(manager, "MddAgent", win32service.SERVICE_QUERY_STATUS)
        status = win32service.QueryServiceStatusEx(service)
        expected_pid = int(status.get("ProcessId") or 0)
        if not expected_pid or server_pid != expected_pid:
            raise PermissionError(
                f"local control pipe server PID {server_pid} is not the running MddAgent service")
    finally:
        if service is not None:
            win32service.CloseServiceHandle(service)
        win32service.CloseServiceHandle(manager)
