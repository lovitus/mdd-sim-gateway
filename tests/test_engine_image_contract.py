from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_engine_uses_one_pinned_debian_slim_multistage_base():
    dockerfile = (ROOT / "engine" / "Dockerfile").read_text(encoding="utf-8")
    assert dockerfile.count("FROM ${DEBIAN_BASE}") == 2
    assert "debian:trixie-20260824-slim@sha256:" in dockerfile
    assert "DEBIAN_SNAPSHOT=20260824T000000Z" in dockerfile
    assert "snapshot.debian.org/archive/debian/" in dockerfile
    assert "AS builder" in dockerfile and "AS runtime" in dockerfile
    assert "fedora" not in dockerfile.lower()
    assert "dnf " not in dockerfile


def test_runtime_stage_has_no_build_toolchain_or_source_checkout():
    dockerfile = (ROOT / "engine" / "Dockerfile").read_text(encoding="utf-8")
    runtime = dockerfile.split("FROM ${DEBIAN_BASE} AS runtime", 1)[1]
    for forbidden in ("build-essential", "g++ ", "meson ", "ninja-build",
                      "/home/asterisk-build"):
        assert forbidden not in runtime
    runtime_install = runtime.split("apt-get install -y --no-install-recommends", 1)[1].split(
        "&& rm -rf /var/lib/apt/lists", 1)[0]
    assert "gcc" not in runtime_install.split()
    assert "git" not in runtime_install.split()
    assert "COPY --from=builder /engine-root/ /" not in runtime
    assert "COPY --from=builder /engine-root/usr/ /usr/" in runtime
    assert "COPY --from=builder /engine-root/var/lib/ /var/lib/" in runtime
    assert "COPY --from=builder /opt/mdd-venv" in runtime
    assert "! command -v gcc >/dev/null" in runtime
    assert "! command -v git >/dev/null" in runtime


def test_engine_python_dependencies_and_runtime_library_closure_are_inputs():
    requirements = (ROOT / "engine" / "requirements.txt").read_text(encoding="utf-8")
    for dependency in ("pyscard==", "pycryptodome==", "cryptography==",
                       "requests==", "Jinja2==", "panoramisk=="):
        assert dependency in requirements
    collector = (ROOT / "engine" / "collect-runtime-packages.sh").read_text(
        encoding="utf-8")
    assert "ldd" in collector and "dpkg-query -S" in collector
    assert "$1 !~ /^diversion /" in collector
    install = (ROOT / "install.sh").read_text(encoding="utf-8")
    base = install.split("engine_fingerprint() {", 1)[1].split(
        "engine_image_label()", 1)[0]
    assert "requirements.txt" in base
    assert "collect-runtime-packages.sh" in base
    assert ".dockerignore" in base


def test_engine_build_context_excludes_local_runtime_artifacts():
    ignored = (ROOT / "engine" / ".dockerignore").read_text(encoding="utf-8")
    assert "**/__pycache__/" in ignored
    assert "**/*.py[cod]" in ignored


def test_control_cannot_overlay_engine_runtime_from_source_checkout():
    engine = (ROOT / "control" / "app" / "engine.py").read_text(encoding="utf-8")
    start = engine.split("def _start_container(", 1)[1].split(
        "def start(", 1)[0]
    assert "MDD_DEV_MOUNTS is no longer supported" in start
    for forbidden in ("/opt/mdd-gateway/engine/templates",
                      'volumes[os.path.join(eng,',
                      '"bind": "/entrypoint.sh"',
                      '"bind": "/engine-runtime.sh"'):
        assert forbidden not in start
