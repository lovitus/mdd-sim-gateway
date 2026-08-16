import sys
import types
import unittest


# Pure decoder tests should remain runnable on development hosts without libpcsclite/pyscard.
try:
    from control.app import sim
except ModuleNotFoundError as exc:
    if not str(exc.name).startswith("smartcard"):
        raise
    smartcard = types.ModuleType("smartcard")
    system = types.ModuleType("smartcard.System"); system.readers = lambda: []
    connection = types.ModuleType("smartcard.CardConnection"); connection.CardConnection = object
    exceptions = types.ModuleType("smartcard.Exceptions")
    exceptions.NoCardException = exceptions.CardConnectionException = RuntimeError
    scard = types.ModuleType("smartcard.scard")
    scard.SCardBeginTransaction = scard.SCardEndTransaction = lambda *args: None
    scard.SCARD_LEAVE_CARD = 0
    sys.modules.update({"smartcard": smartcard, "smartcard.System": system,
                        "smartcard.CardConnection": connection,
                        "smartcard.Exceptions": exceptions, "smartcard.scard": scard})
    from control.app import sim


class SimCarrierIdentityTests(unittest.TestCase):
    def test_gsm_spn_decoding(self):
        self.assertEqual(sim.decode_alpha_identifier(
            [ord(value) for value in "giffgaff"] + [0xFF]), "giffgaff")

    def test_ucs2_spn_decoding(self):
        payload = [0x80] + list("中国移动".encode("utf-16-be")) + [0xFF, 0xFF]
        self.assertEqual(sim.decode_alpha_identifier(payload), "中国移动")

    def test_optional_transparent_ef_uses_the_fcp_file_size(self):
        class Connection:
            def __init__(self):
                self.commands = []

            def transmit(self, command):
                self.commands.append(command)
                if len(self.commands) == 1:  # SELECT
                    return [], 0x61, 6
                if len(self.commands) == 2:  # GET RESPONSE: FCP says three bytes
                    return [0x62, 0x04, 0x80, 0x02, 0x00, 0x03], 0x90, 0x00
                return [0x35, 0x34, 0x4D], 0x90, 0x00

        connection = Connection()
        self.assertEqual(sim._read_transparent(connection, "6f3e"), [0x35, 0x34, 0x4D])
        self.assertEqual(connection.commands[-1][-1], 3)


if __name__ == "__main__":
    unittest.main()
