#!/usr/bin/env python3
"""Host-side normal admission-authority issuer for gate-capable Engine containers."""
from __future__ import annotations

from dataclasses import dataclass
from concurrent.futures import ThreadPoolExecutor
import importlib
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import threading
import time
import uuid

try:
    from engine import admission_gate
except Exception:  # pragma: no cover - installed service runs from the repo root
    admission_gate = None

try:
    from .mdd_maintenance_supervisor import DockerInspector, SupervisorError
    from .mdd_maintenance_proxy import read_manifest
    from .mdd_engine_replacement import read_manifest as read_engine_replacement_manifest
except ImportError:  # pragma: no cover - script execution
    from mdd_maintenance_supervisor import DockerInspector, SupervisorError
    from mdd_maintenance_proxy import read_manifest
    from mdd_engine_replacement import read_manifest as read_engine_replacement_manifest


ENGINE_PREFIX = "mdd-sim-gateway-engine-"
ENGINE_COMPONENT_LABEL = "io.mdd-sim-gateway.component"
ENGINE_ADMISSION_ABI_LABEL = "io.mdd-sim-gateway.admission-abi"
ENGINE_ADMISSION_ABI = "mdd-admission-v1"
AUTHORITY_NAME = "admission-authority.json"
DENY_NAME = "admission-deny"
STATUS_NAME = "admission-authority-status.json"
STATE_NAME = "normal-admission-authority-state.json"
AGGREGATE_STATUS_NAME = "normal-admission-authority-status.json"
RENEWAL_STATUS_NAME = "normal-admission-renewal-status.json"
CONTROL_UPGRADE_NAME = "control-upgrade.json"
CONTROL_MAINTENANCE_FENCE_NAME = "maintenance-entry-fence.json"
ENGINE_REPLACEMENT_NAME = "engine-replacement.json"
ENGINE_REPLACEMENT_POSTFLIGHT_FENCE_NAME = "engine-replacement-postflight.fence"
ENGINE_REPLACEMENT_POSTFLIGHT_REASONS = {
    "engine_replacement_postflight_failed",
    "line_engine_replacement_postflight_failed",
}
RUN_ID_RE = re.compile(
    r"^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
HEX64_RE = re.compile(r"^[0-9a-f]{64}$")
IID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$")
RENEWAL_INTERVAL = 0.5
RENEWAL_DEADLINE = 2.0
RENEWAL_INSPECT_TIMEOUT = 1.0


class AuthorityWriterError(RuntimeError):
    pass


def _admission_gate_module():
    """Return a validated engine.admission_gate module or fail closed."""
    global admission_gate
    repo_root, expected_source = _admission_gate_location()
    module = admission_gate
    if module is None:
        _prepend_repo_root(repo_root)
        try:
            importlib.invalidate_caches()
            module = importlib.import_module("engine.admission_gate")
        except Exception as exc:
            raise AuthorityWriterError("admission_gate_unavailable") from exc
    try:
        _validate_admission_gate_module(module, expected_source)
    except AuthorityWriterError:
        _clear_invalid_admission_gate_cache(module, expected_source.parent)
        raise
    admission_gate = module
    return module


def _admission_gate_location() -> tuple[Path, Path]:
    repo_root = Path(__file__).resolve().parents[1]
    expected_source = (repo_root / "engine" / "admission_gate.py").resolve()
    if not expected_source.is_file():
        raise AuthorityWriterError("admission_gate_unavailable")
    return repo_root, expected_source


def _prepend_repo_root(repo_root: Path) -> None:
    repo_text = str(repo_root)
    sys.path[:] = [entry for entry in sys.path if entry != repo_text]
    sys.path.insert(0, repo_text)


def _validate_admission_gate_module(module, expected_source: Path) -> None:
    try:
        module_file = Path(str(getattr(module, "__file__", ""))).resolve()
    except Exception as exc:
        raise AuthorityWriterError("admission_gate_source_invalid") from exc
    if module_file != expected_source:
        raise AuthorityWriterError("admission_gate_source_invalid")
    if getattr(module, "PROTOCOL", "") != ENGINE_ADMISSION_ABI:
        raise AuthorityWriterError("admission_gate_protocol_invalid")
    if not callable(getattr(module, "probe", None)):
        raise AuthorityWriterError("admission_gate_probe_unavailable")


def _clear_invalid_admission_gate_cache(module, expected_engine_dir: Path) -> None:
    global admission_gate
    if admission_gate is module:
        admission_gate = None
    if sys.modules.get("engine.admission_gate") is module:
        sys.modules.pop("engine.admission_gate", None)
    parent = sys.modules.get("engine")
    if parent is None:
        return
    paths = []
    for entry in getattr(parent, "__path__", []) or []:
        try:
            paths.append(Path(str(entry)).resolve())
        except Exception:
            continue
    if expected_engine_dir not in paths:
        sys.modules.pop("engine", None)


@dataclass(frozen=True)
class EngineObservation:
    iid: str
    container_id: str
    image_id: str
    started_at: str
    restart_count: int
    run_id: str


@dataclass(frozen=True)
class VerifiedLease:
    observation: EngineObservation
    authority_epoch: int
    lease_seq: int
    identity_digest: str
    state_digest: str
    published_monotonic: float


def _digest(value: object) -> str:
    return __import__("hashlib").sha256(json.dumps(
        value, sort_keys=True, separators=(",", ":")).encode("utf-8")).hexdigest()


def _atomic_json(path: Path, value: object, mode: int = 0o600) -> None:
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.{os.getpid()}.{threading.get_ident()}.tmp")
    try:
        with tmp.open("w", encoding="utf-8") as handle:
            json.dump(value, handle, sort_keys=True, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(tmp, mode)
        os.replace(tmp, path)
        dirfd = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(dirfd)
        finally:
            os.close(dirfd)
    finally:
        try:
            tmp.unlink()
        except FileNotFoundError:
            pass


def _read_json(path: Path) -> object:
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def _unlink(path: Path) -> None:
    try:
        path.unlink()
    except FileNotFoundError:
        return
    dirfd = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(dirfd)
    finally:
        os.close(dirfd)


class NormalAuthorityWriter:
    def __init__(self, data: Path, *, interval: float = 1.0,
                 inspector: DockerInspector | None = None,
                 boot_id: str | None = None) -> None:
        self.data = Path(data)
        self.root = self.data / "orchestrator"
        self.interval = float(interval)
        self.inspector = inspector or DockerInspector()
        self.boot_id = boot_id or uuid.uuid4().hex
        self.state_path = self.root / STATE_NAME
        self.aggregate_status_path = self.root / AGGREGATE_STATUS_NAME
        self.renewal_status_path = self.root / RENEWAL_STATUS_NAME
        self.stop_event = threading.Event()
        self._state_lock = threading.RLock()
        self._state_cache: dict | None = None
        self._verified: dict[str, VerifiedLease] = {}
        self._renewal_thread: threading.Thread | None = None

    def _load_state(self) -> dict:
        try:
            value = _read_json(self.state_path)
            if isinstance(value, dict) and value.get("version") == 1 \
                    and isinstance(value.get("lines"), dict):
                return value
        except Exception:
            pass
        return {"version": 1, "writer_boot_id": self.boot_id, "lines": {}}

    def _save_state_unlocked(self, value: dict) -> None:
        now_ns = time.time_ns()
        current = {"version": 1, "writer_boot_id": self.boot_id,
                   "updated_at": now_ns // 1_000_000_000, "updated_at_ns": now_ns,
                   "lines": value.get("lines") or {}}
        _atomic_json(self.state_path, current)
        self._state_cache = current

    def _save_state(self, value: dict) -> None:
        with self._state_lock:
            self._save_state_unlocked(value)

    def _current_state(self) -> dict:
        with self._state_lock:
            if self._state_cache is None:
                self._state_cache = self._load_state()
            return self._state_cache

    def _running_engine_names(self) -> list[tuple[str, str]]:
        result = subprocess.run([
            "docker", "ps", "--no-trunc", "--format", "{{.ID}} {{.Names}}",
            "--filter", f"label={ENGINE_COMPONENT_LABEL}=engine"],
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, timeout=2,
            check=False)
        if result.returncode != 0:
            raise AuthorityWriterError("engine_list_unavailable")
        values = []
        for line in result.stdout.splitlines():
            parts = line.split(None, 1)
            if len(parts) != 2 or not HEX64_RE.fullmatch(parts[0]):
                raise AuthorityWriterError("engine_list_invalid")
            if parts[1].startswith(ENGINE_PREFIX):
                values.append((parts[0], parts[1]))
        return sorted(values, key=lambda item: item[1])

    def _image_abi(self, image_id: str) -> str:
        result = subprocess.run([
            "docker", "image", "inspect", image_id, "--format",
            f"{{{{index .Config.Labels \"{ENGINE_ADMISSION_ABI_LABEL}\"}}}}"],
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, timeout=2,
            check=False)
        return result.stdout.strip() if result.returncode == 0 else ""

    def _global_deny_reason(self) -> str:
        if os.path.lexists(self.root / CONTROL_MAINTENANCE_FENCE_NAME):
            return "global_maintenance_fence"
        replacement = self.root / ENGINE_REPLACEMENT_NAME
        if os.path.lexists(replacement):
            try:
                value = read_engine_replacement_manifest(replacement)
                if value.get("phase") not in {"committed", "aborted"}:
                    return "global_engine_replacement_in_flight"
            except Exception:
                return "global_engine_replacement_unknown"
        upgrade = self.root / CONTROL_UPGRADE_NAME
        if not os.path.lexists(upgrade):
            return ""
        try:
            value = read_manifest(upgrade)
        except Exception:
            return "global_maintenance_unknown"
        phase = value.get("phase")
        if phase not in {"committed", "rollback_committed"}:
            return "global_maintenance_in_flight"
        allowed = {"verified"} if phase == "committed" else {"rollback_verified", "aborted"}
        lines = value.get("lines")
        if not isinstance(lines, list) or any(
                not isinstance(line, dict) or line.get("phase") not in allowed
                for line in lines):
            return "global_maintenance_in_flight"
        return ""

    def _known_iids(self, state: dict) -> list[str]:
        values = set()
        lines = state.get("lines")
        if isinstance(lines, dict):
            values.update(str(key) for key in lines if IID_RE.fullmatch(str(key)))
        instances = self.data / "instances"
        try:
            for entry in instances.iterdir():
                if entry.is_dir() and IID_RE.fullmatch(entry.name) \
                        and (entry / "run").is_dir():
                    values.add(entry.name)
        except OSError:
            pass
        return sorted(values)

    def _run_dir(self, iid: str) -> Path:
        return self.data / "instances" / str(iid) / "run"

    def _sticky_postflight_deny(self, run: Path) -> bool:
        try:
            value = _read_json(run / DENY_NAME)
            return isinstance(value, dict) and value.get("reason") in \
                ENGINE_REPLACEMENT_POSTFLIGHT_REASONS
        except Exception:
            return False

    def _line_deny_reason(self, iid: str) -> str:
        run = self._run_dir(iid)
        if os.path.lexists(run / ENGINE_REPLACEMENT_POSTFLIGHT_FENCE_NAME):
            return "line_engine_replacement_postflight_failed"
        if self._sticky_postflight_deny(run):
            return "line_engine_replacement_postflight_failed"
        if os.path.lexists(run / "engine-maintenance.json"):
            return "line_engine_maintenance"
        if os.path.lexists(run / "pcscf-rebind.json"):
            return "line_pcscf_rebind"
        return ""

    def _stable_observation(self, container_id: str, name: str) -> EngineObservation:
        iid = name[len(ENGINE_PREFIX):]
        first = self.inspector.container(container_id)
        if dict(first.labels).get(ENGINE_COMPONENT_LABEL) != "engine":
            raise AuthorityWriterError("engine_label_invalid")
        run_path = self._run_dir(iid) / "engine-run-id"
        try:
            run_id = run_path.read_text(encoding="utf-8").strip()
        except OSError as exc:
            raise AuthorityWriterError("engine_run_id_unavailable") from exc
        if not RUN_ID_RE.fullmatch(run_id):
            raise AuthorityWriterError("engine_run_id_invalid")
        second = self.inspector.container(container_id)
        if second != first:
            raise AuthorityWriterError("engine_generation_changed")
        return EngineObservation(
            iid, first.container_id, first.image_id, first.started_at,
            first.restart_count, run_id)

    def _authority(self, observation: EngineObservation, epoch: int, seq: int) \
            -> tuple[dict, str, str, str]:
        gate = _admission_gate_module()
        engine = {
            "container_id": observation.container_id,
            "image_id": observation.image_id,
            "started_at": observation.started_at,
            "restart_count": observation.restart_count,
            "run_id": observation.run_id,
        }
        engine_digest = _digest(engine)
        normal_state = {
            "version": 1, "protocol": gate.PROTOCOL,
            "mode": "normal_committed", "issuer_boot_id": self.boot_id,
            "iid": observation.iid, "engine": engine,
            "engine_generation_digest": engine_digest,
            "image_admission_abi": ENGINE_ADMISSION_ABI,
            "data_root": str(self.data.resolve()),
        }
        state_digest = _digest(normal_state)
        commit_id = state_digest[:32]
        authority = {
            "version": 1, "protocol": gate.PROTOCOL,
            "mode": "normal_committed", "iid": observation.iid,
            "issuer_boot_id": self.boot_id, "authority_epoch": epoch,
            "lease_seq": seq, "engine": engine,
            "engine_generation_digest": engine_digest,
            "maintenance": None,
            "normal": {"commit_id": commit_id, "state_digest": state_digest},
        }
        identity = (observation.iid, self.boot_id, epoch, engine_digest,
                    "normal_committed", commit_id, state_digest)
        return authority, _digest(normal_state), _digest(identity), state_digest

    def _reserve_authority_locked(self, observation: EngineObservation,
                                  state: dict) -> tuple[int, int, str, str]:
        """Durably reserve a sequence before making it visible to the Engine gate."""
        iid = observation.iid
        lines = state.setdefault("lines", {})
        previous = lines.get(iid) if isinstance(lines.get(iid), dict) else {}
        _probe, identity_key, _identity_digest, _state_digest = self._authority(
            observation, int(previous.get("authority_epoch") or 1),
            int(previous.get("lease_seq") or 1))
        if previous.get("identity_key") != identity_key:
            epoch = int(previous.get("authority_epoch") or 0) + 1
            seq = 1
        else:
            epoch = int(previous.get("authority_epoch") or 1)
            seq = int(previous.get("lease_seq") or 0) + 1
        authority, identity_key, identity_digest, state_digest = self._authority(
            observation, epoch, seq)
        lines[iid] = {"identity_key": identity_key, "authority_epoch": epoch,
                      "lease_seq": seq, "identity_digest": identity_digest,
                      "state_digest": state_digest}
        # A crash after this write can only skip a sequence. Publishing first could replay a
        # sequence after restart and permanently force the gate back through warmup.
        self._save_state_unlocked(state)
        _atomic_json(self._run_dir(iid) / AUTHORITY_NAME, authority)
        return epoch, seq, identity_digest, state_digest

    def _verified_snapshot(self) -> dict[str, VerifiedLease]:
        with self._state_lock:
            return dict(self._verified)

    def _set_verified_locked(self, observation: EngineObservation, *, epoch: int,
                             seq: int, identity_digest: str,
                             state_digest: str) -> None:
        self._verified[observation.iid] = VerifiedLease(
            observation=observation, authority_epoch=epoch, lease_seq=seq,
            identity_digest=identity_digest, state_digest=state_digest,
            published_monotonic=time.monotonic())

    def _drop_verified_locked(self, iid: str) -> None:
        self._verified.pop(str(iid), None)

    def _batch_verified_generation_issues(
            self, entries: dict[str, VerifiedLease]) -> dict[str, str]:
        """Check all cached generations with one bounded Docker request."""
        if not entries:
            return {}
        command = ["docker", "inspect", "--type", "container"] + [
            entries[iid].observation.container_id for iid in sorted(entries)]
        try:
            result = subprocess.run(
                command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                text=True, timeout=RENEWAL_INSPECT_TIMEOUT, check=False)
            if result.returncode != 0:
                values = []
                isolated_issues: dict[str, str] = {}

                def inspect_one(item: tuple[str, VerifiedLease]) \
                        -> tuple[str, dict | None, str]:
                    iid, lease = item
                    try:
                        single = subprocess.run(
                            ["docker", "inspect", "--type", "container",
                             lease.observation.container_id],
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                            text=True, timeout=RENEWAL_INSPECT_TIMEOUT,
                            check=False)
                        if single.returncode != 0:
                            return iid, None, "renewal_container_unavailable"
                        decoded = json.loads(single.stdout or "[]")
                        if (not isinstance(decoded, list) or len(decoded) != 1
                                or not isinstance(decoded[0], dict)):
                            return iid, None, "renewal_docker_response_invalid"
                        return iid, decoded[0], ""
                    except subprocess.TimeoutExpired:
                        return iid, None, "renewal_docker_timeout"
                    except Exception:
                        return iid, None, "renewal_docker_response_invalid"

                with ThreadPoolExecutor(max_workers=min(32, len(entries))) as pool:
                    for iid, value, reason in pool.map(
                            inspect_one, sorted(entries.items())):
                        if reason:
                            isolated_issues[iid] = reason
                        elif value is not None:
                            values.append(value)
            else:
                isolated_issues = {}
                values = json.loads(result.stdout or "[]")
        except subprocess.TimeoutExpired:
            return {iid: "renewal_docker_timeout" for iid in entries}
        except Exception:
            return {iid: "renewal_docker_response_invalid" for iid in entries}
        if not isinstance(values, list):
            return {iid: "renewal_docker_response_invalid" for iid in entries}
        by_id = {str(value.get("Id") or ""): value for value in values
                 if isinstance(value, dict)}
        issues: dict[str, str] = dict(isolated_issues)
        for iid, lease in entries.items():
            if iid in issues:
                continue
            observation = lease.observation
            value = by_id.get(observation.container_id)
            state = value.get("State") if isinstance(value, dict) else {}
            labels = (value.get("Config") or {}).get("Labels") \
                if isinstance(value, dict) else {}
            if (not isinstance(value, dict)
                    or str(value.get("Name") or "") != f"/{ENGINE_PREFIX}{iid}"
                    or str(value.get("Image") or "") != observation.image_id
                    or not isinstance(state, dict) or state.get("Running") is not True
                    or state.get("Status") != "running"
                    or str(state.get("StartedAt") or "") != observation.started_at
                    or type(value.get("RestartCount")) is not int
                    or value.get("RestartCount") != observation.restart_count
                    or not isinstance(labels, dict)
                    or labels.get(ENGINE_COMPONENT_LABEL) != "engine"):
                issues[iid] = "renewal_generation_changed"
                continue
            try:
                run_id = (self._run_dir(iid) / "engine-run-id").read_text(
                    encoding="utf-8").strip()
            except OSError:
                issues[iid] = "renewal_engine_run_id_unavailable"
                continue
            if run_id != observation.run_id:
                issues[iid] = "renewal_engine_run_id_changed"
        return issues

    def _renewal_file_reason(self, iid: str, observation: EngineObservation) -> str:
        reason = self._global_deny_reason() or self._line_deny_reason(iid)
        if reason:
            return reason
        run = self._run_dir(iid)
        if os.path.lexists(run / DENY_NAME):
            return "local_admission_deny"
        try:
            run_id = (run / "engine-run-id").read_text(encoding="utf-8").strip()
        except OSError:
            return "renewal_engine_run_id_unavailable"
        return "" if run_id == observation.run_id else "renewal_engine_run_id_changed"

    def _write_status(self, iid: str, value: dict) -> None:
        now_ns = time.time_ns()
        payload = {"version": 1, "writer_boot_id": self.boot_id,
                   "updated_at": now_ns // 1_000_000_000,
                   "updated_at_ns": now_ns, **value}
        _atomic_json(self._run_dir(iid) / STATUS_NAME, payload, mode=0o644)

    def _probe(self, iid: str) -> dict:
        gate = _admission_gate_module()
        return gate.probe(self._run_dir(iid) / "admission-gate.sock",
                          "media_check", timeout=0.25)

    def _gate_status(self, iid: str) -> dict:
        try:
            value = _read_json(self._run_dir(iid) / "admission-gate-status.json")
            return value if isinstance(value, dict) else {}
        except Exception:
            return {}

    def _wait_denied_result(self, iid: str, timeout: float = 1.0) -> tuple[bool, str]:
        deadline = time.monotonic() + timeout
        last_error = ""
        while time.monotonic() < deadline:
            try:
                probe = self._probe(iid)
                status = self._gate_status(iid)
                if (probe.get("allowed") is False
                        and not status.get("authority_identity_digest")):
                    return True, ""
                last_error = "deny_not_observed"
            except AuthorityWriterError as exc:
                return False, str(exc)[:120] or exc.__class__.__name__
            except Exception as exc:
                last_error = str(exc)[:120] or exc.__class__.__name__
            time.sleep(0.05)
        return False, last_error

    def _wait_denied(self, iid: str, timeout: float = 1.0) -> bool:
        proved, _cause = self._wait_denied_result(iid, timeout=timeout)
        return proved

    def _wait_allowed(self, iid: str, *, epoch: int, min_seq: int,
                      identity_digest: str, state_digest: str,
                      timeout: float = 1.0) -> bool:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                probe = self._probe(iid)
                status = self._gate_status(iid)
                if (probe.get("allowed") is True
                        and probe.get("authority_epoch") == epoch
                        and int(probe.get("lease_seq") or 0) >= min_seq
                        and status.get("state") == "allow"
                        and status.get("authority_identity_digest") == identity_digest
                        and status.get("normal_state_digest") == state_digest):
                    return True
            except Exception:
                pass
            time.sleep(0.05)
        return False

    def _remove_or_poison_authority(self, run: Path) -> bool:
        try:
            _unlink(run / AUTHORITY_NAME)
            return True
        except OSError:
            pass
        try:
            _atomic_json(run / AUTHORITY_NAME, {
                "version": 1, "protocol": ENGINE_ADMISSION_ABI,
                "mode": "deny_poison", "reason": "authority_revoked",
            })
            return True
        except OSError:
            return False

    def _publish_deny(self, iid: str, reason: str) -> dict:
        run = self._run_dir(iid)
        deny_write_failed = False
        with self._state_lock:
            self._drop_verified_locked(iid)
            try:
                _atomic_json(run / DENY_NAME, {"version": 1, "reason": reason,
                                               "updated_at": int(time.time())})
            except OSError:
                deny_write_failed = True
            removed = self._remove_or_poison_authority(run)
        if deny_write_failed:
            denied, deny_cause = self._wait_denied_result(iid) if removed else (
                False, "authority_remove_failed")
            status = {"iid": iid, "state": "deny", "healthy": False,
                      "reason": "deny_write_failed_not_proven",
                      "authority_removed": removed, "deny_proven": denied,
                      "requested_reason": reason}
            if denied:
                status["healthy"] = True
                status["reason"] = "deny_write_failed"
            elif deny_cause:
                status["deny_cause"] = deny_cause
            try:
                self._write_status(iid, status)
            except OSError:
                pass
            return status
        denied, deny_cause = self._wait_denied_result(iid)
        status = {"iid": iid, "state": "deny", "healthy": denied,
                  "reason": reason or "deny_not_proven", "deny_proven": denied}
        if not denied and deny_cause:
            status["deny_cause"] = deny_cause
        self._write_status(iid, status)
        return status

    def _fast_revoke(self, iid: str, reason: str, *,
                     expected: VerifiedLease | None = None) -> dict | None:
        """Stop renewal and revoke local authority before any blocking proof."""
        run = self._run_dir(iid)
        with self._state_lock:
            current = self._verified.get(iid)
            if expected is not None and current != expected:
                return None
            self._drop_verified_locked(iid)
            deny_write_failed = False
            try:
                _atomic_json(run / DENY_NAME, {
                    "version": 1, "reason": reason,
                    "updated_at": int(time.time()),
                })
            except OSError:
                deny_write_failed = True
            removed = self._remove_or_poison_authority(run)
            status = {
                "iid": iid, "state": "deny", "healthy": False,
                "reason": reason, "deny_proven": False,
                "authority_removed": removed,
                "deny_write_failed": deny_write_failed,
            }
            try:
                self._write_status(iid, status)
            except OSError:
                pass
        return status

    def _prove_fast_revoke(self, iid: str, reason: str, preliminary: dict) -> dict:
        denied, deny_cause = self._wait_denied_result(iid)
        status = dict(preliminary)
        status.update({"healthy": denied, "deny_proven": denied})
        if deny_cause and not denied:
            status["deny_cause"] = deny_cause
        with self._state_lock:
            # A slow exact reconcile may already have proved a new generation. Never let
            # the old generation's delayed proof overwrite that newer line status.
            if iid in self._verified:
                return {**status, "state": "superseded"}
            try:
                self._write_status(iid, status)
            except OSError:
                pass
        return status

    def _publish_allow(self, observation: EngineObservation, state: dict, *,
                       register_verified: bool = False) -> dict:
        iid = observation.iid
        run = self._run_dir(iid)
        if os.path.lexists(run / DENY_NAME):
            if self._sticky_postflight_deny(run):
                return self._publish_deny(
                    iid, "line_engine_replacement_postflight_failed")
            denied, deny_cause = self._wait_denied_result(iid, timeout=0.5)
            if not denied:
                status = {"iid": iid, "state": "deny", "healthy": False,
                          "reason": "deny_not_proven_before_allow",
                          "deny_proven": False}
                if deny_cause:
                    status["deny_cause"] = deny_cause
                self._write_status(iid, status)
                return status
            with self._state_lock:
                sticky_reappeared = self._sticky_postflight_deny(run)
                if not sticky_reappeared:
                    _unlink(run / DENY_NAME)
            if sticky_reappeared:
                return self._publish_deny(
                    iid, "line_engine_replacement_postflight_failed")
        last_identity_digest = ""
        last_state_digest = ""
        for _attempt in range(4):
            with self._state_lock:
                epoch, seq, identity_digest, state_digest = \
                    self._reserve_authority_locked(observation, state)
            last_identity_digest = identity_digest
            last_state_digest = state_digest
            time.sleep(0.15)
            if self._wait_allowed(iid, epoch=epoch, min_seq=seq,
                                  identity_digest=identity_digest,
                                  state_digest=state_digest, timeout=0.25):
                status = {"iid": iid, "state": "allow", "healthy": True,
                          "reason": "allow", "container_id": observation.container_id,
                          "image_id": observation.image_id,
                          "started_at": observation.started_at,
                          "restart_count": observation.restart_count,
                          "run_id": observation.run_id,
                          "authority_epoch": epoch, "lease_seq": seq,
                          "authority_identity_digest": identity_digest,
                          "normal_state_digest": state_digest}
                superseded: VerifiedLease | None = None
                if register_verified:
                    with self._state_lock:
                        renewal_reason = self._renewal_file_reason(iid, observation)
                        if renewal_reason:
                            self._drop_verified_locked(iid)
                        else:
                            current = self._verified.get(iid)
                            if (current is not None
                                    and (current.observation != observation
                                         or current.authority_epoch > epoch
                                         or (current.authority_epoch == epoch
                                             and current.lease_seq > seq))):
                                superseded = current
                            else:
                                self._set_verified_locked(
                                    observation, epoch=epoch, seq=seq,
                                    identity_digest=identity_digest,
                                    state_digest=state_digest)
                    if renewal_reason:
                        return self._publish_deny(iid, renewal_reason)
                if superseded is not None:
                    return {
                        "iid": iid, "state": "allow", "healthy": True,
                        "reason": "allow_superseded",
                        "container_id": superseded.observation.container_id,
                        "image_id": superseded.observation.image_id,
                        "started_at": superseded.observation.started_at,
                        "restart_count": superseded.observation.restart_count,
                        "run_id": superseded.observation.run_id,
                        "authority_epoch": superseded.authority_epoch,
                        "lease_seq": superseded.lease_seq,
                        "authority_identity_digest": superseded.identity_digest,
                        "normal_state_digest": superseded.state_digest,
                    }
                self._write_status(iid, status)
                return status
        status = {"iid": iid, "state": "deny", "healthy": False,
                  "reason": "allow_not_proven", "authority_epoch": epoch,
                  "lease_seq": seq, "authority_identity_digest": last_identity_digest,
                  "normal_state_digest": last_state_digest}
        return self._publish_deny(iid, status["reason"])

    def _write_renewal_status(self, state: str, lines: dict[str, dict],
                              *, reason: str = "") -> dict:
        now_ns = time.time_ns()
        value = {
            "version": 1, "writer_boot_id": self.boot_id, "state": state,
            "updated_at": now_ns // 1_000_000_000,
            "updated_at_ns": now_ns, "lines": lines,
        }
        if reason:
            value["reason"] = reason
        _atomic_json(self.renewal_status_path, value, mode=0o644)
        return value

    def _renew_verified_once(self) -> dict:
        entries = self._verified_snapshot()
        if not entries:
            return self._write_renewal_status("idle", {})

        issues = self._batch_verified_generation_issues(entries)
        global_reason = self._global_deny_reason()
        now = time.monotonic()
        for iid, lease in entries.items():
            if global_reason:
                issues[iid] = global_reason
            elif iid not in issues:
                reason = self._renewal_file_reason(iid, lease.observation)
                if reason:
                    issues[iid] = reason
                elif now - lease.published_monotonic > RENEWAL_DEADLINE:
                    issues[iid] = "renewal_deadline_missed"

        results: dict[str, dict] = {}
        revoked: list[tuple[str, str, dict]] = []
        for iid in sorted(issues):
            preliminary = self._fast_revoke(
                iid, issues[iid], expected=entries[iid])
            if preliminary is not None:
                results[iid] = preliminary
                revoked.append((iid, issues[iid], preliminary))

        good = [(iid, lease) for iid, lease in entries.items()
                if iid not in issues]
        good.sort(key=lambda item: item[1].published_monotonic)
        for iid, lease in good:
            failure = ""
            renewed: VerifiedLease | None = None
            try:
                with self._state_lock:
                    if self._verified.get(iid) != lease:
                        continue
                    failure = self._renewal_file_reason(iid, lease.observation)
                    if (not failure and time.monotonic()
                            - lease.published_monotonic > RENEWAL_DEADLINE):
                        failure = "renewal_deadline_missed"
                    if not failure:
                        epoch, seq, identity_digest, state_digest = \
                            self._reserve_authority_locked(
                                lease.observation, self._current_state())
                        failure = self._renewal_file_reason(iid, lease.observation)
                        if not failure:
                            self._set_verified_locked(
                                lease.observation, epoch=epoch, seq=seq,
                                identity_digest=identity_digest,
                                state_digest=state_digest)
                            renewed = self._verified[iid]
            except Exception as exc:
                failure = str(exc)[:120] or "renewal_publish_failed"
            if failure:
                preliminary = self._fast_revoke(iid, failure, expected=lease)
                if preliminary is not None:
                    results[iid] = preliminary
                    revoked.append((iid, failure, preliminary))
            elif renewed is not None:
                results[iid] = {
                    "iid": iid, "state": "renewed",
                    "authority_published": True, "gate_proven": False,
                    "container_id": renewed.observation.container_id,
                    "image_id": renewed.observation.image_id,
                    "started_at": renewed.observation.started_at,
                    "restart_count": renewed.observation.restart_count,
                    "run_id": renewed.observation.run_id,
                    "authority_epoch": renewed.authority_epoch,
                    "lease_seq": renewed.lease_seq,
                    "authority_identity_digest": renewed.identity_digest,
                    "normal_state_digest": renewed.state_digest,
                }

        # Proof waits are deliberately after all unaffected lines have renewed and never
        # hold the shared sequence/verified-state lock.
        if revoked:
            with ThreadPoolExecutor(max_workers=min(32, len(revoked))) as pool:
                futures = [
                    (iid, pool.submit(
                        self._prove_fast_revoke, iid, reason, preliminary))
                    for iid, reason, preliminary in revoked
                ]
                for iid, future in futures:
                    results[iid] = future.result()
        state = "degraded" if revoked else "active"
        return self._write_renewal_status(state, results)

    def _renewal_loop(self) -> None:
        while not self.stop_event.is_set():
            started = time.monotonic()
            try:
                self._renew_verified_once()
            except Exception as exc:
                reason = str(exc)[:120] or "renewal_loop_failed"
                snapshot = self._verified_snapshot()
                lines = {}
                for iid, lease in snapshot.items():
                    status = self._fast_revoke(
                        iid, "renewal_loop_failed", expected=lease)
                    if status is not None:
                        lines[iid] = status
                try:
                    self._write_renewal_status(
                        "degraded", lines, reason=reason)
                except Exception:
                    pass
            remaining = RENEWAL_INTERVAL - (time.monotonic() - started)
            self.stop_event.wait(max(0.0, remaining))

    def reconcile_once(self) -> dict:
        state = self._current_state()
        statuses: dict[str, dict] = {}
        global_reason = self._global_deny_reason()
        try:
            engines = self._running_engine_names()
        except Exception as exc:
            reason = str(exc)[:120] or "engine_list_unavailable"
            for iid in self._known_iids(state):
                try:
                    statuses[iid] = self._publish_deny(iid, reason)
                except Exception as deny_exc:
                    statuses[iid] = {"iid": iid, "state": "deny", "healthy": False,
                                     "reason": str(deny_exc)[:120] or "deny_failed"}
                    try:
                        self._write_status(iid, statuses[iid])
                    except Exception:
                        pass
            now_ns = time.time_ns()
            aggregate = {"version": 1, "writer_boot_id": self.boot_id,
                         "state": "unhealthy", "reason": reason,
                         "updated_at": now_ns // 1_000_000_000,
                         "updated_at_ns": now_ns, "lines": statuses}
            _atomic_json(self.aggregate_status_path, aggregate, mode=0o644)
            return aggregate
        for container_id, name in engines:
            iid = name[len(ENGINE_PREFIX):]
            try:
                observation = self._stable_observation(container_id, name)
                reason = global_reason or self._line_deny_reason(iid)
                if not reason and self._image_abi(observation.image_id) != ENGINE_ADMISSION_ABI:
                    reason = "legacy_admission_abi"
                statuses[iid] = (self._publish_deny(iid, reason)
                                 if reason else self._publish_allow(
                                     observation, state, register_verified=True))
            except Exception as exc:
                statuses[iid] = self._publish_deny(iid, str(exc)[:120] or "unknown")
        running_iids = {name[len(ENGINE_PREFIX):] for _container_id, name in engines}
        for iid, lease in self._verified_snapshot().items():
            if iid not in running_iids:
                status = self._fast_revoke(
                    iid, "engine_not_running", expected=lease)
                if status is not None:
                    statuses[iid] = status
        now_ns = time.time_ns()
        aggregate = {
            "version": 1, "writer_boot_id": self.boot_id,
            "state": "healthy" if all(item.get("healthy") for item in statuses.values())
            else "unhealthy",
            "updated_at": now_ns // 1_000_000_000,
            "updated_at_ns": now_ns, "lines": statuses,
        }
        _atomic_json(self.aggregate_status_path, aggregate, mode=0o644)
        return aggregate

    def run(self) -> None:
        self._renewal_thread = threading.Thread(
            target=self._renewal_loop, name="mdd-admission-renewal", daemon=True)
        self._renewal_thread.start()
        try:
            while not self.stop_event.is_set():
                try:
                    self.reconcile_once()
                except Exception:
                    pass
                self.stop_event.wait(self.interval)
        finally:
            self.stop_event.set()
            thread = self._renewal_thread
            if thread is not None and thread is not threading.current_thread():
                thread.join(timeout=2.0)
            self._renewal_thread = None
            for iid, lease in self._verified_snapshot().items():
                self._fast_revoke(
                    iid, "authority_writer_stopped", expected=lease)

    def stop(self) -> None:
        self.stop_event.set()
        thread = self._renewal_thread
        if thread is not None and thread is not threading.current_thread():
            thread.join(timeout=2.0)


def main(argv=None) -> int:
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--data", required=True, type=Path)
    parser.add_argument("--once", action="store_true")
    args = parser.parse_args(argv)
    writer = NormalAuthorityWriter(args.data.resolve())
    if args.once:
        print(json.dumps(writer.reconcile_once(), sort_keys=True))
        return 0
    writer.run()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
