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
    assert 'engine-start-quarantine-locks' in body
    assert 'chown root:root "$MDD_DATA_DIR"' in body
    persist = source.split("persist_mode() {", 1)[1].split("\n}\n", 1)[0]
    assert "ensure_runtime_data_layout" in persist
