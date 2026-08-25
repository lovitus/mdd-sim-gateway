#!/usr/bin/env python3
"""Crash-resumable, fail-closed replacement of explicitly selected Engine containers.

This command deliberately does not build an image and never accepts a mutable tag.  The caller
must provide a canonical image ID and explicit line IDs.  Existing ``reload --engines`` remains
disabled because combining a Control reload with Engine replacement creates a second lifecycle
owner.
"""
from __future__ import annotations

import argparse
from contextlib import ExitStack, contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys
import time
import uuid

_SOURCE_ROOT = str(Path(__file__).resolve().parents[1])
if _SOURCE_ROOT not in sys.path:
    sys.path.insert(0, _SOURCE_ROOT)
from control.app import engine_replacement_contract as replacement_contract  # noqa: E402


MANIFEST_NAME = "engine-replacement.json"
LAST_MANIFEST_NAME = "engine-replacement.last.json"
UNSCOPED_REMOVALS_DIR = "engine-replacement-unscoped-removals"
SCOPED_CARD_LOSS_DIR = "engine-replacement-scoped-card-loss"
SCOPED_CARD_LOSS_UNCERTAIN_DIR = "engine-replacement-scoped-card-loss-uncertain"
LOCK_NAME = ".engine-replacement.lock"
EVENT_LOCK_NAME = ".engine-replacement-events.lock"
POSTFLIGHT_FENCE_NAME = "engine-replacement-postflight.fence"
POSTFLIGHT_RECOVERIES_DIR = "engine-replacement-postflight-recoveries"
DEFAULT_PROMOTION_NAME = "engine-default-promotion.json"
DEFAULT_PROMOTION_HISTORY_DIR = "engine-default-promotions"
POSTFLIGHT_DENY_REASONS = {
    "engine_replacement_postflight_failed",
    "line_engine_replacement_postflight_failed",
}
IID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$")
IMAGE_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
TERMINAL_LINES = {"verified", "rollback_verified", "aborted", "skipped_absent"}
GATE_KINDS = ("call_in", "call_out", "media_check", "sms_in", "sms_out")
MARKER_PREDECESSORS = {
    "prepared": {"pending"},
    "source_quiescing": {"prepared"},
    "source_removed": {"source_quiescing"},
    "target_starting": {"source_removed"},
    "target_started": {"target_starting"},
    "verified": {"target_started"},
    "rollback_starting": {
        "source_removed", "target_starting", "target_started", "rollback_required",
        "manual_required", "pending",
    },
    "rollback_started": {"rollback_starting"},
    "rollback_verified": {"rollback_started"},
    "aborted": {"prepared", "source_quiescing", "rollback_required"},
}


class ReplacementError(RuntimeError):
    pass


class ReplacementManualRequired(ReplacementError):
    pass


class ReplacementDirectManual(ReplacementError):
    """The observed start outcome must be fenced, never classified or rolled back."""


class ReplacementPostflightError(ReplacementError):
    pass


def validate_manifest(value: object) -> dict:
    try:
        return replacement_contract.validate_manifest(value)
    except replacement_contract.ContractError as exc:
        raise ReplacementError(str(exc)) from exc


def read_manifest(path: Path) -> dict:
    try:
        return validate_manifest(json.loads(Path(path).read_text(encoding="utf-8")))
    except ReplacementError:
        raise
    except Exception as exc:
        raise ReplacementError("Engine replacement manifest is unreadable") from exc


def _atomic_json(path: Path, value: dict) -> None:
    checked = validate_manifest(value)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.{uuid.uuid4().hex}.tmp")
    payload = json.dumps(checked, ensure_ascii=False, sort_keys=True,
                         separators=(",", ":")) + "\n"
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(payload)
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


def _atomic_fence(path: Path, value: dict) -> None:
    """Durably publish a small local fail-closed fence."""
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.{uuid.uuid4().hex}.tmp")
    payload = json.dumps(value, ensure_ascii=False, sort_keys=True,
                         separators=(",", ":")) + "\n"
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(payload)
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


def _durable_unlink(path: Path) -> None:
    try:
        path.unlink()
    except FileNotFoundError:
        return
    directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


class EngineReplacement:
    def __init__(self, repo: Path, data: Path, iids: list[str], candidate: str,
                 *, health_timeout: float = 180.0, quiet_seconds: float = 2.0,
                 recover_created: dict[str, str] | None = None,
                 recover_unscoped: dict[str, str] | None = None,
                 recovery_evidence: Path | None = None,
                 recover_postflight: str | None = None,
                 postflight_recovery_evidence: Path | None = None,
                 recover_postflight_added: dict[str, str] | None = None,
                 recover_precreate_missing_target: str | None = None,
                 promote_default: bool = False):
        self.repo = Path(repo).resolve()
        self.data = Path(data).resolve()
        self.iids = sorted(iids)
        self.candidate = candidate
        self.health_timeout = float(health_timeout)
        self.quiet_seconds = float(quiet_seconds)
        self.recover_created = dict(recover_created or {})
        self.recover_unscoped = dict(recover_unscoped or {})
        self.recovery_evidence = (Path(recovery_evidence)
                                  if recovery_evidence is not None else None)
        self.recover_postflight = str(recover_postflight or "")
        self.postflight_recovery_evidence = (
            Path(postflight_recovery_evidence)
            if postflight_recovery_evidence is not None else None)
        self.recover_postflight_added = dict(recover_postflight_added or {})
        self.recover_precreate_missing_target = str(
            recover_precreate_missing_target or "")
        self.promote_default = bool(promote_default)
        self.authorized_forensic_iids: set[str] = set()
        self.orchestrator = self.data / "orchestrator"
        self.manifest_path = self.orchestrator / MANIFEST_NAME
        self.last_manifest_path = self.orchestrator / LAST_MANIFEST_NAME
        self.lock_path = self.orchestrator / LOCK_NAME
        self.database = self.data / "mdd-sim-gateway.sqlite"
        self.engine = self.cfg = self.admission_gate = self.guard = None
        self.client = None

    def _load_runtime(self) -> None:
        os.environ["MDD_DATA"] = str(self.data)
        os.environ["MDD_HOST_DATA"] = str(self.data)
        root = str(self.repo)
        if root not in sys.path:
            sys.path.insert(0, root)
        from control.app import config, engine  # pylint: disable=import-outside-toplevel
        from engine import admission_gate  # pylint: disable=import-outside-toplevel
        from host import mdd_upgrade_guard  # pylint: disable=import-outside-toplevel
        import docker  # pylint: disable=import-outside-toplevel
        self.cfg, self.engine = config, engine
        self.admission_gate, self.guard = admission_gate, mdd_upgrade_guard
        self.client = docker.from_env(timeout=5)

    def _line(self, manifest: dict, iid: str) -> dict:
        return next(line for line in manifest["lines"] if line["iid"] == iid)

    def _save(self, manifest: dict, *, phase: str | None = None) -> dict:
        updated = json.loads(json.dumps(manifest))
        if phase is not None:
            updated["phase"] = phase
        updated["updated_at"] = time.time()
        _atomic_json(self.manifest_path, updated)
        return validate_manifest(updated)

    def _snapshot_containers(self, excluded: set[str]) -> list[dict]:
        try:
            return replacement_contract.snapshot_unscoped_engines(
                self.client, set(excluded))
        except replacement_contract.ContractError as exc:
            raise ReplacementError(str(exc)) from exc

    @contextmanager
    def _event_locked(self):
        """Linearize final commit against card-loss intent and containment."""
        path = self.orchestrator / EVENT_LOCK_NAME
        fd = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
        handle = os.fdopen(fd, "r+")
        try:
            fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
            yield
        finally:
            try:
                fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
            finally:
                handle.close()

    def _new_manifest(self) -> dict:
        resolved = self.engine._require_engine_admission_abi(  # noqa: SLF001
            self.client, self.candidate)
        if resolved != self.candidate:
            raise ReplacementError("candidate image did not resolve immutably")
        lines = []
        for iid in self.iids:
            try:
                facts = self.engine.engine_generation_facts(iid)
            except self.engine.docker.errors.NotFound:
                lines.append({"iid": iid, "phase": "skipped_absent", "source": None,
                              "terminal": None, "error": ""})
                continue
            source_image = self.client.images.get(facts["image_id"])
            labels = ((source_image.attrs or {}).get("Config") or {}).get("Labels") or {}
            if labels.get(self.engine.ENGINE_ADMISSION_ABI_LABEL) != \
                    self.engine.ENGINE_ADMISSION_ABI:
                raise ReplacementError(f"line {iid} source lacks the admission ABI")
            lines.append({"iid": iid, "phase": "pending", "source": facts,
                          "terminal": None, "error": ""})
        now = time.time()
        return validate_manifest({
            "version": 2, "txid": f"engine-replace-{int(now)}-{uuid.uuid4().hex[:12]}",
            "phase": "prepared", "candidate_image": self.candidate,
            "promote_default": self.promote_default,
            "iids": self.iids, "started_at": now, "updated_at": now,
            "unscoped": self._snapshot_containers(set(self.iids)), "lines": lines,
        })

    def _load_or_create(self) -> dict:
        if self.manifest_path.exists():
            manifest = read_manifest(self.manifest_path)
            if manifest["iids"] != self.iids or manifest["candidate_image"] != self.candidate:
                raise ReplacementManualRequired(
                    "another Engine replacement transaction owns the durable manifest")
            persisted_promotion = bool(manifest.get("promote_default", False))
            if self.promote_default and not persisted_promotion:
                raise ReplacementManualRequired(
                    "default promotion cannot be added to an active replacement")
            self.promote_default = persisted_promotion
            if self.promote_default:
                self._ensure_default_promotion_prepared(manifest)
            return manifest
        manifest = self._new_manifest()
        _atomic_json(self.manifest_path, manifest)
        if self.promote_default:
            self._ensure_default_promotion_prepared(manifest)
        return manifest

    def _rollback_ref(self, manifest: dict, iid: str) -> str:
        token = re.sub(r"[^A-Za-z0-9_.-]", "-", manifest["txid"])[-80:]
        return f"mdd-sim-gateway/engine-rollback:{token}-{iid}"

    def _retain_source_image(self, manifest: dict, line: dict) -> str:
        source = line["source"]
        reference = self._rollback_ref(manifest, line["iid"])
        repository, tag = reference.rsplit(":", 1)
        image = self.client.images.get(source["image_id"])
        if not image.tag(repository, tag=tag, force=True):
            raise ReplacementError("Docker refused the rollback retention tag")
        if str(self.client.images.get(reference).id) != source["image_id"]:
            raise ReplacementError("rollback retention tag did not bind the source image")
        return reference

    @property
    def _default_promotion_path(self) -> Path:
        return self.orchestrator / DEFAULT_PROMOTION_NAME

    def _default_promotion_rollback_ref(self, manifest: dict) -> str:
        token = re.sub(r"[^A-Za-z0-9_.-]", "-", manifest["txid"])[-80:]
        return f"mdd-sim-gateway/engine-rollback:{token}-default"

    @staticmethod
    def _split_image_ref(reference: str) -> tuple[str, str]:
        if not isinstance(reference, str) or not reference or "@" in reference \
                or reference.startswith("sha256:"):
            raise ReplacementManualRequired("installed Engine image reference is not taggable")
        slash, colon = reference.rfind("/"), reference.rfind(":")
        if colon > slash:
            repository, tag = reference[:colon], reference[colon + 1:]
        else:
            repository, tag = reference, "latest"
        if not repository or not tag:
            raise ReplacementManualRequired("installed Engine image reference is invalid")
        return repository, tag

    def _read_default_promotion(self, manifest: dict | None = None) -> dict | None:
        try:
            value = json.loads(self._default_promotion_path.read_text(encoding="utf-8"))
        except FileNotFoundError:
            return None
        except Exception as exc:
            raise ReplacementManualRequired(
                "Engine default promotion receipt is unreadable") from exc
        try:
            return replacement_contract.validate_default_promotion(value, manifest)
        except replacement_contract.ContractError as exc:
            raise ReplacementManualRequired(str(exc)) from exc

    def _save_default_promotion(self, manifest: dict, value: dict) -> dict:
        try:
            checked = replacement_contract.validate_default_promotion(value, manifest)
        except replacement_contract.ContractError as exc:
            raise ReplacementManualRequired(str(exc)) from exc
        _atomic_fence(self._default_promotion_path, checked)
        reread = self._read_default_promotion(manifest)
        if reread != checked:
            raise ReplacementManualRequired(
                "Engine default promotion receipt readback mismatch")
        return checked

    def _archive_previous_default_promotion(self, value: dict) -> None:
        if value["phase"] not in {"committed", "aborted"}:
            raise ReplacementManualRequired(
                "another Engine default promotion is still in flight")
        history = self.orchestrator / DEFAULT_PROMOTION_HISTORY_DIR / f"{value['txid']}.json"
        if history.exists():
            try:
                existing = replacement_contract.validate_default_promotion(
                    json.loads(history.read_text(encoding="utf-8")))
            except Exception as exc:
                raise ReplacementManualRequired(
                    "Engine default promotion history is unreadable") from exc
            if existing != value:
                raise ReplacementManualRequired(
                    "Engine default promotion history conflicts")
        else:
            _atomic_fence(history, value)
        _durable_unlink(self._default_promotion_path)

    def _ensure_default_promotion_prepared(self, manifest: dict) -> dict:
        if manifest.get("promote_default") is not True:
            raise ReplacementManualRequired("manifest does not own default promotion")
        existing = self._read_default_promotion()
        if existing is not None and existing["txid"] != manifest["txid"]:
            self._archive_previous_default_promotion(existing)
            existing = None
        if existing is not None:
            return self._read_default_promotion(manifest)
        default_ref = str(self.engine.IMAGE)
        self._split_image_ref(default_ref)
        try:
            previous = str(self.client.images.get(default_ref).id)
        except Exception as exc:
            raise ReplacementManualRequired(
                "installed Engine default image is unavailable") from exc
        if not IMAGE_RE.fullmatch(previous):
            raise ReplacementManualRequired(
                "installed Engine default did not resolve immutably")
        now = time.time()
        return self._save_default_promotion(manifest, {
            "version": 1, "txid": manifest["txid"],
            "scope_digest": replacement_contract.replacement_scope_digest(manifest),
            "candidate_image": manifest["candidate_image"],
            "default_ref": default_ref, "previous_image": previous,
            "rollback_ref": self._default_promotion_rollback_ref(manifest),
            "phase": "prepared", "created_at": now, "updated_at": now,
        })

    def _transition_default_promotion(self, manifest: dict, receipt: dict,
                                      phase: str) -> dict:
        updated = dict(receipt)
        updated["phase"] = phase
        updated["updated_at"] = time.time()
        return self._save_default_promotion(manifest, updated)

    def _require_default_ref(self, receipt: dict, expected: str) -> None:
        try:
            actual = str(self.client.images.get(receipt["default_ref"]).id)
        except Exception as exc:
            raise ReplacementManualRequired(
                "installed Engine default image is unavailable") from exc
        if actual != expected:
            raise ReplacementManualRequired(
                "installed Engine default image changed outside the transaction")

    def _require_default_rollback(self, receipt: dict) -> None:
        try:
            retained = str(self.client.images.get(receipt["rollback_ref"]).id)
        except Exception as exc:
            raise ReplacementManualRequired(
                "Engine default rollback image is unavailable") from exc
        if retained != receipt["previous_image"]:
            raise ReplacementManualRequired(
                "Engine default rollback image changed")

    def _prepare_default_promotion_commit(self, manifest: dict) -> None:
        """Promote only behind commit_ready; crash states remain normal-start fenced."""
        if not self.promote_default:
            return
        receipt = self._read_default_promotion(manifest)
        if receipt is None:
            raise ReplacementManualRequired("Engine default promotion receipt disappeared")
        promotable = all(line["phase"] in {"verified", "skipped_absent"}
                         for line in manifest["lines"])
        if not promotable:
            self._require_default_ref(receipt, receipt["previous_image"])
            self._transition_default_promotion(manifest, receipt, "aborted")
            return
        for line in manifest["lines"]:
            if line["phase"] == "skipped_absent":
                continue
            current = self.engine.engine_generation_facts(line["iid"])
            if current != line["terminal"] or current["image_id"] != manifest["candidate_image"]:
                raise ReplacementManualRequired(
                    f"line {line['iid']} changed before default promotion")
        if receipt["phase"] == "prepared":
            try:
                source = self.client.images.get(receipt["previous_image"])
                repository, tag = self._split_image_ref(receipt["rollback_ref"])
                if not source.tag(repository, tag=tag, force=True):
                    raise ReplacementError("Docker refused the default rollback tag")
            except Exception:
                # Docker may have committed the tag but lost the response. Only exact readback
                # decides whether it is safe to advance.
                self._require_default_rollback(receipt)
            self._require_default_rollback(receipt)
            receipt = self._transition_default_promotion(
                manifest, receipt, "old_default_retained")
        if receipt["phase"] == "old_default_retained":
            self._require_default_rollback(receipt)
            try:
                current = str(self.client.images.get(receipt["default_ref"]).id)
            except Exception as exc:
                raise ReplacementManualRequired(
                    "installed Engine default image is unavailable") from exc
            if current == receipt["previous_image"]:
                try:
                    target = self.client.images.get(receipt["candidate_image"])
                    repository, tag = self._split_image_ref(receipt["default_ref"])
                    if not target.tag(repository, tag=tag, force=True):
                        raise ReplacementError("Docker refused the Engine default promotion")
                except Exception:
                    self._require_default_ref(receipt, receipt["candidate_image"])
            elif current != receipt["candidate_image"]:
                raise ReplacementManualRequired(
                    "installed Engine default changed before promotion")
            self._require_default_ref(receipt, receipt["candidate_image"])
            resolved = self.engine._require_engine_admission_abi(  # noqa: SLF001
                self.client, receipt["default_ref"])
            if resolved != receipt["candidate_image"]:
                raise ReplacementManualRequired(
                    "promoted Engine default does not resolve to the candidate")
            receipt = self._transition_default_promotion(
                manifest, receipt, "global_promoted")
        if receipt["phase"] != "global_promoted":
            raise ReplacementManualRequired(
                "Engine default promotion is not ready for postflight")

    def _commit_default_promotion(self, manifest: dict) -> None:
        if not self.promote_default:
            return
        receipt = self._read_default_promotion(manifest)
        if receipt is None:
            raise ReplacementManualRequired("Engine default promotion receipt disappeared")
        if receipt["phase"] == "aborted":
            self._require_default_ref(receipt, receipt["previous_image"])
            return
        if receipt["phase"] == "committed":
            self._require_default_ref(receipt, receipt["candidate_image"])
            self._require_default_rollback(receipt)
            return
        if receipt["phase"] != "global_promoted":
            raise ReplacementManualRequired(
                "Engine default promotion requires explicit recovery")
        self._require_default_ref(receipt, receipt["candidate_image"])
        self._require_default_rollback(receipt)
        self._transition_default_promotion(manifest, receipt, "committed")

    def _mark_default_promotion_manual(self, manifest: dict) -> None:
        if not self.promote_default:
            return
        receipt = self._read_default_promotion(manifest)
        if receipt is not None and receipt["phase"] not in {"committed", "aborted"}:
            self._transition_default_promotion(manifest, receipt, "manual_required")

    def _resume_default_promotion_postflight(self, manifest: dict) -> None:
        """Exact postflight recovery may re-arm only a fully applied global promotion."""
        if not self.promote_default:
            return
        receipt = self._read_default_promotion(manifest)
        if receipt is None:
            raise ReplacementManualRequired("Engine default promotion receipt disappeared")
        if receipt["phase"] != "manual_required":
            return
        self._require_default_ref(receipt, receipt["candidate_image"])
        self._require_default_rollback(receipt)
        self._transition_default_promotion(manifest, receipt, "global_promoted")

    def _probe_gate(self, iid: str, allowed: bool) -> bool:
        socket_path = self.data / "instances" / iid / "run" / "admission-gate.sock"
        try:
            for kind in GATE_KINDS:
                result = self.admission_gate.probe(socket_path, kind, timeout=0.5)
                if result.get("allowed") is not allowed:
                    return False
            return True
        except Exception:
            return False

    def _wait_gate(self, iid: str, allowed: bool, timeout: float = 20.0) -> None:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self._probe_gate(iid, allowed):
                return
            time.sleep(0.25)
        state = "ALLOW" if allowed else "DENY"
        raise ReplacementError(f"line {iid} admission gate did not prove {state}")

    def _normal_allow(self, iid: str) -> bool:
        run = self.data / "instances" / iid / "run"
        try:
            authority = json.loads((run / "admission-authority-status.json").read_text())
            gate = json.loads((run / "admission-gate-status.json").read_text())
            identity = authority.get("authority_identity_digest")
            state_digest = authority.get("normal_state_digest")
            return bool(
                authority.get("healthy") is True and authority.get("state") == "allow"
                and gate.get("state") == "allow" and isinstance(identity, str) and identity
                and gate.get("authority_identity_digest") == identity
                and isinstance(state_digest, str) and state_digest
                and gate.get("normal_state_digest") == state_digest
                and type(authority.get("authority_epoch")) is int
                and gate.get("authority_epoch") == authority["authority_epoch"]
                and type(authority.get("lease_seq")) is int
                and type(gate.get("lease_seq")) is int
                and gate["lease_seq"] >= authority["lease_seq"]
                and self._probe_gate(iid, True))
        except Exception:
            return False

    def _wait_normal_allow(self, iid: str, timeout: float = 120.0) -> None:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self._normal_allow(iid):
                return
            time.sleep(0.5)
        raise ReplacementPostflightError(
            f"line {iid} normal admission authority did not become healthy")

    def _paid_zero(self) -> None:
        values = self.guard.pending_paid_work(self.database)
        if any(values.values()):
            raise ReplacementError(f"paid work is not idle: {values}")

    def _zero_channels(self, iids: list[str]) -> None:
        for iid in iids:
            count = self.engine.active_channel_count(iid)
            if count != 0:
                raise ReplacementError(
                    f"line {iid} active channel state is not authoritative zero: {count}")

    def _generation(self, iid: str, image: str,
                    expected_container_id: str | None = None) -> dict:
        facts = self.engine.engine_generation_facts(
            iid, expected_container_id=expected_container_id)
        if facts["image_id"] != image:
            raise ReplacementError(f"line {iid} runs an unexpected image")
        return facts

    def _wait_started_generation(self, iid: str, image: str,
                                 container_id: str, timeout: float = 30.0) -> dict:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                facts = self._generation(iid, image, container_id)
                if facts["restart_count"] != 0:
                    raise ReplacementError(
                        f"line {iid} target restarted before generation commit")
                return facts
            except (self.engine.docker.errors.NotFound,
                    self.engine.EngineRunIdUnavailable):
                time.sleep(0.1)
        raise ReplacementError(
            f"line {iid} target run id was not published before the bounded deadline")

    def _healthy(self, iid: str, image: str, *,
                 require_media_websocket: bool) -> dict:
        deadline = time.monotonic() + self.health_timeout
        stable = None
        stable_at = 0.0
        while time.monotonic() < deadline:
            try:
                facts = self._generation(iid, image)
                usim = self.engine.read_run_json(iid, "usim_status.json") or {}
                swu = self.engine.read_run_json(iid, "swu_status.json") or {}
                channels = self.engine.active_channel_count(iid)
                ready = (self._probe_gate(iid, False)
                         and usim.get("state") == "AUTH_OK"
                         and swu.get("state") == "CONNECTED"
                         and self.engine.registration_state(iid) == "Registered"
                         and channels == 0
                         and (not require_media_websocket
                              or self.engine.media_websocket_runtime_ready(
                                  iid, facts["container_id"])))
                if ready:
                    if stable == facts and time.monotonic() - stable_at >= 2.0:
                        return facts
                    if stable != facts:
                        stable, stable_at = facts, time.monotonic()
                else:
                    stable, stable_at = None, 0.0
            except Exception:
                stable, stable_at = None, 0.0
            time.sleep(0.5)
        raise ReplacementError(f"line {iid} candidate health did not stabilize")

    def _sync_line(self, manifest: dict, iid: str, marker: dict | None,
                   *, error: str | None = None) -> dict:
        line = self._line(manifest, iid)
        if marker is not None:
            line["phase"] = marker["phase"]
            if marker["phase"] == "verified":
                line["terminal"] = marker["target"]
            elif marker["phase"] == "rollback_verified":
                line["terminal"] = marker["rollback"]
            elif marker["phase"] == "aborted":
                line["terminal"] = None
        if error is not None:
            line["error"] = str(error)[:500]
        return self._save(manifest, phase="running")

    def _latch_manifest_manual(self, manifest: dict, error: Exception | str,
                               iid: str | None = None) -> dict:
        updated = json.loads(json.dumps(manifest))
        if iid is not None:
            self._line(updated, iid)["error"] = str(error)[:500]
        return self._save(updated, phase="manual_required")

    def _fail_manifest_manual(self, manifest: dict, error: Exception | str,
                              iid: str | None = None) -> None:
        self._latch_manifest_manual(manifest, error, iid)
        prefix = f"line {iid} " if iid is not None else ""
        raise ReplacementManualRequired(f"{prefix}requires manual recovery: {error}")

    def _reconcile_marker(self, manifest: dict, iid: str,
                          marker: dict | None = None) -> tuple[dict, dict | None]:
        """Reconcile the one-record crash window without accepting phase leaps."""
        line = self._line(manifest, iid)
        if marker is None:
            marker = self.engine.read_engine_maintenance(iid)
        if marker is None:
            if line["phase"] in TERMINAL_LINES and manifest["phase"] == "commit_ready":
                return manifest, None
            if line["phase"] == "pending" or line["phase"] == "skipped_absent":
                return manifest, None
            self._fail_manifest_manual(manifest, "line maintenance marker disappeared", iid)
        if (marker.get("txid") != manifest["txid"]
                or str(marker.get("instance")) != iid
                or marker.get("source") != line.get("source")
                or marker.get("target_image_digest") != manifest["candidate_image"]
                or marker.get("rollback_image_ref") != self._rollback_ref(manifest, iid)):
            self._fail_manifest_manual(manifest, "line maintenance scope mismatch", iid)
        marker_phase = marker["phase"]
        line_phase = line["phase"]
        if marker_phase == "manual_required":
            line["phase"] = "manual_required"
            self._fail_manifest_manual(manifest, "line marker is manual-required", iid)
        if marker_phase == line_phase:
            expected_terminal = (marker.get("target") if marker_phase == "verified"
                                 else marker.get("rollback")
                                 if marker_phase == "rollback_verified" else None)
            if marker_phase in TERMINAL_LINES and line.get("terminal") != expected_terminal:
                self._fail_manifest_manual(manifest, "terminal line facts disagree", iid)
            return manifest, marker
        if line_phase not in MARKER_PREDECESSORS.get(marker_phase, set()):
            self._fail_manifest_manual(
                manifest, f"invalid line marker advance {line_phase}->{marker_phase}", iid)
        return self._sync_line(manifest, iid, marker), marker

    def _current_generation(self, iid: str) -> dict | None:
        try:
            return self.engine.engine_generation_facts(iid)
        except self.engine.docker.errors.NotFound:
            return None

    def _line_is_present(self, manifest: dict, iid: str,
                         marker: dict | None = None) -> bool:
        """Prove whether the scoped Docker name is safely present or safely absent."""
        line = self._line(manifest, iid)
        current = self._current_generation(iid)
        phase = marker["phase"] if marker is not None else line["phase"]
        if phase == "skipped_absent":
            if current is not None:
                self._fail_manifest_manual(manifest, "an absent scoped line appeared", iid)
            return False
        if marker is None:
            if phase == "pending":
                expected = line["source"]
            elif manifest["phase"] == "commit_ready" and phase in TERMINAL_LINES:
                expected = (line["terminal"] if phase in {
                    "verified", "rollback_verified"} else line["source"])
            else:
                self._fail_manifest_manual(manifest, "line generation is unowned", iid)
        elif phase in {"prepared", "source_quiescing", "rollback_required", "aborted"}:
            expected = marker["source"]
        elif phase in {"target_started", "verified"}:
            expected = marker["target"]
        elif phase in {"rollback_started", "rollback_verified"}:
            expected = marker["rollback"]
        elif phase == "source_removed":
            if current is not None:
                self._fail_manifest_manual(
                    manifest, "source_removed phase still has a container", iid)
            return False
        elif phase in {"target_starting", "rollback_starting"}:
            if current is None:
                return False
            expected_image = (marker["target_image_digest"] if phase == "target_starting"
                              else marker["source"]["image_id"])
            occupied = {marker["source"]["container_id"]}
            if marker.get("target") is not None:
                occupied.add(marker["target"]["container_id"])
            if (current["image_id"] != expected_image
                    or current["container_id"] in occupied
                    or current["run_id"] == marker["source"]["run_id"]):
                self._fail_manifest_manual(
                    manifest, f"{phase} has an incompatible container", iid)
            return True
        else:
            self._fail_manifest_manual(manifest, f"unsupported line phase {phase}", iid)
        if current is None or current != expected:
            self._fail_manifest_manual(manifest, f"{phase} generation changed", iid)
        return True

    def _prepare_line_gate(self, manifest: dict, iid: str) -> tuple[dict, bool]:
        manifest, marker = self._reconcile_marker(manifest, iid)
        return manifest, self._line_is_present(manifest, iid, marker)

    def _zero_current_channels(self, manifest: dict) -> None:
        for iid in self.iids:
            marker = self.engine.read_engine_maintenance(iid)
            if self._line_is_present(manifest, iid, marker):
                count = self.engine.active_channel_count(iid)
                if count != 0:
                    raise ReplacementError(
                        f"line {iid} active channel state is not authoritative zero: {count}")

    def _zero_current_or_manual(self, manifest: dict) -> None:
        try:
            self._zero_current_channels(manifest)
        except Exception as exc:
            self._fail_manifest_manual(manifest, exc)

    def _manual(self, manifest: dict, iid: str, error: Exception | str) -> None:
        message = str(error)[:500]
        try:
            marker = self.engine.read_engine_maintenance(iid)
            if marker is None or marker["txid"] != manifest["txid"]:
                self._fail_manifest_manual(manifest, message, iid)
            if marker and marker["phase"] != "manual_required":
                marker = self.engine.transition_engine_maintenance(
                    iid, manifest["txid"], marker["phase"], "manual_required")
            manifest = self._sync_line(manifest, iid, marker, error=message)
        except Exception:
            line = self._line(manifest, iid)
            line["phase"] = "manual_required"
            line["error"] = message
        self._save(manifest, phase="manual_required")
        raise ReplacementManualRequired(f"line {iid} requires manual recovery: {message}")

    def _create_or_classify_id(self, iid: str, image: str,
                               txid: str, intent: str) -> str:
        """Perform only the bounded local absent-create and exact receipt readback.

        The caller may hold the shared event lock here. This function never pulls/builds,
        waits for health, sends REGISTER, or retries an unknown Docker create outcome.
        """
        if intent == "target":
            try:
                receipt = self.engine.read_engine_start_receipt(iid)
            except Exception as exc:
                raise ReplacementDirectManual(
                    f"line {iid} target start receipt is unreadable") from exc
            if receipt is not None:
                marker = self.engine.read_engine_maintenance(iid)
                if (receipt.get("txid") != txid or receipt.get("intent") != "target"
                        or receipt.get("image_id") != image
                        or receipt.get("attestation") != "tx_label"
                        or marker is None or marker.get("txid") != txid
                        or receipt.get("source_create_spec_digest") !=
                            marker.get("source_create_spec_digest")):
                    raise ReplacementDirectManual(
                        f"line {iid} target start receipt does not own this transaction")
                container_id = receipt["container_id"]
            else:
                try:
                    container_id = self.engine.start_absent_from_snapshot(
                        iid, image, txid, intent=intent)
                except self.engine.EngineStartReceiptError:
                    raise
                except Exception as exc:
                    raise ReplacementDirectManual(
                        f"line {iid} target create did not return a durable receipt: {exc}") \
                        from exc
                try:
                    receipt = self.engine.read_engine_start_receipt(iid)
                except Exception as exc:
                    raise ReplacementDirectManual(
                        f"line {iid} target start receipt is unreadable") from exc
                if (receipt is None or receipt.get("txid") != txid
                        or receipt.get("container_id") != container_id
                        or receipt.get("image_id") != image
                        or receipt.get("attestation") != "tx_label"):
                    raise ReplacementDirectManual(
                        "Engine target start receipt readback is not exact")
            return container_id
        try:
            return self.engine.start_absent_from_snapshot(
                iid, image, txid, intent=intent)
        except Exception as exc:
            raise ReplacementDirectManual(
                f"line {iid} rollback create outcome is unknown: {exc}") from exc

    def _wait_created_generation(self, iid: str, image: str,
                                 container_id: str, intent: str) -> dict:
        try:
            return self._wait_started_generation(iid, image, container_id)
        except Exception as exc:
            raise ReplacementDirectManual(
                f"line {iid} {intent} generation acquisition failed: {exc}") from exc

    def _start_or_classify(self, iid: str, image: str, txid: str, intent: str) -> dict:
        container_id = self._create_or_classify_id(iid, image, txid, intent)
        return self._wait_created_generation(iid, image, container_id, intent)

    def _candidate_for_rollback(self, iid: str, marker: dict) -> dict | None:
        try:
            facts = self.engine.engine_generation_facts(iid)
        except self.engine.docker.errors.NotFound:
            return None
        if facts["image_id"] != marker["target_image_digest"]:
            raise ReplacementError("a non-candidate container occupies the Engine name")
        receipt = self.engine.read_engine_start_receipt(iid)
        if (receipt is None or receipt.get("txid") != marker["txid"]
                or receipt.get("container_id") != facts["container_id"]
                or receipt.get("image_id") != facts["image_id"]):
            raise ReplacementError(
                "candidate rollback requires the exact target start receipt")
        return facts

    def _remove_candidate_for_rollback(self, iid: str, marker: dict,
                                       expected: dict | None) -> None:
        facts = self._candidate_for_rollback(iid, marker)
        if facts is None:
            if expected is not None:
                raise ReplacementError("candidate disappeared before rollback stop")
            return
        if expected is None or facts != expected:
            raise ReplacementError("candidate changed before rollback stop")
        self._wait_gate(iid, False, timeout=5.0)
        if self.engine.active_channel_count(iid) != 0:
            raise ReplacementError("candidate channel state is not zero for rollback")
        inst = self.cfg.get_instance(iid) or {"id": iid}
        stopped = self.engine.capture_and_stop_if_idle(
            iid, inst, "engine-replacement-rollback", facts["container_id"])
        if stopped.get("status") != "stopped":
            raise ReplacementError(
                f"candidate could not be safely removed for rollback: {stopped}")

    def _rollback(self, manifest: dict, iid: str, cause: Exception) -> dict:
        try:
            with self._scoped_mutation_locked(manifest, iid):
                pass
            marker = self.engine.read_engine_maintenance(iid)
            if marker is None:
                self._manual(manifest, iid, cause)
            phase = marker["phase"]
            if phase in {"prepared", "source_quiescing", "rollback_required"}:
                try:
                    with self._scoped_mutation_locked(manifest, iid):
                        marker = self.engine.transition_engine_maintenance(
                            iid, marker["txid"], phase, "aborted")
                        return self._sync_line(manifest, iid, marker, error=str(cause))
                except ReplacementDirectManual:
                    raise
                except Exception:
                    if phase == "source_quiescing":
                        try:
                            with self._scoped_mutation_locked(manifest, iid):
                                self.engine.transition_engine_maintenance(
                                    iid, marker["txid"], phase, "source_removed")
                                marker = self.engine.read_engine_maintenance(iid)
                                phase = marker["phase"]
                        except ReplacementDirectManual:
                            raise
                        except Exception:
                            self._manual(manifest, iid, cause)
                    else:
                        self._manual(manifest, iid, cause)
            if phase in {"target_starting", "target_started"}:
                try:
                    with self._scoped_mutation_locked(manifest, iid):
                        expected_candidate = self._candidate_for_rollback(iid, marker)
                    self._remove_candidate_for_rollback(
                        iid, marker, expected_candidate)
                    with self._scoped_mutation_locked(manifest, iid):
                        try:
                            self.engine.engine_generation_facts(iid)
                        except self.engine.docker.errors.NotFound:
                            pass
                        else:
                            raise ReplacementDirectManual(
                                f"line {iid} candidate stop did not prove exact absence")
                        marker = self.engine.transition_engine_maintenance(
                            iid, marker["txid"], phase, "rollback_starting")
                        manifest = self._sync_line(
                            manifest, iid, marker, error=str(cause))
                        phase = marker["phase"]
                except ReplacementDirectManual:
                    raise
                except Exception as exc:
                    self._manual(manifest, iid, exc)
            marker = self.engine.read_engine_maintenance(iid)
            phase = marker["phase"]
            if phase in {"source_removed", "rollback_required"}:
                with self._scoped_mutation_locked(manifest, iid):
                    marker = self.engine.transition_engine_maintenance(
                        iid, marker["txid"], phase, "rollback_starting")
                    manifest = self._sync_line(manifest, iid, marker, error=str(cause))
            if marker["phase"] == "rollback_starting":
                try:
                    with self._scoped_mutation_locked(manifest, iid):
                        container_id = self._create_or_classify_id(
                            iid, marker["source"]["image_id"], marker["txid"],
                            "rollback")
                    facts = self._wait_created_generation(
                        iid, marker["source"]["image_id"], container_id, "rollback")
                    with self._scoped_mutation_locked(manifest, iid):
                        if self.engine.engine_generation_facts(
                                iid, container_id) != facts:
                            raise ReplacementDirectManual(
                                f"line {iid} rollback generation changed before transition")
                        marker = self.engine.transition_engine_maintenance(
                            iid, marker["txid"], "rollback_starting", "rollback_started",
                            rollback=facts)
                        manifest = self._sync_line(
                            manifest, iid, marker, error=str(cause))
                except ReplacementDirectManual:
                    raise
                except Exception as exc:
                    self._manual(manifest, iid, exc)
            if marker["phase"] == "rollback_started":
                try:
                    facts = self._healthy(
                        iid, marker["source"]["image_id"],
                        require_media_websocket=False)
                    with self._scoped_mutation_locked(manifest, iid):
                        if self.engine.engine_generation_facts(
                                iid, facts["container_id"]) != facts:
                            raise ReplacementDirectManual(
                                f"line {iid} rollback generation changed before verification")
                        marker = self.engine.transition_engine_maintenance(
                            iid, marker["txid"], "rollback_started", "rollback_verified",
                            rollback=facts)
                        return self._sync_line(
                            manifest, iid, marker, error=str(cause))
                except ReplacementDirectManual:
                    raise
                except Exception as exc:
                    self._manual(manifest, iid, exc)
            return manifest
        except ReplacementDirectManual as exc:
            self._manual(manifest, iid, exc)

    def _drive_line(self, manifest: dict, iid: str) -> dict:
        line = self._line(manifest, iid)
        marker = self.engine.read_engine_maintenance(iid)
        try:
            with self._scoped_mutation_locked(manifest, iid):
                pass
            manifest, marker = self._reconcile_marker(manifest, iid, marker)
            line = self._line(manifest, iid)
            if line["phase"] in TERMINAL_LINES:
                return manifest
            if marker is None:
                with self._scoped_mutation_locked(manifest, iid):
                    if line["phase"] != "pending":
                        raise ReplacementError("line maintenance marker disappeared")
                    source = line["source"]
                    if self.engine.engine_generation_facts(
                            iid, source["container_id"]) != source:
                        raise ReplacementError("source Engine changed before maintenance")
                    rollback_ref = self._retain_source_image(manifest, line)
                    marker = self.engine.begin_engine_maintenance(
                        iid, manifest["txid"], source["container_id"],
                        manifest["candidate_image"], rollback_ref)
                    manifest = self._sync_line(manifest, iid, marker)
            if marker["phase"] == "prepared":
                with self._scoped_mutation_locked(manifest, iid):
                    marker = self.engine.transition_engine_maintenance(
                        iid, marker["txid"], "prepared", "source_quiescing")
                    manifest = self._sync_line(manifest, iid, marker)
            if marker["phase"] == "source_quiescing":
                with self._scoped_mutation_locked(manifest, iid):
                    source = marker["source"]
                    if self.engine.engine_generation_facts(
                            iid, source["container_id"]) != source:
                        raise ReplacementDirectManual(
                            f"line {iid} source changed before quiesce")
                inst = self.cfg.get_instance(iid) or {"id": iid}
                result = self.engine.capture_and_stop_if_idle(
                    iid, inst, "engine-replacement", source["container_id"])
                if result.get("status") != "stopped":
                    raise ReplacementError(f"source Engine did not quiesce: {result}")
                with self._scoped_mutation_locked(manifest, iid):
                    try:
                        self.engine.engine_generation_facts(iid)
                    except self.engine.docker.errors.NotFound:
                        pass
                    else:
                        raise ReplacementDirectManual(
                            f"line {iid} source stop did not prove exact absence")
                    marker = self.engine.transition_engine_maintenance(
                        iid, marker["txid"], "source_quiescing", "source_removed")
                    manifest = self._sync_line(manifest, iid, marker)
            if marker["phase"] == "source_removed":
                with self._scoped_mutation_locked(manifest, iid):
                    marker = self.engine.transition_engine_maintenance(
                        iid, marker["txid"], "source_removed", "target_starting")
                    manifest = self._sync_line(manifest, iid, marker)
            if marker["phase"] == "target_starting":
                with self._scoped_mutation_locked(manifest, iid):
                    container_id = self._create_or_classify_id(
                        iid, marker["target_image_digest"], marker["txid"], "target")
                facts = self._wait_created_generation(
                    iid, marker["target_image_digest"], container_id, "target")
                with self._scoped_mutation_locked(manifest, iid):
                    if self.engine.engine_generation_facts(
                            iid, container_id) != facts:
                        raise ReplacementDirectManual(
                            f"line {iid} target generation changed before transition")
                    marker = self.engine.transition_engine_maintenance(
                        iid, marker["txid"], "target_starting", "target_started",
                        target=facts)
                    manifest = self._sync_line(manifest, iid, marker)
            if marker["phase"] == "target_started":
                facts = self._healthy(
                    iid, marker["target_image_digest"],
                    require_media_websocket=True)
                with self._scoped_mutation_locked(manifest, iid):
                    if self.engine.engine_generation_facts(
                            iid, facts["container_id"]) != facts:
                        raise ReplacementDirectManual(
                            f"line {iid} target generation changed before verification")
                    marker = self.engine.transition_engine_maintenance(
                        iid, marker["txid"], "target_started", "verified", target=facts)
                    return self._sync_line(manifest, iid, marker)
            if marker["phase"] in {"rollback_starting", "rollback_started"}:
                return self._rollback(manifest, iid, ReplacementError(
                    "resuming an unfinished rollback"))
            if marker["phase"] == "manual_required":
                raise ReplacementManualRequired(f"line {iid} is manual-required")
            return manifest
        except ReplacementManualRequired:
            raise
        except ReplacementDirectManual as exc:
            self._manual(manifest, iid, exc)
        except self.engine.EngineStartReceiptError as exc:
            self._manual(manifest, iid, exc)
        except Exception as exc:
            return self._rollback(manifest, iid, exc)

    def _unscoped_receipt_path(self, manifest: dict, iid: str) -> Path:
        return (self.orchestrator / UNSCOPED_REMOVALS_DIR /
                f"{manifest['txid']}.{iid}.json")

    def _read_unscoped_receipt(self, manifest: dict, iid: str) -> dict | None:
        path = self._unscoped_receipt_path(manifest, iid)
        try:
            value = json.loads(path.read_text(encoding="utf-8"))
        except FileNotFoundError:
            return None
        except Exception as exc:
            raise ReplacementManualRequired(
                f"unscoped removal receipt for line {iid} is unreadable") from exc
        try:
            return replacement_contract.validate_unscoped_removal_receipt(value, manifest)
        except replacement_contract.ContractError as exc:
            raise ReplacementManualRequired(str(exc)) from exc

    def _scoped_card_loss_path(self, manifest: dict, iid: str) -> Path:
        return (self.orchestrator / SCOPED_CARD_LOSS_DIR /
                f"{manifest['txid']}.{iid}.json")

    def _scoped_card_loss_uncertain_path(self, iid: str) -> Path:
        return self.orchestrator / SCOPED_CARD_LOSS_UNCERTAIN_DIR / f"{iid}.json"

    def _read_scoped_card_loss(self, manifest: dict, iid: str) -> dict | None:
        path = self._scoped_card_loss_path(manifest, iid)
        try:
            value = json.loads(path.read_text(encoding="utf-8"))
        except FileNotFoundError:
            return None
        except Exception as exc:
            raise ReplacementDirectManual(
                f"line {iid} scoped card-loss intent is unreadable") from exc
        try:
            return replacement_contract.validate_scoped_card_loss_intent(
                value, manifest)
        except replacement_contract.ContractError as exc:
            raise ReplacementDirectManual(str(exc)) from exc

    def _require_no_scoped_card_loss(self, manifest: dict, iid: str) -> None:
        if os.path.lexists(self._scoped_card_loss_uncertain_path(iid)):
            raise ReplacementDirectManual(
                f"line {iid} has a scoped card-loss uncertainty fence")
        if self._read_scoped_card_loss(manifest, iid) is not None:
            raise ReplacementDirectManual(
                f"line {iid} has a durable scoped card-loss intent")

    @contextmanager
    def _scoped_mutation_locked(self, manifest: dict, iid: str):
        """Keep one bounded scoped side effect behind the shared card-event barrier."""
        with self._event_locked():
            self._require_no_scoped_card_loss(manifest, iid)
            yield

    def _authorized_unscoped_removed(self, manifest: dict) -> set[str]:
        accepted = set()
        for original in manifest["unscoped"]:
            iid = original["iid"]
            receipt = self._read_unscoped_receipt(manifest, iid)
            if receipt is None:
                continue
            automatic = (receipt["phase"] == "removed"
                         and receipt["attestation"] == "control_card_monitor"
                         and receipt["channels"] == 0)
            explicit_forensic = (receipt["phase"] == "forensic"
                                 and receipt["attestation"] == "operator_forensic"
                                 and iid in self.authorized_forensic_iids
                                 and receipt["channels"] == 0)
            if automatic or explicit_forensic:
                accepted.add(iid)
        return accepted

    def _running_unscoped_iids(self, manifest: dict) -> list[str]:
        removed = self._authorized_unscoped_removed(manifest)
        return [item["iid"] for item in manifest["unscoped"]
                if item["running"] and item["iid"] not in removed]

    def _verify_unscoped(self, manifest: dict) -> None:
        removed = self._authorized_unscoped_removed(manifest)
        expected = [item for item in manifest["unscoped"] if item["iid"] not in removed]
        current = self._snapshot_containers(set(self.iids))
        if current != expected:
            raise ReplacementManualRequired("an unscoped Engine generation changed")
        # Snapshot subtraction is valid only for actual absence. A newly-created container at
        # the same name is never hidden by an old removal receipt.
        names = {item["iid"] for item in current}
        if removed & names:
            raise ReplacementManualRequired("an externally removed Engine name reappeared")

    def _verify_unscoped_or_manual(self, manifest: dict) -> None:
        try:
            self._verify_unscoped(manifest)
        except Exception as exc:
            self._fail_manifest_manual(manifest, exc)

    def _parse_forensic_evidence(self, manifest: dict, iid: str,
                                 original: dict) -> str:
        path = self.recovery_evidence
        if path is None:
            raise ReplacementManualRequired(
                "operator unscoped recovery requires one immutable evidence file")
        if path.is_symlink():
            raise ReplacementManualRequired("operator recovery evidence cannot be a symlink")
        path = path.resolve()
        data_root = self.data.resolve()
        try:
            path.relative_to(data_root)
        except ValueError as exc:
            raise ReplacementManualRequired(
                "operator recovery evidence must be inside the durable data root") from exc
        if not path.is_file() or path.is_symlink():
            raise ReplacementManualRequired("operator recovery evidence is unavailable")
        payload = path.read_bytes()
        lines = payload.decode("utf-8").splitlines()
        paid = None
        observations = []
        for line in lines:
            if line.startswith("paid="):
                paid = json.loads(line[5:])
            elif line.startswith("{"):
                observations.append(json.loads(line))
        if (not isinstance(paid, dict) or set(paid) != {
                "open_call_leases", "pending_messages", "pending_allowance_queries"}
                or any(type(value) is not int or value != 0 for value in paid.values())):
            raise ReplacementManualRequired(
                "operator recovery evidence does not prove paid work zero")
        observed = next((item for item in observations if str(item.get("iid")) == iid), None)
        if not isinstance(observed, dict) or observed.get("channels") != 0:
            raise ReplacementManualRequired(
                f"operator recovery evidence does not prove line {iid} channels zero")
        facts = observed.get("facts") or {}
        for key in ("container_id", "image_id", "started_at", "restart_count"):
            if facts.get(key) != original[key]:
                raise ReplacementManualRequired(
                    f"operator recovery evidence changed line {iid} generation")
        gates = observed.get("gates")
        if (not isinstance(gates, dict) or set(gates) != set(GATE_KINDS)
                or any(value is not False for value in gates.values())):
            raise ReplacementManualRequired(
                f"operator recovery evidence does not prove line {iid} gates DENY")
        return hashlib.sha256(payload).hexdigest()

    def _prepare_operator_unscoped_recovery(self, manifest: dict) -> None:
        if not self.recover_unscoped:
            return
        creating = manifest["phase"] == "manual_required"
        if manifest["phase"] not in {"manual_required", "running", "commit_ready"}:
            raise ReplacementManualRequired(
                "operator unscoped recovery requires a resumable durable transaction")
        originals = {item["iid"]: item for item in manifest["unscoped"]}
        if set(self.recover_unscoped) - set(originals):
            raise ReplacementManualRequired(
                "operator unscoped recovery named an Engine outside the original snapshot")
        current = self._snapshot_containers(set(self.iids))
        current_by_iid = {item["iid"]: item for item in current}
        for iid, expected_id in self.recover_unscoped.items():
            original = originals[iid]
            if expected_id != original["container_id"] or iid in current_by_iid:
                raise ReplacementManualRequired(
                    f"operator unscoped recovery does not match missing line {iid}")
            try:
                self.client.containers.get(expected_id)
            except self.engine.docker.errors.NotFound:
                pass
            else:
                raise ReplacementManualRequired(
                    f"operator unscoped recovery old container {iid} is still present")
            evidence_digest = self._parse_forensic_evidence(manifest, iid, original)
            existing = self._read_unscoped_receipt(manifest, iid)
            if existing is not None:
                if (existing["phase"] != "forensic"
                        or existing["attestation"] != "operator_forensic"
                        or existing["original"] != original
                        or existing["evidence_digest"] != evidence_digest):
                    raise ReplacementManualRequired(
                        f"operator unscoped recovery receipt for line {iid} conflicts")
                self.authorized_forensic_iids.add(iid)
                continue
            if not creating:
                raise ReplacementManualRequired(
                    f"running transaction lacks the forensic receipt for line {iid}")
            inst = self.cfg.get_instance(iid) or {}
            pin = self.engine.read_run_json(iid, "pin_status.json") or {}
            now = time.time()
            receipt = {
                "version": 1, "txid": manifest["txid"],
                "scope_digest": replacement_contract.replacement_scope_digest(manifest),
                "iid": iid, "original": original, "phase": "forensic",
                "reason": "card_removed", "attestation": "operator_forensic",
                "card": {"reader_name": str(pin.get("reader") or ""),
                         "reader_index": -1, "iccid": str(inst.get("iccid") or ""),
                         "matched": iid},
                "channels": 0, "evidence_digest": evidence_digest,
                "created_at": now, "updated_at": now,
            }
            checked = replacement_contract.validate_unscoped_removal_receipt(
                receipt, manifest)
            path = self._unscoped_receipt_path(manifest, iid)
            path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            temporary = path.with_name(f".{path.name}.{os.getpid()}.{uuid.uuid4().hex}.tmp")
            payload = json.dumps(checked, sort_keys=True, separators=(",", ":")) + "\n"
            fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            try:
                with os.fdopen(fd, "w", encoding="utf-8") as handle:
                    handle.write(payload)
                    handle.flush()
                    os.fsync(handle.fileno())
                os.replace(temporary, path)
                directory = os.open(path.parent,
                                    os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
                try:
                    os.fsync(directory)
                finally:
                    os.close(directory)
            finally:
                try:
                    os.unlink(temporary)
                except FileNotFoundError:
                    pass
            self.authorized_forensic_iids.add(iid)

        remaining = (self._running_unscoped_iids(manifest)
                     + [line["iid"] for line in manifest["lines"]
                        if line["phase"] != "skipped_absent"])
        for gate_iid in remaining:
            self._wait_gate(gate_iid, False)
        self._paid_zero()
        self._zero_channels(remaining)
        if self.recover_postflight:
            self._verify_postflight_unscoped(manifest)
        else:
            self._verify_unscoped(manifest)

    def _preflight_current_topology(self, manifest: dict) -> None:
        """Prove every currently live Engine is denied and idle before scoped mutation."""
        try:
            current_unscoped = self._snapshot_containers(set(self.iids))
            running = [item["iid"] for item in current_unscoped if item["running"]]
            for iid in self.iids:
                if self._current_generation(iid) is not None:
                    running.append(iid)
            for iid in sorted(set(running)):
                self._wait_gate(iid, False)
            self._paid_zero()
            self._zero_channels(sorted(set(running)))
        except Exception as exc:
            self._fail_manifest_manual(manifest, exc)

    def _recover_manual_startup_races(self, manifest: dict) -> dict:
        manual = {line["iid"]: line for line in manifest["lines"]
                  if line["phase"] == "manual_required"}
        if not manual or set(self.recover_created) != set(manual):
            raise ReplacementManualRequired(
                "manual startup-race recovery requires one exact container receipt per line")
        if any(line["error"] != "Engine run id is unavailable"
               for line in manual.values()):
            raise ReplacementManualRequired(
                "the durable replacement is not the exact startup run-id race")
        self._verify_unscoped(manifest)
        relevant = (self._running_unscoped_iids(manifest)
                    + [line["iid"] for line in manifest["lines"]
                       if line["phase"] != "skipped_absent"])
        for iid in relevant:
            self._wait_gate(iid, False)
        self._paid_zero()
        self._zero_channels(relevant)
        updated = manifest
        for iid, line in manual.items():
            marker = self.engine.read_engine_maintenance(iid)
            if (marker is None or marker.get("txid") != manifest["txid"]
                    or marker.get("phase") not in {
                        "manual_required", "target_started", "verified"}
                    or marker.get("target_image_digest") != manifest["candidate_image"]
                    or marker.get("source") != line["source"]
                    or marker.get("rollback") is not None
                    or (marker.get("phase") == "manual_required"
                        and marker.get("target") is not None)):
                raise ReplacementManualRequired(
                    f"line {iid} is not the exact startup-race marker")
            container_id = self.recover_created[iid]
            with self._scoped_mutation_locked(updated, iid):
                self.engine.attest_existing_replacement_target(
                    iid, manifest["txid"], container_id)
            facts = self._healthy(
                iid, manifest["candidate_image"], require_media_websocket=True)
            if (facts["container_id"] != container_id or facts["restart_count"] != 0
                    or facts["container_id"] == line["source"]["container_id"]
                    or facts["run_id"] == line["source"]["run_id"]):
                raise ReplacementManualRequired(
                    f"line {iid} attested target generation changed")
            self._verify_unscoped(updated)
            for gate_iid in relevant:
                self._wait_gate(gate_iid, False)
            self._paid_zero()
            self._zero_channels(relevant)
            with self._scoped_mutation_locked(updated, iid):
                if self.engine.engine_generation_facts(
                        iid, container_id) != facts:
                    raise ReplacementManualRequired(
                        f"line {iid} startup-race target changed before recovery")
                marker = self.engine.recover_engine_maintenance_startup_race(
                    iid, manifest["txid"], container_id)
                updated = self._sync_line(updated, iid, marker)
        return updated

    def _recover_precreate_missing_target_failure(self, manifest: dict) -> dict:
        """Abort untouched sources, then roll back the exact missing-receipt incident."""
        iid = self.recover_precreate_missing_target
        if (manifest["phase"] not in {"manual_required", "running"}
                or iid not in manifest["iids"]
                or not IID_RE.fullmatch(iid)):
            raise ReplacementManualRequired(
                "pre-create recovery requires the exact manual scoped transaction")
        failed = self._line(manifest, iid)
        expected_error = f"line {iid} target start receipt is unreadable"
        recovery_phases = {
            "manual_required", "rollback_starting", "rollback_started",
            "rollback_verified",
        }
        if failed["phase"] not in recovery_phases or failed["error"] != expected_error:
            raise ReplacementManualRequired(
                "pre-create recovery does not match the stable incident error")
        if manifest["phase"] == "running":
            for scoped in manifest["lines"]:
                if scoped["iid"] == iid or scoped["phase"] in {
                        "pending", "prepared", "aborted", "skipped_absent"}:
                    continue
                if (scoped["phase"] in {
                        "rollback_starting", "rollback_started", "rollback_verified"}
                        and scoped["error"] ==
                        "restarting source after exact PC/SC service outage"):
                    continue
                raise ReplacementManualRequired(
                    "running pre-create recovery has an incompatible scoped phase")
        if (self.recover_created or self.recover_unscoped or self.recover_postflight
                or self.recover_postflight_added or self.recovery_evidence is not None
                or self.postflight_recovery_evidence is not None):
            raise ReplacementManualRequired(
                "pre-create recovery cannot be combined with another recovery mode")
        promotion = self._read_default_promotion(manifest) if self.promote_default else None
        if self.promote_default:
            if (promotion is None or promotion.get("phase") != "prepared"
                    or promotion.get("previous_image") != failed["source"]["image_id"]):
                raise ReplacementManualRequired(
                    "pre-create recovery default promotion is not untouched")
            self._require_default_ref(promotion, promotion["previous_image"])

        self._verify_unscoped(manifest)
        self._paid_zero()
        for scoped in manifest["lines"]:
            if scoped["phase"] != "skipped_absent":
                self._require_exact_global_admission_deny(scoped["iid"])

        # Crash safety requires every untouched source to become terminal before the manual
        # line is allowed to leave manual_required. A normal resume can then only continue the
        # rollback and can never create a target on another line.
        for line in manifest["lines"]:
            abort_iid = line["iid"]
            if abort_iid == iid or line["phase"] == "skipped_absent":
                continue
            marker = self.engine.read_engine_maintenance(abort_iid)
            if ((marker is not None and marker.get("phase") in {
                    "rollback_starting", "rollback_started", "rollback_verified"})
                    or (marker is None and line["phase"] == "pending"
                        and self.engine.usim_recovery_fence_pending(abort_iid))
                    or line["phase"] in {
                        "rollback_starting", "rollback_started", "rollback_verified"}):
                manifest = self._recover_usim_fenced_pending_source(
                    manifest, line, marker)
                continue
            if line["phase"] == "aborted":
                if (marker is None or marker.get("txid") != manifest["txid"]
                        or marker.get("phase") != "aborted"
                        or marker.get("source") != line["source"]):
                    raise ReplacementManualRequired(
                        f"line {abort_iid} recovery abort evidence changed")
                if self.engine.engine_generation_facts(
                        abort_iid, line["source"]["container_id"]) != line["source"]:
                    raise ReplacementManualRequired(
                        f"line {abort_iid} aborted source changed")
                self._wait_gate(abort_iid, False)
                if self.engine.active_channel_count(abort_iid) != 0:
                    raise ReplacementManualRequired(
                        f"line {abort_iid} aborted channel state is not exact zero")
                continue
            if line["phase"] != "pending" or (marker is not None and (
                    marker.get("txid") != manifest["txid"]
                    or marker.get("source") != line["source"]
                    or marker.get("phase") not in {"prepared", "aborted"})):
                raise ReplacementManualRequired(
                    f"line {abort_iid} is not an untouched pending source")
            self._wait_gate(abort_iid, False)
            if self.engine.active_channel_count(abort_iid) != 0:
                raise ReplacementManualRequired(
                    f"line {abort_iid} channel state is not exact zero")
            with self._scoped_mutation_locked(manifest, abort_iid):
                if self.engine.engine_generation_facts(
                        abort_iid, line["source"]["container_id"]) != line["source"]:
                    raise ReplacementManualRequired(
                        f"line {abort_iid} source changed before recovery abort")
                if marker is None:
                    rollback_ref = self._retain_source_image(manifest, line)
                    marker = self.engine.begin_engine_maintenance(
                        abort_iid, manifest["txid"], line["source"]["container_id"],
                        manifest["candidate_image"], rollback_ref)
                if marker["phase"] == "prepared":
                    marker = self.engine.transition_engine_maintenance(
                        abort_iid, manifest["txid"], "prepared", "aborted")
                manifest = self._sync_line(
                    manifest, abort_iid, marker,
                    error="transaction aborted before target creation")

        if any(line["iid"] != iid and line["phase"] not in TERMINAL_LINES
               for line in manifest["lines"]):
            raise ReplacementManualRequired(
                "pre-create recovery left another scoped line non-terminal")
        # Close the multi-line observation window once more immediately before the failed line
        # leaves manual state. Earlier per-line samples cannot be carried across later aborts.
        for line in manifest["lines"]:
            if line["iid"] == iid or line["phase"] == "skipped_absent":
                continue
            if line["phase"] == "aborted":
                expected = line["source"]
            elif line["phase"] == "rollback_verified":
                expected = line["terminal"]
            else:
                raise ReplacementManualRequired(
                    f"line {line['iid']} final recovery phase is not terminal")
            if self.engine.engine_generation_facts(
                    line["iid"], expected["container_id"]) != expected:
                raise ReplacementManualRequired(
                    f"line {line['iid']} final recovery generation changed")
            self._wait_gate(line["iid"], False)
            if self.engine.active_channel_count(line["iid"]) != 0:
                raise ReplacementManualRequired(
                    f"line {line['iid']} final channel state is not exact zero")
        self._require_exact_global_admission_deny(iid)
        self._verify_unscoped(manifest)
        self._paid_zero()
        marker = self.engine.read_engine_maintenance(iid)
        if marker is None:
            raise ReplacementManualRequired(
                "pre-create recovery manual marker disappeared")
        if marker["phase"] == "manual_required":
            with self._scoped_mutation_locked(manifest, iid):
                marker = self.engine.recover_precreate_missing_target_to_rollback(
                    iid, manifest["txid"], failed["source"]["container_id"],
                    manifest["candidate_image"])
                manifest = self._sync_line(
                    manifest, iid, marker, error=expected_error)
        else:
            manifest, marker = self._reconcile_marker(manifest, iid, marker)
        if marker["phase"] != "rollback_verified":
            manifest = self._rollback(
                manifest, iid, ReplacementError(expected_error))
        if any(line["phase"] not in TERMINAL_LINES for line in manifest["lines"]):
            raise ReplacementManualRequired(
                "pre-create recovery did not reach scoped terminal lines")
        self._verify_unscoped(manifest)
        self._paid_zero()
        self._zero_channels([
            line["iid"] for line in manifest["lines"]
            if line["phase"] != "skipped_absent"])
        return manifest

    def _require_exact_global_admission_deny(self, iid: str) -> None:
        deny_path = self.data / "instances" / str(iid) / "run" / "admission-deny"
        try:
            deny_stat = deny_path.lstat()
            if (not stat.S_ISREG(deny_stat.st_mode) or deny_path.is_symlink()
                    or stat.S_IMODE(deny_stat.st_mode) != 0o600
                    or deny_stat.st_uid != os.geteuid()):
                raise ValueError("unsafe admission deny file")
            deny = json.loads(deny_path.read_text(encoding="utf-8"))
        except Exception as exc:
            raise ReplacementManualRequired(
                f"line {iid} recovery admission deny is unreadable") from exc
        if (not isinstance(deny, dict)
                or set(deny) != {"version", "reason", "updated_at"}
                or deny.get("version") != 1
                or deny.get("reason") != "global_engine_replacement_in_flight"
                or type(deny.get("updated_at")) is not int
                or deny["updated_at"] <= 0):
            raise ReplacementManualRequired(
                f"line {iid} recovery admission deny is not exact")

    def _recover_usim_fenced_pending_source(
            self, manifest: dict, line: dict, marker: dict | None) -> dict:
        """Recreate one untouched old source whose exact PC/SC outage blocks normal begin."""
        iid = line["iid"]
        outage_error = "restarting source after exact PC/SC service outage"
        if marker is None:
            if line["phase"] != "pending":
                raise ReplacementManualRequired(
                    f"line {iid} USIM-fenced rollback is not pending")
            self._wait_gate(iid, False)
            if self.engine.active_channel_count(iid) != 0:
                raise ReplacementManualRequired(
                    f"line {iid} USIM-fenced rollback channel state is not zero")
            if self.engine.engine_generation_facts(
                    iid, line["source"]["container_id"]) != line["source"]:
                raise ReplacementManualRequired(
                    f"line {iid} USIM-fenced rollback source changed")
            usim = self.engine.read_run_json(iid, "usim_status.json") or {}
            if (usim.get("state") != "AUTH_UNAVAILABLE"
                    or usim.get("cause_class") != "pcsc_service_unavailable"
                    or usim.get("engine_run_id") != line["source"]["run_id"]
                    or type(usim.get("auth_seq")) is not int or usim["auth_seq"] <= 0
                    or self.engine.registration_state(iid) != "Rejected"):
                raise ReplacementManualRequired(
                    f"line {iid} USIM-fenced rollback evidence changed")
            with self._scoped_mutation_locked(manifest, iid):
                rollback_ref = self._retain_source_image(manifest, line)
                marker = self.engine.prepare_usim_fenced_source_rollback(
                    iid, manifest["txid"], line["source"]["container_id"],
                    manifest["candidate_image"], rollback_ref,
                    line["source"]["run_id"], usim["auth_seq"])
                manifest = self._sync_line(
                    manifest, iid, marker,
                    error=outage_error)
        else:
            # Close helper-marker -> manifest crash window with one durable phase+reason write.
            # The reason is part of the outer recovery admission, so it may never lag behind
            # this special pending -> rollback_starting marker.
            if line["phase"] == "pending" and marker.get("phase") == "rollback_starting":
                if (marker.get("txid") != manifest["txid"]
                        or marker.get("source") != line["source"]
                        or marker.get("target_image_digest") != manifest["candidate_image"]
                        or marker.get("rollback_image_ref") !=
                            self._rollback_ref(manifest, iid)):
                    raise ReplacementManualRequired(
                        f"line {iid} USIM-fenced rollback marker scope changed")
                line["error"] = outage_error
            manifest, marker = self._reconcile_marker(manifest, iid, marker)
        if marker["phase"] == "rollback_starting":
            current = self._current_generation(iid)
            if current is not None:
                if current != line["source"]:
                    raise ReplacementManualRequired(
                        f"line {iid} USIM-fenced rollback has an unknown generation")
                self._wait_gate(iid, False)
                self._require_exact_global_admission_deny(iid)
                self._paid_zero()
                if self.engine.active_channel_count(iid) != 0:
                    raise ReplacementManualRequired(
                        f"line {iid} USIM-fenced rollback source is not idle")
                inst = self.cfg.get_instance(iid) or {"id": iid}
                stopped = self.engine.capture_and_stop_if_idle(
                    iid, inst, "engine-replacement-usim-fenced-rollback",
                    line["source"]["container_id"])
                if stopped.get("status") != "stopped":
                    raise ReplacementManualRequired(
                        f"line {iid} USIM-fenced rollback source did not stop: {stopped}")
                if self._current_generation(iid) is not None:
                    raise ReplacementManualRequired(
                        f"line {iid} USIM-fenced rollback source absence is unproven")
        if marker["phase"] != "rollback_verified":
            manifest = self._rollback(
                manifest, iid,
                ReplacementError(outage_error))
        if self._line(manifest, iid)["phase"] != "rollback_verified":
            raise ReplacementManualRequired(
                f"line {iid} USIM-fenced rollback did not verify")
        return manifest

    def _archive(self, manifest: dict) -> None:
        _atomic_json(self.manifest_path, manifest)
        os.replace(self.manifest_path, self.last_manifest_path)
        directory = os.open(self.orchestrator,
                            os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)

    def _postflight_receipt_path(self, manifest: dict) -> Path:
        return (self.orchestrator / POSTFLIGHT_RECOVERIES_DIR /
                f"{manifest['txid']}.json")

    def _postflight_pcscf_state(self, iid: str, facts: dict) -> dict:
        run = self.data / "instances" / iid / "run"
        if os.path.lexists(run / "pcscf-rebind.json"):
            raise ReplacementManualRequired(
                f"line {iid} still has a pending P-CSCF rebind")
        if os.path.lexists(run / "engine-maintenance.json"):
            raise ReplacementManualRequired(
                f"line {iid} still has an Engine maintenance marker")
        try:
            configured = (run / "pcscf").read_text(encoding="utf-8").strip()
            applied = (run / "pcscf.applied").read_text(encoding="utf-8").strip()
            discovery = json.loads(
                (run / "pcscf-discovery.json").read_text(encoding="utf-8"))
            swu = json.loads((run / "swu_status.json").read_text(encoding="utf-8"))
            usim = json.loads((run / "usim_status.json").read_text(encoding="utf-8"))
        except Exception as exc:
            raise ReplacementManualRequired(
                f"line {iid} P-CSCF completion evidence is unreadable") from exc
        if (not configured or configured != applied
                or not isinstance(discovery, dict)
                or discovery.get("address") != configured
                or discovery.get("engine_run_id") != facts["run_id"]
                or not isinstance(discovery.get("observed_at"), (int, float))
                or not isinstance(swu, dict) or swu.get("state") != "CONNECTED"
                or swu.get("pcscf") != configured
                or not isinstance(usim, dict) or usim.get("state") != "AUTH_OK"
                or usim.get("engine_run_id") != facts["run_id"]
                or self.engine.registration_state(iid) != "Registered"):
            raise ReplacementManualRequired(
                f"line {iid} P-CSCF/current-run state is not converged")
        return {
            "configured": configured, "applied": applied,
            "discovery": discovery, "swu": swu, "usim": usim,
        }

    def _verify_postflight_unscoped(self, manifest: dict) -> list[dict]:
        removed = self._authorized_unscoped_removed(manifest)
        current_unscoped = self._snapshot_containers(set(self.iids))
        current_by_iid = {item["iid"]: item for item in current_unscoped}
        if len(current_by_iid) != len(current_unscoped):
            raise ReplacementManualRequired("postflight topology has duplicate line IDs")
        for original in manifest["unscoped"]:
            iid = original["iid"]
            if iid in removed:
                if iid in current_by_iid:
                    raise ReplacementManualRequired(
                        f"postflight removed line {iid} reappeared")
            elif current_by_iid.get(iid) != original:
                raise ReplacementManualRequired(
                    f"postflight original unscoped line {iid} changed")
        original_iids = {item["iid"] for item in manifest["unscoped"]}
        additions = {item["iid"]: item for item in current_unscoped
                     if item["iid"] not in original_iids}
        if set(additions) != set(self.recover_postflight_added):
            raise ReplacementManualRequired(
                "postflight new unscoped topology lacks exact operator authorization")
        for item in current_unscoped:
            if item["iid"] in additions and (
                    item.get("running") is not True
                    or self.recover_postflight_added[item["iid"]] !=
                    item["container_id"]):
                raise ReplacementManualRequired(
                    f"postflight new unscoped line {item['iid']} is not exact")
        return current_unscoped

    def _postflight_topology(self, manifest: dict) -> tuple[list[dict], dict[str, dict]]:
        current_unscoped = self._verify_postflight_unscoped(manifest)

        live_iids = {item["iid"] for item in current_unscoped if item["running"]}
        current_facts: dict[str, dict] = {}
        for line in manifest["lines"]:
            if line["phase"] != "verified" or line.get("terminal") is None:
                raise ReplacementManualRequired(
                    "postflight recovery requires every scoped line verified")
            iid = line["iid"]
            facts = self._healthy(
                iid, manifest["candidate_image"], require_media_websocket=True)
            terminal = line["terminal"]
            if (facts["container_id"] != terminal["container_id"]
                    or facts["image_id"] != manifest["candidate_image"]):
                raise ReplacementManualRequired(
                    f"line {iid} postflight container identity changed")
            current_facts[iid] = facts
            live_iids.add(iid)
        for iid in sorted(live_iids):
            if iid not in current_facts:
                facts = self._current_generation(iid)
                if facts is None:
                    raise ReplacementManualRequired(
                        f"live postflight line {iid} generation is unavailable")
                current_facts[iid] = facts
        return current_unscoped, current_facts

    def _postflight_evidence_payload(self, manifest: dict, current_unscoped: list[dict],
                                     current_facts: dict[str, dict],
                                     pcscf: dict[str, dict], fences: dict[str, str],
                                     denies_before: dict[str, dict],
                                     denies_prepared: dict[str, dict]) -> tuple[dict, str]:
        path = self.postflight_recovery_evidence
        if path is None:
            raise ReplacementManualRequired(
                "postflight recovery requires one immutable forensic evidence file")
        if path.is_symlink():
            raise ReplacementManualRequired("postflight evidence cannot be a symlink")
        path = path.resolve()
        try:
            path.relative_to(self.data.resolve())
        except ValueError as exc:
            raise ReplacementManualRequired(
                "postflight evidence must be inside the durable data root") from exc
        if not path.is_file() or path.is_symlink():
            raise ReplacementManualRequired("postflight evidence is unavailable")
        payload = path.read_bytes()
        try:
            value = json.loads(payload.decode("utf-8"))
        except Exception as exc:
            raise ReplacementManualRequired("postflight evidence is unreadable") from exc
        paid = self.guard.pending_paid_work(self.database)
        expected = {
            "version": 1, "txid": manifest["txid"],
            "attestation": "operator_forensic_postflight",
            "paid": paid, "unscoped": current_unscoped,
            "terminals": {line["iid"]: line["terminal"] for line in manifest["lines"]},
            "engines": current_facts, "pcscf": pcscf, "fences": fences,
            "denies_before": denies_before, "denies_prepared": denies_prepared,
        }
        if value != expected:
            raise ReplacementManualRequired(
                "postflight forensic evidence does not match current exact state")
        if set(paid) != {"open_call_leases", "pending_messages",
                         "pending_allowance_queries"} or any(paid.values()):
            raise ReplacementManualRequired("postflight paid work is not zero")
        return value, hashlib.sha256(payload).hexdigest()

    def _postflight_fence_digests(self, manifest: dict, *, allow_missing: bool) -> dict[str, str]:
        result = {}
        for line in manifest["lines"]:
            iid = line["iid"]
            path = self.data / "instances" / iid / "run" / POSTFLIGHT_FENCE_NAME
            try:
                payload = path.read_bytes()
                value = json.loads(payload.decode("utf-8"))
            except FileNotFoundError:
                if allow_missing:
                    continue
                raise ReplacementManualRequired(
                    f"line {iid} exact postflight fence is missing")
            except Exception as exc:
                raise ReplacementManualRequired(
                    f"line {iid} postflight fence is unreadable") from exc
            if (value.get("version") != 1
                    or value.get("reason") != "engine_replacement_postflight_failed"
                    or value.get("txid") != manifest["txid"]):
                raise ReplacementManualRequired(
                    f"line {iid} postflight fence does not own this transaction")
            result[iid] = hashlib.sha256(payload).hexdigest()
        return result

    def _postflight_prepared_denies(self, manifest: dict) -> dict[str, dict]:
        return {line["iid"]: {
            "version": 1, "reason": "global_engine_replacement_in_flight",
            "txid": manifest["txid"],
        } for line in manifest["lines"]}

    def _postflight_deny_state(self, manifest: dict, fence_present: set[str],
                               receipt: dict | None) -> dict[str, dict]:
        result = {}
        for line in manifest["lines"]:
            iid = line["iid"]
            path = self.data / "instances" / iid / "run" / "admission-deny"
            try:
                value = json.loads(path.read_text(encoding="utf-8"))
            except Exception as exc:
                raise ReplacementManualRequired(
                    f"line {iid} admission deny is unreadable") from exc
            if not isinstance(value, dict) or value.get("version") != 1:
                raise ReplacementManualRequired(
                    f"line {iid} admission deny is invalid")
            owned = {key: value[key] for key in ("version", "reason", "txid")
                     if key in value}
            if receipt is not None:
                allowed = {json.dumps(receipt["denies_before"][iid], sort_keys=True),
                           json.dumps(receipt["denies_prepared"][iid], sort_keys=True)}
                if json.dumps(owned, sort_keys=True) not in allowed:
                    raise ReplacementManualRequired(
                        f"line {iid} admission deny changed during postflight recovery")
            else:
                exact = (value.get("reason") in POSTFLIGHT_DENY_REASONS | {
                    "global_engine_replacement_in_flight"}
                         and value.get("txid") == manifest["txid"])
                legacy = (iid in fence_present and "txid" not in value
                          and value.get("reason") in POSTFLIGHT_DENY_REASONS | {
                              "global_engine_replacement_in_flight"})
                if not (exact or legacy):
                    raise ReplacementManualRequired(
                        f"line {iid} admission deny is owned by another reason")
            result[iid] = owned
        return result

    def _read_postflight_receipt(self, manifest: dict) -> dict | None:
        path = self._postflight_receipt_path(manifest)
        try:
            value = json.loads(path.read_text(encoding="utf-8"))
        except FileNotFoundError:
            return None
        except Exception as exc:
            raise ReplacementManualRequired(
                "postflight recovery receipt is unreadable") from exc
        if (not isinstance(value, dict) or value.get("version") != 1
                or value.get("txid") != manifest["txid"]
                or value.get("phase") not in {"prepared", "failed", "completed"}
                or value.get("scope_digest") !=
                    replacement_contract.replacement_scope_digest(manifest)
                or type(value.get("attempt")) is not int or value["attempt"] < 1
                or not re.fullmatch(r"[0-9a-f]{64}", str(value.get("evidence_digest") or ""))
                or not isinstance(value.get("fences"), dict)
                or not isinstance(value.get("engines"), dict)
                or not isinstance(value.get("denies_before"), dict)
                or not isinstance(value.get("denies_prepared"), dict)):
            raise ReplacementManualRequired("postflight recovery receipt is invalid")
        return value

    def _archive_failed_postflight_receipt(self, manifest: dict, receipt: dict) -> None:
        path = self._postflight_receipt_path(manifest)
        history = path.with_name(
            f"{manifest['txid']}.attempt-{receipt['attempt']}-failed.json")
        if history.exists():
            try:
                if json.loads(history.read_text(encoding="utf-8")) == receipt:
                    return
            except Exception:
                pass
            raise ReplacementManualRequired("postflight failed-attempt history conflicts")
        _atomic_fence(history, receipt)

    def _mark_postflight_receipt_failed(self, manifest: dict, error: Exception) -> None:
        receipt = self._read_postflight_receipt(manifest)
        if receipt is None or receipt["phase"] != "prepared":
            return
        failed = dict(receipt)
        failed["phase"] = "failed"
        failed["error"] = str(error)[:500]
        self._save_postflight_receipt(manifest, failed)

    def _save_postflight_receipt(self, manifest: dict, value: dict) -> None:
        path = self._postflight_receipt_path(manifest)
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        _atomic_fence(path, value)

    def _fail_prepared_postflight_attempt(self, manifest: dict,
                                          error: Exception) -> list[str]:
        failures = []
        with self._event_locked():
            current = read_manifest(self.manifest_path)
            if current["txid"] != manifest["txid"]:
                raise ReplacementManualRequired(
                    "postflight transaction changed while restoring fences")
            if current["phase"] == "committed":
                current = self._latch_manifest_manual(current, error)
            elif current["phase"] != "manual_required":
                raise ReplacementManualRequired(
                    "postflight transaction is not safely fenceable")
            for line in current["lines"]:
                iid = line["iid"]
                try:
                    _atomic_fence(
                        self.data / "instances" / iid / "run" /
                        POSTFLIGHT_FENCE_NAME,
                        {"version": 1,
                         "reason": "engine_replacement_postflight_failed",
                         "txid": current["txid"], "updated_at": int(time.time())})
                except Exception as exc:
                    failures.append(f"{iid}: postflight fence restore failed: {exc}")
            try:
                self._mark_postflight_receipt_failed(current, error)
            except Exception as exc:
                failures.append(f"postflight receipt failure latch failed: {exc}")
        for line in current["lines"]:
            try:
                self._wait_gate(line["iid"], False)
            except Exception as exc:
                failures.append(f"{line['iid']}: restored DENY not proven: {exc}")
        return failures

    def _recover_failed_postflight(self, manifest: dict) -> dict:
        try:
            return self._recover_failed_postflight_attempt(manifest)
        except Exception as exc:
            try:
                receipt = self._read_postflight_receipt(manifest)
            except Exception:
                receipt = None
            failures = []
            if receipt is not None and receipt["phase"] == "prepared":
                try:
                    failures = self._fail_prepared_postflight_attempt(manifest, exc)
                except Exception as fence_exc:
                    failures = [f"postflight attempt re-fence failed: {fence_exc}"]
            suffix = f"; {'; '.join(failures)}" if failures else ""
            if suffix:
                raise ReplacementManualRequired(f"{exc}{suffix}") from exc
            raise

    def _recover_failed_postflight_attempt(self, manifest: dict) -> dict:
        if (not self.recover_postflight
                or self.recover_postflight != manifest["txid"]
                or manifest["phase"] != "manual_required"):
            raise ReplacementManualRequired(
                "postflight recovery requires the exact explicit transaction ID")
        self._resume_default_promotion_postflight(manifest)
        stored_receipt = self._read_postflight_receipt(manifest)
        if stored_receipt is not None and stored_receipt["phase"] == "completed":
            raise ReplacementManualRequired(
                "completed postflight receipt conflicts with an active manual transaction")
        failed_receipt = (stored_receipt
                          if stored_receipt is not None
                          and stored_receipt["phase"] == "failed" else None)
        receipt = None if failed_receipt is not None else stored_receipt
        current_fences = self._postflight_fence_digests(
            manifest, allow_missing=receipt is not None)
        fences = dict(current_fences)
        if receipt is not None:
            if (set(current_fences) - set(receipt["fences"])
                    or any(receipt["fences"].get(iid) != digest
                           for iid, digest in current_fences.items())):
                raise ReplacementManualRequired("postflight recovery fences changed")
            fences = dict(receipt["fences"])
        denies_current = self._postflight_deny_state(
            manifest, set(current_fences), receipt)
        denies_before = (dict(receipt["denies_before"])
                         if receipt is not None else denies_current)
        denies_prepared = self._postflight_prepared_denies(manifest)
        if receipt is not None and receipt["denies_prepared"] != denies_prepared:
            raise ReplacementManualRequired("postflight prepared deny scope changed")

        current_unscoped, current_facts = self._postflight_topology(manifest)
        live = sorted(current_facts)
        for iid in live:
            self._wait_gate(iid, False)
        self._paid_zero()
        self._zero_channels(live)
        pcscf = {line["iid"]: self._postflight_pcscf_state(
            line["iid"], current_facts[line["iid"]]) for line in manifest["lines"]}
        _evidence, evidence_digest = self._postflight_evidence_payload(
            manifest, current_unscoped, current_facts, pcscf, fences,
            denies_before, denies_prepared)
        expected_receipt = {
            "version": 1, "txid": manifest["txid"], "phase": "prepared",
            "attempt": ((failed_receipt or {}).get("attempt", 0) + 1),
            "scope_digest": replacement_contract.replacement_scope_digest(manifest),
            "evidence_digest": evidence_digest, "fences": fences,
            "engines": current_facts, "denies_before": denies_before,
            "denies_prepared": denies_prepared,
        }
        if (failed_receipt is not None
                and evidence_digest == failed_receipt["evidence_digest"]):
            raise ReplacementManualRequired(
                "a failed postflight retry requires new forensic evidence")
        if receipt is not None:
            comparable = dict(receipt)
            comparable["phase"] = "prepared"
            if comparable != expected_receipt:
                raise ReplacementManualRequired("postflight recovery receipt changed")

        with self._event_locked():
            current = read_manifest(self.manifest_path)
            if current != manifest:
                raise ReplacementManualRequired(
                    "postflight manifest changed before finalization")
            current_unscoped2, current_facts2 = self._postflight_topology(manifest)
            if current_unscoped2 != current_unscoped or current_facts2 != current_facts:
                raise ReplacementManualRequired(
                    "postflight topology changed before finalization")
            pcscf2 = {line["iid"]: self._postflight_pcscf_state(
                line["iid"], current_facts2[line["iid"]]) for line in manifest["lines"]}
            if pcscf2 != pcscf:
                raise ReplacementManualRequired(
                    "postflight P-CSCF state changed before finalization")
            for iid in live:
                self._wait_gate(iid, False)
            self._paid_zero()
            self._zero_channels(live)
            locked_fences = self._postflight_fence_digests(
                manifest, allow_missing=receipt is not None)
            if (set(locked_fences) - set(fences)
                    or any(fences.get(iid) != digest
                           for iid, digest in locked_fences.items())):
                raise ReplacementManualRequired(
                    "postflight fences changed before finalization")
            self._postflight_deny_state(manifest, set(locked_fences), receipt)
            if receipt is None:
                if failed_receipt is not None:
                    self._archive_failed_postflight_receipt(manifest, failed_receipt)
                self._save_postflight_receipt(manifest, expected_receipt)
            for line in manifest["lines"]:
                iid = line["iid"]
                run = self.data / "instances" / iid / "run"
                _atomic_fence(run / "admission-deny", denies_prepared[iid])
                _durable_unlink(run / POSTFLIGHT_FENCE_NAME)
            manifest = self._save(manifest, phase="committed")
        result = self._finish_committed(manifest)
        completed = dict(expected_receipt)
        completed["phase"] = "completed"
        self._save_postflight_receipt(result, completed)
        return result

    def _finish_committed(self, manifest: dict) -> dict:
        try:
            for line in manifest["lines"]:
                if line["phase"] != "skipped_absent":
                    self._wait_normal_allow(line["iid"])
            self._paid_zero()
            self._zero_channels([
                line["iid"] for line in manifest["lines"]
                if line["phase"] != "skipped_absent"])
            self._commit_default_promotion(manifest)
        except Exception as exc:
            failures = []
            try:
                self._mark_default_promotion_manual(manifest)
            except Exception as promotion_exc:
                failures.append(
                    f"default promotion manual fence write failed: {promotion_exc}")
            try:
                manifest = self._latch_manifest_manual(manifest, exc)
            except Exception as manifest_exc:
                failures.append(f"global manual fence write failed: {manifest_exc}")
            for line in manifest["lines"]:
                if line["phase"] == "skipped_absent":
                    continue
                iid = line["iid"]
                try:
                    _atomic_fence(
                        self.data / "instances" / iid / "run" /
                        POSTFLIGHT_FENCE_NAME,
                        {"version": 1,
                         "reason": "engine_replacement_postflight_failed",
                         "txid": manifest["txid"], "updated_at": int(time.time())})
                except Exception as fence_exc:
                    failures.append(f"{iid}: sticky deny fence write failed: {fence_exc}")
                try:
                    _atomic_fence(
                        self.data / "instances" / iid / "run" / "admission-deny",
                        {"version": 1, "reason": "engine_replacement_postflight_failed",
                         "txid": manifest["txid"], "updated_at": int(time.time())})
                except Exception as fence_exc:
                    failures.append(f"{iid}: deny fence write failed: {fence_exc}")
            for line in manifest["lines"]:
                if line["phase"] == "skipped_absent":
                    continue
                try:
                    self._wait_gate(line["iid"], False)
                except Exception as deny_exc:
                    failures.append(f"{line['iid']}: DENY not proven: {deny_exc}")
            try:
                self._mark_postflight_receipt_failed(manifest, exc)
            except Exception as receipt_exc:
                failures.append(f"postflight receipt failure latch failed: {receipt_exc}")
            suffix = f"; {'; '.join(failures)}" if failures else ""
            raise ReplacementManualRequired(
                f"normal admission postflight failed: {exc}{suffix}") from exc
        self._archive(manifest)
        # Source-first updates intentionally remain action-required until the installed default
        # and every running Engine carry the media ABI. A status write failure is fail-visible:
        # it leaves the pending action in place and never weakens replacement completion.
        try:
            from host import mdd_update  # pylint: disable=import-outside-toplevel
            mdd_update.complete_engine_media_migration_status(self.repo, self.data)
        except Exception:
            pass
        return manifest

    def run(self) -> dict:
        self.orchestrator.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd = os.open(self.lock_path, os.O_RDWR | os.O_CREAT, 0o600)
        lock = os.fdopen(fd, "r+")
        try:
            try:
                fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise ReplacementError("another Engine replacement process is running") from exc
            self._load_runtime()
            manifest = self._load_or_create()
            if manifest["phase"] in {"committed", "aborted"}:
                return self._finish_committed(manifest)
            if self.recover_precreate_missing_target:
                manifest = self._recover_precreate_missing_target_failure(manifest)
            self._prepare_operator_unscoped_recovery(manifest)
            if self.recover_postflight:
                return self._recover_failed_postflight(manifest)
            # Detect unrelated lifecycle drift before touching any scoped line.  The previous
            # ordering discovered it only after target mutation and could deepen a manual
            # incident before recording the original mismatch.
            self._verify_unscoped_or_manual(manifest)
            self._preflight_current_topology(manifest)
            if manifest["phase"] == "manual_required":
                manifest = self._recover_manual_startup_races(manifest)

            running_iids = [line["iid"] for line in manifest["lines"]
                            if line["phase"] != "skipped_absent"]
            scoped_running = []
            for iid in self.iids:
                manifest, present = self._prepare_line_gate(manifest, iid)
                if present:
                    scoped_running.append(iid)
            all_running = self._running_unscoped_iids(manifest) + scoped_running
            for iid in all_running:
                self._wait_gate(iid, False)

            paths = []
            for iid in running_iids:
                logs = self.data / "instances" / iid / "logs"
                paths.extend([logs / "messages.txt", logs / "calls.txt"])
                self.guard.filesystem_durability_probe(logs, manifest["txid"])
            self.guard.sqlite_durability_probe(self.database, manifest["txid"])
            with ExitStack() as stack:
                watcher = stack.enter_context(self.guard.MessageFileGuard(paths)) if paths else None
                if watcher:
                    watcher.wait_quiet(self.quiet_seconds)
                self._paid_zero()
                self._zero_current_or_manual(manifest)
                time.sleep(0.5)
                if watcher:
                    watcher.check()
                self._paid_zero()
                self._zero_current_or_manual(manifest)
                manifest = self._save(manifest, phase="running")
                for iid in self.iids:
                    manifest = self._drive_line(manifest, iid)
                    self._verify_unscoped_or_manual(manifest)
                    if watcher:
                        watcher.check()
                    self._paid_zero()
                    self._zero_current_or_manual(manifest)
                self._verify_unscoped_or_manual(manifest)
                if any(line["phase"] not in TERMINAL_LINES for line in manifest["lines"]):
                    raise ReplacementManualRequired("replacement did not reach terminal lines")
                manifest = self._save(manifest, phase="commit_ready")
                self._zero_current_or_manual(manifest)
                if watcher:
                    watcher.check()
                self._paid_zero()
                self._zero_current_or_manual(manifest)
                with self._event_locked():
                    for line in manifest["lines"]:
                        if line["phase"] == "skipped_absent":
                            continue
                        try:
                            self._require_no_scoped_card_loss(manifest, line["iid"])
                        except ReplacementDirectManual as exc:
                            self._fail_manifest_manual(manifest, exc, line["iid"])
                    self._verify_unscoped_or_manual(manifest)
                    self._paid_zero()
                    self._zero_current_or_manual(manifest)
                    self._prepare_default_promotion_commit(manifest)
                    # Keep intent check, partial marker clear, and durable commit under one
                    # event barrier. A crash after any subset of clears leaves commit_ready,
                    # which remains globally fenced and is explicitly resumable.
                    for line in manifest["lines"]:
                        if line["phase"] in {
                                "verified", "rollback_verified", "aborted"}:
                            marker = self.engine.read_engine_maintenance(line["iid"])
                            if marker is not None:
                                self.engine.clear_engine_maintenance(
                                    line["iid"], manifest["txid"], line["phase"])
                    manifest = self._save(manifest, phase="committed")
            return self._finish_committed(manifest)
        finally:
            if self.client is not None:
                try:
                    self.client.close()
                except Exception:
                    pass
            try:
                fcntl.flock(lock.fileno(), fcntl.LOCK_UN)
            finally:
                lock.close()


def _parse(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--data", required=True)
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--iid", action="append", required=True)
    parser.add_argument(
        "--promote-default", action="store_true",
        help="after every selected non-absent line verifies, transactionally publish the "
             "candidate as the installed Engine default")
    parser.add_argument("--recover-created-container", action="append", default=[],
                        metavar="IID=FULL_CONTAINER_ID")
    parser.add_argument("--recover-unscoped-removed", action="append", default=[],
                        metavar="IID=FULL_CONTAINER_ID")
    parser.add_argument("--recovery-evidence")
    parser.add_argument("--recover-postflight", metavar="TXID")
    parser.add_argument("--postflight-recovery-evidence")
    parser.add_argument("--recover-postflight-added", action="append", default=[],
                        metavar="IID=FULL_CONTAINER_ID")
    parser.add_argument("--recover-precreate-missing-target", metavar="IID")
    parser.add_argument("--health-timeout", type=float, default=180.0)
    parser.add_argument("--quiet-seconds", type=float, default=2.0)
    args = parser.parse_args(argv)
    if not IMAGE_RE.fullmatch(args.candidate):
        parser.error("--candidate must be canonical sha256:<64hex>")
    if (any(not IID_RE.fullmatch(str(iid)) for iid in args.iid)
            or len(set(args.iid)) != len(args.iid)):
        parser.error("--iid values must be unique explicit instance IDs")
    if args.health_timeout <= 0 or args.quiet_seconds < 0:
        parser.error("timeouts must be positive")
    if (args.recover_precreate_missing_target is not None
            and (not IID_RE.fullmatch(args.recover_precreate_missing_target)
                 or args.recover_precreate_missing_target not in args.iid)):
        parser.error("--recover-precreate-missing-target must name one scoped IID")
    recover_created = {}
    for item in args.recover_created_container:
        match = re.fullmatch(r"([A-Za-z0-9][A-Za-z0-9_.-]{0,63})=([0-9a-f]{64})",
                             str(item or ""))
        if not match or match.group(1) not in args.iid \
                or match.group(1) in recover_created:
            parser.error("--recover-created-container must bind one scoped IID to a full ID")
        recover_created[match.group(1)] = match.group(2)
    args.recover_created = recover_created
    recover_unscoped = {}
    for item in args.recover_unscoped_removed:
        match = re.fullmatch(r"([A-Za-z0-9][A-Za-z0-9_.-]{0,63})=([0-9a-f]{64})",
                             str(item or ""))
        if not match or match.group(1) in args.iid \
                or match.group(1) in recover_unscoped:
            parser.error("--recover-unscoped-removed must bind one unscoped IID to a full ID")
        recover_unscoped[match.group(1)] = match.group(2)
    if bool(recover_unscoped) != bool(args.recovery_evidence):
        parser.error("--recovery-evidence is required exactly with unscoped recovery")
    args.recover_unscoped = recover_unscoped
    if bool(args.recover_postflight) != bool(args.postflight_recovery_evidence):
        parser.error("--postflight-recovery-evidence is required exactly with "
                     "--recover-postflight")
    if args.recover_postflight and not re.fullmatch(
            r"engine-replace-[0-9]+-[0-9a-f]{12}", args.recover_postflight):
        parser.error("--recover-postflight must be one exact transaction ID")
    recover_postflight_added = {}
    for item in args.recover_postflight_added:
        match = re.fullmatch(r"([A-Za-z0-9][A-Za-z0-9_.-]{0,63})=([0-9a-f]{64})",
                             str(item or ""))
        if (not match or match.group(1) in args.iid
                or match.group(1) in recover_postflight_added):
            parser.error("--recover-postflight-added must bind one unscoped IID "
                         "to a full ID")
        recover_postflight_added[match.group(1)] = match.group(2)
    if recover_postflight_added and not args.recover_postflight:
        parser.error("--recover-postflight-added requires --recover-postflight")
    args.recover_postflight_added = recover_postflight_added
    return args


def main(argv: list[str] | None = None) -> int:
    args = _parse(argv)
    try:
        result = EngineReplacement(
            Path(args.repo), Path(args.data), args.iid, args.candidate,
            health_timeout=args.health_timeout,
            quiet_seconds=args.quiet_seconds,
            recover_created=args.recover_created,
            recover_unscoped=args.recover_unscoped,
            recovery_evidence=(Path(args.recovery_evidence)
                               if args.recovery_evidence else None),
            recover_postflight=args.recover_postflight,
            postflight_recovery_evidence=(
                Path(args.postflight_recovery_evidence)
                if args.postflight_recovery_evidence else None),
            recover_postflight_added=args.recover_postflight_added,
            recover_precreate_missing_target=args.recover_precreate_missing_target,
            promote_default=args.promote_default).run()
        print(json.dumps({"ok": True, "txid": result["txid"],
                          "phase": result["phase"], "lines": [
                              {"iid": line["iid"], "phase": line["phase"]}
                              for line in result["lines"]]}, sort_keys=True))
        return 0
    except Exception as exc:
        print(json.dumps({"ok": False, "error": str(exc),
                          "error_type": type(exc).__name__}, sort_keys=True),
              file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
