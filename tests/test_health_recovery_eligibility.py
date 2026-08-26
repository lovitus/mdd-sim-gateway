import asyncio
import copy
import threading
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock

import pytest

from control.app import main
from control.app import engine_start_quarantine_contract as contract


STARTED = "2026-08-26T06:56:00.000000000Z"


@pytest.fixture
def recovery(monkeypatch, tmp_path):
    inst = {"id": "1", "enabled": True, "iccid": "8933010000000000001",
            "retry": {"max": 3, "interval": 30}}
    card = {"present": True, "matched": "1", "iccid": inst["iccid"], "remote": True,
            "connection_online": True, "identity_current": True,
            "session_generation": "reader-session", "identity_session_generation": "reader-session"}
    env = SimpleNamespace(inst=inst, card=card, desired=True, maintenance=False,
                          promotion=False, after_zero=lambda: None, during_graceful=lambda: None,
                          after_policy_disable=lambda: None)

    class Container:
        id = "generation-1"
        status = "running"

        def __init__(self):
            self.attrs = {
                "Config": {"Labels": {main.engine.MANAGED_LABEL: "true"}},
                "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}},
                "State": {"Pid": 101, "StartedAt": STARTED}, "RestartCount": 0}
            self.updates, self.commands, self.removes, self.stops = [], [], [], []

        def reload(self):
            pass

        def update(self, **kwargs):
            self.updates.append(copy.deepcopy(kwargs))
            self.attrs["HostConfig"]["RestartPolicy"] = copy.deepcopy(kwargs["restart_policy"])
            if kwargs["restart_policy"]["Name"] == "no":
                env.after_policy_disable()

        def exec_run(self, command):
            self.commands.append(command[-1])
            if command[-1] == "core show channels count":
                env.after_zero()
            elif command[-1] == "core stop gracefully":
                env.during_graceful()
                self.status = "exited"
            return 0, b"0 active channels\n0 active calls\n"

        def remove(self, force=False):
            assert self.status == "exited"
            self.removes.append(force)

        def stop(self, timeout=0):
            self.stops.append(timeout)
            self.status = "exited"

    env.container = Container()
    env.runtime = {"running": True, "container_id": env.container.id, "started_at": STARTED}
    env.plan = {"action": main.failover.HOLD, "ledger": {}, "country": "gb",
                "node": "node-a", "candidates": [], "pinned": False,
                "peer_registered": False, "swu": "CONNECTED", "retransmits": 1,
                "verdict": main.failover.BLAMES_ELSEWHERE, "was_backing_off": False}
    env.status = {"state": "REGISTERING", "label": "Registering to IMS",
                  "reason_code": "reg_unanswered", "reason": "Carrier IMS did not answer.",
                  # These tests target the destructive-boundary guards after Asterisk has
                  # already consumed its native retry/result opportunity.
                  "detail": {"registration": "Rejected", "active_channels": 0,
                             "registration_event_key": "a" * 64,
                             "registration_event_at": main.time.time() - 300,
                             "retry_after_seconds": 30}}
    env.diagnostics = Mock()
    env.plan_call = Mock(return_value=env.plan)
    monkeypatch.setattr(main.engine, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(main.engine, "global_maintenance_pending", lambda: env.maintenance)
    monkeypatch.setattr(main.engine, "engine_default_promotion_pending", lambda: env.promotion)
    monkeypatch.setattr(main.control_lifecycle, "shutdown_started", lambda: False)
    monkeypatch.setattr(main.cfg, "get_instance", lambda _iid: copy.deepcopy(env.inst))
    monkeypatch.setattr(main.cfg, "get_settings", lambda: {})
    monkeypatch.setattr(main.hub, "cards_list", lambda: [env.card] if env.card else [])
    monkeypatch.setattr(main, "_device_for_card", lambda *_args: ("reader-one", "reader"))
    monkeypatch.setattr(main.device_state, "desired", lambda: {
        "defaults": {"vowifi_enabled": env.desired}})
    for name in ("health", "ok_since", "engine_recovery_locks", "engine_lifecycle_epoch",
                 "reg_unanswered_recovery_at", "exit_ledgers"):
        monkeypatch.setattr(main.hub, name, {})
    monkeypatch.setattr(main.hub, "engine_recovering", set())
    monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(side_effect=lambda *_args, **_kw: dict(env.runtime)))
    monkeypatch.setattr(main.hub, "drop_ami", AsyncMock())
    monkeypatch.setattr(main, "_plan_exit_failure", env.plan_call)
    monkeypatch.setattr(main, "_save_exit_ledgers", Mock())
    monkeypatch.setattr(main.engine, "capture_diagnostics", env.diagnostics)
    monkeypatch.setattr(main.engine.docker, "from_env", lambda **_kw: SimpleNamespace(
        containers=SimpleNamespace(get=lambda _name: env.container), close=lambda: None))
    return env


@pytest.mark.asyncio
async def test_unknown_identity_does_not_remove_engine_or_commit_exit(recovery):
    recovery.card["identity_current"] = False
    result = await main._apply_health_with_recovery(
        "1", copy.deepcopy(recovery.inst), recovery.status, recovery.container.id,
        sampled_started_at=STARTED)
    assert result["detail"]["recovery_blocked"] == "card_identity_unknown"
    assert recovery.container.updates == []
    assert recovery.container.commands == []
    assert recovery.container.removes == []
    assert main.hub.exit_ledgers == {}
    recovery.plan_call.assert_not_called()


async def apply(env, *, sampled=None, started=STARTED):
    return await main._apply_health_with_recovery(
        "1", copy.deepcopy(env.inst if sampled is None else sampled),
        copy.deepcopy(env.status), env.container.id, sampled_started_at=started)


def assert_no_quiesce(env):
    assert env.container.updates == []
    assert env.container.commands == []
    assert env.container.removes == []
    assert env.container.stops == []
    assert main.hub.exit_ledgers == {}


@pytest.mark.asyncio
@pytest.mark.parametrize("blocked,reason", [
    ("offline", "card_identity_unknown"), ("generation", "card_identity_unknown"),
    ("missing", "no_card"), ("desired", "vowifi_disabled"),
    ("quarantine", "engine_start_quarantined"),
    ("promotion", "engine_default_promotion_pending")])
async def test_initial_ineligible_line_never_diagnoses_or_changes_exit(recovery, tmp_path, blocked, reason):
    if blocked == "offline":
        recovery.card["connection_online"] = False
    elif blocked == "generation":
        recovery.card["identity_session_generation"] = "old-session"
    elif blocked == "missing":
        recovery.card = None
    elif blocked == "desired":
        recovery.desired = False
    elif blocked == "promotion":
        recovery.promotion = True
    else:
        contract.write_active(tmp_path, {
            "version": 1, "instance": "1", "owner": {
                "type": "deployment", "txid": "deploy-health-recovery-0001"},
            "reason": "Keep this Engine protected", "created_at": 1787720000,
        })
    result = await apply(recovery)
    assert result["detail"]["recovery_blocked"] == reason
    assert_no_quiesce(recovery)
    recovery.diagnostics.assert_not_called()
    recovery.plan_call.assert_not_called()


@pytest.mark.asyncio
async def test_disabled_line_never_stops_or_commits_exit(recovery):
    recovery.inst["enabled"] = False
    result = await apply(recovery)
    assert result["state"] == "STOPPED"
    assert_no_quiesce(recovery)


@pytest.mark.asyncio
@pytest.mark.parametrize("new_started", [None, "2026-08-26T07:30:00.000000000Z"])
async def test_missing_or_different_sampled_incarnation_never_stops(recovery, new_started):
    recovery.runtime["started_at"] = new_started
    result = await apply(recovery)
    assert result["detail"]["recovery_blocked"] == (
        "generation_changed" if new_started else "generation_unknown")
    assert_no_quiesce(recovery)


@pytest.mark.asyncio
async def test_sampled_configuration_cannot_be_reused_for_changed_line(recovery):
    sampled = copy.deepcopy(recovery.inst)
    recovery.inst["name"] = "changed while status was sampled"
    result = await apply(recovery, sampled=sampled)
    assert result["detail"]["recovery_blocked"] == "line_configuration_changed"
    assert_no_quiesce(recovery)


def change_eligibility(env, kind):
    if kind == "identity":
        env.card["identity_current"] = False
        return "card_identity_unknown"
    if kind == "desired":
        env.desired = False
        return "vowifi_disabled"
    if kind == "config":
        env.inst["name"] = "changed during diagnostics"
        return "line_configuration_changed"
    if kind == "epoch":
        main.hub.bump_lifecycle_epoch("1")
        return "lifecycle_changed"
    if kind == "maintenance":
        env.maintenance = True
        return "maintenance_in_progress"
    if kind == "promotion":
        env.promotion = True
        return "engine_default_promotion_pending"
    raise AssertionError(kind)


@pytest.mark.asyncio
@pytest.mark.parametrize("kind", ["identity", "desired", "config", "epoch", "maintenance", "promotion"])
async def test_diagnostic_window_rechecks_before_first_restart_policy_change(recovery, kind):
    changed = []
    recovery.diagnostics.side_effect = lambda *_args: changed.append(change_eligibility(recovery, kind))
    result = await apply(recovery)
    assert result["detail"]["recovery_blocked"] == changed[0]
    assert_no_quiesce(recovery)
    recovery.diagnostics.assert_called_once()


@pytest.mark.asyncio
@pytest.mark.parametrize("kind", ["identity", "desired", "config", "epoch", "maintenance"])
async def test_first_zero_sample_rechecks_and_restores_restart_without_graceful(recovery, kind):
    changed = []
    recovery.after_zero = lambda: changed.append(change_eligibility(recovery, kind))
    result = await apply(recovery)
    assert result["detail"]["recovery_blocked"] == changed[0]
    assert [v["restart_policy"]["Name"] for v in recovery.container.updates] == ["no", "unless-stopped"]
    assert recovery.container.commands == ["core show channels count"]
    assert recovery.container.status == "running"
    assert recovery.container.removes == [] and recovery.container.stops == []
    assert main.hub.exit_ledgers == {}


@pytest.mark.asyncio
@pytest.mark.parametrize("terminal", [False, True])
async def test_new_same_id_incarnation_during_diagnostics_is_not_touched(recovery, terminal):
    def restarted(*_args):
        recovery.container.attrs["State"]["StartedAt"] = "2026-08-26T07:30:00.000000000Z"
        recovery.container.attrs["State"]["Pid"] = 202
        recovery.container.status = "exited" if terminal else "running"
    recovery.diagnostics.side_effect = restarted
    result = await apply(recovery)
    assert result["detail"]["recovery_blocked"] == "generation_changed"
    assert_no_quiesce(recovery)


@pytest.mark.asyncio
@pytest.mark.parametrize("window", ["policy", "zero"])
async def test_new_same_id_running_incarnation_restores_only_original_policy(recovery, window):
    def restarted():
        recovery.container.attrs["State"].update(Pid=202, StartedAt="2026-08-26T07:30:00.000000000Z")
        recovery.container.attrs["RestartCount"] = 1
    if window == "policy":
        recovery.after_policy_disable = restarted
    else:
        recovery.after_zero = restarted
    result = await apply(recovery)
    assert result["detail"]["recovery_blocked"] == "generation_changed"
    assert recovery.container.attrs["HostConfig"]["RestartPolicy"]["Name"] == "unless-stopped"
    assert [v["restart_policy"]["Name"] for v in recovery.container.updates] == ["no", "unless-stopped"]
    assert recovery.container.commands == ([] if window == "policy" else ["core show channels count"])
    assert recovery.container.removes == [] and recovery.container.stops == []
    assert main.hub.exit_ledgers == {}


@pytest.mark.asyncio
async def test_new_terminal_incarnation_is_not_removed_or_restarted_by_policy_restore(recovery):
    def restarted_then_exited():
        recovery.container.attrs["State"].update(Pid=0, StartedAt="2026-08-26T07:30:00.000000000Z")
        recovery.container.attrs["RestartCount"] = 1
        recovery.container.status = "exited"
    recovery.after_policy_disable = restarted_then_exited
    result = await apply(recovery)
    assert result["detail"]["recovery_blocked"] == "restart_policy_restore_failed"
    assert [v["restart_policy"]["Name"] for v in recovery.container.updates] == ["no"]
    assert recovery.container.commands == []
    assert recovery.container.removes == [] and recovery.container.stops == []
    assert main.hub.exit_ledgers == {}


@pytest.mark.asyncio
async def test_restore_policy_failure_is_explicit_after_late_guard_denies(recovery, monkeypatch):
    original_update = recovery.container.update
    restore_attempts = []
    def restore_fails(**kwargs):
        if kwargs["restart_policy"]["Name"] == "unless-stopped":
            restore_attempts.append(kwargs)
            raise RuntimeError("Docker rejected restore")
        original_update(**kwargs)
    monkeypatch.setattr(recovery.container, "update", restore_fails)
    recovery.after_zero = lambda: change_eligibility(recovery, "identity")
    result = await apply(recovery)
    assert result["detail"]["recovery_blocked"] == "restart_policy_restore_failed"
    assert len(restore_attempts) == 2
    assert recovery.container.commands == ["core show channels count"]
    assert recovery.container.removes == [] and recovery.container.stops == []


@pytest.mark.asyncio
async def test_host_ex_denies_health_worker_before_diagnostics(recovery, tmp_path):
    with contract.global_lifecycle_locked(tmp_path, exclusive=True, blocking=False):
        result = await apply(recovery)
    assert result["detail"]["recovery_blocked"] == "maintenance_in_progress"
    assert_no_quiesce(recovery)
    recovery.diagnostics.assert_not_called()


@pytest.mark.asyncio
async def test_usim_recovery_fence_keeps_health_worker_in_place(recovery, tmp_path):
    run = tmp_path / "instances" / "1" / "run"
    run.mkdir(parents=True)
    (run / "usim-auth-recovery.fence").write_text("fenced", encoding="utf-8")

    result = await apply(recovery)

    assert result["detail"]["recovery_blocked"] == "usim_recovery_pending"
    assert_no_quiesce(recovery)
    recovery.plan_call.assert_not_called()


@pytest.mark.asyncio
async def test_current_config_or_epoch_changed_during_runtime_await_preserves(recovery, monkeypatch):
    async def changed_runtime(*_args, **_kwargs):
        main.hub.bump_lifecycle_epoch("1")
        return dict(recovery.runtime)
    monkeypatch.setattr(main.hub.runtime, "get", changed_runtime)
    result = await apply(recovery)
    assert result["detail"]["recovery_blocked"] == "lifecycle_changed"
    assert_no_quiesce(recovery)


def test_optional_guard_exception_preserves_engine_with_specific_reason(recovery):
    result = main.engine.capture_and_stop_if_idle(
        "1", recovery.inst, "health-freeze:reg_unanswered", recovery.container.id,
        expected_started_at=STARTED, before_quiesce=Mock(side_effect=RuntimeError("broken guard")))
    assert result == {"status": "recovery_blocked", "stopped": False, "reason": "recovery_guard_failed"}
    assert_no_quiesce(recovery)


@pytest.mark.asyncio
async def test_guard_exception_in_current_identity_is_not_disguised_as_call_unknown(recovery, monkeypatch):
    def fail_identity(*_args):
        monkeypatch.setattr(main.hub, "cards_list", Mock(side_effect=RuntimeError("snapshot broken")))
    recovery.diagnostics.side_effect = fail_identity
    result = await apply(recovery)
    assert result["detail"]["recovery_blocked"] == "recovery_guard_failed"
    assert_no_quiesce(recovery)


@pytest.mark.asyncio
async def test_eligible_no_response_fast_stop_is_once_and_logs_actual_outcome(recovery, caplog):
    caplog.set_level("INFO", logger=main.log.name)
    first = await apply(recovery)
    second = await apply(recovery)
    await asyncio.sleep(0)
    assert first["frozen"] and second["frozen"]
    assert recovery.container.removes == [False]
    assert recovery.container.commands.count("core stop gracefully") == 1
    assert main.hub.health_for("1")["next_retry_at"] is not None
    assert main.hub.reg_unanswered_recovery_at["1"] > 0
    assert "froze after stopping the idle Engine" in caplog.text
    assert "kept the current Engine" not in caplog.text
    main.hub.drop_ami.assert_awaited_once_with("1")


@pytest.mark.asyncio
async def test_503_keeps_existing_engine_and_does_not_enter_eligibility_transaction(recovery):
    recovery.status.update(reason_code="reg_temporary", reason="Carrier retry is scheduled.")
    recovery.status["detail"].update(sip_status=503, retry_after_seconds=300)
    recovery.card["identity_current"] = False
    main.hub.health_for("1")["fail_start"] = main.time.monotonic() - 100_000
    result = await apply(recovery)
    assert result["retry"]["count"] == 0 and not result.get("frozen")
    assert_no_quiesce(recovery)
    recovery.plan_call.assert_not_called()


@pytest.mark.asyncio
async def test_cancelled_actual_stop_commits_receipt_before_releasing_locks(recovery, tmp_path):
    entered, manual_entered = asyncio.Event(), asyncio.Event()
    finish = threading.Event()
    loop = asyncio.get_running_loop()

    def pause_after_last_guard():
        loop.call_soon_threadsafe(entered.set)
        assert finish.wait(3), "test did not release graceful stop"

    recovery.during_graceful = pause_after_last_guard
    task = asyncio.create_task(apply(recovery))

    async def manual_action():
        async with main.hub.recovery_lock("1"):
            assert recovery.container.removes == [False]
            assert main.hub.health_for("1")["frozen_code"] == "reg_unanswered"
            assert main.hub.health_for("1")["next_retry_at"] is not None
            manual_entered.set()

    manual = None
    try:
        await asyncio.wait_for(entered.wait(), 1)
        task.cancel()
        manual = asyncio.create_task(manual_action())
        await asyncio.sleep(0)
        assert not task.done() and not manual_entered.is_set()
        assert "1" in main.hub.engine_recovering
        with pytest.raises(contract.QuarantineContractError, match="lifecycle lock is busy"):
            with contract.global_lifecycle_locked(tmp_path, exclusive=True, blocking=False):
                pytest.fail("Host EX entered while health stop worker was active")
    finally:
        finish.set()
        with pytest.raises(asyncio.CancelledError):
            await asyncio.wait_for(task, 1)
        if manual is not None:
            await asyncio.wait_for(manual, 1)
    assert manual_entered.is_set()
    with contract.global_lifecycle_locked(tmp_path, exclusive=True, blocking=False):
        pass
    assert "1" not in main.hub.engine_recovering
    main.hub.drop_ami.assert_awaited_once_with("1")


@pytest.mark.asyncio
async def test_legacy_absent_recovery_identity_unknown_does_not_blame_disabled_switch(recovery, monkeypatch):
    recovery.card["identity_current"] = False
    monkeypatch.setattr(main.hub, "broadcast", AsyncMock())
    await main._auto_recover_instance("1", recovery.inst, 10)
    status = main.hub.status_cache["1"]
    assert status["reason_code"] == "card_identity_unknown"
    assert "verified" in status["reason"]
    assert "disabled" not in status["reason"]
    assert_no_quiesce(recovery)
