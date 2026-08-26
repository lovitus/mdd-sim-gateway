from copy import deepcopy
from unittest.mock import AsyncMock, patch

import pytest

from control.app import browser_media, call_media, config, egress, main


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


@pytest.mark.asyncio
@pytest.mark.parametrize('budget', [500, 1000, 1500, 2000])
async def test_both_prepare_protocols_expose_the_session_audio_budget(private_config, budget):
    registry = browser_media.BrowserMediaRegistry()
    runtime = {'running': True, 'container_id': 'a' * 64, 'engine_run_id': 'run',
               'media_websocket': True}
    with patch.object(main.cfg, 'get_settings', return_value={'cellular_audio_buffer_ms': budget}), \
            patch.object(main.cfg, 'get_instance', return_value={'id': '1'}), \
            patch.object(main, '_browser_media_cookie_subject', return_value='subject'), \
            patch.object(main, '_line_admission_blocked', AsyncMock(return_value=False)), \
            patch.object(main.hub.runtime, 'get', AsyncMock(return_value=runtime)), \
            patch.object(main.browser_media, 'registry', registry), \
            patch.object(main, '_expire_browser_media_session', AsyncMock()):
        result = await main._allocate_browser_media_locked('1', None, purpose='canary')
        session = registry.get(result['session_id'])
        assert result['buffer_limit_ms'] == session.pcm_buffer_ms == budget
        main.cfg.get_settings.return_value['cellular_audio_buffer_ms'] = 200
        assert session.pcm_buffer_ms == budget
        await registry.close_all()
    cellular = call_media.MediaSession(call_id='c', iccid='card', token='token',
        owner_subject='subject', owner_token='owner', pcm_buffer_ms=budget)
    assert main._cellular_prepare_response(cellular)['audio']['buffer_limit_ms'] == budget


@pytest.mark.asyncio
@pytest.mark.parametrize('budget', [99, 2001, True, '1000', 500.0, None])
async def test_native_invalid_audio_budget_cannot_allocate(budget):
    registry = browser_media.BrowserMediaRegistry()
    with pytest.raises(browser_media.BrowserMediaUnavailable):
        await registry.allocate(iid='1', generation='g', engine_run_id='r', subject='s',
                                pcm_buffer_ms=budget)
