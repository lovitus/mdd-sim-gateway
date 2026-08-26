from types import SimpleNamespace
from unittest.mock import Mock

import pytest

from control.app import sim


@pytest.mark.parametrize("failure", ["missing_handle", "error_code", "exception"])
def test_strict_transaction_never_transmits_without_successful_begin(monkeypatch, failure):
    conn = SimpleNamespace(transmit=Mock())
    if failure != "missing_handle":
        conn.hcard = 123
    begin = Mock(return_value=0x8010000B)
    if failure == "exception":
        begin.side_effect = RuntimeError("native begin failed")
    end = Mock(return_value=0)
    monkeypatch.setattr(sim, "SCardBeginTransaction", begin)
    monkeypatch.setattr(sim, "SCardEndTransaction", end)
    with pytest.raises(RuntimeError):
        with sim._Tx(conn, required=True):
            conn.transmit([0, 0xA4, 0, 4])
    conn.transmit.assert_not_called()
    end.assert_not_called()


@pytest.mark.parametrize("body_fails", [False, True])
def test_strict_transaction_releases_with_leave_on_success_or_body_error(monkeypatch, body_fails):
    begin, end = Mock(return_value=0), Mock(return_value=0)
    monkeypatch.setattr(sim, "SCardBeginTransaction", begin)
    monkeypatch.setattr(sim, "SCardEndTransaction", end)
    conn = SimpleNamespace(hcard=123)
    def run():
        with sim._Tx(conn, required=True):
            if body_fails:
                raise ValueError("test APDU failed")
    if body_fails:
        with pytest.raises(ValueError):
            run()
    else:
        run()
    begin.assert_called_once_with(123)
    end.assert_called_once_with(123, sim.SCARD_LEAVE_CARD)


def test_strict_transaction_end_failure_is_not_reported_as_success(monkeypatch):
    monkeypatch.setattr(sim, "SCardBeginTransaction", lambda _handle: 0)
    monkeypatch.setattr(sim, "SCardEndTransaction", lambda *_args: 0x8010000B)
    with pytest.raises(RuntimeError, match="could not be released"):
        with sim._Tx(SimpleNamespace(hcard=123), required=True):
            pass


def test_expected_reader_is_checked_before_connect_or_apdu(monkeypatch):
    reader = Mock()
    reader.__str__ = Mock(return_value="different-reader")
    monkeypatch.setattr(sim, "readers", lambda: [reader])
    result = sim.read_card(0, strict_transaction=True, expected_reader="requested-reader")
    assert result.reader == "different-reader"
    assert result.error == "reader identity changed before card probe"
    reader.createConnection.assert_not_called()


@pytest.mark.parametrize("fail_end", [False, True])
def test_strict_read_connects_with_leave_and_requires_successful_end(monkeypatch, fail_end):
    conn = Mock(hcard=123)
    conn.transmit.return_value = ([], 0x90, 0)
    reader = Mock()
    reader.__str__ = Mock(return_value="requested-reader")
    reader.createConnection.return_value = conn
    monkeypatch.setattr(sim, "readers", lambda: [reader])
    monkeypatch.setattr(sim.usbreader, "port_for_index", lambda _idx: "")
    monkeypatch.setattr(sim, "SCardBeginTransaction", lambda _handle: 0)
    end = Mock(return_value=0x8010000B if fail_end else 0)
    monkeypatch.setattr(sim, "SCardEndTransaction", end)
    monkeypatch.setattr(sim, "_read_binary", lambda *_args: ([0x98] * 10, 0x90, 0))
    monkeypatch.setattr(sim, "_select_adf_usim", lambda _conn: True)
    monkeypatch.setattr(sim, "_pin_tries", lambda _conn: None)
    monkeypatch.setattr(sim, "_read_transparent", lambda *_args: None)
    monkeypatch.setattr(sim, "_read_smsc", lambda _conn: None)
    result = sim.read_card(0, strict_transaction=True, expected_reader="requested-reader")
    conn.connect.assert_called_once_with(disposition=sim.SCARD_LEAVE_CARD)
    conn.disconnect.assert_called_once_with()
    end.assert_called_once_with(123, sim.SCARD_LEAVE_CARD)
    assert result.reader == "requested-reader" and result.present is True
    assert bool(result.error) is fail_end
    assert all(call.args[0][1] != 0x20 for call in conn.transmit.call_args_list)
