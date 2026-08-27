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
    """A slot whose persisted ``current_identity``/``session_generation`` matches a
    real card. The host-side maintenance process has no live WebSocket observation,
    so ``begin_maintenance_reservation`` is validated against this persisted record,
    the same one a prior ``RecoveryReservation`` would have kept pinned through the
    original fault (a plain disconnect with no reservation instead migrates
    ``current_identity`` to ``last_known_identity``, which is a different, weaker
    case covered separately below)."""
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
    digest = registry.current_identity_digest(current)
    return registry, clock, claim, digest


def test_maintenance_reservation_persists_and_blocks_the_slot_after_reload(tmp_path):
    registry, clock, claim, digest = exact_registry(tmp_path)
    reservation = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_session_generation=claim.session_generation,
        current_identity_digest=digest, deadline=1010.0)
    assert isinstance(reservation, MaintenanceReservation)
    assert registry.validate_maintenance_reservation(reservation) is True

    # Like RecoveryReservation, current_identity is never trusted across a process
    # restart (see VpcdSlotRegistry._migrate_record); only the same live process that
    # observed the card can call validate_maintenance_reservation() meaningfully. A
    # fresh instantiation still must not let the slot be reused, though.
    restored = VpcdSlotRegistry(registry.path, max_slots=2, clock=clock)
    assert restored.maintenance_reservation(0) == reservation
    with pytest.raises(SlotBusy):
        restored.claim(agent_id="agent-b", reader_id="reader-b", requested_slot=0)


def test_maintenance_reservation_requires_exact_persisted_identity(tmp_path):
    registry, _clock, claim, digest = exact_registry(tmp_path)
    with pytest.raises(SlotBusy):
        registry.begin_maintenance_reservation(
            0, maintenance_txid=TXID, expected_session_generation="wrong-generation",
            current_identity_digest=digest, deadline=1010.0)
    with pytest.raises(SlotBusy):
        registry.begin_maintenance_reservation(
            0, maintenance_txid=TXID,
            expected_session_generation=claim.session_generation,
            current_identity_digest="0" * 64, deadline=1010.0)


def test_maintenance_reservation_is_idempotent_for_same_transaction(tmp_path):
    registry, _clock, claim, digest = exact_registry(tmp_path)
    first = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_session_generation=claim.session_generation,
        current_identity_digest=digest, deadline=1010.0)
    second = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_session_generation=claim.session_generation,
        current_identity_digest=digest, deadline=1010.0)
    assert first == second


def test_maintenance_reservation_rejects_a_second_transaction_on_same_slot(tmp_path):
    registry, _clock, claim, digest = exact_registry(tmp_path)
    registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_session_generation=claim.session_generation,
        current_identity_digest=digest, deadline=1010.0)
    with pytest.raises(SlotBusy):
        registry.begin_maintenance_reservation(
            0, maintenance_txid=OTHER_TXID,
            expected_session_generation=claim.session_generation,
            current_identity_digest=digest, deadline=1010.0)


def test_maintenance_and_recovery_reservations_are_mutually_exclusive(tmp_path):
    registry, _clock, claim, digest = exact_registry(tmp_path)
    registry.begin_recovery_reservation(
        0, campaign_epoch="c" * 64, expected_session_generation=claim.session_generation,
        current_identity_digest=digest, deadline=1010.0)
    with pytest.raises(SlotBusy):
        registry.begin_maintenance_reservation(
            0, maintenance_txid=TXID,
            expected_session_generation=claim.session_generation,
            current_identity_digest=digest, deadline=1010.0)


def test_maintenance_reservation_clear_requires_exact_token_and_txid(tmp_path):
    registry, _clock, claim, digest = exact_registry(tmp_path)
    reservation = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_session_generation=claim.session_generation,
        current_identity_digest=digest, deadline=1010.0)
    assert registry.clear_maintenance_reservation(
        replace(reservation, token="f" * 32)) is False
    assert registry.clear_maintenance_reservation(
        replace(reservation, maintenance_txid=OTHER_TXID)) is False
    assert registry.clear_maintenance_reservation(reservation) is True
    assert registry.maintenance_reservation(0) is None


def test_maintenance_reservation_invalidated_if_card_identity_changes_underneath(
        tmp_path):
    registry, _clock, claim, digest = exact_registry(tmp_path)
    reservation = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_session_generation=claim.session_generation,
        current_identity_digest=digest, deadline=1010.0)
    # The card is unplugged/replaced and the slot's persisted current_identity is
    # migrated away (no recovery reservation protects it here) while the maintenance
    # transaction is in flight -- the reservation must fail closed, not silently
    # keep trusting a stale digest.
    registry.release(claim)
    assert registry.validate_maintenance_reservation(reservation) is False


def test_deadline_alone_does_not_release_the_slot_without_exact_clear(tmp_path):
    registry, clock, claim, digest = exact_registry(tmp_path)
    reservation = registry.begin_maintenance_reservation(
        0, maintenance_txid=TXID, expected_session_generation=claim.session_generation,
        current_identity_digest=digest, deadline=1010.0)
    clock.value = 1011.0
    assert registry.validate_maintenance_reservation(reservation) is False
    # Still occupies the reservation record until explicitly cleared with proof.
    assert registry.maintenance_reservation(0) == reservation
