"""Pure durable contract shared by Control and the host Engine replacement owner."""
from __future__ import annotations

import json
import hashlib
import math
import re


IID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$")
TXID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.:-]{7,127}$")
IMAGE_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
CONTAINER_RE = re.compile(r"^[0-9a-f]{64}$")
RUN_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$")
STARTED_RE = re.compile(
    r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$")
PHASES = {
    "prepared", "running", "commit_ready", "committed", "aborted", "manual_required",
}
LINE_PHASES = {
    "pending", "prepared", "source_quiescing", "source_removed", "target_starting",
    "target_started", "verified", "rollback_required", "rollback_starting",
    "rollback_started", "rollback_verified", "aborted", "manual_required",
    "skipped_absent",
}
TERMINAL_LINES = {"verified", "rollback_verified", "aborted", "skipped_absent"}
UNSCOPED_REMOVAL_PHASES = {"planned", "removed", "forced_unknown", "forensic"}
UNSCOPED_REMOVAL_REASONS = {"card_removed", "reader_unplugged"}
UNSCOPED_REMOVAL_ATTESTATIONS = {"control_card_monitor", "operator_forensic"}
SCOPED_CARD_LOSS_REASONS = {"card_removed", "reader_unplugged"}
SCOPED_CARD_LOSS_ATTESTATION = "control_card_monitor"
DEFAULT_PROMOTION_PHASES = {
    "prepared", "old_default_retained", "global_promoted", "committed",
    "aborted", "manual_required",
}


class ContractError(ValueError):
    """A replacement manifest or snapshot is not the exact shared contract."""


def _fact(value: object, *, allow_none: bool = False) -> dict | None:
    if value is None and allow_none:
        return None
    if not isinstance(value, dict) or set(value) != {
            "container_id", "image_id", "started_at", "restart_count", "pid",
            "run_id", "run_id_mode"}:
        raise ContractError("invalid Engine generation fact")
    if (not CONTAINER_RE.fullmatch(str(value.get("container_id") or ""))
            or not IMAGE_RE.fullmatch(str(value.get("image_id") or ""))):
        raise ContractError("invalid Engine generation identity")
    if (not isinstance(value.get("started_at"), str)
            or not STARTED_RE.fullmatch(value["started_at"])
            or type(value.get("restart_count")) is not int
            or value["restart_count"] < 0 or type(value.get("pid")) is not int
            or value["pid"] <= 0):
        raise ContractError("invalid Engine process generation")
    if (value.get("run_id_mode") != "present"
            or not RUN_RE.fullmatch(str(value.get("run_id") or ""))):
        raise ContractError("replacement requires an Engine run ID")
    return json.loads(json.dumps(value))


def _unscoped_item(value: object, scoped: set[str]) -> dict:
    if not isinstance(value, dict) or set(value) != {
            "iid", "container_id", "image_id", "started_at", "restart_count", "running"}:
        raise ContractError("invalid unscoped Engine fact")
    iid = value.get("iid")
    if (not isinstance(iid, str) or not IID_RE.fullmatch(iid) or iid in scoped
            or not CONTAINER_RE.fullmatch(str(value.get("container_id") or ""))
            or not IMAGE_RE.fullmatch(str(value.get("image_id") or ""))
            or not isinstance(value.get("started_at"), str)
            or not STARTED_RE.fullmatch(value["started_at"])
            or type(value.get("restart_count")) is not int
            or value["restart_count"] < 0
            or type(value.get("running")) is not bool):
        raise ContractError("invalid unscoped Engine identity")
    return json.loads(json.dumps(value))


def validate_manifest(value: object) -> dict:
    if not isinstance(value, dict):
        raise ContractError("invalid Engine replacement manifest schema")
    version = value.get("version")
    base_keys = {
        "version", "txid", "phase", "candidate_image", "iids", "started_at",
        "updated_at", "unscoped", "lines",
    }
    expected_keys = base_keys if version == 1 else base_keys | {"promote_default"}
    if set(value) != expected_keys:
        raise ContractError("invalid Engine replacement manifest schema")
    if type(version) is not int or version not in {1, 2}:
        raise ContractError("invalid Engine replacement manifest version")
    if version == 2 and type(value.get("promote_default")) is not bool:
        raise ContractError("invalid Engine replacement promotion intent")
    if not TXID_RE.fullmatch(str(value.get("txid") or "")):
        raise ContractError("invalid Engine replacement transaction")
    if value.get("phase") not in PHASES:
        raise ContractError("invalid Engine replacement phase")
    if not IMAGE_RE.fullmatch(str(value.get("candidate_image") or "")):
        raise ContractError("invalid Engine replacement image")
    iids = value.get("iids")
    if (not isinstance(iids, list) or not iids
            or any(not isinstance(iid, str) or not IID_RE.fullmatch(iid) for iid in iids)
            or len(set(iids)) != len(iids) or iids != sorted(iids)):
        raise ContractError("invalid Engine replacement scope")
    for key in ("started_at", "updated_at"):
        if (type(value.get(key)) not in (int, float)
                or not math.isfinite(value[key]) or value[key] <= 0):
            raise ContractError("invalid Engine replacement timestamp")
    unscoped = value.get("unscoped")
    if not isinstance(unscoped, list):
        raise ContractError("invalid unscoped Engine snapshot")
    checked_unscoped = [_unscoped_item(item, set(iids)) for item in unscoped]
    unscoped_iids = [item["iid"] for item in checked_unscoped]
    if len(set(unscoped_iids)) != len(unscoped_iids) or unscoped_iids != sorted(unscoped_iids):
        raise ContractError("unscoped Engine snapshot is not canonical")
    lines = value.get("lines")
    if not isinstance(lines, list) or len(lines) != len(iids):
        raise ContractError("invalid Engine replacement lines")
    seen_lines = set()
    for line in lines:
        if not isinstance(line, dict) or set(line) != {
                "iid", "phase", "source", "terminal", "error"}:
            raise ContractError("invalid Engine replacement line schema")
        iid = line.get("iid")
        phase = line.get("phase")
        if (iid not in iids or iid in seen_lines or phase not in LINE_PHASES
                or not isinstance(line.get("error"), str)):
            raise ContractError("invalid Engine replacement line")
        seen_lines.add(iid)
        source = line.get("source")
        terminal = line.get("terminal")
        if phase == "skipped_absent":
            if source is not None or terminal is not None:
                raise ContractError("absent line has generation facts")
        else:
            _fact(source)
            _fact(terminal, allow_none=phase not in {"verified", "rollback_verified"})
        if phase in {"verified", "rollback_verified"} and terminal is None:
            raise ContractError("terminal Engine fact is missing")
        if phase == "verified" and terminal["image_id"] != value["candidate_image"]:
            raise ContractError("verified line does not use the candidate image")
        if phase == "rollback_verified" and terminal["image_id"] != source["image_id"]:
            raise ContractError("rollback line does not use the source image")
        if terminal is not None and (terminal["container_id"] == source["container_id"]
                                     or terminal["run_id"] == source["run_id"]):
            raise ContractError("terminal line did not create a new Engine generation")
    if [line["iid"] for line in lines] != iids:
        raise ContractError("Engine replacement lines are not canonical")
    if value["phase"] in {"committed", "aborted"} \
            and any(line["phase"] not in TERMINAL_LINES for line in lines):
        raise ContractError("terminal manifest contains a non-terminal line")
    return json.loads(json.dumps(value))


def replacement_scope_digest(manifest: object) -> str:
    """Digest only immutable transaction scope, so line phase updates cannot stale receipts."""
    checked = validate_manifest(manifest)
    immutable = {
        "version": checked["version"], "txid": checked["txid"],
        "candidate_image": checked["candidate_image"], "iids": checked["iids"],
        "started_at": checked["started_at"], "unscoped": checked["unscoped"],
        "lines": [{"iid": line["iid"], "source": line["source"]}
                  for line in checked["lines"]],
    }
    # Version 1 scope digests are already durable production evidence and must remain byte-for-
    # byte compatible. Version 2 adds the release intent to the immutable transaction scope.
    if checked["version"] == 2:
        immutable["promote_default"] = checked["promote_default"]
    payload = json.dumps(immutable, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(payload).hexdigest()


def validate_default_promotion(value: object, manifest: object | None = None) -> dict:
    """Validate the durable handoff from an immutable canary to the installed default tag."""
    if not isinstance(value, dict) or set(value) != {
            "version", "txid", "scope_digest", "candidate_image", "default_ref",
            "previous_image", "rollback_ref", "phase", "created_at", "updated_at"}:
        raise ContractError("invalid Engine default promotion schema")
    if (type(value.get("version")) is not int or value["version"] != 1
            or not TXID_RE.fullmatch(str(value.get("txid") or ""))
            or not re.fullmatch(r"[0-9a-f]{64}", str(value.get("scope_digest") or ""))
            or not IMAGE_RE.fullmatch(str(value.get("candidate_image") or ""))
            or not IMAGE_RE.fullmatch(str(value.get("previous_image") or ""))
            or value.get("phase") not in DEFAULT_PROMOTION_PHASES):
        raise ContractError("invalid Engine default promotion identity")
    for key in ("default_ref", "rollback_ref"):
        ref = value.get(key)
        if (not isinstance(ref, str) or not ref or len(ref) > 255
                or "@" in ref or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:/-]*", ref)):
            raise ContractError("invalid Engine default promotion reference")
    for key in ("created_at", "updated_at"):
        if (type(value.get(key)) not in (int, float)
                or not math.isfinite(value[key]) or value[key] <= 0):
            raise ContractError("invalid Engine default promotion timestamp")
    if value["updated_at"] < value["created_at"]:
        raise ContractError("Engine default promotion timestamps are reversed")
    if manifest is not None:
        checked = validate_manifest(manifest)
        if (checked["version"] != 2 or checked.get("promote_default") is not True
                or value["txid"] != checked["txid"]
                or value["scope_digest"] != replacement_scope_digest(checked)
                or value["candidate_image"] != checked["candidate_image"]):
            raise ContractError("Engine default promotion scope mismatch")
    return json.loads(json.dumps(value))


def validate_unscoped_removal_receipt(value: object, manifest: object) -> dict:
    """Validate one exact external removal without weakening the original snapshot."""
    checked_manifest = validate_manifest(manifest)
    if not isinstance(value, dict) or set(value) != {
            "version", "txid", "scope_digest", "iid", "original", "phase",
            "reason", "attestation", "card", "channels", "evidence_digest",
            "created_at", "updated_at"}:
        raise ContractError("invalid unscoped removal receipt schema")
    iid = value.get("iid")
    original = value.get("original")
    if (type(value.get("version")) is not int or value["version"] != 1
            or value.get("txid") != checked_manifest["txid"]
            or value.get("scope_digest") != replacement_scope_digest(checked_manifest)
            or not isinstance(iid, str) or not IID_RE.fullmatch(iid)
            or iid in checked_manifest["iids"]
            or value.get("phase") not in UNSCOPED_REMOVAL_PHASES
            or value.get("reason") not in UNSCOPED_REMOVAL_REASONS
            or value.get("attestation") not in UNSCOPED_REMOVAL_ATTESTATIONS
            or type(value.get("channels")) is not int or value["channels"] < -1
            or not re.fullmatch(r"[0-9a-f]{64}", str(value.get("evidence_digest") or ""))):
        raise ContractError("invalid unscoped removal receipt identity")
    expected = next((item for item in checked_manifest["unscoped"]
                     if item["iid"] == iid), None)
    if expected is None or _unscoped_item(original, set(checked_manifest["iids"])) != expected:
        raise ContractError("unscoped removal receipt changed the original snapshot")
    card = value.get("card")
    if not isinstance(card, dict) or set(card) != {
            "reader_name", "reader_index", "iccid", "matched"}:
        raise ContractError("invalid unscoped removal card event")
    if (not isinstance(card.get("reader_name"), str)
            or len(card["reader_name"]) > 255
            or type(card.get("reader_index")) is not int or card["reader_index"] < -1
            or not isinstance(card.get("iccid"), str) or len(card["iccid"]) > 32
            or card.get("matched") != iid):
        raise ContractError("invalid unscoped removal card identity")
    if (value["attestation"] == "control_card_monitor"
            and (not card["reader_name"] or card["reader_index"] < 0
                 or not card["iccid"] or value["phase"] == "forensic")):
        raise ContractError("card-monitor receipt lacks live event identity")
    if value["attestation"] == "operator_forensic" and value["phase"] != "forensic":
        raise ContractError("operator receipt must remain forensic")
    if value["phase"] in {"planned", "removed"} and value["channels"] != 0:
        raise ContractError("authorized unscoped removal lacks zero-channel proof")
    for key in ("created_at", "updated_at"):
        if (type(value.get(key)) not in (int, float)
                or not math.isfinite(value[key]) or value[key] <= 0):
            raise ContractError("invalid unscoped removal timestamp")
    if value["updated_at"] < value["created_at"]:
        raise ContractError("unscoped removal timestamps are reversed")
    return json.loads(json.dumps(value))


def validate_scoped_card_loss_intent(value: object, manifest: object) -> dict:
    """Validate one immutable card-loss tombstone for a scoped replacement line.

    This is a fail-closed record of intent, never authorization to subtract topology or
    recreate a source/target.  Its only consumer action is to latch replacement manual.
    """
    checked_manifest = validate_manifest(manifest)
    if not isinstance(value, dict) or set(value) != {
            "version", "txid", "scope_digest", "iid", "source", "reason",
            "attestation", "card", "evidence_digest", "created_at"}:
        raise ContractError("invalid scoped card-loss intent schema")
    iid = value.get("iid")
    if (type(value.get("version")) is not int or value["version"] != 1
            or value.get("txid") != checked_manifest["txid"]
            or value.get("scope_digest") != replacement_scope_digest(checked_manifest)
            or not isinstance(iid, str) or iid not in checked_manifest["iids"]
            or value.get("reason") not in SCOPED_CARD_LOSS_REASONS
            or value.get("attestation") != SCOPED_CARD_LOSS_ATTESTATION
            or not re.fullmatch(r"[0-9a-f]{64}",
                                str(value.get("evidence_digest") or ""))
            or type(value.get("created_at")) not in (int, float)
            or not math.isfinite(value["created_at"]) or value["created_at"] <= 0):
        raise ContractError("invalid scoped card-loss intent identity")
    line = next(item for item in checked_manifest["lines"] if item["iid"] == iid)
    if line["phase"] == "skipped_absent" or line["source"] is None:
        raise ContractError("absent scoped line cannot own a card-loss intent")
    if _fact(value.get("source")) != line["source"]:
        raise ContractError("scoped card-loss intent changed the source generation")
    card = value.get("card")
    if not isinstance(card, dict) or set(card) != {
            "reader_name", "reader_index", "iccid", "matched"}:
        raise ContractError("invalid scoped card-loss card event")
    if (not isinstance(card.get("reader_name"), str) or not card["reader_name"]
            or len(card["reader_name"]) > 255
            or type(card.get("reader_index")) is not int or card["reader_index"] < 0
            or not isinstance(card.get("iccid"), str) or not card["iccid"]
            or len(card["iccid"]) > 32 or card.get("matched") != iid):
        raise ContractError("scoped card-loss intent lacks live card identity")
    evidence = {
        "iid": iid, "source": line["source"], "reason": value["reason"], "card": card,
    }
    expected_digest = hashlib.sha256(json.dumps(
        evidence, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    if value["evidence_digest"] != expected_digest:
        raise ContractError("scoped card-loss evidence digest mismatch")
    return json.loads(json.dumps(value))


def snapshot_unscoped_engines(client, excluded: set[str]) -> list[dict]:
    """Capture the same canonical unscoped Docker snapshot for host and Control."""
    values = []
    prefix = "mdd-sim-gateway-engine-"
    for container in client.containers.list(all=True, filters={
            "label": "io.mdd-sim-gateway.component=engine"}):
        name = str(getattr(container, "name", "") or "")
        if not name.startswith(prefix):
            continue
        iid = name[len(prefix):]
        if iid in excluded:
            continue
        container.reload()
        attrs = container.attrs or {}
        state = attrs.get("State") or {}
        values.append(_unscoped_item({
            "iid": iid,
            "container_id": str(container.id),
            "image_id": str(attrs.get("Image") or ""),
            "started_at": str(state.get("StartedAt") or ""),
            "restart_count": attrs.get("RestartCount"),
            "running": state.get("Status") == "running",
        }, excluded))
    ordered = sorted(values, key=lambda item: item["iid"])
    if len({item["iid"] for item in ordered}) != len(ordered):
        raise ContractError("duplicate unscoped Engine identity")
    return ordered
