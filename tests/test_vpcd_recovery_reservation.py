import json
import os
from dataclasses import replace

import pytest

from control.app.vpcd_slots import (
    RecoveryReservation, SlotBusy, VpcdSlotRegistry,
)


class Clock:
    def __init__(self, value=1000.0):
        self.value = value

    def __call__(self):
        return self.value


def exact_registry(tmp_path, slots=2):
    clock = Clock()
    registry = VpcdSlotRegistry(
        str(tmp_path / "vpcd-slots.json"), max_slots=slots, clock=clock)
    claim = registry.claim(agent_id="agent-a", reader_id="reader-a", agent_run_id="run-a")
    assert registry.mark_ready(claim)
    generation = registry.begin_observation("Virtual PCD 00 00")
    assert registry.observe_card(
        "Virtual PCD 00 00",
        {"iccid": "8944110000000000001", "imsi": "234100000000001", "matched": "7"},
        eid="89049032000000000000000000000001",
        expected_generation=generation)
    current = registry.snapshot()[0]["current_identity"]
    digest = registry.current_identity_digest(current)
    reservation = registry.begin_recovery_reservation(
        0, campaign_epoch="c" * 64,
        expected_session_generation=claim.session_generation,
        current_identity_digest=digest, deadline=1010.0)
    return registry, clock, claim, reservation


def test_disconnect_keeps_reserved_slot_busy_but_another_slot_is_available(tmp_path):
    registry, _clock, claim, reservation = exact_registry(tmp_path)
    assert registry.release(claim)
    with pytest.raises(SlotBusy):
        registry.begin_recovery_reservation(
            0, campaign_epoch=reservation.campaign_epoch,
            expected_session_generation=reservation.expected_session_generation,
            current_identity_digest=reservation.current_identity_digest,
            deadline=reservation.deadline)
    with pytest.raises(SlotBusy):
        registry.claim(agent_id="agent-b", reader_id="reader-b", requested_slot=0)
    other = registry.claim(agent_id="agent-b", reader_id="reader-b")
    assert other.slot == 1


def test_control_restart_reloads_reservation_and_blocks_same_slot(tmp_path):
    registry, clock, claim, reservation = exact_registry(tmp_path)
    assert registry.release(claim)
    restored = VpcdSlotRegistry(registry.path, max_slots=2, clock=clock)
    assert restored.recovery_reservation(0) == reservation
    with pytest.raises(SlotBusy):
        restored.claim(agent_id="agent-b", reader_id="reader-b", requested_slot=0)
    assert restored.claim(agent_id="agent-b", reader_id="reader-b").slot == 1


def test_deadline_never_releases_slot_without_exact_terminal_clear(tmp_path):
    registry, clock, claim, reservation = exact_registry(tmp_path)
    assert registry.release(claim)
    clock.value = 1011.0
    assert registry.validate_recovery_reservation(reservation) is False
    with pytest.raises(SlotBusy):
        registry.claim(agent_id="agent-b", reader_id="reader-b", requested_slot=0)
    assert registry.clear_recovery_reservation(
        replace(reservation, token="f" * 32)) is False
    with pytest.raises(SlotBusy):
        registry.claim(agent_id="agent-b", reader_id="reader-b", requested_slot=0)
    assert registry.clear_recovery_reservation(reservation) is True
    assert registry.claim(
        agent_id="agent-b", reader_id="reader-b", requested_slot=0).slot == 0


def test_reserved_identity_is_only_observed_idempotently(tmp_path):
    registry, _clock, _claim, reservation = exact_registry(tmp_path)
    before = registry.snapshot()[0]["current_identity"]
    generation = registry.begin_observation("Virtual PCD 00 00")
    assert generation == reservation.expected_session_generation
    assert registry.snapshot()[0]["current_identity"] == before
    assert registry.observe_card(
        "Virtual PCD 00 00", before,
        expected_generation=generation) is True
    assert registry.observe_card(
        "Virtual PCD 00 00", {"iccid": "8944110000000000999", "matched": "8"},
        expected_generation=generation) is False
    assert registry.confirm_card_absent("Virtual PCD 00 00", generation) is False
    assert registry.snapshot()[0]["current_identity"] == before


def test_begin_requires_exact_current_generation_and_identity(tmp_path):
    registry, _clock, _claim, reservation = exact_registry(tmp_path)
    assert registry.begin_recovery_reservation(
        0, campaign_epoch=reservation.campaign_epoch,
        expected_session_generation=reservation.expected_session_generation,
        current_identity_digest=reservation.current_identity_digest,
        deadline=1099.0) == reservation
    with pytest.raises(SlotBusy):
        registry.begin_recovery_reservation(
            0, campaign_epoch="d" * 64,
            expected_session_generation=reservation.expected_session_generation,
            current_identity_digest=reservation.current_identity_digest,
            deadline=1010.0)


def test_strict_reservation_file_is_private_and_corruption_fails_closed(tmp_path):
    registry, _clock, claim, _reservation = exact_registry(tmp_path)
    assert os.stat(registry.reservation_path).st_mode & 0o777 == 0o600
    assert registry.release(claim)
    with open(registry.reservation_path, "w", encoding="utf-8") as handle:
        json.dump({"broken": True}, handle)
    broken = VpcdSlotRegistry(registry.path, max_slots=2)
    with pytest.raises(SlotBusy):
        broken.claim(agent_id="agent-b", reader_id="reader-b", requested_slot=1)
    assert broken.begin_observation("Virtual PCD 00 00") is None


def test_malformed_known_slot_is_fenced_without_blocking_another_slot(tmp_path):
    path = tmp_path / "vpcd-slots.json"
    reservation_path = tmp_path / "vpcd-slots.recovery-reservations.json"
    reservation_path.write_text(json.dumps({
        "version": 1, "updated_at": 1000.0,
        "reservations": {"0": {"broken": True}},
    }), encoding="utf-8")
    os.chmod(reservation_path, 0o600)
    registry = VpcdSlotRegistry(str(path), max_slots=2, clock=Clock())
    with pytest.raises(SlotBusy):
        registry.claim(agent_id="agent-a", reader_id="reader-a", requested_slot=0)
    assert registry.claim(
        agent_id="agent-b", reader_id="reader-b", requested_slot=1).slot == 1


def test_nonprivate_reservation_file_fails_all_slots_closed(tmp_path):
    path = tmp_path / "vpcd-slots.json"
    reservation_path = tmp_path / "vpcd-slots.recovery-reservations.json"
    reservation_path.write_text(json.dumps({
        "version": 1, "updated_at": 1000.0, "reservations": {},
    }), encoding="utf-8")
    os.chmod(reservation_path, 0o644)
    registry = VpcdSlotRegistry(str(path), max_slots=2, clock=Clock())
    with pytest.raises(SlotBusy):
        registry.claim(agent_id="agent-a", reader_id="reader-a", requested_slot=1)


def test_clear_requires_the_real_reservation_type(tmp_path):
    registry, _clock, _claim, reservation = exact_registry(tmp_path)
    assert isinstance(reservation, RecoveryReservation)
    assert registry.clear_recovery_reservation({
        "slot": reservation.slot, "token": reservation.token}) is False
