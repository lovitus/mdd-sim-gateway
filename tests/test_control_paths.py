import os
from pathlib import Path
import subprocess
import stat
import sys


ROOT = Path(__file__).resolve().parents[1]


def evaluate_paths(environment):
    command = [
        sys.executable,
        "-c",
        ("from control.app import paths; "
         "print(paths.CONFIG_DIR); print(paths.STATE_DIR); "
         "print(paths.ARTIFACT_DIR); print(paths.RUNTIME_DIR)"),
    ]
    value = subprocess.run(command, cwd=ROOT, env=environment, text=True,
                           check=True, capture_output=True)
    return value.stdout.splitlines()


def test_fresh_local_defaults_follow_xdg_and_never_the_checkout(tmp_path):
    environment = {key: value for key, value in os.environ.items()
                   if not key.startswith("MDD_") and not key.startswith("XDG_")}
    environment.update({
        "HOME": str(tmp_path / "home"),
        "XDG_CONFIG_HOME": str(tmp_path / "config"),
        "XDG_STATE_HOME": str(tmp_path / "state"),
        "XDG_DATA_HOME": str(tmp_path / "share"),
        "XDG_RUNTIME_DIR": str(tmp_path / "run"),
    })
    config, state, artifacts, runtime = evaluate_paths(environment)
    assert config == str(tmp_path / "config" / "mdd-sim-gateway")
    assert state == str(tmp_path / "state" / "mdd-sim-gateway")
    assert artifacts == str(tmp_path / "share" / "mdd-sim-gateway" / "artifacts")
    assert runtime == str(tmp_path / "run" / "mdd-sim-gateway")
    assert all(str(ROOT) not in value for value in (config, state, artifacts, runtime))


def test_legacy_data_environment_remains_one_explicit_migration_compatibility(tmp_path):
    environment = {**os.environ, "MDD_DATA": str(tmp_path / "legacy")}
    for name in ("MDD_CONFIG_DIR", "MDD_STATE_DIR", "MDD_ARTIFACT_DIR"):
        environment.pop(name, None)
    config, state, artifacts, _runtime = evaluate_paths(environment)
    assert {config, state, artifacts} == {str(tmp_path / "legacy")}


def test_relative_mdd_root_is_rejected(tmp_path):
    environment = {**os.environ, "MDD_STATE_DIR": "relative/state"}
    result = subprocess.run(
        [sys.executable, "-c", "from control.app import paths"], cwd=ROOT,
        env=environment, text=True, capture_output=True)
    assert result.returncode != 0
    assert "MDD_STATE_DIR must be an absolute path" in result.stderr


def test_private_directory_contract_repairs_existing_permissions(tmp_path):
    from control.app import paths

    target = tmp_path / "state"
    target.mkdir(mode=0o755)
    paths.ensure_private_dir(str(target))
    assert stat.S_IMODE(target.stat().st_mode) == 0o700
