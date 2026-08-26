import asyncio
import threading
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock

import pytest

from control.app import main
from control.app import engine_start_quarantine_contract as contract
from test_card_probe_lifecycle_resume import probe_env, blocked_insert


@pytest.fixture
def owned(probe_env, monkeypatch):
    state = probe_env
    state.running = state.inst
    state.card.reader, state.card.present, state.card.error = state.name, True, None
    state.runtime = {"running": True, "container_id": "owner-generation",
                     "started_at": "2026-08-26T08:00:00Z", "engine_run_id": "engine-run",
                     "container_status": "running"}
    state.authority = {"session_id": "health-session", "pcsc": {
        "generation": 1, "readers": [{"reader_id": "reader-one", "card_present": True}]}}
    state.channels = {"ok": True, "count": 0}
    state.cellular_leases, state.browser_leases = [], []
    state.browser_reserved = False
    monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(side_effect=lambda *_a, **_k: dict(state.runtime)))
    monkeypatch.setattr(main.agent_health_registry, "reader_authority", AsyncMock(
        side_effect=lambda *_a: state.authority))
    state.ami = SimpleNamespace(complete_channel_snapshot=AsyncMock(side_effect=lambda: dict(state.channels)))
    monkeypatch.setattr(main.hub, "ami_for", AsyncMock(return_value=state.ami))
    monkeypatch.setattr(main.hub, "drop_ami", AsyncMock())
    monkeypatch.setattr(main.hub, "broadcast", AsyncMock())
    monkeypatch.setattr(main.cfg, "get_instance", lambda _iid: state.inst)
    monkeypatch.setattr(main.cfg, "list_instances", lambda **_k: [state.inst])
    monkeypatch.setattr(main.store, "list_open_cellular_call_leases", lambda: state.cellular_leases)
    monkeypatch.setattr(main.store, "list_nonterminal_browser_calls", lambda: state.browser_leases)
    monkeypatch.setattr(main.call_media.manager, "sessions", lambda: [])
    monkeypatch.setattr(main.browser_media.registry, "line_reserved", lambda _iid: state.browser_reserved)
    monkeypatch.setattr(main, "_durable_maintenance_pending", lambda _iid: state.maintenance)
    monkeypatch.setattr(main.engine, "stop", Mock(side_effect=AssertionError("identity probe must not stop Engine")))
    return state


def entry(state):
    return main.hub.cards[state.name]


def assert_unknown_once(state, error):
    value = entry(state)
    assert value["identity_current"] is False and value["matched"] is None
    assert value["probe_resume_attempted"] is True
    assert value["probe_error"] == error
    assert main._consume_probe_resume(value, state_unknown=False) is False
    assert state.registry.snapshot()[0]["identity_current"] is False
    state.start.assert_not_called()
    main.cfg.upsert_instance_unique_iccid.assert_not_called()
    main.engine.stop.assert_not_called()


@pytest.mark.asyncio
@pytest.mark.parametrize("resumed", [False, True])
async def test_idle_running_owner_recovers_from_real_card_without_cfg_side_effects(owned, tmp_path, resumed):
    if resumed:
        await blocked_insert(owned, tmp_path)
    await main._on_card_insert(owned.name, 1, resumed_from_quarantine=resumed)
    owned.read.assert_called_once_with(1, strict_transaction=True, expected_reader=owned.name)
    assert entry(owned)["matched"] == "7" and entry(owned)["identity_current"] is True
    assert entry(owned)["probe_deferred"] is False
    record = owned.registry.snapshot()[0]
    assert record["identity_current"] is True
    assert record["identity_session_generation"] == record["session_generation"] == owned.claim.session_generation
    owned.start.assert_not_called()
    main.cfg.upsert_instance_unique_iccid.assert_not_called()
    main.engine.stop.assert_not_called()


@pytest.mark.asyncio
async def test_existing_same_generation_actual_identity_is_retained_without_reprobe(owned):
    owned.observe._mock_wraps(owned.name, {**owned.inst, "matched": "7"},
                             expected_generation=owned.claim.session_generation)
    await main._on_card_insert(owned.name, 1)
    owned.read.assert_not_called()
    assert entry(owned)["identity_current"] is True
    assert entry(owned)["iccid"] == owned.inst["iccid"]


@pytest.mark.asyncio
async def test_normal_running_insert_cannot_borrow_cfg_when_health_is_unknown(owned):
    owned.authority = None
    await main._on_card_insert(owned.name, 1)
    assert entry(owned)["identity_current"] is False and entry(owned)["matched"] is None
    assert entry(owned)["probe_resume_attempted"] is False
    assert entry(owned)["probe_owner_container_id"] == "owner-generation"
    owned.read.assert_not_called()
    owned.start.assert_not_called()


@pytest.mark.asyncio
@pytest.mark.parametrize("field,value,error", [
    ("reader", "Virtual PCD 00 02", "reader_identity_changed"),
    ("iccid", "8944000000000000999", "identity_mismatch"),
    ("imsi", "234100000000999", "identity_mismatch"),
    ("error", "transaction did not release", "identity_probe_failed"),
    ("present", False, "identity_probe_failed"),
])
async def test_unverified_strict_result_never_publishes_current_or_retries(owned, field, value, error):
    setattr(owned.card, field, value)
    await main._on_card_insert(owned.name, 1)
    assert_unknown_once(owned, error)
    entry(owned)["probe_resume_check_at"] = 0
    await main._rearm_parked_reader_probe(entry(owned))
    assert main._consume_probe_resume(entry(owned), state_unknown=False) is False
    owned.read.assert_called_once()


@pytest.mark.asyncio
@pytest.mark.parametrize("change", ["health_session", "pcsc_generation", "transport", "container", "started_at", "config"])
async def test_exact_identity_generations_are_rechecked_after_strict_worker(owned, change):
    def read(*_args, **_kwargs):
        if change == "health_session":
            owned.authority["session_id"] = "replacement-health-session"
        elif change == "pcsc_generation":
            owned.authority["pcsc"]["generation"] += 1
        elif change == "transport":
            owned.registry.release(owned.claim)
            owned.registry.claim(agent_id="probe-agent", reader_id="reader-one", requested_slot=1,
                                 agent_run_id="agent-run")
        elif change == "container":
            owned.runtime["container_id"] = "replacement-owner"
        elif change == "started_at":
            owned.runtime["started_at"] = "2026-08-26T08:01:00Z"
        else:
            owned.inst = {**owned.inst, "iccid": "8944000000000000999"}
        return owned.card
    owned.read.side_effect = read
    await main._on_card_insert(owned.name, 1)
    assert_unknown_once(owned, "identity_mismatch" if change == "config" else "identity_generation_changed")


@pytest.mark.asyncio
@pytest.mark.parametrize("change", ["another_reader", "health_session"])
async def test_new_health_authority_rearms_one_read_after_inflight_generation_change(owned, change):
    def read(*_args, **_kwargs):
        if owned.read.call_count == 1:
            if change == "another_reader":
                owned.authority["pcsc"]["readers"].append({
                    "reader_id": "other-reader", "card_present": True})
                owned.authority["pcsc"]["generation"] += 1
            else:
                owned.authority["session_id"] = "new-health-session"
        return owned.card
    owned.read.side_effect = read
    await main._on_card_insert(owned.name, 1)
    assert_unknown_once(owned, "identity_generation_changed")
    assert entry(owned)["probe_consumed_health_key"] == ["health-session", 1, "reader-one"]
    entry(owned)["probe_resume_check_at"] = 0
    await main._rearm_parked_reader_probe(entry(owned))
    assert entry(owned)["identity_current"] is False
    assert main._consume_probe_resume(entry(owned), state_unknown=False)
    await main._on_card_insert(owned.name, 1, resumed_from_quarantine=True)
    assert entry(owned)["identity_current"] is True
    assert owned.read.call_count == 2
    for _ in range(3):
        entry(owned)["probe_resume_check_at"] = 0
        await main._rearm_parked_reader_probe(entry(owned))
        assert not main._consume_probe_resume(entry(owned), state_unknown=False)
    assert owned.read.call_count == 2


@pytest.mark.asyncio
@pytest.mark.parametrize("failure", ["apdu_error", "identity_mismatch"])
async def test_same_health_authority_does_not_retry_a_consumed_read(owned, failure):
    if failure == "apdu_error":
        owned.card.error = "native read failed"
    else:
        owned.card.iccid = "8944000000000000999"
    await main._on_card_insert(owned.name, 1)
    for _ in range(3):
        entry(owned)["probe_resume_check_at"] = 0
        await main._rearm_parked_reader_probe(entry(owned))
        assert not main._consume_probe_resume(entry(owned), state_unknown=False)
        assert entry(owned)["identity_current"] is False
        assert entry(owned)["probe_resume_attempted"] is True
    owned.read.assert_called_once()


@pytest.mark.asyncio
async def test_failure_after_new_authority_consumes_that_new_key_only_once(owned):
    owned.card.error = "native read failed"
    await main._on_card_insert(owned.name, 1)
    owned.authority["pcsc"]["generation"] += 1
    entry(owned)["probe_resume_check_at"] = 0
    await main._rearm_parked_reader_probe(entry(owned))
    assert main._consume_probe_resume(entry(owned), state_unknown=False)
    await main._on_card_insert(owned.name, 1, resumed_from_quarantine=True)
    assert_unknown_once(owned, "identity_probe_failed")
    assert entry(owned)["probe_consumed_health_key"] == ["health-session", 2, "reader-one"]
    for _ in range(3):
        entry(owned)["probe_resume_check_at"] = 0
        await main._rearm_parked_reader_probe(entry(owned))
        assert not main._consume_probe_resume(entry(owned), state_unknown=False)
    assert owned.read.call_count == 2


@pytest.mark.asyncio
@pytest.mark.parametrize("untrusted", ["stale_health", "zero_generation", "target_absent"])
async def test_invalid_new_health_does_not_rearm_a_consumed_read(owned, untrusted):
    owned.card.error = "native read failed"
    await main._on_card_insert(owned.name, 1)
    if untrusted == "stale_health":
        owned.authority = None
    elif untrusted == "zero_generation":
        owned.authority["pcsc"]["generation"] = 0
    else:
        owned.authority["pcsc"]["generation"] += 1
        owned.authority["pcsc"]["readers"][0]["card_present"] = False
    entry(owned)["probe_resume_check_at"] = 0
    await main._rearm_parked_reader_probe(entry(owned))
    assert not main._consume_probe_resume(entry(owned), state_unknown=False)
    owned.read.assert_called_once()


@pytest.mark.asyncio
@pytest.mark.parametrize("busy", ["active_channels", "unknown_channels", "browser_owner", "browser_durable", "cellular_durable", "lpa", "maintenance"])
async def test_existing_idle_admission_gates_defer_without_spending_probe(owned, busy):
    if busy == "active_channels":
        owned.channels["count"] = 1
    elif busy == "unknown_channels":
        owned.channels["ok"] = False
    elif busy == "browser_owner":
        owned.browser_reserved = True
    elif busy == "browser_durable":
        owned.browser_leases.append({"instance": "7"})
    elif busy == "cellular_durable":
        owned.cellular_leases.append({"instance": "7", "iccid": owned.inst["iccid"]})
    elif busy == "lpa":
        main.hub.lpa_busy[owned.name] = True
    else:
        owned.maintenance = True
    await main._on_card_insert(owned.name, 1)
    owned.read.assert_not_called()
    assert entry(owned)["identity_current"] is False
    assert entry(owned)["probe_resume_attempted"] is False
    calls = main.hub.runtime.get.await_count
    for _ in range(5):
        await main._rearm_parked_reader_probe(entry(owned))
        assert main._consume_probe_resume(entry(owned), state_unknown=False) is False
    assert main.hub.runtime.get.await_count == calls


@pytest.mark.asyncio
async def test_busy_then_idle_gets_one_deferred_real_probe(owned):
    owned.channels["count"] = 1
    await main._on_card_insert(owned.name, 1)
    owned.channels["count"] = 0
    entry(owned)["probe_resume_check_at"] = 0
    await main._rearm_parked_reader_probe(entry(owned))
    assert main._consume_probe_resume(entry(owned), state_unknown=False)
    await main._on_card_insert(owned.name, 1, resumed_from_quarantine=True)
    assert entry(owned)["identity_current"] is True
    owned.read.assert_called_once()


@pytest.mark.asyncio
@pytest.mark.parametrize("source", ["event", "fallback"])
async def test_owner_departure_rearms_once_even_after_a_failed_real_read(owned, source):
    owned.card.error = "read failed"
    await main._on_card_insert(owned.name, 1)
    assert_unknown_once(owned, "identity_probe_failed")
    owned.running = None
    owned.runtime.update(running=False, container_status="missing")
    if source == "event":
        await main.hub.runtime_changed("7", owned.runtime, "die")
    else:
        entry(owned)["probe_resume_check_at"] = 0
        await main._rearm_parked_reader_probe(entry(owned))
    assert main._consume_probe_resume(entry(owned), state_unknown=False)
    owned.card.error = None
    await main._on_card_insert(owned.name, 1, resumed_from_quarantine=True)
    await asyncio.sleep(0)
    assert entry(owned)["identity_current"] is True
    await main.hub.runtime_changed("7", owned.runtime, "destroy")
    assert not main._consume_probe_resume(entry(owned), state_unknown=False)
    assert owned.read.call_count == 2


@pytest.mark.asyncio
async def test_wrong_generation_stop_event_does_not_rearm_failed_reader(owned):
    owned.card.error = "read failed"
    await main._on_card_insert(owned.name, 1)
    await main.hub.runtime_changed("7", {"running": False, "container_id": "older-unrelated"}, "die")
    assert_unknown_once(owned, "identity_probe_failed")


@pytest.mark.asyncio
async def test_same_container_restart_is_a_new_owner_opportunity(owned):
    owned.card.error = "read failed"
    await main._on_card_insert(owned.name, 1)
    owned.runtime["started_at"] = "2026-08-26T08:02:00Z"
    entry(owned)["probe_resume_check_at"] = 0
    await main._rearm_parked_reader_probe(entry(owned))
    assert main._consume_probe_resume(entry(owned), state_unknown=False)
    owned.card.error = None
    await main._on_card_insert(owned.name, 1, resumed_from_quarantine=True)
    assert entry(owned)["identity_current"] is True
    assert owned.read.call_count == 2


@pytest.mark.asyncio
async def test_line_ex_fence_blocks_running_probe_without_spending_read(owned, tmp_path):
    with contract.locked_lines(tmp_path, ["7"], exclusive=True, blocking=False):
        await main._on_card_insert(owned.name, 1)
    owned.read.assert_not_called()
    assert entry(owned)["identity_current"] is False
    assert entry(owned)["probe_resume_attempted"] is False


@pytest.mark.asyncio
async def test_health_change_during_idle_gate_prevents_any_apdu(owned):
    async def changed_snapshot():
        owned.authority["pcsc"]["generation"] += 1
        return {"ok": True, "count": 0}
    owned.ami.complete_channel_snapshot.side_effect = changed_snapshot
    await main._on_card_insert(owned.name, 1)
    owned.read.assert_not_called()
    assert entry(owned)["identity_current"] is False
    assert entry(owned)["probe_resume_attempted"] is False


@pytest.mark.asyncio
async def test_actual_bind_lost_after_read_is_consumed_and_not_left_running(owned, monkeypatch):
    monkeypatch.setattr(main.engine._CardProbePermit, "bind_actual",
                        Mock(side_effect=main.engine.EngineLifecycleFenced("actual line changed")))
    await main._on_card_insert(owned.name, 1)
    assert_unknown_once(owned, "identity_generation_changed")
    owned.read.assert_called_once()


@pytest.mark.asyncio
@pytest.mark.parametrize("transition", ["physical_insert", "transport_reconnect"])
async def test_new_card_event_is_not_swallowed_by_previous_mismatch(owned, transition):
    original = owned.card.iccid
    owned.card.iccid = "8944000000000000999"
    await main._on_card_insert(owned.name, 1)
    assert_unknown_once(owned, "identity_mismatch")
    owned.card.iccid = original
    if transition == "transport_reconnect":
        owned.registry.release(owned.claim)
        owned.registry.claim(agent_id="probe-agent", reader_id="reader-one", requested_slot=1,
                             agent_run_id="agent-run")
        entry(owned)["probe_resume_check_at"] = 0
        await main._rearm_parked_reader_probe(entry(owned))
        assert main._consume_probe_resume(entry(owned), state_unknown=False)
    await main._on_card_insert(owned.name, 1, resumed_from_quarantine=transition == "transport_reconnect")
    assert entry(owned)["identity_current"] is True
    assert owned.read.call_count == 2


@pytest.mark.asyncio
async def test_cancelled_strict_read_keeps_all_locks_until_real_worker_finishes(owned, tmp_path):
    entered, finish = asyncio.Event(), threading.Event()
    loop = asyncio.get_running_loop()
    def slow_read(*_args, **_kwargs):
        loop.call_soon_threadsafe(entered.set)
        assert finish.wait(2), "test must finish its worker"
        return owned.card
    owned.read.side_effect = slow_read
    task = asyncio.create_task(main._on_card_insert(owned.name, 1))
    try:
        await asyncio.wait_for(entered.wait(), 1)
        task.cancel()
        await asyncio.sleep(0)
        assert not task.done()
        assert main.hub.reader_lock(owned.name).locked()
        assert main.hub.recovery_lock("7").locked()
        with pytest.raises(contract.QuarantineContractError):
            with contract.global_lifecycle_locked(tmp_path, exclusive=True, blocking=False):
                pytest.fail("EX cannot enter while worker still owns the card")
    finally:
        finish.set()
        with pytest.raises(asyncio.CancelledError):
            await asyncio.wait_for(task, 1)
    assert not main.hub.reader_lock(owned.name).locked()
    assert not main.hub.recovery_lock("7").locked()
    assert_unknown_once(owned, "identity_probe_cancelled")


def test_enrichment_cannot_promote_hub_true_across_transport_generation(probe_env):
    probe_env.registry.observe_card(probe_env.name, {**probe_env.inst, "matched": "7"},
                                   expected_generation=probe_env.claim.session_generation)
    probe_env.registry.release(probe_env.claim)
    probe_env.registry.claim(agent_id="probe-agent", reader_id="reader-one", requested_slot=1)
    row = probe_env.registry.enrich_cards([{"name": probe_env.name, "present": True,
        "iccid": probe_env.inst["iccid"], "matched": "7", "identity_current": True}])[0]
    assert row["connection_online"] is True and row["identity_current"] is False
    assert row["iccid"] is None and row["matched"] is None
    assert row["last_known_iccid"] == probe_env.inst["iccid"]
    assert row["last_known_matched"] == "7"


def test_same_generation_registry_fields_replace_stale_hub_identity(probe_env):
    probe_env.registry.observe_card(probe_env.name, {**probe_env.inst, "matched": "7"},
                                   expected_generation=probe_env.claim.session_generation)
    row = probe_env.registry.enrich_cards([{"name": probe_env.name, "present": True,
        "iccid": "obsolete-hub-card", "matched": "other-line", "identity_current": True,
        "identity_session_generation": "obsolete-generation"}])[0]
    assert row["identity_current"] is True
    assert row["iccid"] == probe_env.inst["iccid"] and row["matched"] == "7"
    assert row["identity_session_generation"] == row["session_generation"]
