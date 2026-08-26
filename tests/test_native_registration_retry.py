import copy
import time
from pathlib import Path
from unittest.mock import AsyncMock, Mock

import pytest

from control.app import main, status


STARTED = "2026-08-26T09:38:41.000000000Z"


@pytest.fixture
def state(monkeypatch):
    monkeypatch.setattr(main.hub, "health", {})
    monkeypatch.setattr(main.hub, "reg_unanswered_recovery_at", {})
    monkeypatch.setattr(main.hub, "engine_recovery_locks", {})
    monkeypatch.setattr(main.hub, "engine_lifecycle_epoch", {})
    monkeypatch.setattr(main.hub, "exit_ledgers", {})
    monkeypatch.setattr(main.cfg, "get_settings", lambda: {})
    return {
        "state": "REGISTERING", "label": "Registering to IMS",
        "reason_code": "reg_unanswered", "reason": "No REGISTER response.",
        "detail": {"active_channels": 0, "registration": "Rejected",
                   "registration_event_key": "a" * 64,
                   "registration_event_at": time.time(), "retry_after_seconds": 30},
    }


@pytest.mark.asyncio
async def test_first_unanswered_event_leaves_native_retry_and_does_not_spend_rebuild_limit(state, monkeypatch):
    capture = Mock()
    runtime = AsyncMock()
    monkeypatch.setattr(main.engine, "capture_and_stop_if_idle", capture)
    monkeypatch.setattr(main.hub.runtime, "get", runtime)
    result = await main._apply_health_with_recovery(
        "1", {"id": "1", "enabled": True}, state, "container-1", sampled_started_at=STARTED)
    assert result["detail"]["recovery_action"] == "native_registration_retry"
    assert result["detail"]["recovery_mode"] == "in_place"
    assert result["detail"]["recovery_retry_in"] >= 157
    assert not main.hub.reg_unanswered_recovery_at
    assert not main.hub.exit_ledgers
    capture.assert_not_called()
    runtime.assert_not_awaited()


def test_polling_same_marker_does_not_slide_deadline(state, monkeypatch):
    clock = [1000.0]
    monkeypatch.setattr(main.time, "monotonic", lambda: clock[0])
    state["detail"].pop("registration_event_at")
    first = main._native_registration_retry_overlay("1", {"enabled": True}, state, "c", STARTED)
    deadline = main.hub.health_for("1")["native_registration_retry"]["deadline"]
    clock[0] += 50
    second = main._native_registration_retry_overlay("1", {"enabled": True}, state, "c", STARTED)
    assert main.hub.health_for("1")["native_registration_retry"]["deadline"] == deadline
    assert first["detail"]["recovery_retry_in"] - second["detail"]["recovery_retry_in"] == 50
    clock[0] = deadline + .1
    assert main._native_registration_retry_overlay("1", {"enabled": True}, state, "c", STARTED) is None


def test_new_distinct_failure_after_scheduled_retry_releases_guard_early(state):
    main._native_registration_retry_overlay("1", {"enabled": True}, state, "c", STARTED)
    changed = copy.deepcopy(state)
    changed["detail"]["registration_event_key"] = "b" * 64
    changed["detail"]["registration_event_at"] += 2
    assert main._native_registration_retry_overlay("1", {"enabled": True}, changed, "c", STARTED)
    changed["detail"]["registration_event_at"] += 30
    assert main._native_registration_retry_overlay("1", {"enabled": True}, changed, "c", STARTED) is None


@pytest.mark.parametrize("reason", ["registering", "local_registration_unreadable", "reg_rejected"])
def test_unknown_samples_preserve_first_deadline_and_later_failure_identity(state, monkeypatch, reason):
    clock = [1000.0]
    epoch = state["detail"]["registration_event_at"]
    monkeypatch.setattr(main.time, "monotonic", lambda: clock[0])
    monkeypatch.setattr(main.time, "time", lambda: epoch + clock[0] - 1000)
    inst = {"enabled": True}
    main._native_registration_retry_overlay("1", inst, state, "c", STARTED)
    window = dict(main.hub.health_for("1")["native_registration_retry"])
    clock[0] += 31
    unknown = {"state": "REGISTERING", "reason_code": reason, "detail": {"registration": "unknown"}}
    assert main._native_registration_retry_overlay("1", inst, unknown, "c", STARTED) is None
    assert main.hub.health_for("1")["native_registration_retry"] == window
    clock[0] += 19
    second = copy.deepcopy(state)
    second["detail"].update(registration_event_key="b" * 64, registration_event_at=epoch + 50)
    assert main._native_registration_retry_overlay("1", inst, second, "c", STARTED) is None
    assert main.hub.health_for("1")["native_registration_retry"] == window


@pytest.mark.parametrize("reason,code", [("reg_temporary", 503), ("reg_rejected", 403)])
def test_concrete_sip_reply_ends_unanswered_episode(state, reason, code):
    main._native_registration_retry_overlay("1", {"enabled": True}, state, "c", STARTED)
    reply = {"state": "REGISTERING", "reason_code": reason, "detail": {"sip_status": code}}
    assert main._native_registration_retry_overlay("1", {"enabled": True}, reply, "c", STARTED) is None
    assert "native_registration_retry" not in main.hub.health_for("1")


def test_old_current_generation_evidence_does_not_restart_full_wait_after_control_restart(state):
    state["detail"]["registration_event_at"] -= 300
    assert main._native_registration_retry_overlay("1", {"enabled": True}, state, "c", STARTED) is None


def test_same_container_new_incarnation_gets_its_own_window(state):
    main._native_registration_retry_overlay("1", {"enabled": True}, state, "c", STARTED)
    old = main.hub.health_for("1")["native_registration_retry"]
    result = main._native_registration_retry_overlay(
        "1", {"enabled": True}, state, "c", "2026-08-26T09:40:00.000000000Z")
    assert result is not None
    assert main.hub.health_for("1")["native_registration_retry"]["owner"] != old["owner"]


def test_success_or_other_concrete_reply_clears_native_window_but_not_frozen_pin(state):
    main._native_registration_retry_overlay("1", {"enabled": True}, state, "c", STARTED)
    success = {**state, "state": "OK", "reason_code": "ok"}
    assert main._native_registration_retry_overlay("1", {"enabled": True}, success, "c", STARTED) is None
    assert "native_registration_retry" not in main.hub.health_for("1")
    main.hub.health_for("1")["frozen_code"] = "pin_wrong"
    assert main._native_registration_retry_overlay("1", {"enabled": True}, state, "c", STARTED) is None
    assert main.hub.health_for("1")["frozen_code"] == "pin_wrong"


def test_real_no_response_marker_exposes_native_delay_and_event_identity():
    event = status._registration_failure_event(
        "[2026-08-26 16:55:31+0800] No response received from 'sip:ims.invalid' "
        "on registration attempt to 'sip:user@ims.invalid', retrying in '30'")
    assert event["kind"] == "unanswered"
    assert event["retry_after_seconds"] == 30
    assert len(event["event_key"]) == 64 and event["event_at"] > 0


def test_preceding_same_id_incarnation_failure_is_not_saved_as_current(monkeypatch):
    born = time.time() - 10
    runtime = {"container_id": "same-container", "started_at_epoch": born}
    inst = {"id": "1", "iccid": "8944100000000000001"}
    monkeypatch.setattr(status.engine, "read_registration_evidence", lambda *_: {})
    write = Mock()
    monkeypatch.setattr(status.engine, "write_registration_evidence", write)
    event = {"kind": "unanswered", "event_at": born - 30,
             "event_key": "f" * 64, "retry_after_seconds": 30}
    assert status._saved_registration_evidence(inst, runtime, event) == {"kind": "unknown"}
    write.assert_not_called()


def test_retry_result_window_covers_actual_engine_transaction_timer():
    template = Path(__file__).parents[1] / 'engine/templates/pjsip.conf.j2'
    timer = int(next(line.split('=', 1)[1] for line in template.read_text().splitlines()
                     if line.startswith('timer_b='))) / 1000
    assert main.NATIVE_REGISTER_TRANSACTION_SECONDS >= timer
