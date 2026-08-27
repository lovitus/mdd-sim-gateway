from smartcard.Exceptions import CardConnectionException

from engine import ami_usim


def test_recognizes_service_not_available_messages():
    for protocol in ("T0", "T1"):
        exc = CardConnectionException(
            f"Failed to transmit with protocol {protocol}. Service not available.")
        assert ami_usim._pcsc_unavailable_cause(exc) == "pcsc_service_unavailable"
    assert ami_usim._pcsc_unavailable_cause(
        CardConnectionException("Service not available.")) == "pcsc_service_unavailable"


def test_recognizes_card_was_reset_messages():
    for protocol in ("T0", "T1"):
        exc = CardConnectionException(
            f"Failed to transmit with protocol {protocol}. Card was reset.")
        assert ami_usim._pcsc_unavailable_cause(exc) == "pcsc_card_reset"


def test_recognizes_card_was_removed_messages_as_the_same_transient_class():
    """A remote VPCD reader reports this exact message when the WebSocket bridge to the
    Agent drops mid-transmit -- the physical card has not moved, but pcsc-lite has no
    other vocabulary for "the backing transport disappeared momentarily"."""
    for protocol in ("T0", "T1"):
        exc = CardConnectionException(
            f"Failed to transmit with protocol {protocol}. Card was removed.")
        assert ami_usim._pcsc_unavailable_cause(exc) == "pcsc_card_reset"


def test_does_not_recognize_unrelated_or_non_pcsc_exceptions():
    assert ami_usim._pcsc_unavailable_cause(ValueError("Card was removed.")) == ""
    assert ami_usim._pcsc_unavailable_cause(
        CardConnectionException("some other transmit failure")) == ""
    assert ami_usim._pcsc_unavailable_cause(
        CardConnectionException("No card present in reader")) == ""
