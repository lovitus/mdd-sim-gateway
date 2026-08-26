"""Run real challenge issuers/validators with a local clock; no sockets or paid actions."""
import ast
import inspect
from pathlib import Path
from types import SimpleNamespace
import textwrap

import pytest

from control.app import browser_media, call_media


class IssuanceComplete(Exception):
    pass


class Clock:
    def __init__(self):
        self.now = 100.0

    def monotonic(self):
        return self.now

    async def sleep(self, seconds):
        self.now += seconds


class ChallengeSink:
    def __init__(self, clock, count, message_types=None):
        self.clock, self.count = clock, count
        self.message_types = message_types or {"browser.media.challenge", "cellular.media.challenge"}
        self.issued = []

    async def send_json(self, value):
        if value.get("type") in self.message_types:
            self.issued.append((self.clock.now, value["challenge"]))
            if len(self.issued) == self.count:
                raise IssuanceComplete


def session_for(protocol):
    if protocol == "cellular":
        return call_media.MediaSession(
            call_id="c" * 32, iccid="test-card", token="test-token",
            owner_subject="owner", owner_token="owner-token")
    return browser_media.BrowserMediaSession(
        session_id="s" * 32, ticket="ticket", engine_sid="sid", subject="owner",
        iid="7", generation="a" * 64, engine_run_id="run-7", channel_id="canary")


async def issue_using_production_loop(protocol, clock, count, monkeypatch, session=None):
    """Extract only the issuer coroutine; all session methods remain production methods.

    This avoids starting the full HTTP/PCM bridge solely to rotate its challenges, and it
    also makes the regression follow the real issuer when it gains bounded history.
    """
    session = session or session_for(protocol)
    if protocol == "cellular":
        module = call_media
        tree = ast.parse(textwrap.dedent(inspect.getsource(call_media.MediaSession._bridge)))
        function = next(node for node in ast.walk(tree)
                        if isinstance(node, ast.AsyncFunctionDef) and node.name == "monitor")
        args = ()
    else:
        module = browser_media
        path = Path(__file__).resolve().parents[1] / "control/app/main.py"
        tree = ast.parse(path.read_text())
        function = next(node for node in tree.body
                        if isinstance(node, ast.AsyncFunctionDef)
                        and node.name == "_browser_media_challenges")
        args = (session,)
    monkeypatch.setattr(module, "time", clock)
    sink = ChallengeSink(clock, count)
    session.browser_ws = sink
    namespace = {**vars(module), "self": session, "browser_media": browser_media,
                 "asyncio": SimpleNamespace(sleep=clock.sleep), "time": clock}
    compiled = compile(ast.fix_missing_locations(ast.Module(
        body=[function], type_ignores=[])), "<production-challenge-issuer>", "exec")
    exec(compiled, namespace)
    with pytest.raises(IssuanceComplete):
        await namespace[function.name](*args)
    return session, sink.issued


async def issue_initial_challenge(protocol, clock, monkeypatch):
    """Exercise the first started/claimed challenge, not only periodic rotation."""
    module = call_media if protocol == "cellular" else browser_media
    monkeypatch.setattr(module, "time", clock)
    session = session_for(protocol)
    sink = ChallengeSink(clock, 1, {"cellular.media.started", "browser.media.claimed"})
    if protocol == "cellular":
        with pytest.raises(IssuanceComplete):
            await session.attach_browser(sink, "owner", "owner-token")
    else:
        # The native claimed reply lives among HTTP/AMI setup operations. Compile only its
        # immediately preceding issuance statement and the actual send; never run setup.
        path = Path(__file__).resolve().parents[1] / "control/app/main.py"
        tree = ast.parse(path.read_text())
        endpoint = next(node for node in tree.body if isinstance(node, ast.AsyncFunctionDef)
                        and node.name == "api_browser_media_ws")
        sections = []
        for node in ast.walk(endpoint):
            for field in ("body", "orelse", "finalbody"):
                body = getattr(node, field, None)
                if not isinstance(body, list):
                    continue
                for index, statement in enumerate(body):
                    if (isinstance(statement, ast.Expr)
                            and isinstance(statement.value, ast.Await)
                            and isinstance(statement.value.value, ast.Call)
                            and getattr(statement.value.value.func, "attr", "") == "send_json"
                            and any(isinstance(item, ast.Constant)
                                    and item.value == "browser.media.claimed"
                                    for item in ast.walk(statement))):
                        assert index > 0
                        sections.append(body[index - 1:index + 1])
        assert len(sections) == 1
        function = ast.parse("async def initial(session):\n    pass\n").body[0]
        function.body = sections[0]
        namespace = {**vars(browser_media), "time": clock}
        session.browser_ws = sink
        exec(compile(ast.fix_missing_locations(ast.Module(body=[function], type_ignores=[])),
                     "<production-initial-challenge>", "exec"), namespace)
        with pytest.raises(IssuanceComplete):
            await namespace[function.name](session)
    return session, sink.issued[0]


def evidence(protocol, challenge, counter=4):
    return {"type": "cellular.media.evidence" if protocol == "cellular"
            else "browser.media.evidence", "version": 1, "challenge": challenge,
            "capture_callbacks": counter, "playback_callbacks": counter,
            "played_frames": counter}


def accepted_evidence_state(protocol, session):
    if protocol == "cellular":
        return (dict(session.browser_evidence), session.browser_evidence_at,
                session.browser_capture_growth_at, session.browser_playback_growth_at)
    return (session.capture_callbacks, session.playback_callbacks, session.played_frames,
            session.evidence_at, session.challenge_ack_at)


@pytest.mark.asyncio
@pytest.mark.parametrize("protocol", ["cellular", "native"])
async def test_recent_issued_challenge_survives_more_than_one_rotation(protocol, monkeypatch):
    clock = Clock()
    session, issued = await issue_using_production_loop(protocol, clock, 3, monkeypatch)
    first_at, first = issued[0]
    clock.now += .1
    # Cellular: 2.1 seconds old; steady-state native: 4.1 seconds old. Both are
    # inside the existing five-second media freshness window, not arbitrary tokens.
    assert 0 < clock.now - first_at < module_freshness(protocol)
    assert first not in [nonce for _stamp, nonce in issued[-2:]]
    session.record_browser_evidence(evidence(protocol, first))
    observed = accepted_evidence_state(protocol, session)
    assert clock.now in observed


def module_freshness(protocol):
    return (call_media if protocol == "cellular" else browser_media).EVIDENCE_FRESH_SECONDS


@pytest.mark.asyncio
@pytest.mark.parametrize("protocol", ["cellular", "native"])
async def test_initial_started_or_claimed_challenge_is_actually_issued(protocol, monkeypatch):
    clock = Clock()
    session, (_issued_at, challenge) = await issue_initial_challenge(protocol, clock, monkeypatch)
    session.record_browser_evidence(evidence(protocol, challenge))
    assert clock.now in accepted_evidence_state(protocol, session)


@pytest.mark.asyncio
async def test_native_initial_challenge_survives_immediate_rotation_and_three_second_echo(monkeypatch):
    clock = Clock()
    session, (issued_at, initial) = await issue_initial_challenge("native", clock, monkeypatch)
    await issue_using_production_loop("native", clock, 2, monkeypatch, session)
    clock.now = issued_at + 3.0
    session.record_browser_evidence(evidence("native", initial))
    assert session.challenge_ack_at == clock.now


@pytest.mark.asyncio
@pytest.mark.parametrize("protocol", ["cellular", "native"])
async def test_latest_challenge_cannot_keep_evidence_fresh_after_issuance_expires(protocol, monkeypatch):
    clock = Clock()
    session, issued = await issue_using_production_loop(protocol, clock, 1, monkeypatch)
    issued_at, challenge = issued[0]
    session.record_browser_evidence(evidence(protocol, challenge))
    before = accepted_evidence_state(protocol, session)
    clock.now = issued_at + module_freshness(protocol) + .1
    error = call_media.MediaUnavailable if protocol == "cellular" else browser_media.BrowserMediaUnavailable
    with pytest.raises(error, match="challenge.*stale"):
        session.record_browser_evidence(evidence(protocol, challenge, counter=8))
    assert accepted_evidence_state(protocol, session) == before


@pytest.mark.asyncio
@pytest.mark.parametrize("protocol", ["cellular", "native"])
async def test_unknown_challenge_never_updates_evidence(protocol, monkeypatch):
    clock = Clock()
    session, _ = await issue_using_production_loop(protocol, clock, 1, monkeypatch)
    before = accepted_evidence_state(protocol, session)
    error = call_media.MediaUnavailable if protocol == "cellular" else browser_media.BrowserMediaUnavailable
    with pytest.raises(error, match="challenge.*stale"):
        session.record_browser_evidence(evidence(protocol, "never-issued"))
    assert accepted_evidence_state(protocol, session) == before
