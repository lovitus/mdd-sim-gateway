import importlib.util
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "mdd_data_migration", ROOT / "deploy" / "migrate-data-layout.py")
MIGRATION = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MIGRATION)


def roots(tmp_path):
    return {name: tmp_path / name for name in ("config", "state", "artifacts")}


def test_legacy_files_are_split_verified_and_source_is_preserved(tmp_path):
    source = tmp_path / "legacy"
    (source / "certs").mkdir(parents=True)
    (source / "backups").mkdir(parents=True)
    (source / "instances" / "1" / "run").mkdir(parents=True)
    (source / "lpac").mkdir(parents=True)
    (source / "config.yaml").write_text("instances: {}\n", encoding="utf-8")
    (source / "certs" / "self-signed.key").write_bytes(b"private")
    (source / "backups" / "auth-reset.json").write_bytes(b"credential backup")
    (source / "instances" / "1" / "run" / "engine-run-id").write_text("r1\n")
    (source / "lpac" / "lpac").write_bytes(b"binary")
    targets = roots(tmp_path)

    manifest = MIGRATION.migrate(source, targets)

    assert (targets["config"] / "config.yaml").read_text() == "instances: {}\n"
    assert (targets["config"] / "certs" / "self-signed.key").read_bytes() == b"private"
    assert (targets["config"] / "backups" / "auth-reset.json").read_bytes() == b"credential backup"
    assert (targets["state"] / "instances" / "1" / "run" / "engine-run-id").exists()
    assert (targets["artifacts"] / "lpac" / "lpac").read_bytes() == b"binary"
    assert (source / "config.yaml").exists()
    assert manifest["source_preserved"] is True
    assert len(manifest["files"]) == 5
    record = targets["artifacts"] / "migration-records" / manifest["transaction"] / "manifest.json"
    assert record.is_file()


def test_migration_refuses_existing_target_without_touching_source(tmp_path):
    source = tmp_path / "legacy"
    source.mkdir()
    (source / "config.yaml").write_text("settings: {}\n")
    targets = roots(tmp_path)
    targets["state"].mkdir()

    with pytest.raises(MIGRATION.MigrationError, match="already exists"):
        MIGRATION.migrate(source, targets)

    assert (source / "config.yaml").read_text() == "settings: {}\n"
    assert not targets["config"].exists()
    assert not targets["artifacts"].exists()


def test_migration_refuses_symlinks_and_special_entries(tmp_path):
    source = tmp_path / "legacy"
    source.mkdir()
    (source / "config.yaml").symlink_to(tmp_path / "outside")

    with pytest.raises(MIGRATION.MigrationError, match="symlinks"):
        MIGRATION.inventory(source)
