#!/usr/bin/env python3
"""Migrate one legacy all-in-one data root into the v2 four-root layout.

The service must be stopped first.  This command never overwrites a target and never removes
the source.  On failure it removes only target roots created by this invocation.  A successful
copy is byte-verified and records a manifest under the artifact root before returning.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import sys
import time
import uuid


CONFIG_NAMES = {"config.yaml", "auth.json", "certs", "install-mode", "runtime.env"}
ARTIFACT_NAMES = {
    "agent-packages", "agent-releases", "build-cache", "deploy-backups",
    "deploy-records", "deploy-stage", "deploy-staging", "lpac",
    "runtime-control-image.txt", "sources", "tools", "update",
}
IGNORED_NAMES = {".gitkeep"}


class MigrationError(RuntimeError):
    pass


def category(relative: Path) -> str:
    top = relative.parts[0]
    if top == "backups":
        return "artifacts" if relative.name.endswith(".tar.gz") else "config"
    if top in CONFIG_NAMES:
        return "config"
    if top in ARTIFACT_NAMES:
        return "artifacts"
    return "state"


def _identity(path: Path) -> os.stat_result:
    try:
        value = path.lstat()
    except OSError as exc:
        raise MigrationError(f"cannot inspect {path}: {exc}") from exc
    if stat.S_ISLNK(value.st_mode):
        raise MigrationError(f"symlinks are not accepted in legacy data: {path}")
    return value


def inventory(source: Path) -> list[dict]:
    source = source.absolute()
    root_stat = _identity(source)
    if not stat.S_ISDIR(root_stat.st_mode):
        raise MigrationError("legacy source is not a directory")
    rows: list[dict] = []
    for directory, dirnames, filenames in os.walk(source, followlinks=False):
        base = Path(directory)
        dirnames.sort()
        filenames.sort()
        for name in list(dirnames):
            value = _identity(base / name)
            if not stat.S_ISDIR(value.st_mode):
                raise MigrationError(f"legacy directory entry is not a directory: {base / name}")
        for name in filenames:
            path = base / name
            relative = path.relative_to(source)
            if relative.parts[0] in IGNORED_NAMES:
                continue
            value = _identity(path)
            if not stat.S_ISREG(value.st_mode):
                raise MigrationError(f"legacy data contains a special file: {path}")
            rows.append({
                "relative": relative.as_posix(),
                "category": category(relative),
                "size": value.st_size,
                "mode": stat.S_IMODE(value.st_mode),
            })
    return rows


def _digest(path: Path) -> str:
    result = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            result.update(chunk)
    return result.hexdigest()


def _fsync_tree(root: Path) -> None:
    for directory, dirnames, filenames in os.walk(root, topdown=False, followlinks=False):
        base = Path(directory)
        for name in sorted(filenames):
            descriptor = os.open(base / name, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
            try:
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
        for name in sorted(dirnames):
            descriptor = os.open(base / name, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
            try:
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
        descriptor = os.open(base, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)


def _validate_roots(source: Path, targets: dict[str, Path]) -> None:
    source = source.absolute()
    source_resolved = source.resolve(strict=False)
    resolved = []
    for name, root in targets.items():
        if not root.is_absolute():
            raise MigrationError(f"{name} target must be absolute")
        absolute = root.absolute()
        canonical = absolute.resolve(strict=False)
        if canonical == source_resolved or source_resolved in canonical.parents:
            raise MigrationError(f"{name} target must be outside the legacy source")
        if os.path.lexists(absolute):
            raise MigrationError(f"{name} target already exists: {absolute}")
        resolved.append(canonical)
    if len(set(resolved)) != len(resolved):
        raise MigrationError("config, state and artifact targets must be distinct")


def migrate(source: Path, targets: dict[str, Path]) -> dict:
    source = source.absolute()
    _validate_roots(source, targets)
    rows = inventory(source)
    txid = f"{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{uuid.uuid4().hex[:12]}"
    created: list[Path] = []
    try:
        for root in targets.values():
            root.mkdir(parents=True, mode=0o700)
            os.chmod(root, 0o700)
            created.append(root)
        enriched = []
        for row in rows:
            relative = Path(row["relative"])
            source_file = source / relative
            target_file = targets[row["category"]] / relative
            target_file.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            os.chmod(target_file.parent, 0o700)
            with source_file.open("rb") as reader, target_file.open("xb") as writer:
                shutil.copyfileobj(reader, writer, 1024 * 1024)
                writer.flush()
                os.fsync(writer.fileno())
            os.chmod(target_file, row["mode"] & 0o700)
            source_digest = _digest(source_file)
            target_digest = _digest(target_file)
            if source_digest != target_digest or source_file.stat().st_size != target_file.stat().st_size:
                raise MigrationError(f"copy verification failed: {relative.as_posix()}")
            enriched.append({**row, "sha256": source_digest})
        manifest = {
            "version": 1,
            "transaction": txid,
            "created_at": int(time.time()),
            "source": str(source),
            "targets": {name: str(path) for name, path in targets.items()},
            "source_preserved": True,
            "files": enriched,
        }
        record_dir = targets["artifacts"] / "migration-records" / txid
        record_dir.mkdir(parents=True, mode=0o700)
        manifest_path = record_dir / "manifest.json"
        with manifest_path.open("x", encoding="utf-8") as handle:
            json.dump(manifest, handle, ensure_ascii=False, sort_keys=True, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(manifest_path, 0o600)
        for root in targets.values():
            (root / ".mdd-layout-v2").write_text(txid + "\n", encoding="ascii")
            os.chmod(root / ".mdd-layout-v2", 0o600)
            _fsync_tree(root)
        return manifest
    except Exception:
        for root in reversed(created):
            shutil.rmtree(root, ignore_errors=True)
        raise


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--legacy-root", required=True, type=Path)
    parser.add_argument("--config-root", required=True, type=Path)
    parser.add_argument("--state-root", required=True, type=Path)
    parser.add_argument("--artifact-root", required=True, type=Path)
    parser.add_argument("--execute", action="store_true",
                        help="perform the copy; without this flag only print the inventory")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv or sys.argv[1:])
    targets = {
        "config": args.config_root,
        "state": args.state_root,
        "artifacts": args.artifact_root,
    }
    try:
        if args.execute:
            result = migrate(args.legacy_root, targets)
            print(json.dumps({"ok": True, "transaction": result["transaction"],
                              "files": len(result["files"]), "source_preserved": True}))
        else:
            rows = inventory(args.legacy_root)
            counts = {name: sum(1 for row in rows if row["category"] == name)
                      for name in targets}
            print(json.dumps({"ok": True, "dry_run": True, "files": len(rows),
                              "categories": counts}, sort_keys=True))
        return 0
    except (MigrationError, OSError) as exc:
        print(f"migration refused: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
