import os
import pathlib
import subprocess


ROOT = pathlib.Path(__file__).resolve().parents[1]
RUNTIME_FILES = (
    "pin_keeper.py", "ami_usim.py", "swu_ike.py", "pcscf_state.py",
    "admission_gate.py", "log_capture.py", "render.py", "notify.py",
    "entrypoint.sh", "engine-runtime.sh",
)


def fingerprint(tree: pathlib.Path, kind: str) -> str:
    script = (
        'task_root="$1"; task_tree="$2"; task_kind="$3"; '
        'set -- help; . "$task_root/install.sh" >/dev/null; '
        'REPO_DIR="$task_tree"; engine_fingerprint "$task_kind"'
    )
    result = subprocess.run(
        ["bash", "-c", script, "fingerprint-test", str(ROOT), str(tree), kind],
        check=True, text=True, capture_output=True,
        env={**os.environ, "PCSC_VERSION": "2.3.3"},
    )
    return result.stdout.strip().splitlines()[-1]


def test_engine_fingerprints_ignore_only_python_cache(tmp_path):
    engine = tmp_path / "engine"
    templates = engine / "templates"
    patches = engine / "patches" / "asterisk"
    templates.mkdir(parents=True)
    patches.mkdir(parents=True)
    (engine / "Dockerfile").write_text("FROM scratch\n", encoding="utf-8")
    for index, name in enumerate(RUNTIME_FILES):
        (engine / name).write_text(f"runtime-{index}\n", encoding="utf-8")
    (templates / "extensions.conf.j2").write_text("template\n", encoding="utf-8")
    (patches / "mdd.py").write_text("patch\n", encoding="utf-8")

    clean_runtime = fingerprint(tmp_path, "runtime")
    clean_base = fingerprint(tmp_path, "base")

    template_cache = templates / "__pycache__"
    patch_cache = patches / "__pycache__"
    template_cache.mkdir()
    patch_cache.mkdir()
    (template_cache / "render.cpython-314.pyc").write_bytes(b"ignored-template-cache")
    (patch_cache / "mdd.cpython-314.pyc").write_bytes(b"ignored-patch-cache")
    (templates / "loose.pyc").write_bytes(b"ignored-loose-cache")
    (patches / "loose.pyc").write_bytes(b"ignored-loose-cache")

    assert fingerprint(tmp_path, "runtime") == clean_runtime
    assert fingerprint(tmp_path, "base") == clean_base

    (patches / "real-extra.patch").write_text("real patch input\n", encoding="utf-8")
    assert fingerprint(tmp_path, "base") != clean_base
    (templates / "real-extra.conf.j2").write_text("real template input\n", encoding="utf-8")
    assert fingerprint(tmp_path, "runtime") != clean_runtime
