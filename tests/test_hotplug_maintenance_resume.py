import asyncio
import threading
import types
from unittest.mock import AsyncMock, Mock, patch

import pytest

from control.app import main


@pytest.fixture
def hotplug(monkeypatch, tmp_path):
    state = types.SimpleNamespace(
        maintenance=True, promotion=False, started=False, failure=None,
        inst={"id": "7", "iccid": "deferred-sim", "enabled": True,
              "provisioning_state": "ready", "reader_index": 1},
        card={"name": "deferred-reader", "index": 1, "matched": "7", "present": True,
              "iccid": "deferred-sim", "remote": True, "connection_online": True,
              "identity_current": True, "session_generation": "current-session",
              "identity_session_generation": "current-session"},
        desired={"defaults": {"vowifi_enabled": True}}, calls=[])
    monkeypatch.setattr(main.hub, "hotplug_deferred", {}, raising=False)
    monkeypatch.setattr(main.hub, "hotplug_starts", set())
    monkeypatch.setattr(main.hub, "engine_recovery_locks", {})
    monkeypatch.setattr(main.hub, "engine_lifecycle_epoch", {})
    monkeypatch.setattr(main.hub, "cards_list", lambda: [state.card])
    monkeypatch.setattr(main.hub, "reset_health", Mock())
    monkeypatch.setattr(main.hub, "broadcast", AsyncMock())
    monkeypatch.setattr(main.cfg, "get_instance", lambda iid: state.inst)
    monkeypatch.setattr(main.cfg, "get_settings", lambda: {})
    monkeypatch.setattr(main.device_state, "desired", lambda: state.desired)
    monkeypatch.setattr(main, "_device_for_card", lambda *_args: ("reader", "reader"))
    monkeypatch.setattr(main.control_lifecycle, "shutdown_started", lambda: False)
    monkeypatch.setattr(main.engine, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(main.engine, "is_running", lambda iid: state.started)
    monkeypatch.setattr(main.engine, "global_maintenance_pending", lambda: state.maintenance)
    monkeypatch.setattr(main.engine, "engine_maintenance_pending", lambda iid: False)
    monkeypatch.setattr(main.engine, "engine_default_promotion_pending", lambda: state.promotion)

    def checked(inst, settings, dev_mounts=False, reason="manual", **kwargs):
        state.calls.append((dict(inst), kwargs))
        if state.maintenance:
            raise main.HTTPException(409, {"code": "maintenance_in_progress"})
        if state.failure:
            raise state.failure
        if kwargs.get("permit") is not None:
            main.engine._require_start_permit(kwargs["permit"], "7")
        state.started = True
        return "new-engine"

    monkeypatch.setattr(main, "_start_engine_checked", checked)
    return state


async def remember_initial_failure():
    with patch.object(main.asyncio, "sleep", AsyncMock()):
        await main._auto_start_hotplugged_line("7")


@pytest.mark.asyncio
async def test_only_maintenance_rejection_remembers_settled_hotplug_intent(hotplug):
    await remember_initial_failure()
    assert "7" in main.hub.hotplug_deferred
    assert main.hub.hotplug_deferred["7"]["iccid"] == "deferred-sim"
    assert main.hub.hotplug_deferred["7"]["lifecycle_epoch"] == main.hub.lifecycle_epoch("7")
    assert "7" not in main.hub.hotplug_starts
    assert len(hotplug.calls) == 1


async def resume_tick():
    tasks = main._schedule_deferred_hotplug_starts()
    assert main._schedule_deferred_hotplug_starts() == []
    if tasks:
        await asyncio.wait_for(asyncio.gather(*tasks), 1)


@pytest.mark.asyncio
async def test_committed_manifest_with_ex_postflight_lock_cannot_start_unscoped_line(hotplug):
    await remember_initial_failure()
    assert main._schedule_deferred_hotplug_starts() == []
    hotplug.maintenance = False  # durable manifest says committed, but postflight still owns EX
    contract = main.engine.start_quarantine_contract
    with contract.global_lifecycle_locked(main.engine.DATA_DIR, exclusive=True, blocking=False):
        await resume_tick()
        await resume_tick()
        assert len(hotplug.calls) == 1 and "7" in main.hub.hotplug_deferred
    hotplug.promotion = True
    assert main._schedule_deferred_hotplug_starts() == []
    hotplug.promotion = False
    await resume_tick()
    assert len(hotplug.calls) == 2 and hotplug.started
    assert hotplug.calls[-1][1]["replace_existing"] is False
    assert not hotplug.calls[-1][1]["permit"].active
    assert not main.hub.hotplug_deferred
    await resume_tick()
    assert len(hotplug.calls) == 2


@pytest.mark.asyncio
@pytest.mark.parametrize("change", ["disabled", "deleted", "iccid", "epoch", "removed",
                                   "offline", "identity", "generation", "desired"])
async def test_deferred_start_revalidates_and_consumes_obsolete_intent(hotplug, change):
    await remember_initial_failure()
    hotplug.maintenance = False
    if change == "disabled":
        hotplug.inst["enabled"] = False
    elif change == "deleted":
        hotplug.inst = None
    elif change == "iccid":
        hotplug.inst["iccid"] = "different-sim"
    elif change == "epoch":
        main.hub.bump_lifecycle_epoch("7")
    elif change == "removed":
        hotplug.card["present"] = False
    elif change == "offline":
        hotplug.card["connection_online"] = False
    elif change == "identity":
        hotplug.card["identity_current"] = False
    elif change == "generation":
        hotplug.card["session_generation"] = "new-unconfirmed-session"
    elif change == "desired":
        hotplug.desired["defaults"]["vowifi_enabled"] = False
    await resume_tick()
    assert len(hotplug.calls) == 1
    assert not main.hub.hotplug_deferred


@pytest.mark.asyncio
@pytest.mark.parametrize("failure", [RuntimeError("startup failed"),
                                     main.engine.EngineAlreadyExists("another owner won")])
async def test_one_real_retry_or_existing_generation_consumes_intent_without_restart_loop(hotplug, failure):
    await remember_initial_failure()
    hotplug.maintenance = False
    hotplug.failure = failure
    await resume_tick()
    assert len(hotplug.calls) == 2 and not main.hub.hotplug_deferred
    hotplug.failure = None
    await resume_tick()
    assert len(hotplug.calls) == 2


@pytest.mark.asyncio
async def test_initial_nonmaintenance_failure_has_no_deferred_retry(hotplug):
    hotplug.maintenance = False
    hotplug.failure = main.HTTPException(409, {"code": "pin_required"})
    await remember_initial_failure()
    assert len(hotplug.calls) == 1 and not main.hub.hotplug_deferred
    hotplug.failure = None
    await resume_tick()
    assert len(hotplug.calls) == 1


@pytest.mark.asyncio
@pytest.mark.parametrize("bad", ["identity", "desired"])
async def test_resume_never_borrows_identity_or_desired_from_another_matched_slot(hotplug, monkeypatch, bad):
    await remember_initial_failure()
    hotplug.maintenance = False
    stale = {**hotplug.card, "iccid": "other-sim", "device_id": "stale-device"}
    exact = {**hotplug.card, "device_id": "exact-device"}
    if bad == "identity":
        exact.update(identity_current=False, identity_session_generation="old-session")
    else:
        hotplug.desired = {"devices": {
            "stale-device": {"vowifi_enabled": True}, "exact-device": {"vowifi_enabled": False}}}
    monkeypatch.setattr(main.hub, "cards_list", lambda: [stale, exact])
    monkeypatch.setattr(main, "_device_for_card", lambda card, cards: (card["device_id"], "reader"))
    await resume_tick()
    assert len(hotplug.calls) == 1 and not main.hub.hotplug_deferred


@pytest.mark.asyncio
async def test_reconfirmed_current_generation_can_resume_same_logical_sim(hotplug):
    await remember_initial_failure()
    hotplug.maintenance = False
    hotplug.card.update(session_generation="reconnected", identity_session_generation="reconnected")
    await resume_tick()
    assert hotplug.started and len(hotplug.calls) == 2


@pytest.mark.asyncio
async def test_cancelled_resume_keeps_permit_and_line_lock_until_executor_finishes(hotplug, monkeypatch):
    await remember_initial_failure()
    hotplug.maintenance = False
    entered, proceed = threading.Event(), threading.Event()
    checked = main._start_engine_checked

    def paused(*args, **kwargs):
        entered.set()
        assert proceed.wait(2)
        return checked(*args, **kwargs)

    monkeypatch.setattr(main, "_start_engine_checked", paused)
    task = main._schedule_deferred_hotplug_starts()[0]
    contract = main.engine.start_quarantine_contract
    try:
        assert await asyncio.to_thread(entered.wait, 1)
        task.cancel()
        await asyncio.sleep(0.02)
        assert not task.done()
        assert main.hub.recovery_lock("7").locked()
        with pytest.raises(contract.QuarantineContractError):
            with contract.global_lifecycle_locked(main.engine.DATA_DIR, exclusive=True, blocking=False):
                raise AssertionError("replacement acquired EX while startup worker still owned SH")
    finally:
        proceed.set()
    with pytest.raises(asyncio.CancelledError):
        await task
    with contract.global_lifecycle_locked(main.engine.DATA_DIR, exclusive=True, blocking=False):
        pass
    assert not main.hub.recovery_lock("7").locked()
    assert hotplug.started and len(hotplug.calls) == 2
    assert not main.hub.hotplug_deferred
    assert not main._recovery_workers
