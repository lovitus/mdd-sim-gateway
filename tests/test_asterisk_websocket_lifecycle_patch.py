import hashlib
from pathlib import Path
import subprocess


ROOT = Path(__file__).resolve().parents[1]
PATCH_DIR = ROOT / "engine/patches/asterisk/chan_websocket"
BACKPORT = PATCH_DIR / "0001-Backport-Asterisk-media-WebSocket-support-to-sysmoco.patch"
LIFECYCLE = PATCH_DIR / "0002-Fix-masqueraded-owner-and-EOF-lifecycle.patch"


def test_lifecycle_patch_applies_after_the_pinned_backport_and_preserves_owner_contract(tmp_path):
    section = BACKPORT.read_text().split(
        "diff --git a/channels/chan_websocket.c b/channels/chan_websocket.c", 1)[1]
    section = section.split("\ndiff --git ", 1)[0]
    source = "\n".join(line[1:] for line in section.splitlines()
                       if line.startswith("+") and not line.startswith("+++")) + "\n"
    assert hashlib.sha256(source.encode()).hexdigest() == (
        "bd8d6de82a0272a3ca291bb4d93e14d9d80b914030da4bdfd51a24336ea1ea44")
    channel = tmp_path / "channels/chan_websocket.c"
    channel.parent.mkdir()
    channel.write_text(source)
    for args in (("--check",), ()):
        subprocess.run(["git", "apply", *args, str(LIFECYCLE)], cwd=tmp_path,
                       check=True, capture_output=True, text=True)
    fixed = channel.read_text()
    assert ".fixup = webchan_fixup," in fixed
    fixup = fixed.split("static int webchan_fixup(", 2)[2].split(
        "static int webchan_hangup", 1)[0]
    assert "ast_channel_tech_pvt(newchan)" in fixup
    assert fixup.index("instance->channel != oldchan") < fixup.index("return -1;")
    assert fixup.index("return -1;") < fixup.index(
        "ao2_replace(instance->channel, newchan)")
    # Asterisk's ao2_replace refs the new owner before unrefing the old owner.
    assert fixup.count("ao2_replace(") == 1
    assert "ao2_lock" not in fixup and "ast_channel_lock" not in fixup
    read = fixed.split("static struct ast_frame *webchan_read(", 2)[2].split(
        "static int queue_frame_from_buffer", 1)[0]
    assert "if (read_from_ws_and_queue(instance) < 0) {\n\t\t\treturn NULL;" in read


def test_dockerfile_pins_lifecycle_patch_before_compilation():
    digest = hashlib.sha256(LIFECYCLE.read_bytes()).hexdigest()
    assert digest == "fd024503eaf44d69ced45b2dfd144040fcc06481299760bad06dcfdbb798420b"
    dockerfile = (ROOT / "engine/Dockerfile").read_text()
    assert f'{digest}  $ws_lifecycle_patch' in dockerfile
    assert dockerfile.index('git apply "$ws_patch"') < dockerfile.index(
        'git apply --check "$ws_lifecycle_patch"') < dockerfile.index(
        'git apply "$ws_lifecycle_patch"') < dockerfile.index("./configure --enable-binary-modules")
