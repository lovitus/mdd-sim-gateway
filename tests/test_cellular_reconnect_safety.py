import asyncio
import time
import types
from unittest.mock import AsyncMock, Mock, patch

import pytest
import pytest_asyncio

from control.app import main
from control.app.modem_registry import (
    Attachment, MODEM_HEARTBEAT_TIMEOUT, ModemRegistry, ModemUnavailable,
)


LEASE = {
    "call_id": "disconnected-call", "instance": "6", "iccid": "test-sim",
    "direction": "out", "state": "active",
}
IDLE = {
    "status": "idle", "fresh": True, "authoritative": True, "terminal_samples": 2,
}


def reconnected_attachment():
    return Attachment(
        iccid=LEASE["iccid"], agent_id="test-agent", modem_id="test-modem",
        session_id="reconnected-session", websocket=None)


@pytest.mark.asyncio
@pytest.mark.parametrize("initial_status", ["idle", "active"])
async def test_orphaned_paid_lease_recovers_with_real_reconnected_attachment(initial_status):
    attachment = reconnected_attachment()
    assert attachment.is_online()
    # Only public()/list() dictionaries have an "online" field; resolve() returns this object.
    assert not hasattr(attachment, "online")
    responses = [dict(IDLE)] if initial_status == "idle" else [
        {"status": "active", "fresh": True, "authoritative": True, "terminal_samples": 0},
        {"ok": True}, dict(IDLE),
    ]
    rpc = AsyncMock(side_effect=responses)
    with patch.object(main.store, "list_open_cellular_call_leases", return_value=[LEASE]), \
            patch.object(main.store, "save_cellular_call_lease") as save, \
            patch.object(main.call_media.manager, "get", return_value=None), \
            patch.object(main.modem_registry, "resolve", return_value=attachment), \
            patch.object(main.modem_registry, "rpc", rpc), \
            patch.object(main.asyncio, "sleep", AsyncMock(
                side_effect=[None, asyncio.CancelledError])):
        with pytest.raises(asyncio.CancelledError):
            await main.cellular_call_lease_recovery()
    expected_methods = (["call.status"] if initial_status == "idle" else
                        ["call.status", "call.hangup", "call.status"])
    assert [call.args[1] for call in rpc.await_args_list] == expected_methods
    if initial_status == "active":
        rpc.assert_any_await(
            LEASE["iccid"], "call.hangup", {},
            operation_id=f"restart-release:{LEASE['call_id']}", timeout=20)
    save.assert_called_once_with(
        LEASE["call_id"], LEASE["instance"], LEASE["iccid"], LEASE["direction"],
        "terminal_confirmed")


@pytest.mark.asyncio
@pytest.mark.parametrize("attachment_state", ["absent", "heartbeat_expired"])
async def test_unavailable_attachment_keeps_orphaned_paid_lease_quarantined(attachment_state):
    attachment = None
    if attachment_state == "heartbeat_expired":
        attachment = reconnected_attachment()
        attachment.seen_at = time.time() - MODEM_HEARTBEAT_TIMEOUT - 1
        assert not attachment.is_online()
    rpc = AsyncMock()
    with patch.object(main.store, "list_open_cellular_call_leases", return_value=[LEASE]), \
            patch.object(main.store, "save_cellular_call_lease") as save, \
            patch.object(main.call_media.manager, "get", return_value=None), \
            patch.object(main.modem_registry, "resolve", return_value=attachment), \
            patch.object(main.modem_registry, "rpc", rpc), \
            patch.object(main.asyncio, "sleep", AsyncMock(
                side_effect=[None, asyncio.CancelledError])):
        with pytest.raises(asyncio.CancelledError):
            await main.cellular_call_lease_recovery()
    rpc.assert_not_awaited()
    save.assert_not_called()


@pytest_asyncio.fixture
async def continuity(monkeypatch):
    clock = [100.0]
    attachment = reconnected_attachment()
    attachment.session_id = "original-session"
    session = main.call_media.MediaSession(
        call_id="c" * 32, iccid=LEASE["iccid"], token="media-token",
        owner_subject="browser-subject", owner_token="browser-owner",
        instance_iid="6", agent_session_id=attachment.session_id,
        commit_result={"ok": True})
    session.agent_id, session.modem_id = attachment.agent_id, attachment.modem_id
    session.agent_ws, session.browser_ws = object(), object()
    session.bridge_task = asyncio.get_running_loop().create_future()
    env = types.SimpleNamespace(
        clock=clock, session=session, attachment=attachment, current=attachment,
        owner=session, connected=True, media_fresh=True, sleeps=[], renewals=[],
        known={**attachment.public(), "online": False}, after_sleep=None, after_renew=None)

    def progress():
        if not env.media_fresh:
            return
        session.browser_to_agent_frames += 2
        session.agent_to_browser_frames += 2
        session.browser_to_agent_at = session.agent_to_browser_at = clock[0]
        count = session.helper_capture_callbacks + 2
        session.record_helper_telemetry({
            "type": "audio.telemetry",
            "capture_callbacks": count, "playback_callbacks": count,
            "capture_bytes": count * 320, "playback_bytes": count * 320})
        session.issue_challenge()
        session.record_browser_evidence({
            "type": "cellular.media.evidence", "version": 1,
            "challenge": session.challenge, "capture_callbacks": count,
            "playback_callbacks": count, "played_frames": count})

    async def sleep(seconds):
        assert 0 <= seconds <= 2
        env.sleeps.append(seconds)
        clock[0] += seconds
        if env.after_sleep:
            env.after_sleep()
        progress()
        assert clock[0] <= 114, "recovery exceeded its fixed deadline"

    async def renew(iccid, method, params, **kwargs):
        env.renewals.append((clock[0], method, dict(params), kwargs))
        if env.current is None:
            raise ModemUnavailable("SIM is not attached to an online modem")
        if env.after_renew:
            return env.after_renew()
        return {"ok": True, "status": "renewed", "ttl_seconds": 12}

    monkeypatch.setattr(main.call_media.time, "monotonic", lambda: clock[0])
    local_asyncio = types.SimpleNamespace(**vars(asyncio))
    local_asyncio.get_running_loop = lambda: types.SimpleNamespace(time=lambda: clock[0])
    local_asyncio.sleep = sleep
    monkeypatch.setattr(main, "asyncio", local_asyncio)
    monkeypatch.setattr(main.call_media.manager, "get", lambda _call_id: env.owner)
    monkeypatch.setattr(main.call_media.manager, "for_iccid", lambda _iccid: env.owner)
    monkeypatch.setattr(main.modem_registry, "resolve", lambda _iccid: env.current)
    monkeypatch.setattr(main.modem_registry, "list", lambda: [
        env.current.public() if env.current else env.known])
    monkeypatch.setattr(main.modem_registry, "rpc", AsyncMock(side_effect=renew))
    monkeypatch.setattr(main.cfg, "list_instances", lambda: [{"id": "6", "iccid": session.iccid}])
    monkeypatch.setattr(main.cfg, "get_instance", lambda _iid: {"id": "6", "iccid": session.iccid})
    monkeypatch.setattr(main, "_finalize_abandoned_cellular_media", AsyncMock())
    monkeypatch.setattr(main.store, "save_cellular_call_lease", Mock())
    progress()
    return env


@pytest.mark.asyncio
async def test_control_gap_recovers_same_call_on_new_sid_after_four_seconds(continuity):
    env = continuity
    env.current = None
    def reconnect():
        if env.clock[0] >= 104:
            env.current = reconnected_attachment()
        if env.renewals:
            env.owner = None
    env.after_sleep = reconnect
    await main._supervise_paid_call_lease(env.session)
    assert env.session.agent_session_id == "reconnected-session"
    assert env.session.lease_last_healthy_at == 104
    assert len(env.renewals) == 1
    assert env.renewals[0][1:3] == ("call.lease.renew", {"lease_id": env.session.call_id})
    assert env.renewals[0][3]["expected_attachment"] is not env.attachment
    main._finalize_abandoned_cellular_media.assert_not_awaited()


@pytest.mark.asyncio
async def test_control_gap_has_fixed_ten_second_deadline_without_renewals(continuity):
    env = continuity
    env.current = None
    await main._supervise_paid_call_lease(env.session)
    assert env.clock[0] == 110
    assert env.session.lease_last_healthy_at == 100
    assert not env.renewals
    main._finalize_abandoned_cellular_media.assert_awaited_once_with(env.session)


@pytest.mark.asyncio
@pytest.mark.parametrize("change", ["agent", "modem", "closed", "prepared"])
async def test_control_recovery_never_rebinds_invalid_owner(continuity, change):
    env = continuity
    env.current = reconnected_attachment()
    if change == "agent":
        env.current.agent_id = "another-agent"
    elif change == "modem":
        env.current.modem_id = "another-modem"
    elif change == "closed":
        env.session.closed.set()
    else:
        env.session.commit_result = None
    await main._supervise_paid_call_lease(env.session)
    assert not env.renewals
    assert env.session.agent_session_id == "original-session"


@pytest.mark.asyncio
@pytest.mark.parametrize("race", ["release", "replaced", "deadline", "closed"])
async def test_renewal_ack_races_cannot_publish_new_sid_or_health(continuity, race):
    env = continuity
    env.current = reconnected_attachment()
    def race_after_renew():
        if race == "release":
            env.session.release_requested = True
        elif race == "replaced":
            env.current = reconnected_attachment()
            env.current.agent_id = "foreign-agent"
        elif race == "deadline":
            env.clock[0] = 110
        else:
            env.session.closed.set()
        return {"ok": True, "status": "renewed", "ttl_seconds": 12}
    env.after_renew = race_after_renew
    await main._supervise_paid_call_lease(env.session)
    assert env.session.agent_session_id == "original-session"
    assert env.session.lease_last_healthy_at == 100
    assert len(env.renewals) == 1


@pytest.mark.asyncio
@pytest.mark.parametrize("rejection", ["missing", "conflict", "restart_recovery", "terminating"])
async def test_agent_explicit_lease_rejection_is_not_a_reconnect_retry(continuity, rejection):
    env = continuity
    env.current = reconnected_attachment()
    env.after_renew = lambda: {"ok": False, "status": rejection}
    await main._supervise_paid_call_lease(env.session)
    assert env.clock[0] == 100
    assert len(env.renewals) == 1
    assert env.session.agent_session_id == "original-session"
    main._finalize_abandoned_cellular_media.assert_awaited_once_with(env.session)


@pytest.mark.asyncio
async def test_foreign_same_iccid_cannot_receive_hangup_or_supply_terminal_idle(continuity):
    env = continuity
    env.current = reconnected_attachment()
    env.current.agent_id = "foreign-agent"
    rpc = AsyncMock(return_value={"ok": True, **IDLE})
    with patch.object(main.modem_registry, "rpc", rpc), \
            patch.object(main.call_media.manager, "close", AsyncMock()):
        terminal, _ = await main._attempt_cellular_termination(env.session)
        assert terminal is False
        assert not await main._close_confirmed_terminal_cellular_media(env.session)
        await main._close_cellular_media(env.session)
    rpc.assert_not_awaited()
    main.store.save_cellular_call_lease.assert_not_called()


@pytest.mark.asyncio
@pytest.mark.parametrize("replace_when", ["before_send", "after_reply"])
async def test_registry_rpc_exact_attachment_is_checked_before_send_and_after_reply(replace_when):
    with patch.object(ModemRegistry, "_load"):
        registry = ModemRegistry()
    old = reconnected_attachment()
    replacement = reconnected_attachment()
    replacement.session_id = "other-session"
    sent = []
    async def send_json(message):
        sent.append(message)
        old.pending[message["id"]].set_result({"ok": True, "status": "renewed"})
        registry._by_iccid[old.iccid] = replacement
    old.websocket = types.SimpleNamespace(send_json=send_json)
    registry._by_iccid[old.iccid] = old if replace_when == "after_reply" else replacement
    with patch.object(registry, "_persist") as persist:
        with pytest.raises(ModemUnavailable):
            await registry.rpc(old.iccid, "call.lease.renew", {"lease_id": "same-call"},
                               expected_attachment=old)
    assert len(sent) == (1 if replace_when == "after_reply" else 0)
    assert not old.pending
    persist.assert_not_called()


@pytest.mark.asyncio
async def test_real_registry_late_old_ack_retries_new_attachment_without_extending_grace(
        continuity, monkeypatch):
    env = continuity
    with patch.object(ModemRegistry, "_load"):
        registry = ModemRegistry()
    old, new = env.attachment, reconnected_attachment()
    registry._by_iccid[old.iccid] = old
    registry._known[old.iccid] = old.public()
    messages = []

    async def send_old(message):
        messages.append(message)
        old.pending[message["id"]].set_result({"ok": True, "status": "renewed"})
        registry._by_iccid[old.iccid] = new

    async def send_new(message):
        assert env.session.lease_last_healthy_at == 100
        assert env.session.agent_session_id == "original-session"
        messages.append(message)
        new.pending[message["id"]].set_result({"ok": True, "status": "renewed"})

    old.websocket = types.SimpleNamespace(send_json=send_old)
    new.websocket = types.SimpleNamespace(send_json=send_new)
    monkeypatch.setattr(registry, "_persist", Mock())
    monkeypatch.setattr(main, "modem_registry", registry)
    env.after_sleep = lambda: setattr(env, "owner", None) if len(messages) == 2 else None
    await main._supervise_paid_call_lease(env.session)
    assert len(messages) == 2
    assert messages[0]["operation_id"] != messages[1]["operation_id"]
    assert all(message["method"] == "call.lease.renew" and
               message["params"] == {"lease_id": env.session.call_id} for message in messages)
    assert env.session.agent_session_id == new.session_id
    assert env.session.lease_last_healthy_at == 100.5
    main._finalize_abandoned_cellular_media.assert_not_awaited()


@pytest.mark.asyncio
@pytest.mark.parametrize("endpoint", ["http_status", "poller"])
async def test_foreign_attachment_status_cannot_update_old_call_history(continuity, monkeypatch, endpoint):
    env = continuity
    async def replaced_status(*_args, **_kwargs):
        env.current = reconnected_attachment()
        env.current.agent_id = "foreign-agent"
        return {"ok": True, **IDLE}
    monkeypatch.setattr(main.modem_registry, "rpc", AsyncMock(side_effect=replaced_status))
    monkeypatch.setattr(main, "_sync_cellular_call_record", Mock())
    monkeypatch.setattr(main.store, "update_call", Mock())
    monkeypatch.setattr(main.store, "get_open_call", Mock())
    if endpoint == "http_status":
        result = await main.api_cellular_call_status("6")
        assert result["unavailable"] and result["status"] == "unknown"
    else:
        env.current.capabilities["call_signalling"] = True
        monkeypatch.setattr(main, "_match_instance_by_iccid", lambda _iccid: {"id": "6"})
        main.asyncio.sleep = AsyncMock(side_effect=asyncio.CancelledError)
        with pytest.raises(asyncio.CancelledError):
            await main.remote_call_poller()
    main._sync_cellular_call_record.assert_not_called()
    main.store.update_call.assert_not_called()
    main.store.get_open_call.assert_not_called()


@pytest.mark.asyncio
async def test_cleanup_accepts_same_physical_new_sid_without_renewing_or_rebinding(continuity, monkeypatch):
    env = continuity
    env.current = reconnected_attachment()
    env.session.release_requested = True
    env.session.lease_last_healthy_at = 100
    candidate = env.current
    rpc = AsyncMock(side_effect=[{"ok": True}, {"ok": True, **IDLE}])
    monkeypatch.setattr(main.modem_registry, "rpc", rpc)
    terminal, result = await main._attempt_cellular_termination(env.session)
    assert terminal and result["terminal_confirmed"]
    assert [call.args[1] for call in rpc.await_args_list] == ["call.hangup", "call.status"]
    assert all(call.kwargs["expected_attachment"] is candidate for call in rpc.await_args_list)
    assert env.session.agent_session_id == "original-session"
    assert env.session.lease_last_healthy_at == 100


@pytest.mark.asyncio
@pytest.mark.parametrize("foreign", [False, True])
async def test_closed_ram_owner_recovery_retains_its_physical_identity(continuity, monkeypatch, foreign):
    env = continuity
    env.session.closed.set()
    env.session.release_state = "hangup_failed"
    env.current = reconnected_attachment()
    if foreign:
        env.current.agent_id = "foreign-agent"
    lease = {**LEASE, "call_id": env.session.call_id}
    rpc = AsyncMock(side_effect=[
        {"status": "active", "fresh": True, "authoritative": True},
        {"ok": True}, dict(IDLE),
    ])
    monkeypatch.setattr(main.store, "list_open_cellular_call_leases", lambda: [lease])
    monkeypatch.setattr(main.modem_registry, "rpc", rpc)
    main.asyncio.sleep = AsyncMock(side_effect=[None, asyncio.CancelledError])
    with pytest.raises(asyncio.CancelledError):
        await main.cellular_call_lease_recovery()
    if foreign:
        rpc.assert_not_awaited()
        main.store.save_cellular_call_lease.assert_not_called()
    else:
        assert [call.args[1] for call in rpc.await_args_list] == [
            "call.status", "call.hangup", "call.status"]
        assert all(call.kwargs["expected_attachment"] is env.current for call in rpc.await_args_list)
        main.store.save_cellular_call_lease.assert_called_once_with(
            lease["call_id"], lease["instance"], lease["iccid"], lease["direction"],
            "terminal_confirmed")
