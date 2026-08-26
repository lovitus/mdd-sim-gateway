from copy import deepcopy
from unittest.mock import patch

import pytest

from control.app import call_media, config, egress, main


@pytest.fixture
def private_config(tmp_path):
    with patch.multiple(config, DATA_DIR=str(tmp_path),
                        CONFIG_PATH=str(tmp_path / "config.yaml"),
                        _load_cache_key=None, _load_cache_value=None):
        yield


def test_audio_buffer_defaults_agree_and_do_not_change_egress_generation(private_config):
    before = main.api_get_settings()
    assert before["cellular_audio_buffer_ms"] == call_media.DEFAULT_PCM_BUFFER_MS == 500
    after = deepcopy(before)
    after["cellular_audio_buffer_ms"] = 1500
    assert egress.desired_document([], before) == egress.desired_document([], after)


@pytest.mark.parametrize("value", [100, 200, 500, 1500, 2000])
def test_audio_buffer_setting_persists_in_existing_settings_api(private_config, value):
    with patch.object(main.egress, "publish"):
        result = main.api_put_settings({"cellular_audio_buffer_ms": value})
    assert result["cellular_audio_buffer_ms"] == value
    config._load_cache_key = None
    config._load_cache_value = None
    assert main.api_get_settings()["cellular_audio_buffer_ms"] == value


@pytest.mark.parametrize("value", [99, 2001, -1, True, False, 500.0, "500", None])
def test_audio_buffer_invalid_value_is_rejected_before_any_save(value):
    with patch.object(main.cfg, "update_settings") as save, \
            patch.object(main.egress, "publish") as publish:
        with pytest.raises(main.HTTPException) as raised:
            main.api_put_settings({"cellular_audio_buffer_ms": value})
    assert raised.value.status_code == 400
    save.assert_not_called()
    publish.assert_not_called()


@pytest.mark.asyncio
async def test_audio_budget_is_snapshotted_for_new_sessions_only(private_config):
    manager = call_media.CallMediaManager()
    options = dict(owner_subject="owner", owner_token="token", instance_iid="5",
                   direction="out", number="123")
    with patch.object(main.egress, "publish"):
        main.api_put_settings({"cellular_audio_buffer_ms": 1500})
        first = await manager.allocate("first", **options,
            pcm_buffer_ms=config.get_settings()["cellular_audio_buffer_ms"])
        main.api_put_settings({"cellular_audio_buffer_ms": 500})
        second = await manager.allocate("second", **options,
            pcm_buffer_ms=config.get_settings()["cellular_audio_buffer_ms"])
    try:
        assert first.media_status()["buffer_limit_ms"] == 1500
        assert second.media_status()["buffer_limit_ms"] == 500
        assert manager.for_iccid("first") is first
    finally:
        await manager.close(first.call_id)
        await manager.close(second.call_id)


@pytest.mark.asyncio
@pytest.mark.parametrize("value", [0, 2001, True, "1500", None])
async def test_direct_invalid_configuration_cannot_allocate_a_session(value):
    manager = call_media.CallMediaManager()
    with pytest.raises(ValueError):
        await manager.allocate("card", owner_subject="owner", owner_token="token",
                               instance_iid="5", direction="out", number="123", pcm_buffer_ms=value)
    assert manager.for_iccid("card") is None
