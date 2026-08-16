"""Single product-version source shared by source and packaged deployments."""
from __future__ import annotations

import os
from pathlib import Path


def current() -> str:
    override = os.environ.get("MDD_VERSION", "").strip()
    if override:
        return override.removeprefix("v")
    for candidate in (
        Path(__file__).resolve().parents[2] / "VERSION",
        Path("/app/VERSION"),
    ):
        try:
            value = candidate.read_text(encoding="utf-8").strip()
            if value:
                return value.removeprefix("v")
        except OSError:
            pass
    return "1.3.1"


VERSION = current()
