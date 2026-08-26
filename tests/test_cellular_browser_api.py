import asyncio
import json
import sqlite3
import threading
import time
import types
from contextlib import asynccontextmanager, nullcontext
from unittest.mock import AsyncMock, Mock, patch

import pytest
import pytest_asyncio
import httpx

from control.app import main
from control.app.modem_registry import Attachment


OWNER = "a" * 32
OTHER_OWNER = "b" * 32
REQUEST = types.SimpleNamespace(cookies={main.auth.SESSION_COOKIE: "cookie-a"})
OTHER_REQUEST = types.SimpleNamespace(cookies={main.auth.SESSION_COOKIE: "cookie-b"})
REAL_MAINTENANCE_BOUNDARY = main._maintenance_submission_boundary
REAL_STORE_METHODS = {name: getattr(main.store, name) for name in (
    "open_cellular_call_lease", "list_open_cellular_call_leases", "save_cellular_call_lease",
    "mark_cellular_call_terminating", "list_nonterminal_browser_calls", "get_call_by_id",
    "get_open_call_for_transport", "get_open_call", "update_call", "add_call")}


class Socket:
    def __init__(self):
        self.incoming = asyncio.Queue()
        self.messages = []
        self.pcm = []
        self.closed = False
        self.cookies = REQUEST.cookies
        self.headers = {"host": "localhost:12345", "origin": "http://localhost:12345"}

    async def accept(self):
        pass

    async def receive(self):
        return await self.incoming.get()

    async def receive_text(self):
        return (await self.receive())["text"]

    async def send_bytes(self, payload):
        self.pcm.append(payload)

    async def send_json(self, message):
        self.messages.append(message)

    async def close(self, **_kwargs):
        self.closed = True


async def wait_until(predicate):
    async with asyncio.timeout(2):
        while not predicate():
            await asyncio.sleep(0.005)


@pytest_asyncio.fixture
async def native_env(monkeypatch):
    manager = main.call_media.CallMediaManager()
    attachment = Attachment(iccid="test-sim", agent_id="test-agent", modem_id="test-modem",
                            session_id="attachment-one", websocket=None)
    env = types.SimpleNamespace(
        manager=manager, attachment=attachment, agent=Socket(), calls=[], tasks=[], leases={},
        state="ringing-in", record={"id": 10, "instance": "5", "direction": "in",
                                    "transport": "cellular", "status": "ringing"})

    def save(call_id, iid, iccid, direction, state):
        env.leases[call_id] = {"call_id": call_id, "instance": iid, "iccid": iccid,
                               "direction": direction, "state": state}

    def open_lease(iccid):
        return next((lease for lease in env.leases.values()
                     if lease["iccid"] == iccid and
                     lease["state"] not in {"cancelled", "terminal_confirmed"}), None)

    async def rpc(iccid, method, params=None, **kwargs):
        env.calls.append((method, params or {}, kwargs))
        if method == "audio.open":
            session = manager.get(params["call_id"])
            env.agent = Socket()
            env.tasks.append(asyncio.create_task(session.attach_agent(env.agent, params["token"])))
            await session.agent_ready.wait()
            return {"ok": True, "ready": True}
        if method in {"call.dial", "call.answer"}:
            env.state = "active"
            return {"ok": True, "status": "active"}
        if method == "call.hangup":
            env.state = "idle"
            return {"ok": True, "status": "idle", "terminal_confirmed": True}
        if method == "call.status":
            return {"ok": True, "status": env.state, "number": "+44123456789",
                    "fresh": True, "authoritative": True,
                    "terminal_samples": 2 if env.state == "idle" else 0}
        if method == "call.lease.renew":
            return {"ok": True, "status": "renewed", "ttl_seconds": 12}
        return {"ok": True}

    @asynccontextmanager
    async def admitted(_iid):
        yield True

    monkeypatch.setattr(main.call_media, "manager", manager)
    monkeypatch.setattr(main.browser_media, "registry", main.browser_media.BrowserMediaRegistry())
    monkeypatch.setattr(main.hub, "engine_recovery_locks", {})
    monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(return_value={
        "running": False, "container_id": None, "container_status": "missing"}))
    monkeypatch.setattr(main, "_remote_voice_attachment", lambda iid: ("test-sim", attachment))
    monkeypatch.setattr(main.modem_registry, "resolve", lambda iccid: attachment)
    monkeypatch.setattr(main.modem_registry, "list", lambda: [attachment.public()])
    monkeypatch.setattr(main.modem_registry, "rpc", rpc)
    monkeypatch.setattr(main.auth, "session", lambda token: bool(token and token != "invalid"))
    monkeypatch.setattr(main.cfg, "list_instances", lambda: [{"id": "5", "iccid": "test-sim"}])
    monkeypatch.setattr(main.cfg, "get_instance", lambda iid: {"id": "5", "iccid": "test-sim"})
    monkeypatch.setattr(main.store, "open_cellular_call_lease", open_lease)
    monkeypatch.setattr(main.store, "list_open_cellular_call_leases", lambda: [
        lease for lease in env.leases.values()
        if lease["state"] not in {"cancelled", "terminal_confirmed"}])
    monkeypatch.setattr(main.store, "list_nonterminal_browser_calls", lambda: [])
    monkeypatch.setattr(main.store, "save_cellular_call_lease", save)
    monkeypatch.setattr(main.store, "mark_cellular_call_terminating", lambda _call_id: True)
    monkeypatch.setattr(main.store, "get_call_by_id", lambda iid, call_id:
                        dict(env.record) if str(call_id) == str(env.record["id"]) else None)
    monkeypatch.setattr(main.store, "get_open_call_for_transport", lambda iid, transport:
                        dict(env.record) if str(env.record.get("instance")) == str(iid)
                        and env.record.get("direction") == "out"
                        and env.record.get("transport") == transport
                        and not env.record.get("end_ts") else None)
    monkeypatch.setattr(main.store, "get_open_call", lambda iid, direction, within_s=None:
                        dict(env.record) if str(env.record.get("instance")) == str(iid)
                        and env.record.get("direction") == direction
                        and not env.record.get("end_ts") else None)
    monkeypatch.setattr(main.store, "update_call", lambda call_id, status: env.record.update(status=status))
    monkeypatch.setattr(main.store, "add_call", lambda *args, **kwargs: {"id": 11, "status": "ringing"})
    monkeypatch.setattr(main.hub, "broadcast", AsyncMock())
    monkeypatch.setattr(main, "_resolve_cellular_call_alert", AsyncMock())
    monkeypatch.setattr(main, "_maintenance_submission_boundary", admitted)
    yield env
    await main.browser_media.registry.close_all()
    for session in manager.sessions():
        await manager.close(session.call_id)
    await asyncio.gather(*env.tasks, return_exceptions=True)


async def prepare(env, *, owner=OWNER, incoming=False, request=REQUEST):
    if incoming:
        return await main.api_cellular_incoming_prepare(
            "5", {"owner_token": owner, "source_call_id": "10"}, request)
    return await main.api_cellular_call_prepare(
        "5", {"owner_token": owner, "to": "+44123456789"}, request)


async def browser_connect(env, prepared, *, owner=OWNER):
    browser = Socket()
    await browser.incoming.put({"text": json.dumps({
        "type": "cellular.media.hello", "version": 1, "owner_token": owner})})
    task = asyncio.create_task(main.api_cellular_browser_ws(browser, "5", prepared["call_id"]))
    env.tasks.append(task)
    return browser, task


async def prove_media(env, prepared):
    browser, _task = await browser_connect(env, prepared)
    session = env.manager.get(prepared["call_id"])
    await wait_until(lambda: session.bridge_task is not None)
    frame = b"\x01\x00" * 160
    await browser.incoming.put({"bytes": frame})
    await browser.incoming.put({"bytes": frame})
    await env.agent.incoming.put({"bytes": frame * 2})
    await wait_until(lambda: len(browser.pcm) == 2 and len(env.agent.pcm) == 2)
    await env.agent.incoming.put({"text": json.dumps({
        "type": "audio.telemetry", "capture_callbacks": 4, "playback_callbacks": 4,
        "capture_bytes": 1280, "playback_bytes": 1280})})
    await browser.incoming.put({"text": json.dumps({
        "type": "cellular.media.evidence", "version": 1, "challenge": session.challenge,
        "capture_callbacks": 4, "playback_callbacks": 4, "played_frames": 4})})
    await wait_until(lambda: session.media_status()["ready"])
    return browser, session


@pytest.mark.asyncio
async def test_prepare_uses_no_engine_or_ip_route_and_same_owner_retry_is_idempotent(native_env, monkeypatch):
    forbidden = AsyncMock(side_effect=AssertionError("cellular audio must not use an Engine"))
    # The runtime reports an explicitly missing Engine; no AMI/media anchor is required.
    monkeypatch.setattr(main.hub, "ami_for", forbidden)
    monkeypatch.setattr(main, "_softphone_provisioning", forbidden)
    first, second = await asyncio.gather(prepare(native_env), prepare(native_env))
    assert first == second
    assert first["owner_token"] == OWNER
    assert first["audio"]["transport"] == "same-origin-wss-pcm-v1"
    assert first["audio"]["frame_bytes"] == 320
    assert not {"softphone", "media_anchor", "media_target"}.intersection(first)
    assert [item[0] for item in native_env.calls] == ["audio.open"]
    forbidden.assert_not_awaited()


@pytest.mark.asyncio
async def test_other_tab_or_cookie_cannot_prepare_cancel_release_or_commit_owner(native_env):
    prepared = await prepare(native_env)
    session = native_env.manager.get(prepared["call_id"])
    for owner, request in ((OTHER_OWNER, REQUEST), (OWNER, OTHER_REQUEST)):
        with pytest.raises(main.HTTPException) as error:
            await prepare(native_env, owner=owner, request=request)
        assert error.value.status_code == 409
        for endpoint in (main.api_cellular_call_cancel, main.api_cellular_call_release,
                         main.api_cellular_call_commit, main.api_cellular_incoming_answer):
            with pytest.raises(main.HTTPException) as error:
                await endpoint("5", session.call_id, {"owner_token": owner}, request)
            assert error.value.status_code == 409
    assert native_env.manager.get(session.call_id) is session
    assert not session.release_requested and not session.closed.is_set()
    assert [item[0] for item in native_env.calls] == ["audio.open"]


@pytest.mark.asyncio
async def test_failed_incoming_permission_claim_releases_only_self_and_another_tab_can_answer(native_env):
    prepared = await prepare(native_env, incoming=True)
    result = await main.api_cellular_call_release(
        "5", prepared["call_id"], {"owner_token": OWNER}, REQUEST)
    assert result["released"] and not result["physical_hangup"]
    assert not any(item[0] in {"call.answer", "call.hangup"} for item in native_env.calls)
    second = await prepare(native_env, owner=OTHER_OWNER, incoming=True)
    assert second["call_id"] != prepared["call_id"]
    assert native_env.manager.get(second["call_id"]).owns(
        main.browser_media.subject_digest("cookie-a"), OTHER_OWNER)


@pytest.mark.asyncio
@pytest.mark.parametrize("change", ["id", "status", "end_ts", "direction", "transport"])
async def test_incoming_prepare_rejects_stale_or_wrong_exact_call_before_audio_open(native_env, change):
    native_env.record[change] = {
        "id": 20, "status": "answered", "end_ts": 123,
        "direction": "out", "transport": "vowifi"}[change]
    with pytest.raises(main.HTTPException):
        await prepare(native_env, incoming=True)
    assert native_env.calls == []


@pytest.mark.asyncio
@pytest.mark.parametrize("state", ["ringing-in", "waiting"])
async def test_repeated_incoming_polls_keep_the_same_answerable_record(native_env, state):
    assert main._cellular_call_result_status(state) == ("ringing", False)
    native_env.record["status"] = state
    prepared = await prepare(native_env, incoming=True)
    assert prepared["source_call_id"] == "10"


@pytest.mark.asyncio
@pytest.mark.parametrize("incoming", [False, True])
async def test_true_ws_media_allows_exactly_one_paid_action_and_owner_release_confirms_terminal(native_env, incoming):
    prepared = await prepare(native_env, incoming=incoming)
    _browser, session = await prove_media(native_env, prepared)
    endpoint = main.api_cellular_incoming_answer if incoming else main.api_cellular_call_commit
    first, retried = await asyncio.gather(
        endpoint("5", session.call_id, {"owner_token": OWNER}, REQUEST),
        endpoint("5", session.call_id, {"owner_token": OWNER}, REQUEST))
    assert first == retried and first["ok"]
    assert first["record"]["cellular_owner_call_id"] == session.call_id
    paid = [item for item in native_env.calls if item[0] in {"call.answer", "call.dial"}]
    assert len(paid) == 1
    result = await main.api_cellular_call_release(
        "5", session.call_id, {"owner_token": OWNER}, REQUEST)
    assert result["released"] and result["terminal_confirmed"]
    assert native_env.manager.get(session.call_id) is None


@pytest.mark.asyncio
async def test_sqlite_http_incoming_prepare_and_answer_use_the_exact_incoming_record(native_env, monkeypatch, tmp_path):
    store = main.store
    for name, method in REAL_STORE_METHODS.items():
        monkeypatch.setattr(store, name, method)
    monkeypatch.setattr(store, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(store, "DB_PATH", str(tmp_path / "incoming.sqlite"))
    monkeypatch.setattr(store, "PREVIOUS_DB_PATH", str(tmp_path / "previous.sqlite"))
    store.init()
    record = store.add_call("5", "in", "+44123456789", status="ringing", transport="cellular")
    assert store.get_open_call_for_transport("5", "cellular") is None
    assert store.get_open_call("5", "in")["id"] == record["id"]
    monkeypatch.setattr(main.auth, "session", lambda token: {"csrf": "test-csrf"})
    monkeypatch.setattr(main.engine, "global_maintenance_pending", lambda: False)
    monkeypatch.setattr(main.engine, "engine_maintenance_pending", lambda _iid: False)
    async with httpx.AsyncClient(transport=httpx.ASGITransport(app=main.app),
                                base_url="https://gateway.example",
                                cookies={main.auth.SESSION_COOKIE: "cookie-a"},
                                headers={"x-mdd-csrf-token": "test-csrf"}) as client:
        response = await client.post("/api/instances/5/cellular-call/incoming/prepare",
                                     json={"owner_token": OWNER, "source_call_id": str(record["id"])})
        assert response.status_code == 200, response.text
        prepared = response.json()
        _browser, session = await prove_media(native_env, prepared)
        answers = await asyncio.gather(*(client.post(
            f"/api/instances/5/cellular-call/{session.call_id}/answer",
            json={"owner_token": OWNER}) for _ in range(2)))
        assert all(reply.status_code == 200 and reply.json()["ok"] for reply in answers)
        assert answers[0].json() == answers[1].json()
        assert answers[0].json()["record"]["id"] == record["id"]
        assert store.get_call_by_id("5", record["id"])["status"] == "answered"
        assert sum(call[0] == "call.answer" for call in native_env.calls) == 1
        released = await client.post(f"/api/instances/5/cellular-call/{session.call_id}/release",
                                     json={"owner_token": OWNER})
        assert released.status_code == 200 and released.json()["terminal_confirmed"]
    assert not store.list_open_cellular_call_leases()


@pytest.mark.asyncio
@pytest.mark.parametrize("stage", ["retry", "before_coordinator"])
async def test_http_release_keeps_same_terminal_receipt_when_status_poller_wins(native_env, monkeypatch, stage):
    prepared = await prepare(native_env)
    _browser, session = await prove_media(native_env, prepared)
    await main.api_cellular_call_commit("5", session.call_id, {"owner_token": OWNER}, REQUEST)
    original_rpc = main.modem_registry.rpc
    original_sleep = asyncio.sleep
    waiting, resume = asyncio.Event(), asyncio.Event()

    async def unconfirmed_hangup(iccid, method, params=None, **kwargs):
        if method == "call.hangup":
            native_env.calls.append((method, params, kwargs))
            return {"ok": True, "status": "hangup_requested", "terminal_confirmed": False}
        return await original_rpc(iccid, method, params, **kwargs)

    async def gated_retry(delay):
        if delay == 1.5:
            waiting.set()
            await resume.wait()
        else:
            await original_sleep(delay)

    monkeypatch.setattr(main.modem_registry, "rpc", unconfirmed_hangup)
    monkeypatch.setattr(main.asyncio, "sleep", gated_retry)
    monkeypatch.setattr(main.auth, "session", lambda token: {"csrf": "test-csrf"})
    lock_owned = False
    poller = None
    if stage == "before_coordinator":
        native_env.state = "idle"
        await session.commit_lock.acquire()
        lock_owned = True
        poller = asyncio.create_task(main._close_confirmed_terminal_cellular_media(session))
        await original_sleep(0)
    async with httpx.AsyncClient(transport=httpx.ASGITransport(app=main.app),
                                base_url="https://gateway.example",
                                cookies={main.auth.SESSION_COOKIE: "cookie-a"},
                                headers={"x-mdd-csrf-token": "test-csrf"}) as client:
        request = asyncio.create_task(client.post(
            f"/api/instances/5/cellular-call/{session.call_id}/release",
            json={"owner_token": OWNER}))
        try:
            if stage == "retry":
                await asyncio.wait_for(waiting.wait(), 1)
                native_env.state = "idle"
                assert await main._close_confirmed_terminal_cellular_media(session)
            else:
                await wait_until(lambda: session.release_requested)
                session.commit_lock.release()
                lock_owned = False
                assert await asyncio.wait_for(poller, 1)
            observed = {"release_state": session.release_state,
                        "release_result": session.release_result,
                        "manager_present": native_env.manager.get(session.call_id) is not None}
        finally:
            if lock_owned:
                session.commit_lock.release()
            resume.set()
        response = await asyncio.wait_for(request, 1)
    result = response.json()
    print(json.dumps({"poller_won": observed, "http_status": response.status_code,
                      "http_release": result}, sort_keys=True))
    assert response.status_code == 200 and isinstance(result, dict)
    assert result["ok"] and result["released"] and result["terminal_confirmed"]
    assert result["hangup"] == session.release_result
    assert session.release_result["terminal_confirmed"] is True
    assert session.release_result["confirmed_by"] == "call.status"
    assert session.release_result["call_id"] == session.call_id
    assert session.release_result["fresh"] is True
    assert session.release_result["authoritative"] is True
    assert session.release_result["terminal_samples"] >= 2
    assert native_env.leases[session.call_id]["state"] == "terminal_confirmed"
    assert sum(call[0] == "call.hangup" for call in native_env.calls) == (1 if stage == "retry" else 0)


@pytest.mark.asyncio
@pytest.mark.parametrize("endpoint", ["release", "cancel"])
@pytest.mark.parametrize("state", ["signalling", "active", "unknown"])
async def test_http_missing_ram_session_keeps_durable_paid_call_pending(
        monkeypatch, tmp_path, endpoint, state):
    store = main.store
    monkeypatch.setattr(store, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(store, "DB_PATH", str(tmp_path / "gateway.sqlite"))
    monkeypatch.setattr(store, "PREVIOUS_DB_PATH", str(tmp_path / "previous.sqlite"))
    store.init()
    store.save_cellular_call_lease("restart-call", "5", "restart-sim", "out", state)
    monkeypatch.setattr(main.call_media.manager, "get", lambda call_id: None)
    monkeypatch.setattr(main.auth, "session", lambda token: {"csrf": "test-csrf"})
    monkeypatch.setattr(main.engine, "global_maintenance_pending", lambda: False)
    monkeypatch.setattr(main.engine, "engine_maintenance_pending", lambda _iid: False)
    rpc = AsyncMock(side_effect=AssertionError("missing RAM session must not issue commands"))
    monkeypatch.setattr(main.modem_registry, "rpc", rpc)
    async with httpx.AsyncClient(transport=httpx.ASGITransport(app=main.app),
                                base_url="https://gateway.example",
                                cookies={main.auth.SESSION_COOKIE: "cookie-a"},
                                headers={"x-mdd-csrf-token": "test-csrf"}) as client:
        response = await client.post(
            f"/api/instances/5/cellular-call/restart-call/{endpoint}", json={"owner_token": OWNER})
    result = response.json()
    print(json.dumps({"endpoint": endpoint, "durable_state": state, "response": result}))
    assert response.status_code == 200
    assert not result.get("released") and not result.get("missing")
    assert not result.get("cancelled") and result.get("terminal_confirmed") is not True
    assert result["termination_pending"] is True and result["outcome"] == "unknown"
    assert store.open_cellular_call_lease("restart-sim")["state"] == state
    rpc.assert_not_awaited()


@pytest.mark.asyncio
@pytest.mark.parametrize("invalid", ["missing", "other_call", "not_fresh", "not_authoritative",
                                     "one_sample", "not_terminal", "wrong_state"])
async def test_removed_manager_never_confirms_unverified_or_other_call_receipt(native_env, invalid):
    prepared = await prepare(native_env)
    session = native_env.manager.get(prepared["call_id"])
    session.commit_result = {"ok": True}
    await native_env.manager.close(session.call_id)
    evidence = {"ok": True, "call_id": session.call_id, "confirmed_by": "call.status",
                "terminal_confirmed": True, "fresh": True, "authoritative": True,
                "terminal_samples": 2, "status": "idle"}
    session.release_state = "terminated"
    if invalid == "missing":
        evidence = None
    elif invalid == "other_call":
        evidence["call_id"] = "another-call"
    elif invalid == "not_fresh":
        evidence["fresh"] = False
    elif invalid == "not_authoritative":
        evidence["authoritative"] = False
    elif invalid == "one_sample":
        evidence["terminal_samples"] = 1
    elif invalid == "not_terminal":
        evidence["status"] = "active"
    else:
        session.release_state = "termination_pending"
    session.release_result = evidence
    before = len(native_env.calls)
    supervised = await main._supervise_cellular_termination(session)
    coordinated = await main._finalize_abandoned_cellular_media_owned(session)
    for result in (supervised, coordinated):
        assert not result["ok"] and not result["released"] and not result["terminal_confirmed"]
        assert not result.get("missing") and result["outcome"] == "unknown"
    assert len(native_env.calls) == before


@pytest.mark.asyncio
async def test_terminal_receipt_is_published_only_after_durable_confirmation(native_env, monkeypatch):
    prepared = await prepare(native_env)
    session = native_env.manager.get(prepared["call_id"])
    session.release_result = {"terminal_confirmed": False}
    monkeypatch.setattr(main.store, "save_cellular_call_lease", Mock(side_effect=OSError("disk unavailable")))
    with pytest.raises(OSError):
        await main._record_cellular_terminal(session, {
            "status": "idle", "fresh": True, "authoritative": True, "terminal_samples": 2},
            expected_attachment=native_env.attachment)
    assert session.release_state != "terminated"
    assert session.release_result["terminal_confirmed"] is False


@pytest.mark.asyncio
@pytest.mark.parametrize("rows", [[], [{"call_id": "other", "instance": "5"}],
                                  [{"call_id": "requested", "instance": "7"}]])
async def test_missing_owner_query_never_attributes_an_unrelated_call(monkeypatch, rows):
    monkeypatch.setattr(main.store, "list_open_cellular_call_leases", lambda: rows)
    result = await main._missing_cellular_media_result("5", "requested", "released")
    assert result == {"ok": True, "released": False, "missing": True}


@pytest.mark.asyncio
async def test_missing_owner_query_failure_is_pending_not_a_missing_success(monkeypatch):
    monkeypatch.setattr(main.store, "list_open_cellular_call_leases",
                        Mock(side_effect=OSError("database unavailable")))
    result = await main._missing_cellular_media_result("5", "requested", "released")
    assert not result["ok"] and not result["released"] and not result["missing"]
    assert result["termination_pending"] and result["outcome"] == "unknown"


@pytest.mark.asyncio
async def test_attachment_reconnect_before_commit_blocks_paid_action(native_env):
    prepared = await prepare(native_env)
    _browser, session = await prove_media(native_env, prepared)
    native_env.attachment.session_id = "replacement-attachment"
    with pytest.raises(main.HTTPException):
        await main.api_cellular_call_commit("5", session.call_id, {"owner_token": OWNER}, REQUEST)
    assert not any(item[0] in {"call.answer", "call.dial"} for item in native_env.calls)


@pytest.mark.asyncio
async def test_uncertain_incoming_answer_retains_owner_without_false_answered_history(native_env, monkeypatch):
    prepared = await prepare(native_env, incoming=True)
    _browser, session = await prove_media(native_env, prepared)
    rpc = main.modem_registry.rpc

    async def uncertain_rpc(iccid, method, params=None, **kwargs):
        if method == "call.answer":
            native_env.calls.append((method, params, kwargs))
            raise main.ModemTimeout("answer reply lost")
        if method == "operation.result":
            return {"found": False}
        return await rpc(iccid, method, params, **kwargs)

    monkeypatch.setattr(main.modem_registry, "rpc", uncertain_rpc)
    result = await main.api_cellular_incoming_answer(
        "5", session.call_id, {"owner_token": OWNER}, REQUEST)
    assert result["uncertain"] and native_env.state == "ringing-in"
    assert result["record"]["status"] == native_env.record["status"] == "unknown"
    assert main.hub.broadcast.await_args.args[0]["call"]["status"] == "unknown"
    assert native_env.manager.for_iccid(session.iccid) is session
    assert result["record"]["cellular_owner_call_id"] == session.call_id
    assert sum(item[0] == "call.answer" for item in native_env.calls) == 1


@pytest.mark.asyncio
async def test_wrong_owner_ws_disconnect_does_not_release_winner(native_env):
    prepared = await prepare(native_env)
    browser, task = await browser_connect(native_env, prepared, owner=OTHER_OWNER)
    await asyncio.wait_for(task, 1)
    assert browser.closed
    session = native_env.manager.get(prepared["call_id"])
    assert session is not None and session.browser_ws is None and not session.release_requested
    assert [item[0] for item in native_env.calls] == ["audio.open"]


@pytest.mark.asyncio
async def test_owned_ws_disconnect_before_answer_does_not_hang_up_physical_ring(native_env):
    prepared = await prepare(native_env, incoming=True)
    browser, task = await browser_connect(native_env, prepared)
    await wait_until(lambda: bool(browser.messages))
    await browser.incoming.put({"type": "websocket.disconnect"})
    await asyncio.wait_for(task, 1)
    assert native_env.manager.get(prepared["call_id"]) is None
    assert not any(item[0] in {"call.answer", "call.hangup"} for item in native_env.calls)


@pytest.mark.asyncio
async def test_closed_ws_cleanup_owner_fences_restart_recovery_until_cancel_is_persisted(native_env, monkeypatch):
    prepared = await prepare(native_env, incoming=True)
    browser, route = await browser_connect(native_env, prepared)
    session = native_env.manager.get(prepared["call_id"])
    await wait_until(lambda: bool(browser.messages))
    entered, proceed = threading.Event(), threading.Event()
    save = main.store.save_cellular_call_lease

    def paused_save(call_id, iid, iccid, direction, state):
        if state == "cancelled":
            entered.set()
            assert proceed.wait(2)
        return save(call_id, iid, iccid, direction, state)

    monkeypatch.setattr(main.store, "save_cellular_call_lease", paused_save)
    monkeypatch.setattr(main.store, "list_open_cellular_call_leases",
                        lambda: list(native_env.leases.values()))
    try:
        # This fixture exercises the pre-ready/non-resumable cleanup owner. Healthy
        # committed media uses the separate reconnect-deadline regression.
        session.last_media_healthy_at = 0.0
        with patch.object(main.call_media, "CALL_HEARTBEAT_TIMEOUT_SECONDS", 0.0):
            await browser.incoming.put({"type": "websocket.disconnect"})
            assert await asyncio.to_thread(entered.wait, 1)
        assert session.closed.is_set()
        assert session.orphan_task is not None and not session.orphan_task.done()
        with patch.object(main.asyncio, "sleep", AsyncMock(
                side_effect=[None, asyncio.CancelledError])):
            with pytest.raises(asyncio.CancelledError):
                await main.cellular_call_lease_recovery()
        assert not any(item[0] == "call.hangup" for item in native_env.calls)
        assert native_env.manager.get(session.call_id) is session
    finally:
        proceed.set()
    await asyncio.wait_for(route, 1)
    if session.orphan_task:
        await asyncio.wait_for(asyncio.shield(session.orphan_task), 1)
    assert native_env.leases[session.call_id]["state"] == "cancelled"
    assert native_env.manager.get(session.call_id) is None
    next_owner = await prepare(native_env, incoming=True, owner=OTHER_OWNER)
    assert next_owner["call_id"] != session.call_id


@pytest.mark.asyncio
@pytest.mark.parametrize("initial,advanced,expected,hangup", [
    ("prepared", None, "cancelled", False),
    ("prepared", "signalling", "signalling", False),
    ("prepared", "terminal_confirmed", "terminal_confirmed", False),
    ("signalling", None, "terminal_confirmed", True),
    ("active", None, "terminal_confirmed", True),
])
async def test_crash_recovery_cancels_only_current_unowned_prepared_cas(
        monkeypatch, tmp_path, initial, advanced, expected, hangup):
    store = main.store
    path = tmp_path / "gateway.sqlite"
    monkeypatch.setattr(store, "DATA_DIR", str(tmp_path))
    monkeypatch.setattr(store, "DB_PATH", str(path))
    monkeypatch.setattr(store, "PREVIOUS_DB_PATH", str(tmp_path / "previous.sqlite"))
    store.init()
    store.save_cellular_call_lease("crash-call", "5", "sim", "in", initial)
    snapshot = store.list_open_cellular_call_leases()
    if advanced:
        store.save_cellular_call_lease("crash-call", "5", "sim", "in", advanced)
    monkeypatch.setattr(store, "list_open_cellular_call_leases", lambda: snapshot)
    monkeypatch.setattr(main.call_media.manager, "get", lambda call_id: None)
    attachment = Attachment(
        iccid="sim", agent_id="test-agent", modem_id="test-modem",
        session_id="test-session", websocket=None)
    monkeypatch.setattr(main.modem_registry, "resolve", lambda _iccid: attachment)
    rpc = AsyncMock(side_effect=[
        {"fresh": True, "authoritative": True, "status": "ringing-in", "terminal_samples": 0},
        {"ok": True},
        {"fresh": True, "authoritative": True, "status": "idle", "terminal_samples": 2},
    ])
    monkeypatch.setattr(main.modem_registry, "rpc", rpc)
    with patch.object(main.asyncio, "sleep", AsyncMock(side_effect=[None, asyncio.CancelledError])):
        with pytest.raises(asyncio.CancelledError):
            await main.cellular_call_lease_recovery()
    with sqlite3.connect(path) as connection:
        state = connection.execute(
            "SELECT state FROM cellular_call_leases WHERE call_id='crash-call'").fetchone()[0]
    assert state == expected
    if hangup:
        assert [call.args[1] for call in rpc.await_args_list] == [
            "call.status", "call.hangup", "call.status"]
    else:
        rpc.assert_not_awaited()


@pytest.fixture
def cross_env(native_env, monkeypatch):
    env = native_env
    env.channel_counts = {"5": 0, "7": 0}
    env.redirects = []

    def runtime(iid, **_kwargs):
        return {"running": True, "container_status": "running", "ip": "192.0.2.5",
                "container_id": "generation-" + str(iid), "engine_run_id": "run-" + str(iid),
                "media_websocket": True, "browser_outbound": True, "browser_inbound": True}

    async def ami_for(iid, *_args):
        return types.SimpleNamespace(complete_channel_snapshot=AsyncMock(
            side_effect=lambda: {"ok": True, "count": env.channel_counts[str(iid)]}))

    async def redirect(*, channel):
        env.redirects.append(channel)
        return {"ok": True}

    @asynccontextmanager
    async def ami_transaction(*_args, **_kwargs):
        yield types.SimpleNamespace(prepare_browser_media_redirect=AsyncMock(return_value={"ok": True}),
                                    redirect_browser_media_outbound=redirect)

    monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(side_effect=runtime))
    monkeypatch.setattr(main.hub, "ami_for", ami_for)
    monkeypatch.setattr(main.cfg, "get_instance", lambda iid: {
        "id": str(iid), "iccid": "test-sim" if str(iid) == "5" else "other-sim",
        "ami_secret": "mock-secret"})
    monkeypatch.setattr(main.cfg, "list_instances", lambda: [
        {"id": "5", "iccid": "test-sim"}, {"id": "7", "iccid": "other-sim"}])
    monkeypatch.setattr(main, "_line_admission_blocked", AsyncMock(return_value=False))
    monkeypatch.setattr(main, "OneShotAmiSession", ami_transaction)
    monkeypatch.setattr(main, "_maintenance_submission_boundary", REAL_MAINTENANCE_BOUNDARY)
    monkeypatch.setattr(main, "_durable_maintenance_pending", lambda _iid: False)
    monkeypatch.setattr(main.engine, "engine_maintenance_locked", lambda *_args, **_kwargs: nullcontext())
    yield env


async def native_prepare(iid="5", *, incoming=False):
    return await main._allocate_browser_media(
        iid, REQUEST, purpose="inbound" if incoming else "outbound",
        destination="" if incoming else "+44123456789", backend_call_id=91 if incoming else 0,
        backend_revision=0 if incoming else -1,
        source_call_id=f"run-{iid}:171.5" if incoming else "")


def mark_mock_native_healthy(prepared):
    # The cross-transport test supplies a healthy Asterisk peer; the normal browser media
    # tests prove its frame/challenge protocol separately. No arbitration guard is mocked.
    session = main.browser_media.registry.get(prepared["session_id"])
    session.browser_ws = Socket()
    session.asterisk_ws = Socket()
    session.asterisk_channel = "WebSocket/mock-" + session.iid
    session.asterisk_channel_id = session.channel_id
    session.started = True
    session.phase = "warmup"
    session.browser_to_engine_frames = session.engine_to_browser_frames = 2
    session.capture_callbacks = session.playback_callbacks = session.played_frames = 2
    now = time.monotonic()
    session.browser_to_engine_at = session.engine_to_browser_at = now
    session.evidence_at = session.challenge_ack_at = session.asterisk_status_at = now
    return session


@pytest.mark.asyncio
@pytest.mark.parametrize("first", ["native", "cellular"])
async def test_same_line_transport_orders_keep_only_first_owner_and_one_paid_action(cross_env, first):
    if first == "native":
        owner = mark_mock_native_healthy(await native_prepare())
        await main._redirect_native_browser_outbound(owner)
        with pytest.raises(main.HTTPException) as blocked:
            await prepare(cross_env)
        assert main.browser_media.registry.outbound("5") is owner
        assert not any(call[0] == "call.dial" for call in cross_env.calls)
        assert len(cross_env.redirects) == 1
    else:
        prepared = await prepare(cross_env)
        _browser, owner = await prove_media(cross_env, prepared)
        await main.api_cellular_call_commit("5", owner.call_id, {"owner_token": OWNER}, REQUEST)
        with pytest.raises(main.HTTPException) as blocked:
            await native_prepare()
        assert cross_env.manager.for_iccid("test-sim") is owner
        assert sum(call[0] == "call.dial" for call in cross_env.calls) == 1
        assert not cross_env.redirects
    assert blocked.value.status_code == 409


@pytest.mark.asyncio
@pytest.mark.parametrize("native_first", [False, True])
async def test_simultaneous_cross_transport_prepare_has_one_reservation(cross_env, native_first):
    calls = [native_prepare(), prepare(cross_env)]
    if not native_first:
        calls.reverse()
    result = await asyncio.gather(*calls, return_exceptions=True)
    assert sum(isinstance(item, dict) for item in result) == 1
    errors = [item for item in result if isinstance(item, main.HTTPException)]
    assert len(errors) == 1 and errors[0].status_code == 409
    assert sum((main.browser_media.registry.line_reserved("5"),
                cross_env.manager.for_iccid("test-sim") is not None)) == 1
    assert not any(call[0] in {"call.dial", "call.answer"} for call in cross_env.calls)


@pytest.mark.asyncio
@pytest.mark.parametrize("native_first", [False, True])
async def test_incoming_reservation_cannot_overlap_other_transport(cross_env, native_first):
    if native_first:
        native = await native_prepare(incoming=True)
        with pytest.raises(main.HTTPException):
            await prepare(cross_env, incoming=True)
        assert main.browser_media.registry.get(native["session_id"]) is not None
    else:
        cellular = await prepare(cross_env, incoming=True)
        with pytest.raises(main.HTTPException):
            await native_prepare(incoming=True)
        assert cross_env.manager.get(cellular["call_id"]) is not None
    assert not any(call[0] in {"call.answer", "call.hangup"} for call in cross_env.calls)


@pytest.mark.asyncio
async def test_closed_native_registry_does_not_hide_still_present_physical_channels(cross_env):
    native = await native_prepare()
    session = main.browser_media.registry.get(native["session_id"])
    await main.browser_media.registry.close(session)
    assert not main.browser_media.registry.line_reserved("5")
    cross_env.channel_counts["5"] = 1
    with pytest.raises(main.HTTPException) as blocked:
        await prepare(cross_env)
    assert blocked.value.status_code == 409
    assert cross_env.manager.for_iccid("test-sim") is None
    assert not cross_env.calls


@pytest.mark.asyncio
async def test_closed_cellular_media_keeps_native_blocked_until_physical_termination(cross_env):
    cellular = await prepare(cross_env)
    session = cross_env.manager.get(cellular["call_id"])
    session.commit_result = {"ok": True}
    await session.close()
    with pytest.raises(main.HTTPException):
        await native_prepare()
    assert cross_env.manager.for_iccid("test-sim") is session


@pytest.mark.asyncio
async def test_unrelated_lines_can_reserve_and_one_busy_lock_never_waits_a_whole_call(cross_env):
    lock = main.hub.recovery_lock("5")
    await lock.acquire()
    try:
        same, other = await asyncio.wait_for(asyncio.gather(
            prepare(cross_env), native_prepare("7"), return_exceptions=True), 1.5)
        assert isinstance(same, main.HTTPException) and same.status_code == 409
        assert isinstance(other, dict)
        assert lock.locked()
    finally:
        lock.release()
    cellular = await prepare(cross_env)
    assert cross_env.manager.get(cellular["call_id"]) is not None
    assert main.browser_media.registry.line_reserved("7")


@pytest.mark.asyncio
@pytest.mark.parametrize("status", ["missing", "exited", "dead"])
async def test_explicit_no_running_engine_needs_no_ami_for_cellular(native_env, monkeypatch, status):
    monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(return_value={
        "running": False, "container_status": status,
        "container_id": None if status == "missing" else "stopped-generation"}))
    forbidden = AsyncMock(side_effect=AssertionError("no AMI dependency when Engine is absent/stopped"))
    monkeypatch.setattr(main.hub, "ami_for", forbidden)
    prepared = await prepare(native_env)
    assert native_env.manager.get(prepared["call_id"])
    forbidden.assert_not_awaited()


@pytest.mark.asyncio
@pytest.mark.parametrize("runtime", [{}, {"running": False},
    {"running": False, "container_status": "paused", "container_id": "paused"},
    {"running": False, "container_status": "restarting", "container_id": "restarting"}])
async def test_unknown_or_suspended_engine_is_not_misreported_as_idle(native_env, monkeypatch, runtime):
    monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(return_value=runtime))
    with pytest.raises(main.HTTPException) as unknown:
        await prepare(native_env)
    assert unknown.value.status_code == 409
    assert not native_env.calls


@pytest.mark.asyncio
async def test_last_cellular_commit_rechecks_native_reservation_without_canceling_it(cross_env):
    cellular = await prepare(cross_env)
    _browser, session = await prove_media(cross_env, cellular)
    # A native reservation reconstructed independently between prepare and commit must also
    # be caught by the final paid boundary, not only by initial HTTP admission.
    native = await main.browser_media.registry.allocate(
        iid="5", generation="generation-5", engine_run_id="run-5", subject="subject",
        purpose="outbound", destination="+44123456789", call_token="T" * 43)
    with pytest.raises(main.HTTPException):
        await main.api_cellular_call_commit("5", session.call_id, {"owner_token": OWNER}, REQUEST)
    assert not any(call[0] == "call.dial" for call in cross_env.calls)
    assert main.browser_media.registry.get(native.session_id) is native


@pytest.mark.asyncio
@pytest.mark.parametrize("incoming", [False, True])
async def test_last_native_paid_boundary_rechecks_durable_cellular_occupancy(cross_env, monkeypatch, incoming):
    native = await native_prepare(incoming=incoming)
    session = mark_mock_native_healthy(native)
    cross_env.leases["recovered-cellular"] = {
        "call_id": "recovered-cellular", "instance": "5", "iccid": "test-sim",
        "direction": "out", "state": "active"}
    if incoming:
        claim = Mock(side_effect=AssertionError("no incoming claim over a cellular lease"))
        monkeypatch.setattr(main.store, "claim_browser_call", claim)
        async with main._line_call_reservation("5"):
            await main._run_browser_inbound_owner_locked(session)
        claim.assert_not_called()
    else:
        with pytest.raises(main.HTTPException):
            await main._redirect_native_browser_outbound(session)
    assert not cross_env.redirects
    assert cross_env.leases["recovered-cellular"]["state"] == "active"


@pytest.mark.asyncio
@pytest.mark.parametrize("state", ["prepared", "signalling", "active", "unknown", "hangup_failed"])
async def test_durable_cellular_lease_without_live_socket_still_blocks_native_prepare(cross_env, state):
    cross_env.leases["unresolved-cellular"] = {
        "call_id": "unresolved-cellular", "instance": "5", "iccid": "test-sim",
        "direction": "out", "state": state}
    with pytest.raises(main.HTTPException) as blocked:
        await native_prepare()
    assert blocked.value.status_code == 409
    assert not main.browser_media.registry.line_reserved("5")
    assert cross_env.leases["unresolved-cellular"]["state"] == state


@pytest.mark.asyncio
@pytest.mark.parametrize("iid", ["5", "7"])
async def test_durable_native_owner_is_scoped_to_its_line(cross_env, monkeypatch, iid):
    monkeypatch.setattr(main.store, "list_nonterminal_browser_calls", lambda: [{
        "instance": iid, "browser_state": "ending"}])
    if iid == "5":
        with pytest.raises(main.HTTPException):
            await prepare(cross_env)
        assert not cross_env.calls
    else:
        prepared = await prepare(cross_env)
        assert cross_env.manager.get(prepared["call_id"])


@pytest.mark.asyncio
@pytest.mark.parametrize("snapshot", [{}, {"ok": False, "count": 0}, {"ok": True, "count": None}])
async def test_incomplete_ami_snapshot_never_means_idle_cellular_line(cross_env, monkeypatch, snapshot):
    monkeypatch.setattr(main.hub, "ami_for", AsyncMock(return_value=types.SimpleNamespace(
        complete_channel_snapshot=AsyncMock(return_value=snapshot))))
    with pytest.raises(main.HTTPException) as unknown:
        await prepare(cross_env)
    assert unknown.value.status_code == 409
    assert not cross_env.calls


@pytest.mark.asyncio
async def test_engine_generation_change_during_idle_snapshot_blocks_cellular_reservation(cross_env, monkeypatch):
    first = await main.hub.runtime.get("5", force=True)
    monkeypatch.setattr(main.hub.runtime, "get", AsyncMock(side_effect=[
        first, {**first, "container_id": "replacement"}]))
    with pytest.raises(main.HTTPException) as unknown:
        await prepare(cross_env)
    assert unknown.value.status_code == 409
    assert not cross_env.calls


@pytest.mark.asyncio
@pytest.mark.parametrize("native", [False, True])
async def test_cross_transport_lease_read_failure_is_unknown_not_idle(cross_env, monkeypatch, native):
    reader = "list_open_cellular_call_leases" if native else "list_nonterminal_browser_calls"
    monkeypatch.setattr(main.store, reader, Mock(side_effect=OSError("database unavailable")))
    with pytest.raises(main.HTTPException) as unknown:
        await (native_prepare() if native else prepare(cross_env))
    assert unknown.value.status_code == 409
    assert unknown.value.detail == "Call state is unknown"
    assert not cross_env.calls
