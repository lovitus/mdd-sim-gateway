#!/usr/bin/env python3
"""Durable P-CSCF generation state shared by entrypoint and the SWu worker.

PJSIP objects are deliberately immutable for one Asterisk process.  When the ePDG assigns a
different P-CSCF, the SWu worker records a desired configuration generation; Control fences new
work and asks Asterisk to stop gracefully.  The next complete entrypoint run renders the newly
discovered address before Asterisk starts and clears the fence atomically.
"""
from __future__ import annotations

import fcntl
import ipaddress
import json
import math
import os
import re
import subprocess
import sys
import time
from contextlib import contextmanager


MARKER_NAME = "pcscf-rebind.json"
LOCK_NAME = ".pcscf-rebind.lock"
RUN_ID_NAME = "engine-run-id"
APPLIED_NAME = "pcscf.applied"
DISCOVERY_NAME = "pcscf-discovery.json"
_RUN_ID_RE = re.compile(r"^[A-Za-z0-9_.:-]{1,128}$")
_INSTANCE_RE = re.compile(r"^[A-Za-z0-9_.:-]{1,128}$")
_PHASES = {"pending", "submitted", "cancel_requested", "abort_submitted"}


def _path(rundir: str, name: str) -> str:
    return os.path.join(rundir, name)


def _valid_address(value: object) -> str:
    text = str(value or "").strip()
    try:
        return str(ipaddress.ip_address(text))
    except ValueError:
        return ""


def _atomic_text(path: str, value: str, mode: int = 0o600) -> None:
    temporary = f"{path}.tmp.{os.getpid()}"
    try:
        with open(temporary, "w", encoding="utf-8") as handle:
            handle.write(value)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


@contextmanager
def locked(rundir: str):
    os.makedirs(rundir, exist_ok=True)
    with open(_path(rundir, LOCK_NAME), "a+", encoding="utf-8") as handle:
        os.chmod(handle.name, 0o600)
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)


def validate_marker(value: object) -> dict | None:
    if not isinstance(value, dict) or type(value.get("version")) is not int \
            or value.get("version") != 1:
        return None
    instance = str(value.get("instance") or "")
    run_id = str(value.get("engine_run_id") or "")
    applied = _valid_address(value.get("applied"))
    desired = _valid_address(value.get("desired"))
    phase = str(value.get("phase") or "")
    observed_at = value.get("observed_at")
    # ``applied`` may be empty when entrypoint's bounded initial discovery window elapsed and
    # Asterisk booted without a route; a later discovery must still request a full generation.
    if (not _INSTANCE_RE.fullmatch(instance) or not _RUN_ID_RE.fullmatch(run_id)
            or (value.get("applied") not in (None, "") and not applied)
            or not desired or phase not in _PHASES
            or not isinstance(observed_at, (int, float)) or isinstance(observed_at, bool)
            or not math.isfinite(float(observed_at)) or float(observed_at) <= 0
            or type(value.get("shutdown_reserved")) is not bool):
        return None
    result = dict(value)
    result.update({"instance": instance, "engine_run_id": run_id,
                   "applied": applied, "desired": desired, "phase": phase,
                   "observed_at": float(observed_at)})
    return result


def _read_unlocked(rundir: str) -> dict | None:
    try:
        with open(_path(rundir, MARKER_NAME), encoding="utf-8") as handle:
            return validate_marker(json.load(handle))
    except (OSError, ValueError, TypeError):
        return None


def _write_unlocked(rundir: str, marker: dict) -> None:
    _atomic_text(_path(rundir, MARKER_NAME),
                 json.dumps(marker, ensure_ascii=True, sort_keys=True) + "\n")


def _read_text(rundir: str, name: str) -> str:
    try:
        with open(_path(rundir, name), encoding="utf-8") as handle:
            return handle.read(256).strip()
    except OSError:
        return ""


def _read_discovery_unlocked(rundir: str, run_id: str) -> dict | None:
    try:
        with open(_path(rundir, DISCOVERY_NAME), encoding="utf-8") as handle:
            value = json.load(handle)
    except (OSError, ValueError, TypeError):
        return None
    address = _valid_address((value or {}).get("address")) if isinstance(value, dict) else ""
    observed_at = (value or {}).get("observed_at") if isinstance(value, dict) else None
    if (not address or str((value or {}).get("engine_run_id") or "") != str(run_id)
            or not isinstance(observed_at, (int, float)) or isinstance(observed_at, bool)
            or not math.isfinite(float(observed_at)) or float(observed_at) <= 0):
        return None
    return {"engine_run_id": str(run_id), "address": address,
            "observed_at": float(observed_at)}


def read_marker(rundir: str) -> dict | None:
    with locked(rundir):
        return _read_unlocked(rundir)


def write_run_id(rundir: str, run_id: str) -> None:
    value = str(run_id or "").strip()
    if not _RUN_ID_RE.fullmatch(value):
        raise ValueError("invalid Engine run id")
    with locked(rundir):
        _atomic_text(_path(rundir, RUN_ID_NAME), value + "\n")


def publish_discovered(rundir: str, instance: str, run_id: str, desired: str,
                       *, observed_at: float | None = None) -> str:
    """Atomically publish every fresh discovery and its required generation transition.

    This runs before and after Asterisk starts.  It never relies on a CLI readiness probe: a
    bootstrap discovery is durable, and Control independently waits for fully-booted Asterisk
    before reserving a graceful stop.  The shared lock also makes it impossible for entrypoint
    to render one address and commit another.
    """
    iid = str(instance or "").strip()
    owner = str(run_id or "").strip()
    new = _valid_address(desired)
    if not _INSTANCE_RE.fullmatch(iid) or not _RUN_ID_RE.fullmatch(owner) or not new:
        return "invalid"
    timestamp = float(observed_at if observed_at is not None else time.time())
    if not math.isfinite(timestamp) or timestamp <= 0:
        return "invalid"

    with locked(rundir):
        raw_applied = _read_text(rundir, APPLIED_NAME)
        old = _valid_address(raw_applied)
        current = _read_unlocked(rundir)
        action = ""
        if current and (current.get("instance") != iid
                        or current.get("engine_run_id") != owner):
            # A replacement entrypoint may have rendered the previous run's latest target as a
            # gated fallback.  Fresh confirmation of that exact address is the only event that
            # may release the inherited marker; a different address starts a new transaction.
            if (current.get("instance") == iid and old and new == old
                    and current.get("desired") == new):
                try:
                    os.unlink(_path(rundir, MARKER_NAME))
                except FileNotFoundError:
                    pass
                action = "confirmed"
            current = None
        if not action:
            original = str((current or {}).get("applied") or old)
            reserved = bool((current or {}).get("shutdown_reserved", False))
            if original and new == original:
                if not current:
                    action = "unchanged"
                elif not reserved:
                    try:
                        os.unlink(_path(rundir, MARKER_NAME))
                    except FileNotFoundError:
                        pass
                    action = "cancelled"
                else:
                    marker = {**current, "desired": new, "observed_at": timestamp,
                              "phase": "cancel_requested",
                              "previous_phase": str(current.get("phase") or "pending")}
                    _write_unlocked(rundir, marker)
                    action = "cancel_requested"
            else:
                marker = {
                    "version": 1,
                    "instance": iid,
                    "engine_run_id": owner,
                    "applied": original,
                    "desired": new,
                    "observed_at": timestamp,
                    "phase": "submitted" if reserved else "pending",
                    "shutdown_reserved": reserved,
                }
                for key in ("shutdown_reserved_at", "submit_result", "abort_reserved_at",
                            "submit_rejections", "next_submit_at", "abort_rejections",
                            "next_abort_at", "manual_required"):
                    if current and key in current:
                        marker[key] = current[key]
                _write_unlocked(rundir, marker)
                action = "coalesced" if current else "pending"

        # For a changed route, the marker is visible to Asterisk's STAT() gate before the
        # published address.  An inbound carrier leg can therefore only be ordered before the
        # transition or rejected after it; it cannot slip through a pcscf→marker write window.
        _atomic_text(_path(rundir, "pcscf"), new)
        _atomic_text(_path(rundir, DISCOVERY_NAME), json.dumps({
            "engine_run_id": owner, "address": new,
            "observed_at": timestamp}, sort_keys=True) + "\n")
        return action


def render_bootstrap(rundir: str, run_id: str, render_path: str) -> tuple[str, str]:
    """Render and commit one locked address snapshot before Asterisk starts.

    A current-run discovery wins.  Otherwise the previous run's latest desired address may be
    rendered as a fallback, but its admission marker remains until fresh SWu confirmation.
    Holding the same lock used by ``publish_discovered`` across render + applied commit closes
    both config/applied mismatch directions.
    """
    owner = str(run_id or "").strip()
    if not _RUN_ID_RE.fullmatch(owner):
        raise ValueError("invalid Engine run id")
    with locked(rundir):
        discovery = _read_discovery_unlocked(rundir, owner)
        marker = _read_unlocked(rundir)
        kind = "fresh" if discovery else "fallback" if marker else "none"
        address = str((discovery or {}).get("address") or
                      (marker or {}).get("desired") or "")
        if not address:
            return "none", ""
        environment = dict(os.environ)
        environment["MDD_PCSCF_OVERRIDE"] = address
        completed = subprocess.run(
            ["python3", render_path], env=environment, check=False, timeout=30,
            stdout=sys.stderr)
        if completed.returncode != 0:
            raise RuntimeError(f"P-CSCF {kind} render failed ({completed.returncode})")
        _atomic_text(_path(rundir, APPLIED_NAME), address)
        _atomic_text(_path(rundir, "pcscf"), address)
        if kind == "fresh":
            try:
                os.unlink(_path(rundir, MARKER_NAME))
            except FileNotFoundError:
                pass
        return kind, address


def main(argv: list[str] | None = None) -> int:
    args = list(sys.argv[1:] if argv is None else argv)
    rundir = os.environ.get("MDD_RUNDIR", "/run/mdd-sim-gateway")
    try:
        if len(args) == 2 and args[0] == "init-run":
            write_run_id(rundir, args[1])
            return 0
        if len(args) == 3 and args[0] == "render-bootstrap":
            kind, address = render_bootstrap(rundir, args[1], args[2])
            print(f"{kind} {address}".rstrip())
            return 0
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as exc:
        print(f"pcscf state error: {exc}", file=sys.stderr)
        return 1
    print("usage: pcscf_state.py init-run RUN_ID | "
          "render-bootstrap RUN_ID RENDER_PATH", file=sys.stderr)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
