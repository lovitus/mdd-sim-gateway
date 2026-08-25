import hashlib
import importlib.util
from pathlib import Path

import pytest
from jinja2 import Environment, FileSystemLoader

from control.app import config


ROOT = Path(__file__).resolve().parents[1]
PATCH = (ROOT / "engine/patches/asterisk/chan_websocket" /
         "0001-Backport-Asterisk-media-WebSocket-support-to-sysmoco.patch")


def render_module():
    path = ROOT / "engine/render.py"
    spec = importlib.util.spec_from_file_location("mdd_engine_render", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def engine_config(*, self_signed=True, manager_url="https://host.docker.internal:8443"):
    return {
        "id": "7", "imsi": "001010000000007", "mcc": "001", "mnc": "01",
        "ami_secret": "ami-secret", "manager_url": manager_url,
        "manager_event_token": "A" * 43,
        "manager_tls_self_signed": self_signed,
        "sip": {"webrtc": {"password": "webrtc-secret"}},
    }


def test_manager_media_url_preserves_context_prefix_and_rejects_ambiguous_input():
    module = render_module()
    assert module.manager_media_websocket_url(
        "https://gateway.example:8443/mdd/") == (
            "wss://gateway.example:8443/mdd/api/engine/media/call", True)
    fixed = len(("wss://g/" + "/api/engine/media/call" +
                 "?sid=" + "A" * 32).encode("ascii"))
    exact_path = "p" * (160 - fixed)
    exact, _tls = module.manager_media_websocket_url(f"https://g/{exact_path}")
    assert len((exact + "?sid=" + "A" * 32).encode("ascii")) == 160
    with pytest.raises(ValueError, match="160-byte"):
        module.manager_media_websocket_url(f"https://g/{exact_path}x")
    for invalid in ("", "http://gateway", "ftp://gateway", "https://user@gateway",
                    "https://gateway/?x=1", "https://例.example"):
        with pytest.raises(ValueError):
            module.manager_media_websocket_url(invalid)


def test_websocket_client_uses_run_scoped_basic_secret_not_uri_and_exact_self_signed_pin(
        monkeypatch):
    module = render_module()
    monkeypatch.setenv("MDD_ENGINE_RUN_ID", "run-7")
    context = module.build_context(engine_config())
    template = Environment(loader=FileSystemLoader(
        str(ROOT / "engine/templates"))).get_template("websocket_client.conf.j2")
    rendered = template.render(**context)
    assert "uri = wss://host.docker.internal:8443/api/engine/media/call" in rendered
    assert "username = mdd-engine" in rendered
    from control.app import browser_media
    expected = browser_media.engine_media_token("A" * 43, "7", "run-7")
    assert "password = " + expected in rendered
    assert "A" * 43 not in rendered
    assert context["manager_media_token"] == expected
    assert "ca_list_file = /etc/asterisk/certificate.crt" in rendered
    # Existing self-signed leaf is the exact mounted trust anchor; it is not a broad CA bypass.
    assert "verify_server_cert = yes" in rendered
    assert "verify_server_hostname = no" in rendered

    custom = module.build_context(engine_config(
        self_signed=False, manager_url="https://gateway.example:8443"))
    custom_rendered = template.render(**custom)
    assert "verify_server_hostname = yes" in custom_rendered
    source = (ROOT / "engine/render.py").read_text(encoding="utf-8")
    assert '"/etc/asterisk/websocket_client.conf"' in source
    assert "os.chmod(dest, 0o600)" in source


def test_engine_contract_carries_certificate_mode_without_exposing_media_secret():
    inst = engine_config()
    inst.update({"imei": "123456789012345", "iccid": "8901000000000000007"})
    stored = {
        "id": "7", "imsi": inst["imsi"], "mcc": "001", "mnc": "01",
        "imei": inst["imei"], "iccid": inst["iccid"], "ami_secret": "ami-secret",
        "sip": {"webrtc": {"password": "webrtc-secret"}},
    }
    rendered = config.render_instance_json(stored, {
        **config.DEFAULTS["settings"], "tls": {"self_signed": False}})
    assert rendered["manager_tls_self_signed"] is False
    assert rendered["manager_event_token"]


def test_backport_is_pinned_applied_and_does_not_modify_audiosocket_sources():
    assert hashlib.sha256(PATCH.read_bytes()).hexdigest() == (
        "90f937ab2fdd2e8702d1dec24ddbbe4f3fb51e08ad7e398055810a3c85108f71")
    patch_text = PATCH.read_text(encoding="utf-8", errors="replace")
    assert "channels/chan_websocket.c" in patch_text
    assert "res/res_websocket_client.c" in patch_text
    assert "MDD_WEBSOCKET_URI_MAX 160" in patch_text
    assert "ast_websocket_client_add_uri_params" not in patch_text
    assert "const char *uri_params" in patch_text
    for unused in ("res/ari/resource_channels.c", "res/res_ari_channels.c",
                   "rest-api/api-docs/channels.json", "res/res_stasis.c"):
        assert unused not in patch_text
    for path in ("channels/chan_audiosocket.c", "res/res_audiosocket.c",
                 "apps/app_audiosocket.c", "include/asterisk/audiosocket.h"):
        assert path not in patch_text

    dockerfile = (ROOT / "engine/Dockerfile").read_text(encoding="utf-8")
    python_patches = dockerfile.index("for p in /home/asterisk-build/patches/asterisk/*.py")
    checked = dockerfile.index("git apply --check")
    configured = dockerfile.index("./configure --enable-binary-modules")
    assert python_patches < checked < configured
    assert "--enable chan_websocket" in dockerfile
    assert 'io.mdd-sim-gateway.media-websocket="mdd-media-ws-v1"' in dockerfile


def test_product_runtime_has_no_custom_audiosocket_relay_path():
    runtime = (ROOT / "engine/engine-runtime.sh").read_text(encoding="utf-8")
    install = (ROOT / "install.sh").read_text(encoding="utf-8")
    assert "media_relay.py" not in runtime
    assert "media_relay.py" not in install
    assert not (ROOT / "engine/media_relay.py").exists()
