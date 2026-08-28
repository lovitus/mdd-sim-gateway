from pathlib import Path


def test_source_sync_never_targets_or_deletes_runtime_data():
    source = (Path(__file__).resolve().parents[1] / "tools" /
              "mdd-source-sync.sh").read_text(encoding="utf-8")
    rsync_command = source.split("exec rsync", 1)[1]
    assert "--exclude '/data/'" in rsync_command
    assert "--delete" not in rsync_command
    assert "refusing a runtime data destination" in source


def test_install_normalizes_only_runtime_contract_roots():
    source = (Path(__file__).resolve().parents[1] / "install.sh").read_text(encoding="utf-8")
    body = source.split("ensure_runtime_data_layout() {", 1)[1].split("\n}\n", 1)[0]
    assert 'for item in instances orchestrator certs notifications' in body
    assert '"$MDD_CONFIG_DIR" "$MDD_ARTIFACT_DIR"' in body
    assert 'engine-start-quarantine-locks' in body
    assert 'chown root:root "$MDD_DATA_DIR"' in body
    persist = source.split("persist_mode() {", 1)[1].split("\n}\n", 1)[0]
    assert "ensure_runtime_data_layout" in persist


def test_install_builds_immutable_images_only_from_dockerfiles():
    source = (Path(__file__).resolve().parents[1] / "install.sh").read_text(encoding="utf-8")
    assert "docker " + "cp" not in source
    assert "docker " + "commit" not in source
    assert "engine_overlay_build" not in source
    assert 'docker build $NOCACHE_FLAG' in source
    root = Path(__file__).resolve().parents[1]
    assert not (root / "engine" / "Dockerfile.overlay").exists()
    assert not (root / "control" / "Dockerfile.runtime-overlay").exists()


def test_compose_entrypoint_updates_control_without_touching_engines():
    root = Path(__file__).resolve().parents[1]
    source = (root / "deploy" / "mdd-compose.sh").read_text(encoding="utf-8")
    assert "config, state, artifact and runtime roots must be distinct" in source
    assert "must be outside the source checkout" in source
    assert "compose build control" in source
    assert "compose up --no-deps -d control" in source
    assert "engine" not in source.split("case \"$COMMAND\"", 1)[1].lower()
