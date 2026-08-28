"""Filesystem layout for Control and the host orchestrator.

The four roots have deliberately different lifetimes and backup policies:

* config: operator-owned settings, credentials and TLS material;
* state: databases, logs, audit history and durable recovery evidence;
* artifacts: downloaded/built helpers and immutable release/deploy records;
* runtime: sockets and other reboot-scoped process coordination objects.

``MDD_DATA`` is accepted only as a legacy all-in-one root.  A fresh process with no
MDD-specific environment follows XDG and never writes into its source checkout.
"""
from __future__ import annotations

import os
from pathlib import Path


APP_DIR = "mdd-sim-gateway"


def _absolute_env(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if value and not os.path.isabs(value):
        raise RuntimeError(f"{name} must be an absolute path")
    return os.path.abspath(value) if value else ""


def _home() -> Path:
    return Path.home()


def _xdg(name: str, fallback: Path) -> Path:
    value = _absolute_env(name)
    return Path(value) if value else fallback


LEGACY_DATA_DIR = _absolute_env("MDD_DATA")

STATE_DIR = (_absolute_env("MDD_STATE_DIR") or LEGACY_DATA_DIR or str(
    _xdg("XDG_STATE_HOME", _home() / ".local" / "state") / APP_DIR))
CONFIG_DIR = (_absolute_env("MDD_CONFIG_DIR") or LEGACY_DATA_DIR or str(
    _xdg("XDG_CONFIG_HOME", _home() / ".config") / APP_DIR))
ARTIFACT_DIR = (_absolute_env("MDD_ARTIFACT_DIR") or LEGACY_DATA_DIR or str(
    _xdg("XDG_DATA_HOME", _home() / ".local" / "share") / APP_DIR / "artifacts"))

_runtime = _absolute_env("MDD_RUNTIME_DIR")
if not _runtime:
    xdg_runtime = _absolute_env("XDG_RUNTIME_DIR")
    _runtime = str(Path(xdg_runtime) / APP_DIR) if xdg_runtime else f"/tmp/{APP_DIR}-{os.getuid()}"
RUNTIME_DIR = _runtime


def ensure_private_dir(path: str) -> None:
    """Create one application-owned directory and enforce owner-only access."""
    os.makedirs(path, mode=0o700, exist_ok=True)
    os.chmod(path, 0o700)


def using_legacy_layout() -> bool:
    """Whether one legacy root currently supplies more than the state root."""
    return bool(LEGACY_DATA_DIR and not any(os.environ.get(name) for name in (
        "MDD_STATE_DIR", "MDD_CONFIG_DIR", "MDD_ARTIFACT_DIR")))
