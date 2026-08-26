"""D2 RED: a VPCD route generation is never a stable card identity."""
import json

from control.app.vpcd_slots import VpcdSlotRegistry


def registry(tmp_path, slots=3):
    return VpcdSlotRegistry(str(tmp_path / "slots.json"), max_slots=slots)


def test_claim_token_is_the_only_session_generation(tmp_path):
    slots = registry(tmp_path)
    claim = slots.claim(agent_id="agent-a", reader_id="reader-a", agent_run_id="run-a")
    assert claim.session_generation == claim.token


def test_same_endpoint_reconnect_keeps_identity_only_as_last_known(tmp_path):
    slots = registry(tmp_path)
    first = slots.claim(agent_id="agent-a", reader_id="reader-a", agent_run_id="run-a")
    assert slots.mark_ready(first)
    generation = slots.begin_observation("Virtual PCD 00 00")
    assert slots.observe_card(
        "Virtual PCD 00 00",
        {"iccid": "8944110000000000001", "matched": "7"},
        eid="89049032000000000000000000000001",
        expected_generation=generation)
    assert slots.release(first)

    second = slots.claim(agent_id="agent-a", reader_id="reader-a", agent_run_id="run-a")
    row = slots.snapshot()[0]
    assert row["session_generation"] == second.token
    assert row.get("current_identity") in (None, {})
    assert row["last_known_identity"]["eid"] == "89049032000000000000000000000001"
    assert row["last_known_identity"]["iccid"] == "8944110000000000001"
    assert row["last_known_identity"]["matched"] == "7"
    assert not any(key in row for key in ("eid", "iccid", "matched"))


def test_observation_is_unavailable_until_current_claim_is_ready(tmp_path):
    slots = registry(tmp_path)
    claim = slots.claim(agent_id="agent-a", reader_id="reader-a", agent_run_id="run-a")
    assert slots.begin_observation("Virtual PCD 00 00") is None
    assert slots.mark_ready(claim)
    assert slots.begin_observation("Virtual PCD 00 00") == claim.token


def test_two_readers_and_stale_claims_never_cross_progress(tmp_path):
    slots = registry(tmp_path, slots=2)
    a = slots.claim(agent_id="agent-a", reader_id="reader-a", agent_run_id="run-a")
    b = slots.claim(agent_id="agent-a", reader_id="reader-b", agent_run_id="run-a")
    assert slots.mark_ready(a) and slots.mark_ready(b)
    ga = slots.begin_observation("Virtual PCD 00 00")
    gb = slots.begin_observation("Virtual PCD 00 01")
    assert (ga, gb) == (a.token, b.token) and ga != gb
    assert slots.observe_card(
        "Virtual PCD 00 00", {"iccid": "8944110000000000001"},
        expected_generation=ga)
    assert slots.observe_card(
        "Virtual PCD 00 01", {"iccid": "8944110000000000002"},
        expected_generation=gb)
    assert slots.release(a)
    replacement = slots.claim(
        agent_id="agent-a", reader_id="reader-a", agent_run_id="run-a")
    assert slots.mark_ready(replacement)
    assert not slots.observe_card(
        "Virtual PCD 00 00", {"iccid": "8944110000000000999"},
        expected_generation=ga)
    rows = {row["reader_id"]: row for row in slots.snapshot()}
    assert rows["reader-b"]["current_identity"]["iccid"] == "8944110000000000002"
    assert rows["reader-b"]["identity_current"] is True


def test_v1_flat_identity_migrates_to_history_not_current(tmp_path):
    path = tmp_path / "slots.json"
    path.write_text(json.dumps({"version": 1, "max_slots": 2, "slots": {"0": {
        "slot": 0, "endpoint_key": "agent-a/reader-a", "agent_id": "agent-a",
        "reader_id": "reader-a", "agent_run_id": "old-run",
        "session_generation": "old-generation", "identity_current": True,
        "eid": "89049032000000000000000000000001",
        "iccid": "8944110000000000001", "matched": "7",
    }}}))
    slots = VpcdSlotRegistry(str(path), max_slots=2)
    history = slots.snapshot()[0]
    assert history["identity_current"] is False
    assert history["last_known_identity"]["matched"] == "7"
    claim = slots.claim(agent_id="agent-a", reader_id="reader-a", agent_run_id="new-run")
    assert slots.mark_ready(claim)
    current = slots.snapshot()[0]
    assert current["session_generation"] == claim.token
    assert current["current_identity"] is None
    assert not any(key in current for key in ("eid", "iccid", "matched"))


def test_new_generation_current_identity_never_inherits_old_card_fields(tmp_path):
    slots = registry(tmp_path)
    old = slots.claim(agent_id="agent-a", reader_id="reader-a", agent_run_id="run-a")
    assert slots.mark_ready(old)
    generation = slots.begin_observation("Virtual PCD 00 00")
    assert slots.observe_card(
        "Virtual PCD 00 00", {"iccid": "8944110000000000001",
                               "imsi": "234100000000001", "matched": "7"},
        eid="89049032000000000000000000000001",
        expected_generation=generation)
    assert slots.release(old)
    replacement = slots.claim(
        agent_id="agent-a", reader_id="reader-a", agent_run_id="run-a")
    assert slots.mark_ready(replacement)
    generation = slots.begin_observation("Virtual PCD 00 00")
    assert slots.observe_card(
        "Virtual PCD 00 00", {"iccid": "8944110000000000002"},
        eid="89049032000000000000000000000002",
        expected_generation=generation)
    row = slots.snapshot()[0]
    assert row["identity_current"] is True
    assert row["current_identity"] == {
        "iccid": "8944110000000000002",
        "eid": "89049032000000000000000000000002",
        "session_generation": replacement.token,
    }
    assert not any(key in row["current_identity"] for key in ("matched", "imsi", "card_id"))
    assert row["last_known_identity"]["matched"] == "7"
    assert row["last_known_identity"]["imsi"] == "234100000000001"
