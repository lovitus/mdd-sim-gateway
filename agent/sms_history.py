"""Persistent per-ICCID record of the last SMSC a successful MO-SMS used.

This is the only way to tell "SMSC missing" from "SMSC wrong" without a second billable
attempt.  The store is a local JSON file keyed by ICCID; it is updated only after a
successful submit and is never used to modify the SIM or the modem.
"""
from __future__ import annotations

import json
import logging
import os
import time


HISTORY_PATH = os.path.expanduser("~/.mdd-agent/sms_smsc.json")
log = logging.getLogger("mdd-modem-agent")


def _ensure_dir():
    directory = os.path.dirname(HISTORY_PATH)
    if directory:
        try:
            os.makedirs(directory, exist_ok=True)
        except OSError:
            pass


def load() -> dict:
    """Return the saved mapping of ICCID to {service_center, timestamp}."""
    try:
        with open(HISTORY_PATH, "r", encoding="utf-8") as handle:
            value = json.load(handle)
        if isinstance(value, dict):
            return value
    except (OSError, ValueError, TypeError, AttributeError):
        pass
    return {}


def save(store: dict) -> None:
    """Write the mapping to the local store, failures are non-fatal."""
    _ensure_dir()
    try:
        with open(HISTORY_PATH, "w", encoding="utf-8") as handle:
            json.dump(store, handle, indent=2)
        os.chmod(HISTORY_PATH, 0o600)
    except OSError as exc:
        log.warning("Could not persist SMSC history: %s", exc)


def record(iccid: str, service_center: str) -> None:
    """Store the SMSC that a successful MO-SMS used for this ICCID."""
    if not iccid or not service_center:
        return
    store = load()
    store[iccid] = {
        "service_center": service_center,
        "timestamp": time.time(),
    }
    save(store)


def get(iccid: str) -> dict | None:
    """Return the last recorded SMSC for this ICCID, or None."""
    if not iccid:
        return None
    return load().get(iccid)


def changed(iccid: str, current: str) -> bool:
    """Return True when the current SMSC differs from the last successful one."""
    if not iccid:
        return False
    previous = load().get(iccid)
    if not previous:
        # No record means we cannot tell whether the current value is wrong.
        return False
    return str(current or "") != str(previous.get("service_center") or "")
