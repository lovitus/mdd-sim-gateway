"""Curated modem firmware baseline matrix.

A modem can be registered, expose every AT/MBN command and still be unable to complete a
mobile-originated SMS because its firmware baseline never enabled the IMS/VoLTE stack the
serving network requires.  That failure surfaces as an opaque submit error, so the operator
has no way to tell a broken gateway apart from a firmware precondition.  This module turns
the baseline into an explicit, auditable verdict.

Deliberate boundaries:

- Detection only.  Nothing here downloads, unpacks, verifies or flashes an image, and no
  caller may use it to start an unattended upgrade.  A guided upgrade is a documented human
  procedure (``agent/windows/ec20-upgrade/``) performed with the module's own QCN/EFS backup.
- The hardware branch is derived from the reported revision string, never from a marketing
  model name: ``ATI`` returns ``EC20F`` for several incompatible hardware branches, while
  ``AT+GMR`` identifies the branch and baseline exactly.
- A guided upgrade is offered only for a *same-baseline* target whose official package digest
  is recorded below.  Without a recorded digest the matrix cannot prove which image an
  operator would install, so it must not present an upgrade as safe.
- Anything not recorded stays ``unknown``.  An unrecognised baseline is a request for manual
  verification, never an implied upgrade or an implied fault.
"""
from __future__ import annotations

import re
from dataclasses import dataclass, field

# ``EC20CEHDLGR08A06M1G`` -> branch ``EC20CEHDLG``, baseline ``R08``, build ``A06M1G``.
# 3GPP does not standardise revision strings, so a baseline is only parsed when the vendor
# pattern matches exactly; a partial match must not be turned into a confident branch.
_REVISION_RE = re.compile(r"^(?P<branch>[A-Z0-9]{4,})(?P<baseline>R\d{2})(?P<build>[A-Z0-9]*)$")

_UPGRADE_DOC = "agent/windows/ec20-upgrade/README.md"


@dataclass(frozen=True)
class Deficiency:
    """A capability that a specific baseline is known to be unable to provide."""

    impact: tuple[str, ...]
    detail: str


@dataclass(frozen=True)
class Branch:
    """One hardware branch: which baselines are accepted and what replaces the rest."""

    verified: frozenset[str]
    target: str
    deficient: dict[str, Deficiency] = field(default_factory=dict)
    # SHA-256 of the official package for ``target``, taken from the offline kit's
    # SHA256SUMS.txt.  Empty means "no signed package recorded", which keeps guided upgrade
    # unavailable by construction rather than by a reviewer remembering to check.
    target_package_sha256: str = ""
    # A cross-baseline move rewrites calibration/CEFS content, so it can never be a guided
    # in-place upgrade even once the package digest is known.
    cross_baseline_requires_service: bool = True
    doc: str = _UPGRADE_DOC


MATRIX: dict[str, Branch] = {
    "EC20CEHDLG": Branch(
        # Accepted on 2026-08-19 real hardware: CREG=5/CEREG=5, IMS/VoLTE active, cellular
        # data and an operator-confirmed MO SMS.
        verified=frozenset({"EC20CEHDLGR08A06M1G"}),
        target="EC20CEHDLGR08A06M1G",
        deficient={
            "EC20CEHDLGR06A13M1G": Deficiency(
                impact=("sms", "ims"),
                detail=("This baseline ships without an enabled IMS/VoLTE stack. While the "
                        "modem is attached to LTE only, a mobile-originated SMS is rejected "
                        "with an unspecified submit error even though registration, data and "
                        "the SMS centre are correct."),
            ),
        },
    ),
}

_UNREPORTED = "unreported"
_VERIFIED = "verified"
_UNKNOWN = "unknown"
_ACTION_REQUIRED = "action_required"


def parse_revision(firmware: str) -> dict:
    """Split a vendor revision string into branch/baseline/build, or return empties."""
    value = str(firmware or "").strip().upper()
    match = _REVISION_RE.match(value)
    if not match:
        return {"revision": value, "branch": "", "baseline": "", "build": ""}
    return {"revision": value, "branch": match.group("branch"),
            "baseline": match.group("baseline"), "build": match.group("build")}


def advise(firmware: str, *, model: str = "") -> dict:
    """Return the compatibility verdict for a reported firmware revision.

    ``model`` is accepted for display only.  It never selects a branch, because several
    incompatible hardware branches report the same model name.
    """
    parsed = parse_revision(firmware)
    advice = {
        "state": _UNREPORTED, "revision": parsed["revision"], "branch": parsed["branch"],
        "baseline": parsed["baseline"], "model": str(model or "").strip(),
        "recommended": "", "same_baseline": False, "guided_upgrade": False,
        "requires_service": False, "impact": [], "reason": "", "doc": "",
    }
    if not parsed["revision"]:
        advice["reason"] = ("This modem did not report a firmware revision, so its baseline "
                           "cannot be checked. Read AT+GMR on the device before relying on "
                           "SMS or voice.")
        return advice

    branch = MATRIX.get(parsed["branch"]) if parsed["branch"] else None
    if branch is None:
        advice["state"] = _UNKNOWN
        advice["reason"] = ("This firmware baseline is not recorded in the compatibility "
                            "matrix. Compare the full ATI/AT+GMR output with the module "
                            "label before trusting SMS or voice; do not flash anything on "
                            "the basis of the model name alone.")
        return advice

    advice["doc"] = branch.doc
    advice["recommended"] = branch.target
    advice["same_baseline"] = bool(
        parsed["baseline"] and parsed["baseline"] == parse_revision(branch.target)["baseline"])
    if parsed["revision"] in branch.verified:
        advice["state"] = _VERIFIED
        advice["reason"] = ""
        return advice

    deficiency = branch.deficient.get(parsed["revision"])
    if deficiency is None:
        advice["state"] = _UNKNOWN
        advice["reason"] = ("This hardware branch is known but this exact baseline has not "
                            f"been verified. The accepted baseline is {branch.target}; "
                            "verify this module manually before relying on SMS or voice.")
        return advice

    advice["state"] = _ACTION_REQUIRED
    advice["impact"] = list(deficiency.impact)
    # Guided upgrade requires all three: the same baseline, a recorded official package
    # digest, and no calibration-rewriting service step.
    advice["guided_upgrade"] = bool(
        advice["same_baseline"] and branch.target_package_sha256
        and not branch.cross_baseline_requires_service)
    advice["requires_service"] = not advice["guided_upgrade"]
    if advice["guided_upgrade"]:
        advice["reason"] = (f"{deficiency.detail} A same-baseline upgrade to {branch.target} "
                            "is available as a documented, attended procedure.")
    else:
        advice["reason"] = (
            f"{deficiency.detail} Moving to {branch.target} crosses a firmware baseline, so "
            "it rewrites calibration/CEFS content and cannot be applied automatically or "
            "unattended. Use the documented attended procedure with this module's own "
            "QCN/EFS backup, or replace the hardware.")
    return advice
