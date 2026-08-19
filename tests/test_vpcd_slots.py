import json

import pytest

from control.app.vpcd_slots import (
    BASE_PORT, SlotBusy, SlotFull, VpcdSlotRegistry, slot_from_reader_name,
)


class Clock:
    def __init__(self):
        self.value = 1000

    def __call__(self):
        return self.value


def registry(tmp_path, slots=3):
    clock = Clock()
    return VpcdSlotRegistry(str(tmp_path / "vpcd-slots.json"), max_slots=slots,
                            clock=clock), clock


def test_reader_name_maps_to_transport_slot():
    assert slot_from_reader_name("Virtual PCD 00 00") == 0
    assert slot_from_reader_name("Virtual PCD 00 0A") == 10
    assert slot_from_reader_name("Virtual PCD 00 0F") == 15
    assert slot_from_reader_name("Virtual PCD 00 15") is None
    assert slot_from_reader_name("USB Reader 00 00") is None


def test_stable_reader_reuses_slot_after_disconnect(tmp_path):
    slots, clock = registry(tmp_path)
    first = slots.claim(agent_id="mac-a", reader_id="reader-a", reader_name="USB A")
    assert (first.slot, first.port) == (0, BASE_PORT)
    clock.value += 5
    assert slots.release(first)
    second = slots.claim(agent_id="mac-a", reader_id="reader-a", reader_name="USB A")
    assert second.slot == 0


def test_legacy_reader_reuses_recent_anonymous_slot_instead_of_growing_history(tmp_path):
    slots, clock = registry(tmp_path)
    first = slots.claim()
    assert first.slot == 0
    clock.value += 5
    slots.release(first)
    second = slots.claim()
    assert second.slot == 0
    assert len(slots.snapshot()) == 1


def test_live_slot_and_duplicate_endpoint_are_never_overwritten(tmp_path):
    slots, _ = registry(tmp_path, slots=2)
    slots.claim(agent_id="mac-a", reader_id="reader-a")
    with pytest.raises(SlotBusy):
        slots.claim(agent_id="mac-a", reader_id="reader-a")
    slots.claim(agent_id="mac-a", reader_id="reader-b")
    with pytest.raises(SlotFull):
        slots.claim(agent_id="mac-a", reader_id="reader-c")


def test_legacy_direct_slots_are_treated_as_unavailable(tmp_path):
    slots, _ = registry(tmp_path, slots=3)
    claim = slots.claim(agent_id="phone", reader_id="omapi",
                        unavailable_slots={0, 1})
    assert claim.slot == 2
    slots.release(claim)
    with pytest.raises(SlotBusy):
        slots.claim(agent_id="phone", reader_id="explicit", requested_slot=1,
                    unavailable_slots={0, 1})


def test_oldest_offline_slot_is_reused_when_history_is_full(tmp_path):
    slots, clock = registry(tmp_path, slots=2)
    first = slots.claim(agent_id="mac", reader_id="oldest")
    slots.release(first)
    clock.value += 10
    second = slots.claim(agent_id="mac", reader_id="newer")
    slots.release(second)
    clock.value += 10
    replacement = slots.claim(agent_id="mac", reader_id="new-reader")
    assert replacement.slot == 0


def test_same_card_hint_reuses_history_even_with_a_new_reader(tmp_path):
    slots, _ = registry(tmp_path, slots=2)
    first = slots.claim(agent_id="mac", reader_id="reader-a", card_id="eid-1")
    slots.release(first)
    replacement = slots.claim(agent_id="phone", reader_id="omapi", card_id="eid-1")
    assert replacement.slot == first.slot


def test_observed_identity_survives_restart_and_enriches_offline_card(tmp_path):
    slots, _ = registry(tmp_path)
    claim = slots.claim(agent_id="mac", reader_id="reader-a", reader_name="USB A")
    assert slots.observe_card("Virtual PCD 00 00", {
        "iccid": "8944110000000000001", "imsi": "234100000000001", "matched": "1",
    }, eid="89049032000000000000000000000001")
    slots.release(claim)

    restored = VpcdSlotRegistry(slots.path, max_slots=3)
    cards = restored.enrich_cards([{
        "index": 0, "name": "Virtual PCD 00 00", "present": False,
    }])
    assert cards[0]["connection_online"] is False
    assert cards[0]["iccid"] == "8944110000000000001"
    assert cards[0]["eid"] == "89049032000000000000000000000001"
    assert json.loads((tmp_path / "vpcd-slots.json").read_text())["slots"]["0"]["reader_id"] == "reader-a"


def test_card_move_retires_its_old_offline_location(tmp_path):
    slots, _ = registry(tmp_path, slots=2)
    old = slots.claim(agent_id="mac", reader_id="reader-a")
    slots.observe_card("Virtual PCD 00 00", {"iccid": "8944110000000000001"})
    slots.release(old)
    new = slots.claim(agent_id="phone", reader_id="reader-b", requested_slot=1)
    slots.observe_card("Virtual PCD 00 01", {"iccid": "8944110000000000001"})
    snapshot = {item["slot"]: item for item in slots.snapshot()}
    assert "iccid" not in snapshot[0]
    assert snapshot[1]["iccid"] == "8944110000000000001"
    slots.release(new)


def test_placeholder_identity_is_removed_but_eid_is_retained(tmp_path):
    slots, _ = registry(tmp_path)
    claim = slots.claim(agent_id="phone", reader_id="omapi")
    slots.observe_card("Virtual PCD 00 00", {
        "iccid": "89111111111111111111", "imsi": "111111111111111", "matched": "4",
    })
    slots.observe_card("Virtual PCD 00 00", {"identity_placeholder": True},
                       eid="89086030202200000025000132416289")
    record = slots.snapshot()[0]
    assert "iccid" not in record
    assert "imsi" not in record
    assert "matched" not in record
    assert record["eid"] == "89086030202200000025000132416289"
    slots.release(claim)
