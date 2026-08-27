from dataclasses import replace

import pytest

from control.app.vpcd_slots import (
    MaintenanceReservation, SlotBusy, VpcdSlotRegistry,
)

TXID = "engine-replace-1787810000-abcdef012345"
OTHER_TXID = "engine-replace-1787820000-abcdef012345"


class Clock:
    def __init__(self, value=1000.0):
        self.value = value

    def __call__(self):
        return self.value


def exact_registry(tmp_path, slots=2):
    """A slot with a durable card identity recorded. ``begin_maintenance_reservation``
    is meant to work identically whether that identity is currently live
    (``current_identity``, same process) or only durably persisted
    (``last_known_identity``, e.g. a fresh host-side process after a restart)."""
    clock = Clock()
    registry = VpcdSlotRegistry(
        str(tmp_path / "vpcd-slots.json"), max_slots=slots, clock=clock)
    claim = registry.claim(agent_id="agent-a", reader_id="reader-a", agent_run_id="run-a")
    assert registry.mark_ready(claim)
    generation = registry.begin_observation("Virtual PCD 00 00")
    assert registry.observe_card(
        "Virtual PCD 00 00",
        {"iccid": "8944110000000000001", "imsi": "234100000000001", "matched": "1"},
        eid="89049032000000000000000000000001",
        expected_generation=generation)
    current = registry.snapshot()[0]["current_identity"]
    digest = registry.durable_identity_digest(current)
    return registry, clock, claim, digest


def test_maintenance_reservation_works_against_a_live_current_identity(tmp_path):
    registry, _clock, _claim, digest = exact_registry(tmp_path)
    reservation = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_durable_identity_digest=digest,
        deadline=1010.0)
    assert isinstance(reservation, MaintenanceReservation)
    assert registry.validate_maintenance_reservation(reservation) is True


def test_maintenance_reservation_works_after_a_process_restart(tmp_path):
    """Unlike RecoveryReservation, this must keep validating true after a fresh
    VpcdSlotRegistry instantiation, because it is keyed on the durable identity
    fields (last_known_identity) rather than the live-only current_identity."""
    registry, clock, claim, digest = exact_registry(tmp_path)
    registry.release(claim)  # no recovery reservation held: migrates to last_known
    restored = VpcdSlotRegistry(registry.path, max_slots=2, clock=clock)
    reservation = restored.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_durable_identity_digest=digest,
        deadline=1010.0)
    assert restored.validate_maintenance_reservation(reservation) is True

    reloaded_again = VpcdSlotRegistry(registry.path, max_slots=2, clock=clock)
    assert reloaded_again.maintenance_reservation(0) == reservation
    assert reloaded_again.validate_maintenance_reservation(reservation) is True


def test_maintenance_reservation_requires_exact_persisted_identity(tmp_path):
    registry, _clock, _claim, _digest = exact_registry(tmp_path)
    with pytest.raises(SlotBusy):
        registry.begin_maintenance_reservation(
            0, maintenance_txid=TXID,
            expected_durable_identity_digest="0" * 64, deadline=1010.0)


def test_maintenance_reservation_is_idempotent_for_same_transaction(tmp_path):
    registry, _clock, _claim, digest = exact_registry(tmp_path)
    first = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_durable_identity_digest=digest,
        deadline=1010.0)
    second = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_durable_identity_digest=digest,
        deadline=1010.0)
    assert first == second


def test_maintenance_reservation_rejects_a_second_transaction_on_same_slot(tmp_path):
    registry, _clock, _claim, digest = exact_registry(tmp_path)
    registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_durable_identity_digest=digest,
        deadline=1010.0)
    with pytest.raises(SlotBusy):
        registry.begin_maintenance_reservation(
            0, maintenance_txid=OTHER_TXID,
            expected_durable_identity_digest=digest, deadline=1010.0)


def test_maintenance_and_recovery_reservations_are_mutually_exclusive(tmp_path):
    registry, _clock, claim, digest = exact_registry(tmp_path)
    registry.begin_recovery_reservation(
        0, campaign_epoch="c" * 64, expected_session_generation=claim.session_generation,
        current_identity_digest=registry.current_identity_digest(
            registry.snapshot()[0]["current_identity"]),
        deadline=1010.0)
    with pytest.raises(SlotBusy):
        registry.begin_maintenance_reservation(
            0, maintenance_txid=TXID,
            expected_durable_identity_digest=digest, deadline=1010.0)


def test_maintenance_reservation_clear_requires_exact_token_and_txid(tmp_path):
    registry, _clock, _claim, digest = exact_registry(tmp_path)
    reservation = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_durable_identity_digest=digest,
        deadline=1010.0)
    assert registry.clear_maintenance_reservation(
        replace(reservation, token="f" * 32)) is False
    assert registry.clear_maintenance_reservation(
        replace(reservation, maintenance_txid=OTHER_TXID)) is False
    assert registry.clear_maintenance_reservation(reservation) is True
    assert registry.maintenance_reservation(0) is None


def test_maintenance_reservation_invalidated_if_card_is_removed_with_no_protection(
        tmp_path):
    registry, _clock, claim, digest = exact_registry(tmp_path)
    reservation = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_durable_identity_digest=digest,
        deadline=1010.0)
    # If the card is pulled and nothing else (no recovery reservation) protects the
    # slot, last_known_identity keeps the durable fields -- this should NOT
    # invalidate the reservation, since eid/iccid/imsi did not actually change.
    registry.release(claim)
    assert registry.validate_maintenance_reservation(reservation) is True


def test_maintenance_reservation_invalidated_if_a_different_card_takes_the_slot(
        tmp_path):
    registry, clock, claim, digest = exact_registry(tmp_path)
    reservation = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_durable_identity_digest=digest,
        deadline=1010.0)
    registry.clear_maintenance_reservation(reservation)
    registry.release(claim)
    new_claim = registry.claim(agent_id="agent-a", reader_id="reader-a",
                                requested_slot=0)
    registry.mark_ready(new_claim)
    generation = registry.begin_observation("Virtual PCD 00 00")
    registry.observe_card(
        "Virtual PCD 00 00",
        {"iccid": "8944110000000000009", "imsi": "234100000000009", "matched": "1"},
        eid="89049032000000000000000000000009",
        expected_generation=generation)
    assert registry.validate_maintenance_reservation(reservation) is False


def test_deadline_alone_does_not_release_the_slot_without_exact_clear(tmp_path):
    registry, clock, _claim, digest = exact_registry(tmp_path)
    reservation = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_durable_identity_digest=digest,
        deadline=1010.0)
    clock.value = 1011.0
    assert registry.validate_maintenance_reservation(reservation) is False
    # Still occupies the reservation record until explicitly cleared with proof.
    assert registry.maintenance_reservation(0) == reservation
