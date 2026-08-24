#!/usr/bin/env python3
"""Verify the exact least-privilege entitlement surface of a macOS Agent package."""

from __future__ import annotations

import argparse
from pathlib import Path
import plistlib
import re
import subprocess


AUDIO = {"com.apple.security.device.audio-input": True}


def entitlements(path: Path) -> dict:
    value = subprocess.run(
        ["codesign", "-d", "--entitlements", ":-", str(path)],
        capture_output=True, check=False)
    if value.returncode:
        raise SystemExit(f"cannot inspect final entitlements: {path.name}")
    if not value.stdout.strip():
        return {}
    try:
        result = plistlib.loads(value.stdout)
    except Exception as exc:
        raise SystemExit(f"invalid final entitlements: {path.name}: {exc}")
    if not isinstance(result, dict):
        raise SystemExit(f"invalid final entitlements object: {path.name}")
    return result


def is_macho(path: Path) -> bool:
    value = subprocess.run(["file", str(path)], capture_output=True, text=True,
                           check=False)
    return value.returncode == 0 and "Mach-O" in value.stdout


def signature(path: Path) -> tuple[str, str]:
    verify = subprocess.run(
        ["codesign", "--verify", "--strict", "--verbose=2", str(path)],
        capture_output=True, check=False)
    if verify.returncode:
        raise SystemExit(f"invalid final code signature: {path}")
    details = subprocess.run(
        ["codesign", "-d", "--verbose=4", str(path)],
        capture_output=True, check=False)
    output = (details.stdout + details.stderr).decode("utf-8", "replace")
    if details.returncode or "Authority=Developer ID Application:" not in output:
        raise SystemExit(f"final code is not Developer ID Application code: {path}")
    team = re.search(r"^TeamIdentifier=(.+)$", output, re.MULTILINE)
    flags = re.search(
        r"(?:^CodeDirectory\b[^\r\n]*?\s|^)"
        r"flags=(0x[0-9a-fA-F]+(?:\([A-Za-z0-9_.-]+(?:,[A-Za-z0-9_.-]+)*\))?)"
        r"(?=$|\s)", output, re.MULTILINE)
    if not team or not flags or "runtime" not in set(
            re.findall(r"[A-Za-z0-9_.-]+", flags.group(1))[1:]):
        raise SystemExit(f"final code lacks stable Team/runtime identity: {path}")
    return team.group(1).strip(), flags.group(1)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("root", type=Path)
    args = parser.parse_args()
    root = args.root
    app = root / "MDD Agent.app"
    main_executable = app / "Contents" / "MacOS" / "mdd-agent-gui"
    embedded_audio = app / "Contents" / "Frameworks" / "mdd-call-audio-helper"
    expected = {
        app: AUDIO,
        main_executable: AUDIO,
        embedded_audio: AUDIO,
        root / "mdd-agent": AUDIO,
        root / "mdd-cellular-io": {},
        root / "mdd-call-audio-helper": AUDIO,
    }
    for path in expected:
        if not path.exists():
            raise SystemExit(f"required entitlement target is missing: {path}")
    for path in (app / "Contents").rglob("*"):
        if not path.is_symlink() and path.is_file() and is_macho(path):
            expected.setdefault(path, {})
    teams = set()
    for path, wanted in expected.items():
        team, _flags = signature(path)
        teams.add(team)
        observed = entitlements(path)
        if observed != wanted:
            raise SystemExit(
                f"unexpected final entitlements for {path}: {sorted(observed)}")
    if len(teams) != 1:
        raise SystemExit(f"final package TeamIdentifier mismatch: {sorted(teams)}")
    deep = subprocess.run(
        ["codesign", "--verify", "--deep", "--strict", "--verbose=2", str(app)],
        capture_output=True, check=False)
    if deep.returncode:
        raise SystemExit("final MDD Agent.app deep signature verification failed")

    info = plistlib.loads((app / "Contents" / "Info.plist").read_bytes())
    if info.get("CFBundleIdentifier") != "com.mdd.agent":
        raise SystemExit("final MDD Agent.app bundle identifier mismatch")
    if not str(info.get("NSMicrophoneUsageDescription") or "").strip():
        raise SystemExit("final MDD Agent.app microphone usage description is missing")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
