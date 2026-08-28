"""Offline APN candidate lookup from the public-domain mobile-broadband-provider-info DB.

The database is a GNOME/NetworkManager dataset released under CC-PDDC (public domain).
It contains only factual carrier settings and is treated as data, not code.  The parser
exposes APNs keyed by MCC/MNC so unknown-SIM APN resolution can offer operator-specific
candidates before falling back to manual entry.
"""
from __future__ import annotations

import functools
import os
import re
from xml.etree import ElementTree


DATA_PATHS = [
    os.path.join(os.path.dirname(__file__), "resources", "serviceproviders.xml"),
    "/usr/share/mobile-broadband-provider-info/serviceproviders.xml",
]


def _database_path() -> str | None:
    for path in DATA_PATHS:
        if os.path.isfile(path):
            return path
    return None


def _plmn_keys(mcc: str, mnc: str) -> list[tuple[str, str]]:
    # Keep the exact MCC/MNC only.  The caller is responsible for trying the 2- and 3-digit
    # MNC variants because the IMSI alone does not encode MNC length.  We deliberately do not
    # strip a leading zero: "003" and "03" are different MNC values.
    return [(mcc, mnc)]


@functools.lru_cache(maxsize=1)
def _load() -> dict[tuple[str, str], list[dict]]:
    """Parse the XML into a map keyed by (mcc, mnc)."""
    path = _database_path()
    if not path:
        return {}
    try:
        with open(path, encoding="utf-8") as handle:
            text = handle.read()
    except OSError:
        return {}
    # ElementTree attempts to load the external DTD if the DOCTYPE is left in place.
    text = re.sub(r"<!DOCTYPE[^>]*>", "", text, count=1)
    try:
        root = ElementTree.fromstring(text)
    except ElementTree.ParseError:
        return {}
    result: dict[tuple[str, str], list[dict]] = {}
    for country in root.findall("country"):
        for provider in country.findall("provider"):
            gsm = provider.find("gsm")
            if gsm is None:
                continue
            names = [(network.get("mcc", ""), network.get("mnc", ""))
                     for network in gsm.findall("network-id")]
            if not names:
                continue
            keys = [(mcc, mnc) for mcc, mnc in names if len(mcc) == 3 and mnc]
            if not keys:
                continue
            for apn in gsm.findall("apn"):
                value = str(apn.get("value") or "").strip()
                if not value:
                    continue
                usage_attr = str(apn.get("usage") or "").strip()
                usage = apn.find("usage")
                usage_type = (usage_attr or
                              (str(usage.get("type") or "").strip() if usage is not None else ""))
                if usage_type and usage_type != "internet":
                    continue
                label = apn.find("name")
                name = str(label.text or "").strip() if label is not None else ""
                plan = apn.find("plan")
                plan_type = str(plan.get("type") or "").strip() if plan is not None else ""
                entry = {"apn": value, "name": name or value, "plan": plan_type}
                for key in keys:
                    result.setdefault(key, []).append(entry)
    return result


def lookup(mcc: str | None, mnc: str | None) -> list[dict]:
    """Return APN candidate dicts for an exact MCC/MNC pair."""
    mcc = re.sub(r"\D", "", str(mcc or ""))
    mnc = re.sub(r"\D", "", str(mnc or ""))
    if len(mcc) != 3 or not mnc:
        return []
    table = _load()
    seen: set[str] = set()
    values: list[dict] = []
    for key in _plmn_keys(mcc, mnc):
        for item in table.get(key, []):
            apn = item["apn"]
            if apn in seen:
                continue
            seen.add(apn)
            values.append(dict(item))
    return values


def lookup_by_imsi(imsi: str | None) -> list[dict]:
    """Return APN candidates from the first 5 or 6 digits of an IMSI.

    The IMSI does not encode whether the MNC is 2 or 3 digits, so both variants are
    tried and the union is returned.  Duplicates are removed; the caller is expected to
    treat all results as candidates, not as authoritative settings.
    """
    imsi = re.sub(r"\D", "", str(imsi or ""))
    if len(imsi) < 5:
        return []
    mcc = imsi[:3]
    seen: set[str] = set()
    values: list[dict] = []
    for mnc in (imsi[3:6], imsi[3:5]):
        if len(mnc) < 2:
            continue
        for item in lookup(mcc, mnc):
            if item["apn"] in seen:
                continue
            seen.add(item["apn"])
            values.append(item)
    return values
