"""The proactive-rekey driver's decisions, exercised without the engine's card/crypto stack.

swu_ike imports the SIM reader and crypto libraries that only exist inside the engine image,
so the class cannot be constructed here. The methods under test read and write plain
attributes and call send_data/state_ue_rekey_child, which makes them exact-copy testable by
binding the real functions to a stand-in object: the assertions below run the shipped code,
not a description of it.
"""
import ast
from collections import deque
import hashlib
import hmac as stdlib_hmac
import struct
import sys
import types
import unittest
from pathlib import Path

from cryptography.hazmat.primitives import hashes, hmac
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

SOURCE = Path(__file__).resolve().parent.parent / "engine" / "swu_ike.py"
WANTED = {"_rekey_tick", "_rekey_give_up", "_rekey_select_timeout", "_liveness_tick",
          "_begin_create_child_request", "_accept_create_child_response",
          "_ike_rekey_tick", "_ike_rekey_give_up", "_ike_rekey_select_timeout"}


def _load_methods():
    """Compile just the rekey methods out of swu_ike, with time.monotonic available."""
    tree = ast.parse(SOURCE.read_text(encoding="utf-8"))
    picked = []
    for node in ast.walk(tree):
        if isinstance(node, ast.FunctionDef) and node.name in WANTED:
            picked.append(node)
    module = ast.Module(body=picked, type_ignores=[])
    namespace = {"time": __import__("time"), "swu_log": lambda *_a, **_k: None}
    exec(compile(module, str(SOURCE), "exec"), namespace)  # noqa: S102
    return {name: namespace[name] for name in WANTED}


METHODS = _load_methods()


class FakeTunnel:
    """Just enough state for the rekey driver, plus a record of what it sent."""

    def __init__(self, **overrides):
        self.child_rekey_period = 3000.0
        self.rekey_response_timeout = 10.0
        self.rekey_retransmits = 3
        self.rekey_retry_interval = 300.0
        self.liveness_period = 20.0
        self._child_sa_time = 0.0
        self._rekey_outstanding = False
        self._rekey_failed = False
        self._rekey_sent_at = None
        self._rekey_packet = None
        self._rekey_tries = 0
        self._rekey_retry_at = None
        self.message_id_request = 41
        self._create_child_request_id = None
        self._create_child_response_handled = False
        self.ike_rekey_period = 36000.0
        self.ike_rekey_retry_interval = 3600.0
        self._ike_sa_time = 0.0
        self._ike_rekey_outstanding = False
        self._ike_rekey_failed = False
        self._ike_rekey_sent_at = None
        self._ike_rekey_packet = None
        self._ike_rekey_tries = 0
        self._ike_rekey_retry_at = None
        self.sent = []
        self.rekeys_started = 0
        self.ike_rekeys_started = 0
        self.teardowns = []
        self.__dict__.update(overrides)
        for name, function in METHODS.items():
            setattr(self, name, types.MethodType(function, self))

    def send_data(self, packet):
        self.sent.append(packet)

    def state_ue_rekey_child(self):
        self.rekeys_started += 1
        self.message_id_request += 1
        self._rekey_packet = b"rekey-request"
        self._rekey_tries = 1
        self.send_data(self._rekey_packet)

    def state_ue_create_sa(self):
        self.ike_rekeys_started += 1
        self.message_id_request += 1
        self._ike_rekey_packet = b"ike-rekey-request"
        self._ike_rekey_tries = 1
        self.send_data(self._ike_rekey_packet)

    def _rekey_teardown(self, reason):
        self.teardowns.append(reason)
        self._rekey_outstanding = False
        self._ike_rekey_outstanding = False


class UnansweredRekeyTests(unittest.TestCase):
    """One lost packet must not cost a working tunnel: it took down a line that had been
    carrying IMS for fifty minutes, and the rebuild cost five more minutes of registration."""

    def _fire_due_rekey(self, **overrides):
        tunnel = FakeTunnel(**overrides)
        tunnel._child_sa_time = __import__("time").monotonic() - tunnel.child_rekey_period - 1
        tunnel._rekey_tick()
        return tunnel

    @staticmethod
    def _let_it_time_out(tunnel, rounds=10):
        """Age the in-flight request past its timeout until the driver stops waiting."""
        for _ in range(rounds):
            if not tunnel._rekey_outstanding:
                return
            tunnel._rekey_sent_at -= tunnel.rekey_response_timeout
            tunnel._rekey_tick()

    def test_an_unanswered_request_is_retransmitted_verbatim(self):
        tunnel = self._fire_due_rekey()
        self.assertEqual(tunnel.sent, [b"rekey-request"])
        for expected in (2, 3):
            tunnel._rekey_sent_at -= tunnel.rekey_response_timeout
            tunnel._rekey_tick()
            self.assertEqual(tunnel._rekey_tries, expected)
        # Same bytes every time: a retransmission carries the original message id and nonce.
        self.assertEqual(tunnel.sent, [b"rekey-request"] * 3)
        self.assertEqual(tunnel.teardowns, [])

    def test_all_retransmissions_unanswered_reestablishes_the_ambiguous_ike_sa(self):
        tunnel = self._fire_due_rekey()
        self._let_it_time_out(tunnel)
        self.assertEqual(tunnel.sent, [b"rekey-request"] * 3)
        self.assertEqual(tunnel.teardowns, ["rekey_timeout"])

    def test_an_explicit_rejection_consumes_the_message_id(self):
        tunnel = self._fire_due_rekey()
        self.assertEqual(tunnel.message_id_request, 42)
        # Match the real response handler: it clears outstanding and records the rejection.
        tunnel._rekey_outstanding = False
        tunnel._rekey_failed = True
        tunnel._rekey_tick()
        self.assertEqual(tunnel.message_id_request, 42)
        self.assertEqual(tunnel.teardowns, [])
        self.assertIsNotNone(tunnel._rekey_retry_at)

    def test_an_explicit_rejection_also_keeps_the_tunnel(self):
        # TEMPORARY_FAILURE means "ask again later"; even NO_PROPOSAL_CHOSEN leaves the
        # current SA usable, so neither is a reason to tear one down.
        tunnel = self._fire_due_rekey()
        tunnel._rekey_outstanding = False
        tunnel._rekey_failed = True
        tunnel._rekey_tick()
        self.assertEqual(tunnel.teardowns, [])
        self.assertIsNotNone(tunnel._rekey_retry_at)

    def test_the_retry_is_honoured_before_sa_age(self):
        tunnel = self._fire_due_rekey()
        tunnel._rekey_outstanding = False
        tunnel._rekey_failed = True
        tunnel._rekey_tick()
        started = tunnel.rekeys_started
        tunnel._rekey_tick()                    # still inside the retry interval
        self.assertEqual(tunnel.rekeys_started, started)
        tunnel._rekey_retry_at = __import__("time").monotonic() - 1
        tunnel._rekey_tick()
        self.assertEqual(tunnel.rekeys_started, started + 1)

    def test_select_wakes_for_the_scheduled_retry(self):
        tunnel = FakeTunnel()
        tunnel._rekey_retry_at = __import__("time").monotonic() + 120
        self.assertAlmostEqual(tunnel._rekey_select_timeout(), 120, delta=2)

    def test_dpd_does_not_overtake_an_inflight_rekey_request(self):
        tunnel = FakeTunnel(_rekey_outstanding=True)
        tunnel._liveness_tick()
        self.assertEqual(tunnel.sent, [])

    def test_a_create_child_response_is_consumed_exactly_once(self):
        tunnel = FakeTunnel()
        tunnel.message_id_request = 42
        tunnel._begin_create_child_request()
        self.assertFalse(tunnel._accept_create_child_response(41))
        self.assertTrue(tunnel._accept_create_child_response(42))
        self.assertFalse(tunnel._accept_create_child_response(42))

    def test_rekey_disabled_stays_a_no_op(self):
        tunnel = FakeTunnel(child_rekey_period=0.0, _child_sa_time=None)
        tunnel._rekey_tick()
        self.assertEqual(tunnel.rekeys_started, 0)
        self.assertIsNone(tunnel._rekey_select_timeout())


class IkeRekeyTests(unittest.TestCase):
    """The proactive IKE-SA rekey preempts the ePDG's own rekey clock (EE fires at ~12 h and we
    refuse the responder role, so being second costs a ~1 min teardown). Same driver contract
    as the CHILD rekey: verbatim retransmission, rejection keeps the SA, silence re-establishes."""

    @staticmethod
    def _now():
        return __import__("time").monotonic()

    def _fire_due_ike_rekey(self, **overrides):
        tunnel = FakeTunnel(**overrides)
        tunnel._ike_sa_time = self._now() - tunnel.ike_rekey_period - 1
        tunnel._ike_rekey_tick()
        return tunnel

    def test_a_due_ike_sa_starts_a_ue_initiated_rekey(self):
        tunnel = self._fire_due_ike_rekey()
        self.assertEqual(tunnel.ike_rekeys_started, 1)
        self.assertTrue(tunnel._ike_rekey_outstanding)
        self.assertEqual(tunnel.sent, [b"ike-rekey-request"])

    def test_a_young_ike_sa_is_left_alone(self):
        tunnel = FakeTunnel()
        tunnel._ike_sa_time = self._now()
        tunnel._ike_rekey_tick()
        self.assertEqual(tunnel.ike_rekeys_started, 0)

    def test_an_unanswered_ike_rekey_is_retransmitted_verbatim_then_reestablishes(self):
        tunnel = self._fire_due_ike_rekey()
        for _ in range(10):
            if not tunnel._ike_rekey_outstanding:
                break
            tunnel._ike_rekey_sent_at -= tunnel.rekey_response_timeout
            tunnel._ike_rekey_tick()
        self.assertEqual(tunnel.sent, [b"ike-rekey-request"] * 3)
        self.assertEqual(tunnel.teardowns, ["ike_rekey_timeout"])

    def test_an_explicit_rejection_keeps_the_established_ike_sa(self):
        tunnel = self._fire_due_ike_rekey()
        # Match the real response handler: it clears outstanding and records the rejection.
        tunnel._ike_rekey_outstanding = False
        tunnel._ike_rekey_failed = True
        tunnel._ike_rekey_tick()
        self.assertEqual(tunnel.teardowns, [])
        self.assertIsNotNone(tunnel._ike_rekey_retry_at)
        # And the retry honours ike_rekey_retry_interval, not the (shorter) child interval.
        self.assertGreater(tunnel._ike_rekey_retry_at, self._now() + tunnel.ike_rekey_retry_interval - 5)

    def test_ike_rekey_disabled_stays_a_no_op(self):
        tunnel = FakeTunnel(ike_rekey_period=0.0, _ike_sa_time=None)
        tunnel._ike_rekey_tick()
        self.assertEqual(tunnel.ike_rekeys_started, 0)
        self.assertIsNone(tunnel._ike_rekey_select_timeout())

    def test_the_two_rekey_exchanges_never_overlap(self):
        # An in-flight CHILD rekey defers a due IKE rekey…
        tunnel = FakeTunnel(_rekey_outstanding=True)
        tunnel._ike_sa_time = self._now() - tunnel.ike_rekey_period - 1
        tunnel._ike_rekey_tick()
        self.assertEqual(tunnel.ike_rekeys_started, 0)
        # …and an in-flight IKE rekey defers a due CHILD rekey.
        tunnel = FakeTunnel(_ike_rekey_outstanding=True)
        tunnel._child_sa_time = self._now() - tunnel.child_rekey_period - 1
        tunnel._rekey_tick()
        self.assertEqual(tunnel.rekeys_started, 0)

    def test_dpd_does_not_overtake_an_inflight_ike_rekey_request(self):
        tunnel = FakeTunnel(_ike_rekey_outstanding=True)
        tunnel._liveness_tick()
        self.assertEqual(tunnel.sent, [])

    def test_select_wakes_for_the_inflight_response_timeout(self):
        tunnel = FakeTunnel(_ike_rekey_outstanding=True)
        tunnel._ike_rekey_sent_at = self._now()
        timeout = tunnel._ike_rekey_select_timeout()
        self.assertAlmostEqual(timeout, tunnel.rekey_response_timeout, delta=2)


class WorkerSupervisionTests(unittest.TestCase):
    def test_both_esp_workers_detach_the_inherited_log_pipe(self):
        tree = ast.parse(SOURCE.read_text(encoding="utf-8"))
        functions = {node.name: node for node in ast.walk(tree)
                     if isinstance(node, ast.FunctionDef)}
        self.assertIn('prepare_ipsec_worker(parent_pid, \'encoder\')',
                      ast.unparse(functions["encapsulate_ipsec"]))
        self.assertIn('prepare_ipsec_worker(parent_pid, \'decoder\')',
                      ast.unparse(functions["decapsulate_ipsec"]))

    def test_worker_processes_receive_their_parent_pid(self):
        source = SOURCE.read_text(encoding="utf-8")
        self.assertGreaterEqual(source.count("worker_parent_pid"), 3)
        self.assertIn("PR_SET_PDEATHSIG", source)

    def test_worker_restores_default_signals_and_keeps_bounded_diagnostics(self):
        source = SOURCE.read_text(encoding="utf-8")
        self.assertIn("signal.signal(signal.SIGTERM, signal.SIG_DFL)", source)
        self.assertIn("signal.signal(signal.SIGINT, signal.SIG_DFL)", source)
        self.assertIn('"esp-%s.log" % role', source)
        self.assertIn("WORKER_LOG_MAX_BYTES", source)


def _load_dataplane():
    """Run shipped IPC/ESP functions with real CBC/HMAC, without importing SIM or devices."""
    tree = ast.parse(SOURCE.read_text(encoding="utf-8"))
    wanted = {"encapsulate_ipsec", "decapsulate_ipsec", "encapsulate_esp_packet",
              "decapsulate_esp_packet", "esp_padding", "_send_inner_esp",
              "encode_inter_process_protocol", "decode_inter_process_protocol", "prf_plus",
              "generate_keying_material_child", "generate_keying_material_child_pfs",
              "generate_keying_material_child_responder", "state_epdg_create_sa_response",
              "_await_child_worker_install", "_install_child_workers", "_start_child_delete",
              "_accept_child_delete_response", "_child_delete_tick", "_verify_ike_icv",
              "_rekey_select_timeout", "_rekey_tick", "_ike_rekey_tick", "_liveness_tick",
              "_ike_rekey_select_timeout", "encode_header", "return_flags", "_encode_protected_payload",
              "set_ike_packet_length", "encode_generic_payload_header", "decode_ike",
              "decode_header", "decode_payload", "decode_generic_payload_header",
              "decode_payload_type_sk", "_decrypt_sk_body", "decode_payload_type_d",
              "decode_payload_type_n", "_handle_skf"}
    namespace = {"struct": struct, "sys": sys, "Cipher": Cipher, "algorithms": algorithms,
                 "modes": modes, "hashes": hashes, "hmac": hmac,
                 "compare_digest": stdlib_hmac.compare_digest,
                 "time": types.SimpleNamespace(time=lambda: 100.0, monotonic=lambda: 100.0),
                 "swu_log": lambda *_a, **_k: None, "SWU_MTU_REFRESH": 0}
    for node in tree.body:
        if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
            try:
                value = ast.literal_eval(node.value)
            except (ValueError, TypeError):
                continue
            if isinstance(value, (int, str)):
                namespace[node.targets[0].id] = value
    namespace["SWU_MTU_REFRESH"] = 0
    functions = [node for node in ast.walk(tree)
                 if isinstance(node, ast.FunctionDef) and node.name in wanted]
    exec(compile(ast.Module(body=functions, type_ignores=[]), str(SOURCE), "exec"), namespace)
    return namespace, wanted


class EspWorkerFixture:
    """Deterministic select/pipe schedule; packets use actual production ESP crypto."""

    def __init__(self):
        self.namespace, methods = _load_dataplane()
        for name in methods:
            setattr(self, name, types.MethodType(self.namespace[name], self))
        self.encr = self.namespace["ENCR_AES_CBC"]
        self.integ = self.namespace["AUTH_HMAC_SHA1_96"]
        self.integ_key_truncated_len_bytes = {self.integ: 12}
        self.integ_function = {self.integ: hashes.SHA1()}
        self.old_spi, self.new_spi = bytes.fromhex("10203040"), bytes.fromhex("50607080")
        self.old_encr, self.new_encr = bytes(range(16)), bytes(range(16, 32))
        self.old_auth, self.new_auth = bytes(range(20)), bytes(range(20, 40))
        self.tunnel, self.socket_nat, self.socket_esp = object(), types.SimpleNamespace(), object()
        self.userplane_mode = self.namespace["NAT_TRAVERSAL"]
        self.delivered, self.sent_outer, self.activity = [], [], []
        self._sendto_outer = self.sent_outer.append
        self.return_random_bytes = lambda count: bytes(range(count))
        self._prepare_egress = lambda packet: [packet]
        self._note_esp_activity = lambda _pipe: self.activity.append(True)

    @staticmethod
    def inner_packet(sequence):
        # A small IPv6 payload; it never leaves the in-memory fixture.
        return b"\x60\x00\x00\x00" + bytes([sequence]) * 44

    def frame(self, spi, sequence):
        old = spi == self.old_spi
        return self.encapsulate_esp_packet(
            self.inner_packet(sequence), self.encr, self.old_encr if old else self.new_encr,
            self.integ, self.old_auth if old else self.new_auth, spi, sequence)

    def update(self, *, old, encoder=False, create=False):
        ns = self.namespace
        return self.encode_inter_process_protocol([
            ns["INTER_PROCESS_CREATE_SA"] if create else ns["INTER_PROCESS_UPDATE_SA"], [
                (ns["INTER_PROCESS_IE_ENCR_ALG"], self.encr),
                (ns["INTER_PROCESS_IE_INTEG_ALG"], self.integ),
                (ns["INTER_PROCESS_IE_ENCR_KEY"], self.old_encr if old else self.new_encr),
                (ns["INTER_PROCESS_IE_INTEG_KEY"], self.old_auth if old else self.new_auth),
                (ns["INTER_PROCESS_IE_SPI_RESP" if encoder else "INTER_PROCESS_IE_SPI_INIT"],
                 self.old_spi if old else self.new_spi),
            ]])

    def run(self, schedule, *, encoder=False):
        events = deque([*schedule, ("pipe", bytes([self.namespace["INTER_PROCESS_DELETE_SA"]]))])
        current = {}
        pipe = types.SimpleNamespace(recv=lambda: current["payload"], send=lambda _packet: None)
        self.socket_nat.recvfrom = lambda _size: (current["payload"], ("192.0.2.1", 4500))

        def select(_read, _write, _error, *_timeout):
            kind, current["payload"] = events.popleft()
            return [{"pipe": pipe, "nat": self.socket_nat, "tun": self.tunnel}[kind]], [], []

        self.namespace["select"] = types.SimpleNamespace(select=select)
        self.namespace["os"] = types.SimpleNamespace(
            environ={"SWU_NATT_KEEPALIVE": "0"}, read=lambda *_args: current["payload"],
            write=lambda _fd, payload: self.delivered.append(payload))
        try:
            (self.encapsulate_ipsec if encoder else self.decapsulate_ipsec)([pipe])
        except SystemExit:
            pass
        assert not events, "the real worker did not consume the deterministic schedule"

    def rekey_state(self):
        ns = self.namespace
        self.spi_init_child, self.spi_resp_child = self.old_spi, bytes.fromhex("20304050")
        self.sa_spi_list = [self.new_spi]
        peer_spi = bytes.fromhex("60708090")
        self.decoded_payload = [[ns["SK"], [
            [ns["SA"], [1, ns["ESP"], peer_spi]],
            [ns["NINR"], [b"responder-Nr"]],
            [ns["KE"], [14, b"peer-public-key"]],
        ]]]
        self.negotiated_prf = ns["PRF_HMAC_SHA2_256"]
        self.prf_function = {self.negotiated_prf: hashes.SHA256()}
        self.SK_D, self.nounce, self.dh_shared_key = b"test-SK-d" * 4, b"initiator-Ni", b"shared-DH"
        self.negotiated_encryption_algorithm_child = self.encr
        self.negotiated_encryption_algorithm_key_size_child = 128
        self.negotiated_integrity_algorithm_child = self.integ
        self.integ_key_len_bytes = {self.integ: 20}
        self.print_esp_sa = lambda: None
        self.dh_calculate_shared_key = lambda _peer: None  # DH agreement is outside these fixtures.
        self.create_INFORMATIONAL_delete = lambda _protocol, spi: b"fixed-delete:" + spi
        self.child_rekey_period = 1800.0
        self.message_id_request = 42
        self.rekey_response_timeout, self.rekey_retransmits = 10.0, 3
        self.events, self.sent, self.teardowns = [], [], []
        self.send_data = lambda packet: (self.events.append("delete-sent"), self.sent.append(packet))
        self._rekey_teardown = self.teardowns.append
        self._note_liveness_rx = lambda: None
        self._deferred_decoder_messages = []
        self._child_delete_pending = None
        self._ike_rekey_outstanding = False
        fixture = self

        class Worker:
            def __init__(self, role, spi_ie, spi):
                self.role, self.sent, self.replies = role, [], deque()
                self.receipt = fixture.encode_inter_process_protocol([
                    ns["INTER_PROCESS_SA_INSTALLED"], [(spi_ie, spi)]])
                self.replies.append(self.receipt)

            def send(self, packet):
                fixture.events.append(self.role + "-queued")
                self.sent.append(packet)

            def poll(self, _timeout):
                return bool(self.replies)

            def recv(self):
                value = self.replies.popleft()
                fixture.events.append(self.role + ("-installed" if value == self.receipt else "-other"))
                return value

        self.ike_to_ipsec_decoder = Worker("decoder", ns["INTER_PROCESS_IE_SPI_INIT"], self.new_spi)
        self.ike_to_ipsec_encoder = Worker("encoder", ns["INTER_PROCESS_IE_SPI_RESP"], peer_spi)
        self.old_ike_message_received = False
        self.role = ns["ROLE_INITIATOR"]  # Same fixed role as production set_variables().
        self.ike_spi_initiator, self.ike_spi_responder = b"I" * 8, b"R" * 8
        self.ike_spi_initiator_old, self.ike_spi_responder_old = b"O" * 8, b"P" * 8
        self.SK_EI, self.SK_ER = bytes(range(16)), bytes(range(16, 32))
        self.SK_AI, self.SK_AR = bytes(range(20)), bytes(range(20, 40))
        self.SK_EI_old, self.SK_ER_old = bytes(range(32, 48)), bytes(range(48, 64))
        self.SK_AI_old, self.SK_AR_old = bytes(range(40, 60)), bytes(range(60, 80))
        self.negotiated_encryption_algorithm = self.encr
        self.negotiated_integrity_algorithm = self.integ
        self.ike_decoded_header = {}
        self._frag_buf = {}
        self.decodable_payloads = [ns["SK"], ns["D"], ns["N"]]
        self.decode_payload_type = lambda kind, data: {
            ns["SK"]: self.decode_payload_type_sk, ns["D"]: self.decode_payload_type_d,
            ns["N"]: self.decode_payload_type_n,
        }[kind](data)
        return self

    def authenticated_info(self, message_id, payloads=()):
        ns = self.namespace
        body = b""
        for position, (kind, value) in enumerate(payloads):
            next_payload = payloads[position + 1][0] if position + 1 < len(payloads) else ns["NONE"]
            if kind == ns["D"]:
                content = bytes([value[0], 4]) + struct.pack("!H", value[1]) + b"".join(value[2])
            elif kind == ns["N"]:
                content = bytes([0, 0]) + struct.pack("!H", value[1])
            else:
                raise AssertionError(kind)
            body += self.encode_generic_payload_header(next_payload, 0, content)
        first = payloads[0][0] if payloads else ns["NONE"]
        header = self.encode_header(self.ike_spi_initiator, self.ike_spi_responder,
                                    first, 2, 0, ns["INFORMATIONAL"], (1, 0, 0), message_id)
        packet = self._encode_protected_payload(header + body, ns["SK"], first, body, None)
        self.decode_ike(packet)
        assert self.ike_decoded_ok
        return packet


class ChildSaDataplaneTests(unittest.TestCase):
    def test_actual_cbc_hmac_current_sa_reaches_tun(self):
        fixture = EspWorkerFixture()
        fixture.run([("pipe", fixture.update(old=True, create=True)),
                     ("nat", fixture.frame(fixture.old_spi, 1))])
        self.assertEqual(fixture.delivered, [fixture.inner_packet(1)])

    def test_inflight_old_inbound_sa_remains_usable_after_new_sa_installation(self):
        fixture = EspWorkerFixture()
        fixture.run([
            ("pipe", fixture.update(old=True, create=True)),
            ("nat", fixture.frame(fixture.old_spi, 1)),
            ("pipe", fixture.update(old=False)),
            ("nat", fixture.frame(fixture.old_spi, 2)),
            ("nat", fixture.frame(fixture.new_spi, 3)),
            ("nat", fixture.frame(fixture.old_spi, 4)),
        ])
        self.assertEqual(fixture.delivered, [fixture.inner_packet(n) for n in (1, 2, 3, 4)])

    def test_new_spi_packet_cannot_arrive_before_decoder_install_ack(self):
        fixture = EspWorkerFixture()
        fixture.run([
            ("pipe", fixture.update(old=True, create=True)),
            # This packet is necessarily dropped: only the parent can prevent activation
            # before the decoder has applied its queued update. Unknown SPIs must stay closed.
            ("nat", fixture.frame(fixture.new_spi, 1)),
            ("pipe", fixture.update(old=False)),
            ("nat", fixture.frame(fixture.new_spi, 2)),
        ])
        self.assertEqual(fixture.delivered, [fixture.inner_packet(2)])

    def test_cbc_packet_with_corrupted_icv_is_not_delivered_or_counted_live(self):
        fixture = EspWorkerFixture()
        corrupt = bytearray(fixture.frame(fixture.old_spi, 1))
        corrupt[-1] ^= 1
        fixture.run([("pipe", fixture.update(old=True, create=True)), ("nat", bytes(corrupt))])
        self.assertEqual(fixture.delivered, [])
        self.assertEqual(fixture.activity, [])

    def test_new_outbound_sa_starts_its_own_sequence_space(self):
        fixture = EspWorkerFixture()
        fixture.run([
            ("pipe", fixture.update(old=True, encoder=True, create=True)),
            ("tun", fixture.inner_packet(1)), ("tun", fixture.inner_packet(2)),
            ("pipe", fixture.update(old=False, encoder=True)),
            ("tun", fixture.inner_packet(3)), ("tun", fixture.inner_packet(4)),
        ], encoder=True)
        actual = [(packet[:4], struct.unpack("!I", packet[4:8])[0]) for packet in fixture.sent_outer]
        self.assertEqual(actual, [(fixture.old_spi, 1), (fixture.old_spi, 2),
                                  (fixture.new_spi, 1), (fixture.new_spi, 2)])

    def test_pfs_nonce_order_and_directional_slices_match_independent_prf_vector(self):
        fixture = EspWorkerFixture()
        ns = fixture.namespace
        fixture.negotiated_prf = ns["PRF_HMAC_SHA2_256"]
        fixture.prf_function = {fixture.negotiated_prf: hashes.SHA256()}
        fixture.SK_D = b"test-SK-d" * 4
        fixture.dh_shared_key = b"test-DH-shared-secret"
        fixture.nounce, fixture.nounce_received = b"initiator-Ni", b"responder-Nr"
        fixture.negotiated_encryption_algorithm_child = fixture.encr
        fixture.negotiated_encryption_algorithm_key_size_child = 128
        fixture.negotiated_integrity_algorithm_child = fixture.integ
        fixture.integ_key_len_bytes = {fixture.integ: 20}
        fixture.print_esp_sa = lambda: None

        def independent_keymat(seed):
            value, previous = b"", b""
            for counter in range(1, 4):
                previous = stdlib_hmac.new(fixture.SK_D, previous + seed + bytes([counter]),
                                           hashlib.sha256).digest()
                value += previous
            return value[:72]

        expected = independent_keymat(fixture.dh_shared_key + fixture.nounce + fixture.nounce_received)
        fixture.generate_keying_material_child_pfs()
        self.assertEqual((fixture.SK_IPSEC_EI, fixture.SK_IPSEC_AI, fixture.SK_IPSEC_ER, fixture.SK_IPSEC_AR),
                         (expected[:16], expected[16:36], expected[36:52], expected[52:72]))
        fixture.nounce, fixture.nounce_received = fixture.nounce_received, fixture.nounce
        fixture.generate_keying_material_child_responder(True)
        self.assertEqual((fixture.SK_IPSEC_EI, fixture.SK_IPSEC_AI, fixture.SK_IPSEC_ER, fixture.SK_IPSEC_AR),
                         (expected[:16], expected[16:36], expected[36:52], expected[52:72]))

    def test_old_sa_grace_starts_on_delete_receipt_and_expires_without_extending(self):
        fixture = EspWorkerFixture()
        ns = fixture.namespace
        clock = [100.0]
        ns["time"].monotonic = lambda: clock[0]
        retire = fixture.encode_inter_process_protocol([
            ns["INTER_PROCESS_RETIRE_SA"], [(ns["INTER_PROCESS_IE_SPI_INIT"], fixture.old_spi)]])
        initial_decode = fixture.decode_inter_process_protocol
        def decode(packet):
            value = initial_decode(packet)
            if value[0] == ns["INTER_PROCESS_RETIRE_SA"]:
                clock[0] += 4.0
            return value
        fixture.decode_inter_process_protocol = decode
        fixture.run([
            ("pipe", fixture.update(old=True, create=True)),
            ("pipe", fixture.update(old=False)),
            ("nat", fixture.frame(fixture.old_spi, 1)),
            ("pipe", retire),  # t=104; old-SA retirement deadline=109
            ("nat", fixture.frame(fixture.old_spi, 2)),
            ("pipe", retire),  # t=108; duplicate must not move deadline to113
            ("nat", fixture.frame(fixture.old_spi, 3)),
            ("pipe", retire),  # t=112; next loop discards old SPI
            ("nat", fixture.frame(fixture.old_spi, 4)),
            ("nat", fixture.frame(fixture.new_spi, 5)),
        ])
        self.assertEqual(fixture.delivered, [fixture.inner_packet(n) for n in (1, 2, 3, 5)])

    def test_gcm_bad_tag_is_dropped_and_decoder_survives_for_next_packet(self):
        from Crypto.Cipher import AES
        for algorithm in ("ENCR_AES_GCM_8", "ENCR_AES_GCM_12", "ENCR_AES_GCM_16"):
            with self.subTest(algorithm=algorithm):
                fixture = EspWorkerFixture()
                fixture.namespace["AES"] = AES
                fixture.encr = fixture.namespace[algorithm]
                fixture.integ = fixture.namespace["NONE"]
                fixture.integ_key_truncated_len_bytes[fixture.integ] = 0
                fixture.old_auth = b""
                fixture.old_encr = bytes(range(20))
                corrupted = bytearray(fixture.frame(fixture.old_spi, 1)); corrupted[-1] ^= 1
                fixture.run([("pipe", fixture.update(old=True, create=True)), ("nat", bytes(corrupted)),
                             ("nat", fixture.frame(fixture.old_spi, 2))])
                self.assertEqual(fixture.delivered, [fixture.inner_packet(2)])

    def test_wrong_key_under_new_spi_never_falls_back_to_old_sa(self):
        fixture = EspWorkerFixture()
        wrong_context = fixture.encapsulate_esp_packet(
            fixture.inner_packet(1), fixture.encr, fixture.old_encr,
            fixture.integ, fixture.old_auth, fixture.new_spi, 1)
        fixture.run([("pipe", fixture.update(old=True, create=True)),
                     ("pipe", fixture.update(old=False)), ("nat", wrong_context),
                     ("nat", fixture.frame(fixture.new_spi, 2))])
        self.assertEqual(fixture.delivered, [fixture.inner_packet(2)])


class ChildSaHandoffTests(unittest.TestCase):
    def test_broken_worker_pipe_uses_supervised_failure_not_false_completion(self):
        def broken(_packet):
            raise BrokenPipeError("fixture worker exited")
        for phase in ("decoder", "encoder", "retire"):
            with self.subTest(phase=phase):
                fixture = EspWorkerFixture().rekey_state()
                if phase == "retire":
                    fixture.state_epdg_create_sa_response()
                    fixture.authenticated_info(43)
                    fixture.ike_to_ipsec_decoder.send = broken
                    self.assertFalse(fixture._accept_child_delete_response(43, []))
                    self.assertEqual(fixture.teardowns, ["child_decoder_retire_failed"])
                    self.assertIsNotNone(fixture._child_delete_pending)
                else:
                    getattr(fixture, "ike_to_ipsec_" + phase).send = broken
                    fixture.state_epdg_create_sa_response()
                    self.assertEqual(fixture.teardowns, ["child_worker_install_failed"])
                    self.assertEqual(fixture.sent, [])

    def test_decoder_install_receipt_precedes_encoder_and_old_delete(self):
        fixture = EspWorkerFixture().rekey_state()
        fixture.state_epdg_create_sa_response()
        self.assertEqual(fixture.events[:5], ["decoder-queued", "decoder-installed",
                                             "encoder-queued", "encoder-installed", "delete-sent"])
        self.assertEqual(fixture._child_delete_pending["message_id"], 43)

    def test_stalled_decoder_cannot_activate_encoder_or_delete_old_sa(self):
        fixture = EspWorkerFixture().rekey_state()
        fixture.ike_to_ipsec_decoder.replies.clear()
        fixture.state_epdg_create_sa_response()
        self.assertEqual(fixture.events, ["decoder-queued"])
        self.assertEqual(fixture.teardowns, ["child_decoder_install_timeout"])
        self.assertEqual(fixture.sent, [])

    def test_unrelated_ike_is_deferred_without_being_lost_or_reentered(self):
        fixture = EspWorkerFixture().rekey_state(); ns = fixture.namespace
        forwarded = fixture.encode_inter_process_protocol([
            ns["INTER_PROCESS_IKE"], [(ns["INTER_PROCESS_IE_IKE_MESSAGE"], b"\0\0\0\0ike-frame")]])
        fixture.ike_to_ipsec_decoder.replies.appendleft(forwarded)
        fixture.state_epdg_create_sa_response()
        self.assertEqual(fixture._deferred_decoder_messages, [forwarded])
        self.assertLess(fixture.events.index("decoder-installed"), fixture.events.index("encoder-queued"))

    def test_lost_delete_and_lost_ack_retransmit_identical_request_without_new_mid(self):
        fixture = EspWorkerFixture().rekey_state()
        fixture.state_epdg_create_sa_response()
        first = fixture.sent[0]
        for _ in range(2):
            fixture._child_delete_pending["sent_at"] -= fixture.rekey_response_timeout
            fixture._rekey_tick()
        self.assertEqual(fixture.sent, [first, first, first])
        self.assertEqual(fixture.message_id_request, 43)
        fixture._child_delete_pending["sent_at"] -= fixture.rekey_response_timeout
        fixture._rekey_tick()
        self.assertEqual(fixture.teardowns, ["child_delete_timeout"])

    def test_pending_delete_owns_window_and_the_timer_even_if_rekey_disabled(self):
        fixture = EspWorkerFixture().rekey_state()
        fixture.state_epdg_create_sa_response()
        fixture.liveness_period = 20.0
        fixture.child_rekey_period = 0
        fixture._liveness_tick(); fixture._ike_rekey_tick()
        self.assertEqual(len(fixture.sent), 1)
        self.assertEqual(fixture._rekey_select_timeout(), 10.0)
        self.assertIsNone(fixture._ike_rekey_select_timeout())

    def test_authenticated_exact_delete_reply_retires_once_and_empty_reply_is_legal(self):
        for empty in (False, True):
            with self.subTest(empty=empty):
                fixture = EspWorkerFixture().rekey_state(); ns = fixture.namespace
                fixture.state_epdg_create_sa_response()
                payloads = [] if empty else [[ns["D"], [ns["ESP"], 1, [fixture.spi_resp_child_old]]]]
                fixture.authenticated_info(43, payloads)
                self.assertTrue(fixture._accept_child_delete_response(43, fixture.decoded_payload[0][1]))
                self.assertFalse(fixture._accept_child_delete_response(43, fixture.decoded_payload[0][1]))
                retired = fixture.decode_inter_process_protocol(fixture.ike_to_ipsec_decoder.sent[-1])
                self.assertEqual(retired, [ns["INTER_PROCESS_RETIRE_SA"],
                                          [(ns["INTER_PROCESS_IE_SPI_INIT"], fixture.old_spi)]])
                self.assertIsNone(fixture._child_delete_pending)

    def test_wrong_mid_spi_and_error_notify_cannot_retire_old_sa(self):
        for case in ("mid", "spi", "notify"):
            with self.subTest(case=case):
                fixture = EspWorkerFixture().rekey_state(); ns = fixture.namespace
                fixture.state_epdg_create_sa_response()
                mid = 42 if case == "mid" else 43
                payloads = ([[ns["D"], [ns["ESP"], 1, [b"bad!"]]]] if case == "spi" else
                            [[ns["N"], [0, ns["INVALID_SPI"], b"", b""]]] if case == "notify" else [])
                fixture.authenticated_info(mid, payloads)
                self.assertFalse(fixture._accept_child_delete_response(mid, fixture.decoded_payload[0][1]))
                self.assertIsNotNone(fixture._child_delete_pending)
                self.assertEqual(len(fixture.ike_to_ipsec_decoder.sent), 1)

    def test_modified_ike_mid_or_icv_never_becomes_an_authenticated_delete_receipt(self):
        for location in (23, -1):
            with self.subTest(location=location):
                fixture = EspWorkerFixture().rekey_state()
                fixture.state_epdg_create_sa_response()
                packet = bytearray(fixture.authenticated_info(43))
                packet[location] ^= 1
                fixture.decode_ike(bytes(packet))
                self.assertFalse(fixture.ike_decoded_ok)
                self.assertFalse(fixture._accept_child_delete_response(43, []))
                self.assertIsNotNone(fixture._child_delete_pending)

    @staticmethod
    def _fragmented_delete(fixture, *, old=False, sender_role=0):
        ns = fixture.namespace
        body = fixture.encode_generic_payload_header(
            ns["NONE"], 0, bytes([ns["ESP"], 4]) + struct.pack("!H", 1) + fixture.spi_resp_child_old)
        parts = [body[:6], body[6:]]
        header = fixture.encode_header(
            fixture.ike_spi_initiator_old if old else fixture.ike_spi_initiator,
            fixture.ike_spi_responder_old if old else fixture.ike_spi_responder,
            ns["D"], 2, 0, ns["INFORMATIONAL"], (1, 0, sender_role), 43)
        fixture.old_ike_message_received = old
        packets = [fixture._encode_protected_payload(
            header, ns["SKF"], ns["D"] if number == 1 else ns["NONE"], part,
            struct.pack("!HH", number, 2)) for number, part in enumerate(parts, 1)]
        fixture.old_ike_message_received = False
        return packets

    def test_local_role_sk_and_skf_reflections_are_not_peer_authentication(self):
        for kind in ("SK", "SKF"):
            for old in (False, True):
                with self.subTest(kind=kind, old_ike=old):
                    fixture = EspWorkerFixture().rekey_state(); ns = fixture.namespace
                    fixture.state_epdg_create_sa_response()
                    if kind == "SKF":
                        packets = self._fragmented_delete(fixture, old=old, sender_role=fixture.role)
                    else:
                        header = fixture.encode_header(
                            fixture.ike_spi_initiator_old if old else fixture.ike_spi_initiator,
                            fixture.ike_spi_responder_old if old else fixture.ike_spi_responder,
                            ns["NONE"], 2, 0, ns["INFORMATIONAL"], (1, 0, fixture.role), 43)
                        fixture.old_ike_message_received = old
                        packets = [fixture._encode_protected_payload(header, ns["SK"], ns["NONE"], b"")]
                        fixture.old_ike_message_received = False
                    for packet in packets:
                        fixture.decode_ike(packet)
                    accepted = fixture._accept_child_delete_response(
                        43, fixture.decoded_payload[0][1] if fixture.ike_decoded_ok else [])
                    self.assertEqual((fixture.ike_decoded_ok, accepted), (False, False))
                    self.assertIsNotNone(fixture._child_delete_pending)
                    self.assertEqual(fixture._frag_buf, {})

    def test_skf_requires_all_authenticated_fragments_before_delete_ack(self):
        fixture = EspWorkerFixture().rekey_state()
        fixture.state_epdg_create_sa_response()
        first, second = self._fragmented_delete(fixture)
        bad = bytearray(first); bad[-1] ^= 1
        fixture.decode_ike(bytes(bad))
        self.assertFalse(fixture.ike_decoded_ok)
        self.assertEqual(fixture._frag_buf, {})
        fixture.decode_ike(second)
        self.assertFalse(fixture.ike_decoded_ok)
        self.assertFalse(fixture._accept_child_delete_response(43, []),
                         "a valid final fragment alone is not a complete authenticated reply")
        fixture.decode_ike(first)
        self.assertTrue(fixture.ike_decoded_ok)
        self.assertTrue(fixture._accept_child_delete_response(43, fixture.decoded_payload[0][1]))

    def test_old_and_current_ike_fragments_with_same_mid_are_never_mixed(self):
        fixture = EspWorkerFixture().rekey_state()
        fixture.state_epdg_create_sa_response()
        old_first, old_second = self._fragmented_delete(fixture, old=True)
        new_first, new_second = self._fragmented_delete(fixture)
        fixture.decode_ike(old_first)
        self.assertFalse(fixture.ike_decoded_ok)
        fixture.decode_ike(new_second)
        self.assertFalse(fixture.ike_decoded_ok)
        self.assertEqual(len(fixture._frag_buf), 2)
        fixture.decode_ike(old_second)
        self.assertTrue(fixture.ike_decoded_ok, "valid old IKE fragments remain compatible")
        self.assertFalse(fixture._accept_child_delete_response(43, fixture.decoded_payload[0][1]))
        fixture.decode_ike(new_first)
        self.assertTrue(fixture.ike_decoded_ok)
        self.assertTrue(fixture._accept_child_delete_response(43, fixture.decoded_payload[0][1]))

if __name__ == "__main__":
    unittest.main()
