import asyncio
import threading
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock

import pytest

from control.app import main
from control.app import engine_start_quarantine_contract as contract
from control.app.vpcd_slots import VpcdSlotRegistry


@pytest.fixture
def probe_env(monkeypatch, tmp_path):
    name = "Virtual PCD 00 01"
    registry = VpcdSlotRegistry(str(tmp_path / "slots.json"))
    claim = registry.claim(agent_id="probe-agent", reader_id="reader-one", requested_slot=1,
                           agent_run_id="agent-run")
    inst = {"id": "7", "enabled": True, "iccid": "8933010000000000007",
            "imsi": "208150000000007", "mcc": "208", "mnc": "15", "mnc_len": 2,
            "smsc": "+33", "carrier_identity": {}, "reader_index": 1, "reader_port": ""}
    card = SimpleNamespace(iccid=inst["iccid"], imsi=inst["imsi"], mcc="208", mnc="15",
                           mnc_len=2, smsc="+33", spn=None, carrier_identity={},
                           pin_enabled=False, pin_tries=None)
    state = SimpleNamespace(name=name, registry=registry, claim=claim, inst=inst,
                            card=card, running=None, maintenance=False)
    state.read = Mock(return_value=card)
    state.start = AsyncMock()
    monkeypatch.setattr(main.engine, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(main.engine, "global_maintenance_pending", lambda: state.maintenance)
    monkeypatch.setattr(main, "vpcd_registry", registry)
    monkeypatch.setattr(main.hub, "cards", {})
    monkeypatch.setattr(main.hub, "reader_locks", {})
    monkeypatch.setattr(main.hub, "lpa_busy", {})
    monkeypatch.setattr(main.hub, "probe_quarantine_blockers", set())
    monkeypatch.setattr(main.hub, "probe_quarantine_state_unknown", False)
    monkeypatch.setattr(main, "_reader_quarantine_candidates", lambda *_args: [])
    monkeypatch.setattr(main, "_find_running_by_reader", lambda _name: state.running)
    monkeypatch.setattr(main, "_modem_identity_for_reader", lambda _name: {})
    monkeypatch.setattr(main.usbreader, "port_for_index", lambda _idx: "")
    monkeypatch.setattr(main.sim, "read_card", state.read)
    monkeypatch.setattr(main.cfg, "instances_by_iccid", lambda iccid: [inst] if iccid == inst["iccid"] else [])
    monkeypatch.setattr(main.cfg, "upsert_instance_unique_iccid", Mock(side_effect=AssertionError("no config change")))
    monkeypatch.setattr(main, "_auto_start_hotplugged_line", state.start)
    state.observe = Mock(wraps=registry.observe_card)
    monkeypatch.setattr(registry, "observe_card", state.observe)
    return state


async def blocked_insert(state, root):
    with contract.global_lifecycle_locked(root, exclusive=True, blocking=False):
        await main._on_card_insert(state.name, 1)
    assert state.read.call_count == 0
    assert main.hub.cards[state.name]["probe_blocked_by_quarantines"] == []


@pytest.mark.asyncio
async def test_empty_quarantine_lifecycle_block_retries_after_unlock_without_replug(probe_env, tmp_path):
    await blocked_insert(probe_env, tmp_path)
    entry = main.hub.cards[probe_env.name]
    assert main._consume_probe_resume(entry, state_unknown=False) is True
    await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    await asyncio.sleep(0)
    assert probe_env.read.call_count == 1
    current = main.hub.cards[probe_env.name]
    assert current["identity_current"] is True and current["matched"] == "7"
    stored = probe_env.registry.snapshot()[0]
    assert stored["identity_current"] is True
    assert stored["identity_session_generation"] == probe_env.claim.session_generation
    assert probe_env.observe.call_count == 2
    assert all(call.kwargs["expected_generation"] == probe_env.claim.session_generation
               for call in probe_env.observe.call_args_list)
    probe_env.start.assert_awaited_once_with("7")
    assert main._consume_probe_resume(current, state_unknown=False) is False


def assert_consumed_unknown(state):
    entry = main.hub.cards[state.name]
    assert entry["present"] is True
    assert entry["identity_current"] is False and entry["matched"] is None
    assert entry["probe_resume_armed"] is False and entry["probe_resume_attempted"] is True
    assert main._consume_probe_resume(entry, state_unknown=False) is False
    state.start.assert_not_called()


@pytest.mark.asyncio
async def test_committed_manifest_does_not_consume_until_real_ex_postflight_releases(probe_env, tmp_path):
    with contract.global_lifecycle_locked(tmp_path, exclusive=True, blocking=False):
        await main._on_card_insert(probe_env.name, 1)
        entry = main.hub.cards[probe_env.name]
        for _ in range(3):
            assert main._consume_probe_resume(entry, state_unknown=False) is False
        assert entry["probe_resume_armed"] is True
        assert entry["probe_resume_attempted"] is False
        probe_env.read.assert_not_called()
    assert main._consume_probe_resume(entry, state_unknown=False) is True
    assert entry["probe_resume_armed"] is True  # precheck itself is not an APDU attempt
    await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    await asyncio.sleep(0)
    probe_env.read.assert_called_once_with(1)
    probe_env.start.assert_awaited_once_with("7")


@pytest.mark.asyncio
async def test_remaining_maintenance_marker_defers_read_even_with_free_flock(probe_env, tmp_path):
    await blocked_insert(probe_env, tmp_path)
    probe_env.maintenance = True
    entry = main.hub.cards[probe_env.name]
    assert main._consume_probe_resume(entry, state_unknown=False) is False
    await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    assert main.hub.cards[probe_env.name]["probe_resume_armed"] is True
    probe_env.read.assert_not_called()
    probe_env.maintenance = False
    assert main._consume_probe_resume(main.hub.cards[probe_env.name], state_unknown=False)
    await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    probe_env.read.assert_called_once_with(1)


@pytest.mark.asyncio
async def test_ex_wins_between_precheck_and_probe_without_consuming_attempt(probe_env, tmp_path):
    await blocked_insert(probe_env, tmp_path)
    assert main._consume_probe_resume(main.hub.cards[probe_env.name], state_unknown=False)
    with contract.global_lifecycle_locked(tmp_path, exclusive=True, blocking=False):
        await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    entry = main.hub.cards[probe_env.name]
    assert entry["probe_resume_armed"] is True and entry["probe_resume_attempted"] is False
    probe_env.read.assert_not_called()
    assert main._consume_probe_resume(entry, state_unknown=False)
    await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    probe_env.read.assert_called_once_with(1)


@pytest.mark.asyncio
async def test_apdu_failure_consumes_once_without_presence_churn_or_rearming(probe_env, tmp_path):
    await blocked_insert(probe_env, tmp_path)
    probe_env.read.side_effect = RuntimeError("real APDU failed")
    await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    probe_env.read.assert_called_once_with(1)
    probe_env.observe.assert_not_called()
    assert_consumed_unknown(probe_env)


@pytest.mark.asyncio
@pytest.mark.parametrize("cas_phase", [1, 2])
async def test_lost_observation_generation_never_rearms_or_publishes_old_card(
        probe_env, tmp_path, cas_phase):
    await blocked_insert(probe_env, tmp_path)
    original_observe = probe_env.observe._mock_wraps
    new_claims = []

    def change_generation(*args, **kwargs):
        if probe_env.observe.call_count == cas_phase:
            assert probe_env.registry.release(probe_env.claim)
            new_claims.append(probe_env.registry.claim(
                agent_id="probe-agent", reader_id="reader-one", requested_slot=1,
                agent_run_id="next-agent-run"))
        return original_observe(*args, **kwargs)

    probe_env.observe.side_effect = change_generation
    await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    probe_env.read.assert_called_once_with(1)
    assert probe_env.observe.call_count == cas_phase
    assert new_claims[0].session_generation != probe_env.claim.session_generation
    assert probe_env.registry.snapshot()[0]["identity_current"] is False
    main.cfg.upsert_instance_unique_iccid.assert_not_called()
    assert_consumed_unknown(probe_env)


@pytest.mark.asyncio
async def test_actual_bind_failure_after_read_does_not_retry_apdu(probe_env, tmp_path, monkeypatch):
    await blocked_insert(probe_env, tmp_path)
    monkeypatch.setattr(main.engine._CardProbePermit, "bind_actual",
                        Mock(side_effect=main.engine.EngineLifecycleFenced("actual line lock busy")))
    await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    probe_env.read.assert_called_once_with(1)
    probe_env.observe.assert_not_called()
    assert_consumed_unknown(probe_env)


@pytest.mark.asyncio
async def test_initial_normal_probe_post_read_fence_does_not_create_lifecycle_retry(probe_env, monkeypatch):
    monkeypatch.setattr(main.engine._CardProbePermit, "bind_actual",
                        Mock(side_effect=main.engine.EngineLifecycleFenced("actual line lock busy")))
    await main._on_card_insert(probe_env.name, 1)
    probe_env.read.assert_called_once_with(1)
    probe_env.observe.assert_not_called()
    assert main.hub.cards[probe_env.name]["probe_lifecycle_deferred"] is False
    assert_consumed_unknown(probe_env)


@pytest.mark.asyncio
async def test_running_remote_reader_parks_retry_without_cfg_identity_or_apdu(probe_env, tmp_path):
    await blocked_insert(probe_env, tmp_path)
    probe_env.running = probe_env.inst
    await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    entry = main.hub.cards[probe_env.name]
    assert entry["identity_current"] is False and entry["iccid"] is None
    assert entry["matched"] is None and entry["present"] is True
    assert entry["probe_resume_suppressed_running"] is True
    assert entry["probe_resume_attempted"] is False
    for _ in range(3):
        assert main._consume_probe_resume(entry, state_unknown=False) is False
    assert probe_env.registry.snapshot()[0]["identity_current"] is False
    probe_env.read.assert_not_called()
    probe_env.observe.assert_not_called()
    probe_env.start.assert_not_called()
    assert main._sanitize_card_for_probe_quarantine(
        probe_env.name, {"present": True}, entry, ["7"], False)
    sanitized = main.hub.cards[probe_env.name]
    assert sanitized["probe_resume_attempted"] is False  # parked, never actually read
    assert sanitized["probe_resume_armed"] is False
    assert main._consume_probe_resume(sanitized, state_unknown=False) is False


@pytest.mark.asyncio
@pytest.mark.parametrize("busy", ["lpa", "reader"])
async def test_temporary_reader_owner_keeps_unconsumed_attempt(probe_env, tmp_path, busy):
    await blocked_insert(probe_env, tmp_path)
    lock = main.hub.reader_lock(probe_env.name)
    if busy == "lpa":
        main.hub.lpa_busy[probe_env.name] = True
    else:
        await lock.acquire()
    try:
        await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
        entry = main.hub.cards[probe_env.name]
        assert entry["probe_resume_armed"] is True and entry["probe_resume_attempted"] is False
        assert entry["identity_current"] is False
        probe_env.read.assert_not_called()
    finally:
        main.hub.lpa_busy.clear()
        if lock.locked():
            lock.release()
    assert main._consume_probe_resume(entry, state_unknown=False)
    await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    probe_env.read.assert_called_once_with(1)


@pytest.mark.asyncio
async def test_remote_without_live_session_waits_without_cfg_or_apdu(probe_env, tmp_path):
    await blocked_insert(probe_env, tmp_path)
    assert probe_env.registry.release(probe_env.claim)
    await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    entry = main.hub.cards[probe_env.name]
    assert entry["probe_resume_armed"] is True and entry["probe_resume_attempted"] is False
    assert entry["identity_current"] is False
    probe_env.read.assert_not_called()
    probe_env.observe.assert_not_called()
    probe_env.start.assert_not_called()


@pytest.mark.asyncio
@pytest.mark.parametrize("kind", ["valid", "corrupt", "state_unknown"])
async def test_lifecycle_resume_does_not_bypass_quarantine_or_untrusted_state(probe_env, tmp_path, kind):
    await blocked_insert(probe_env, tmp_path)
    if kind != "state_unknown":
        contract.write_active(tmp_path, {
            "version": 1, "instance": "7",
            "owner": {"type": "deployment", "txid": "deploy-lifecycle-probe-0001"},
            "reason": "Keep the intentionally absent Engine fenced", "created_at": 1787620000,
        })
        if kind == "corrupt":
            contract.active_path(tmp_path, "7").write_text("{bad", encoding="utf-8")
    entry = main.hub.cards[probe_env.name]
    assert main._consume_probe_resume(entry, state_unknown=(kind == "state_unknown")) is False
    assert entry["probe_resume_attempted"] is False
    if kind != "state_unknown":
        await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    probe_env.read.assert_not_called()
    probe_env.start.assert_not_called()


@pytest.mark.asyncio
async def test_quarantine_sanitization_preserves_consumed_lifecycle_attempt(probe_env, tmp_path):
    await blocked_insert(probe_env, tmp_path)
    probe_env.read.side_effect = RuntimeError("one APDU failure")
    await main._on_card_insert(probe_env.name, 1, resumed_from_quarantine=True)
    entry = main.hub.cards[probe_env.name]
    assert main._sanitize_card_for_probe_quarantine(
        probe_env.name, {"present": True}, entry, ["7"], False)
    assert_consumed_unknown(probe_env)
    probe_env.read.assert_called_once_with(1)


@pytest.mark.asyncio
async def test_cancelled_deferred_apdu_keeps_reader_and_ex_fence_until_worker_finishes(probe_env, tmp_path):
    await blocked_insert(probe_env, tmp_path)
    entered = asyncio.Event()
    finish = threading.Event()
    loop = asyncio.get_running_loop()

    def paused_read(_index):
        loop.call_soon_threadsafe(entered.set)
        assert finish.wait(2), "test did not release the APDU worker"
        return probe_env.card

    probe_env.read.side_effect = paused_read
    task = asyncio.create_task(main._on_card_insert(
        probe_env.name, 1, resumed_from_quarantine=True))
    try:
        await asyncio.wait_for(entered.wait(), 1)
        task.cancel()
        await asyncio.sleep(0)
        assert not task.done()
        assert main.hub.reader_lock(probe_env.name).locked()
        with pytest.raises(contract.QuarantineContractError, match="lifecycle lock is busy"):
            with contract.global_lifecycle_locked(tmp_path, exclusive=True, blocking=False):
                pytest.fail("EX entered while the APDU thread was still active")
    finally:
        finish.set()
        with pytest.raises(asyncio.CancelledError):
            await asyncio.wait_for(task, 1)
    assert not main.hub.reader_lock(probe_env.name).locked()
    with contract.global_lifecycle_locked(tmp_path, exclusive=True, blocking=False):
        pass
    probe_env.read.assert_called_once_with(1)
    probe_env.observe.assert_not_called()
    assert_consumed_unknown(probe_env)
