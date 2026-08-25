#!/usr/bin/env python3
"""Acquire or release one durable absent-Engine start quarantine."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import time

import yaml

_SOURCE_ROOT = str(Path(__file__).resolve().parents[1])
if _SOURCE_ROOT not in sys.path:
    sys.path.insert(0, _SOURCE_ROOT)

from control.app import engine_replacement_contract  # noqa: E402
from control.app import engine_start_quarantine_contract as contract  # noqa: E402


class LineStartQuarantineError(RuntimeError):
    pass


LOCK_TIMEOUT_SECONDS = 5.0


def _strict_json(path: Path) -> object:
    try:
        value = path.lstat()
        if (not stat.S_ISREG(value.st_mode) or stat.S_ISLNK(value.st_mode)
                or value.st_uid != os.geteuid() or value.st_size > 1024 * 1024):
            raise LineStartQuarantineError(f"unsafe transaction state: {path.name}")
        with path.open(encoding="utf-8") as handle:
            return json.load(handle)
    except LineStartQuarantineError:
        raise
    except Exception as exc:
        raise LineStartQuarantineError(
            f"unreadable transaction state: {path.name}") from exc


def _instance_exists(data: Path, iid: str) -> bool:
    path = data / "config.yaml"
    try:
        before = path.lstat()
        if (not stat.S_ISREG(before.st_mode) or stat.S_ISLNK(before.st_mode)
                or before.st_uid != os.geteuid() or before.st_size > 16 * 1024 * 1024):
            raise LineStartQuarantineError("config.yaml is unsafe")
        value = yaml.safe_load(path.read_text(encoding="utf-8"))
        after = path.lstat()
    except LineStartQuarantineError:
        raise
    except Exception as exc:
        raise LineStartQuarantineError("config.yaml is unreadable") from exc
    if ((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns)
            != (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns)):
        raise LineStartQuarantineError("config.yaml changed during quarantine preflight")
    instances = value.get("instances") if isinstance(value, dict) else None
    instance = instances.get(iid) if isinstance(instances, dict) else None
    if not isinstance(instance, dict):
        return False
    if instance.get("soft_deleted"):
        raise LineStartQuarantineError("soft-deleted line cannot be quarantined for deployment")
    return True


def _canonical_docker_object_exists(iid: str, *, timeout: float = 3.0) -> bool:
    try:
        result = subprocess.run(
            ["docker", "container", "ls", "-a", "--no-trunc",
             "--format", "{{.ID}}\t{{.Names}}"],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
            text=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise LineStartQuarantineError("Docker inventory is unavailable") from exc
    if result.returncode != 0:
        raise LineStartQuarantineError("Docker inventory is unavailable")
    expected = f"mdd-sim-gateway-engine-{iid}"
    for line in result.stdout.splitlines():
        fields = line.split("\t")
        if len(fields) != 2 or not re.fullmatch(r"[0-9a-f]{64}", fields[0]):
            raise LineStartQuarantineError("Docker inventory is invalid")
        if expected in fields[1].split(","):
            return True
    return False


def _reject_other_transactions(data: Path, iid: str) -> None:
    orchestrator = data / "orchestrator"
    for name in ("maintenance-entry-fence.json", "control-upgrade.json",
                 "engine-replacement.json"):
        if os.path.lexists(orchestrator / name):
            raise LineStartQuarantineError(f"conflicting transaction exists: {name}")
    promotion = orchestrator / "engine-default-promotion.json"
    if os.path.lexists(promotion):
        try:
            value = engine_replacement_contract.validate_default_promotion(
                _strict_json(promotion))
        except Exception as exc:
            raise LineStartQuarantineError(
                "Engine default promotion state is invalid") from exc
        if value["phase"] not in {"committed", "aborted"}:
            raise LineStartQuarantineError("Engine default promotion is active")
    if os.path.lexists(data / "instances" / iid / "run" / "engine-maintenance.json"):
        raise LineStartQuarantineError("Engine maintenance is active for this line")


def acquire(data: Path, iid: str, owner_txid: str, reason: str, *,
            docker_object_exists=_canonical_docker_object_exists,
            now=lambda: int(time.time()),
            lock_timeout_seconds: float = LOCK_TIMEOUT_SECONDS) -> dict:
    iid = contract.canonical_iid(iid)
    with contract.global_lifecycle_locked(
            data, exclusive=True, timeout_seconds=lock_timeout_seconds):
        with contract.locked_lines(
                data, [iid], exclusive=True, timeout_seconds=lock_timeout_seconds):
            _reject_other_transactions(data, iid)
            if not _instance_exists(data, iid):
                raise LineStartQuarantineError("configured line does not exist")
            if contract.is_pending(data, iid):
                # Strict read supplies the actionable malformed-state error.
                contract.read_active(data, iid)
                raise LineStartQuarantineError("Engine start quarantine already exists")
            if docker_object_exists(iid):
                raise LineStartQuarantineError(
                    "canonical Engine Docker object must be completely absent")
            record, digest = contract.write_active(data, {
                "version": 1,
                "instance": iid,
                "owner": {"type": "deployment", "txid": owner_txid},
                "reason": reason,
                "created_at": int(now()),
            })
            # An external Docker owner does not share our flock.  A second bounded sample
            # cannot undo such an intrusion, but it prevents us from reporting acquisition
            # success while an object is already visible; the marker remains fail-closed.
            if docker_object_exists(iid):
                raise LineStartQuarantineError(
                    "Engine appeared during quarantine acquisition; marker retained")
            reread = contract.read_active(data, iid)
            if reread != (record, digest):
                raise LineStartQuarantineError("quarantine readback changed")
            return {"ok": True, "action": "acquired", "instance": iid,
                    "acquisition_digest": digest, "record": record}


def release(data: Path, iid: str, owner_txid: str, acquisition_digest: str, *,
            lock_timeout_seconds: float = LOCK_TIMEOUT_SECONDS) -> dict:
    iid = contract.canonical_iid(iid)
    with contract.global_lifecycle_locked(
            data, exclusive=True, timeout_seconds=lock_timeout_seconds):
        with contract.locked_lines(
                data, [iid], exclusive=True, timeout_seconds=lock_timeout_seconds):
            target = contract.release_to_tombstone(
                data, iid, owner_type="deployment", owner_txid=owner_txid,
                acquisition_digest=acquisition_digest)
            return {"ok": True, "action": "released", "instance": iid,
                    "acquisition_digest": acquisition_digest,
                    "tombstone": str(target.relative_to(data))}


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--data", required=True, type=Path)
    commands = parser.add_subparsers(dest="action", required=True)
    acquire_cmd = commands.add_parser("acquire")
    acquire_cmd.add_argument("--instance", required=True)
    acquire_cmd.add_argument("--owner-txid", required=True)
    acquire_cmd.add_argument("--reason", required=True)
    release_cmd = commands.add_parser("release")
    release_cmd.add_argument("--instance", required=True)
    release_cmd.add_argument("--owner-txid", required=True)
    release_cmd.add_argument("--acquisition-digest", required=True)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        if args.action == "acquire":
            result = acquire(args.data, args.instance, args.owner_txid, args.reason)
        else:
            result = release(
                args.data, args.instance, args.owner_txid, args.acquisition_digest)
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 0
    except (LineStartQuarantineError, contract.QuarantineContractError) as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, sort_keys=True,
                         separators=(",", ":")), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
