import asyncio
import hashlib
import json
import os
from pathlib import Path
import tempfile
from unittest.mock import patch

from control.app import engine_config_service as service
from engine import config_fetch


def test_unix_config_service_returns_only_the_digest_bound_snapshot():
    secret = "s" * 48
    payload = {"id": "7", "imsi": "001010123456789", "pin": "1234"}
    digest = service.payload_digest(payload)
    proof = service.config_proof(secret, "7", digest)
    short_root = "/var/tmp" if os.path.isdir("/var/tmp") else None
    with tempfile.TemporaryDirectory(prefix="mddcfg-", dir=short_root) as temporary:
        socket_path = Path(temporary) / "engine-config.sock"

        async def scenario():
            server = service.EngineConfigServer(
                str(socket_path), lambda iid: payload if iid == "7" else {}, lambda: secret)
            await server.start()
            try:
                result = await asyncio.to_thread(
                    config_fetch.fetch_once, str(socket_path), "7", digest, proof)
                assert result == payload
                try:
                    await asyncio.to_thread(
                        config_fetch.fetch_once, str(socket_path), "7", digest, "0" * 64)
                except RuntimeError as exc:
                    assert "rejected" in str(exc)
                else:
                    raise AssertionError("invalid proof was accepted")
            finally:
                await server.close()

        asyncio.run(scenario())
        assert not socket_path.exists()


def test_fetcher_reuses_exact_container_local_snapshot_without_socket(tmp_path):
    payload = {"id": "1", "value": "kept"}
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"),
                         ensure_ascii=False).encode("utf-8")
    digest = hashlib.sha256(encoded).hexdigest()
    target = tmp_path / "config" / "instance.json"
    target.parent.mkdir()
    target.write_text(json.dumps(payload), encoding="utf-8")
    env = {
        "MDD_CONFIG_SOCKET": str(tmp_path / "missing.sock"),
        "MDD_ID": "1", "MDD_CONFIG_DIGEST": digest,
        "MDD_CONFIG_PROOF": "a" * 64, "MDD_INSTANCE": str(target),
    }
    with patch.dict("os.environ", env, clear=False):
        config_fetch.main()
    assert json.loads(target.read_text(encoding="utf-8")) == payload


def test_entrypoints_clean_only_owned_ephemeral_socket():
    root = Path(__file__).resolve().parents[1]
    control = (root / "control" / "docker-entrypoint.sh").read_text(encoding="utf-8")
    engine = (root / "engine" / "engine-runtime.sh").read_text(encoding="utf-8")
    assert "engine-config.sock" in control
    assert "config_fetch.py" in engine
    assert "rm -rf" not in control
    assert "MDD_DATA" not in control
