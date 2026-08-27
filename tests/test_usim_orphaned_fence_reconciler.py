import asyncio

import pytest

from control.app import main


@pytest.fixture(autouse=True)
def reset_notified_state():
    main._usim_orphaned_fence_reconcile_notified.clear()
    yield
    main._usim_orphaned_fence_reconcile_notified.clear()


async def _wait_for(predicate, *, attempts=50):
    """The dispatch itself runs in a fire-and-forget asyncio.create_task(to_thread(...)),
    matching the existing host_health_poller convention -- give it a chance to run."""
    for _ in range(attempts):
        if predicate():
            return
        await asyncio.sleep(0.01)


@pytest.mark.asyncio
async def test_reconcile_does_nothing_when_no_fence_is_pending(monkeypatch):
    monkeypatch.setattr(main.engine, "usim_recovery_fence_pending", lambda _iid: False)
    called = []
    monkeypatch.setattr(main.engine, "reconcile_orphaned_usim_recovery_fence",
                        lambda *a, **k: called.append((a, k)))
    await main._reconcile_usim_orphaned_fence({"id": "7", "name": "Free FR"})
    assert called == []


@pytest.mark.asyncio
async def test_reconcile_notifies_host_alert_on_successful_archive(monkeypatch):
    monkeypatch.setattr(main.engine, "usim_recovery_fence_pending", lambda _iid: True)
    monkeypatch.setattr(
        main.engine, "reconcile_orphaned_usim_recovery_fence",
        lambda iid, *, txid: {
            "status": "archived", "terminal": True, "artifacts": {"a": "b"},
            "stale_engine_run_id": "run-old-1234567890",
            "current_engine_run_id": "run-new-1234567890",
        })
    dispatched = []
    monkeypatch.setattr(main.notify_push, "dispatch",
                        lambda *a, **k: dispatched.append((a, k)))
    monkeypatch.setattr(main.cfg, "get_settings", lambda: {"webhook": {}})
    await main._reconcile_usim_orphaned_fence({"id": "7", "name": "Free FR"})
    await _wait_for(lambda: dispatched)
    assert len(dispatched) == 1
    args, _kwargs = dispatched[0]
    _settings, event, _instance, _source, text = args
    assert event == main.notify_push.EV_HOST_ALERT
    assert "Free FR" in text and "run-old" in text and "run-new" in text
    assert "7" in main._usim_orphaned_fence_reconcile_notified


@pytest.mark.asyncio
async def test_reconcile_notifies_host_alert_when_transport_is_not_ready(monkeypatch):
    monkeypatch.setattr(main.engine, "usim_recovery_fence_pending", lambda _iid: True)
    monkeypatch.setattr(
        main.engine, "reconcile_orphaned_usim_recovery_fence",
        lambda iid, *, txid: {"status": "unhealthy", "reason": "transport_not_ready"})
    dispatched = []
    monkeypatch.setattr(main.notify_push, "dispatch",
                        lambda *a, **k: dispatched.append((a, k)))
    monkeypatch.setattr(main.cfg, "get_settings", lambda: {"webhook": {}})
    await main._reconcile_usim_orphaned_fence({"id": "7", "name": "Free FR"})
    await _wait_for(lambda: dispatched)
    assert len(dispatched) == 1
    _args, _kwargs = dispatched[0]
    assert "transport_not_ready" in _args[4]


@pytest.mark.asyncio
async def test_reconcile_does_not_notify_for_untouched_statuses(monkeypatch):
    monkeypatch.setattr(main.engine, "usim_recovery_fence_pending", lambda _iid: True)
    dispatched = []
    monkeypatch.setattr(main.notify_push, "dispatch",
                        lambda *a, **k: dispatched.append((a, k)))
    for status in ("no_fence", "same_generation", "campaign_owns_fence",
                  "current_generation_unknown", "fence_unreadable", "fence_changed"):
        monkeypatch.setattr(main.engine, "reconcile_orphaned_usim_recovery_fence",
                            lambda iid, *, txid, _status=status: {"status": _status})
        await main._reconcile_usim_orphaned_fence({"id": "7", "name": "Free FR"})
    assert dispatched == []


@pytest.mark.asyncio
async def test_reconcile_is_rate_limited_per_line(monkeypatch):
    monkeypatch.setattr(main.engine, "usim_recovery_fence_pending", lambda _iid: True)
    monkeypatch.setattr(
        main.engine, "reconcile_orphaned_usim_recovery_fence",
        lambda iid, *, txid: {
            "status": "archived", "terminal": True, "artifacts": {},
            "stale_engine_run_id": "run-old", "current_engine_run_id": "run-new",
        })
    dispatched = []
    monkeypatch.setattr(main.notify_push, "dispatch",
                        lambda *a, **k: dispatched.append((a, k)))
    monkeypatch.setattr(main.cfg, "get_settings", lambda: {"webhook": {}})
    await main._reconcile_usim_orphaned_fence({"id": "7", "name": "Free FR"})
    await _wait_for(lambda: dispatched)
    await main._reconcile_usim_orphaned_fence({"id": "7", "name": "Free FR"})
    await asyncio.sleep(0.05)
    assert len(dispatched) == 1  # second call within HOST_ALERT_REPEAT_SECONDS is suppressed
