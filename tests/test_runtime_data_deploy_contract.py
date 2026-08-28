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
    assert 'for item in instances orchestrator notifications audit' in body
    assert 'for item in certs backups' in body
    assert 'for item in agent-releases lpac sources tools migration-records' in body
    assert '"$MDD_CONFIG_DIR" "$MDD_ARTIFACT_DIR"' in body
    assert 'engine-start-quarantine-locks' in body
    assert 'chown root:root "$MDD_STATE_DIR"' in body
    assert "layout roots must be outside the source checkout" in body
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


def test_control_entrypoint_never_mutates_host_pcsc_runtime():
    source = (Path(__file__).resolve().parents[1] / "control" /
              "docker-entrypoint.sh").read_text(encoding="utf-8")
    assert "rm -f /run/pcscd" not in source
    assert "pcscd --foreground" not in source
    assert "-S /run/pcscd/pcscd.comm" in source


def test_compose_entrypoint_updates_control_without_touching_engines():
    root = Path(__file__).resolve().parents[1]
    source = (root / "deploy" / "mdd-compose.sh").read_text(encoding="utf-8")
    assert "config, state, artifact and runtime roots must be distinct" in source
    assert "must be outside the source checkout" in source
    assert "compose build control" in source
    assert "compose up --no-deps -d control" in source
    assert "engine" not in source.split("case \"$COMMAND\"", 1)[1].lower()
    assert "migrate-data-layout.py\" --execute" in source
    assert "migrate-legacy requires root" in source


def test_runtime_contract_has_no_tracked_or_default_checkout_data_root():
    root = Path(__file__).resolve().parents[1]
    assert not (root / "data" / ".gitkeep").exists()
    assert 'MDD_STATE_DIR="/var/lib/mdd-sim-gateway"' in (root / "install.sh").read_text()
    compose = (root / "compose.production.yaml").read_text()
    assert "MDD_STATE_DIR: /var/lib/mdd/state" in compose
    assert "MDD_DATA:" not in compose
    offline = (root / "offline-install.sh").read_text()
    assert '${SCRIPT_DIR}/data' not in offline
    assert ':/data' not in offline
