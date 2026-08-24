import unittest
import threading
import time
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

from control.app.ami import AmiClient
from control.app import status


class RegistrationStatusTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.inst = {"id": "1", "enabled": True, "mcc": "310", "mnc": "240",
                     "iccid": "8901000000000000001"}
        status._registration_success_fences.clear()
        self.base = patch.multiple(
            status.engine,
            is_running=lambda _iid: True,
            read_run_json=lambda _iid, _name: {"state": "PIN_DISABLED"},
            read_registration_evidence=lambda *_args: None,
            write_registration_evidence=lambda _iid, _value: True,
            tunnel_installed=lambda _iid: True,
            read_pcscf=lambda _iid: "present",
        )

    async def test_ami_registered_avoids_docker_cli(self):
        ami = SimpleNamespace(registration_state=AsyncMock(return_value="Registered"))
        with self.base, patch.object(status, "resolve_epdg", return_value=True), \
                patch.object(status.engine, "registration_state", return_value="unknown") as cli:
            result = await status.compute(self.inst, ami)
        self.assertEqual(result["state"], "OK")
        ami.registration_state.assert_awaited_once()
        cli.assert_not_called()

    async def test_cli_remains_fallback_while_ami_is_starting(self):
        ami = SimpleNamespace(registration_state=AsyncMock(return_value="unknown"))
        with self.base, patch.object(status, "resolve_epdg", return_value=True), \
                patch.object(status.engine, "registration_state", return_value="Registered"):
            result = await status.compute(self.inst, ami)
        self.assertEqual(result["state"], "OK")
        ami.registration_state.assert_awaited_once()

    async def test_cli_remains_fallback_when_ami_registration_query_fails(self):
        ami = SimpleNamespace(
            registration_state=AsyncMock(side_effect=RuntimeError("AMI failed")))
        with self.base, patch.object(status, "resolve_epdg", return_value=True), \
                patch.object(status.engine, "registration_state",
                             return_value="Registered") as cli:
            result = await status.compute(self.inst, ami)
        self.assertEqual(result["state"], "OK")
        ami.registration_state.assert_awaited_once()
        cli.assert_called_once_with("1")

    async def test_silence_and_refusal_get_different_labels(self):
        # Asterisk says "Rejected" for both, but "no answer" (a stale ESP session the
        # carrier aged out) and "refused" (a real SIP 4xx) need different fixes.
        silence = "WARNING: No response received from 'sip:x' on REGISTER attempt"
        refusal = "WARNING: Fatal response '403' received from 'sip:x' on register attempt"
        for tail, expected in ((silence, "reg_unanswered"), (refusal, "reg_rejected")):
            with self.subTest(expected=expected), self.base, \
                    patch.object(status.engine, "logs", lambda _iid, _tail=200, t=tail: t), \
                    patch.object(status.engine, "registration_state", return_value="Rejected"):
                result = await status.compute(self.inst, None)
            self.assertEqual(result["reason_code"], expected)

    async def test_explicit_sip_rejection_keeps_the_server_status_code(self):
        refusal = "WARNING: Fatal response '403' received on registration attempt"
        with self.base, patch.object(status.engine, "logs", return_value=refusal), \
                patch.object(status.engine, "registration_state", return_value="Rejected"):
            result = await status.compute(self.inst, None)
        self.assertEqual(result["reason_code"], "reg_rejected")
        self.assertEqual(result["detail"]["sip_status"], 403)

    async def test_temporal_registration_response_preserves_asterisk_retry(self):
        temporary = (
            "WARNING: Temporal response '503' received from 'sip:x' on registration "
            "attempt to 'sip:y', retrying in '300'")
        with self.base, patch.object(status.engine, "logs", return_value=temporary), \
                patch.object(status.engine, "registration_state", return_value="Rejected"):
            result = await status.compute(self.inst, None)
        self.assertEqual(result["state"], "REGISTERING")
        self.assertEqual(result["reason_code"], "reg_temporary")
        self.assertEqual(result["detail"]["registration"], "Rejected")
        self.assertEqual(result["detail"]["sip_status"], 503)
        self.assertEqual(result["detail"]["retry_after_seconds"], 300)

    def test_temporal_evidence_requires_one_complete_registration_line(self):
        self.assertEqual(status.registration_failure_evidence(
            "Temporal response '503' received on registration attempt"),
            {"kind": "unknown"})
        self.assertEqual(status.registration_failure_evidence(
            "Temporal response '503' received on message attempt, retrying in '300'"),
            {"kind": "unknown"})
        evidence = status.registration_failure_evidence(
            "Temporal response '503' on REGISTER attempt, retrying in '999999'")
        self.assertEqual(evidence, {"kind": "temporary", "sip_status": 503,
                                    "retry_after_seconds": 86400})

    def test_latest_registration_failure_marker_decides_temporary_or_fatal(self):
        fatal = "Fatal response '403' received on registration attempt"
        temporary = ("Temporal response '503' received on registration attempt, "
                     "retrying in '300'")
        self.assertEqual(status.registration_failure_evidence(
            f"{fatal}\n{temporary}")["kind"], "temporary")
        self.assertEqual(status.registration_failure_evidence(
            f"{temporary}\n{fatal}")["kind"], "rejected")
        unanswered = "No response received on registration attempt"
        self.assertEqual(status.registration_failure_evidence(
            f"{temporary}\n{unanswered}")["kind"], "unanswered")
        self.assertEqual(status.registration_failure_evidence(
            f"{unanswered}\n{temporary}")["kind"], "temporary")

    def test_pinned_asterisk_fatal_retry_wording_is_the_newest_event(self):
        temporary = ("Temporal response '503' received on registration attempt, "
                     "retrying in '300'")
        fatal = ("'403' fatal response received from 'sip:x', "
                 "retrying in '3600' seconds")
        self.assertEqual(status.registration_failure_evidence(
            f"{temporary}\n{fatal}"), {"kind": "rejected", "sip_status": 403})

    async def test_real_registration_attempt_wording_is_unanswered(self):
        real = ("schedule_retry: No response received from 'sip:x' on registration "
                "attempt to 'sip:y', retrying in '30'")
        self.assertTrue(status.registration_unanswered(real))

    async def test_log_read_error_is_not_fast_recovery_evidence(self):
        self.assertFalse(status.registration_unanswered("error: Docker API timed out"))

    async def test_unanswered_registration_records_active_channel_count(self):
        ami = SimpleNamespace(
            registration_state=AsyncMock(return_value="Rejected"),
            active_channel_count=AsyncMock(return_value=0),
        )
        real = "No response received from 'sip:x' on registration attempt"
        with self.base, patch.object(status.engine, "logs", return_value=real):
            result = await status.compute(self.inst, ami)
        self.assertEqual(result["reason_code"], "reg_unanswered")
        self.assertEqual(result["detail"]["active_channels"], 0)
        ami.active_channel_count.assert_awaited_once()

    async def test_unanswered_registration_falls_back_to_bounded_cli_channel_count(self):
        ami = SimpleNamespace(
            registration_state=AsyncMock(return_value="Rejected"),
            active_channel_count=AsyncMock(return_value=None),
        )
        real = "No response received from 'sip:x' on registration attempt"
        with self.base, patch.object(status.engine, "logs", return_value=real), \
                patch.object(status.engine, "active_channel_count", return_value=1) as cli:
            result = await status.compute(self.inst, ami)
        self.assertEqual(result["reason_code"], "reg_unanswered")
        self.assertEqual(result["detail"]["active_channels"], 1)
        cli.assert_called_once_with("1")

    async def test_unanswered_registration_keeps_unknown_when_both_channel_views_fail(self):
        ami = SimpleNamespace(
            registration_state=AsyncMock(return_value="Rejected"),
            active_channel_count=AsyncMock(side_effect=RuntimeError("AMI failed")),
        )
        real = "No response received from 'sip:x' on registration attempt"
        with self.base, patch.object(status.engine, "logs", return_value=real), \
                patch.object(status.engine, "active_channel_count", return_value=None):
            result = await status.compute(self.inst, ami)
        self.assertEqual(result["reason_code"], "reg_unanswered")
        self.assertIsNone(result["detail"]["active_channels"])

    async def test_the_newest_marker_wins_when_the_log_holds_both(self):
        log = ("WARNING: Fatal response '403' received from 'sip:x' on register attempt\n"
               "WARNING: No response received from 'sip:x' on REGISTER attempt")
        self.assertTrue(status.registration_unanswered(log))
        self.assertFalse(status.registration_unanswered("\n".join(log.splitlines()[::-1])))

    async def test_machine_readable_child_rekey_timeout_beats_generic_log_text(self):
        with patch.multiple(
                status.engine,
                charon_log=lambda _iid, _tail=400: "timeout",
                usim_status=lambda _iid: {},
                read_run_json=lambda _iid, _name: {
                    "state": "DOWN", "reason_code": "rekey_timeout"}):
            code, reason = status.classify_ike("1")
        self.assertEqual(code, "tunnel_child_rekey_timeout")
        self.assertIn("CHILD_SA", reason)

    async def test_a_resolver_blip_does_not_mark_an_established_tunnel_down(self):
        # An established tunnel talks to an address, not a name. A DNS outage must surface
        # only on lines that actually need a lookup — those still to build their tunnel.
        with self.base, patch.object(status, "resolve_epdg", return_value=False) as resolver, \
                patch.object(status.engine, "registration_state", return_value="Registered"):
            result = await status.compute(self.inst, None)
        self.assertEqual(result["state"], "OK")
        resolver.assert_not_called()

    async def test_dns_failure_still_surfaces_while_the_tunnel_is_down(self):
        with self.base, patch.object(status.engine, "tunnel_installed", lambda _iid: False), \
                patch.object(status, "resolve_epdg", return_value=False):
            result = await status.compute(self.inst, None)
        self.assertEqual(result["state"], "EPDG_UNRESOLVED")

    async def test_missing_pin_observation_during_rebuild_is_not_no_card(self):
        with patch.multiple(
                status.engine,
                is_running=lambda _iid: True,
                read_run_json=lambda _iid, _name: None):
            result = await status.compute(self.inst, None)

        self.assertEqual(result["state"], "REGISTERING")
        self.assertEqual(result["reason_code"], "registering")
        self.assertEqual(result["detail"]["registration"], "unknown")

    async def test_missing_pin_observation_after_boot_grace_is_a_local_fault(self):
        runtime = {"running": True, "container_id": "generation-1",
                   "started_at_epoch": status.time.time() - 121}
        with patch.multiple(
                status.engine,
                is_running=lambda _iid: True,
                read_run_json=lambda _iid, _name: None):
            result = await status.compute(self.inst, None, runtime)
        self.assertEqual(result["reason_code"], "local_bootstrap_unready")

    async def test_old_unregistered_and_unreadable_local_registration_are_distinct(self):
        runtime = {"running": True, "container_id": "generation-1",
                   "started_at_epoch": status.time.time() - 301}
        for registration, expected in (
                ("Unregistered", "local_registration_stalled"),
                ("unknown", "local_registration_unreadable")):
            with self.subTest(registration=registration), self.base, \
                    patch.object(status.engine, "registration_state",
                                 return_value=registration):
                result = await status.compute(self.inst, None, runtime)
            self.assertEqual(result["reason_code"], expected)

    async def test_same_generation_unanswered_evidence_survives_log_rollover(self):
        runtime = {"running": True, "container_id": "generation-1",
                   "started_at_epoch": status.time.time() - 600}
        saved = {}

        def write(_iid, value):
            saved.update(value)
            return True

        def read(_iid, _generation, _incarnation):
            return saved

        with self.base, patch.object(status.engine, "read_registration_evidence", read), \
                patch.object(status.engine, "write_registration_evidence", write), \
                patch.object(status.engine, "registration_state", return_value="Rejected"), \
                patch.object(status.engine, "logs", side_effect=[
                    "[2026-08-22 20:00:00+0800] No response received on registration attempt",
                    "unrelated output"]):
            first = await status.compute(self.inst, None, runtime)
            second = await status.compute(self.inst, None, runtime)
        self.assertEqual(first["reason_code"], "reg_unanswered")
        self.assertEqual(second["reason_code"], "reg_unanswered")
        self.assertEqual(saved["generation"], "generation-1")

    async def test_registered_tombstone_fences_an_older_same_generation_failure(self):
        runtime = {"running": True, "container_id": "generation-1",
                   "started_at_epoch": status.time.time() - 600}
        generation, incarnation, fingerprint = status._registration_evidence_owner(
            self.inst, runtime)
        stale = ("[2026-08-22 20:00:00+0800] "
                 "No response received on registration attempt")
        stale_event = status._registration_failure_event(stale)
        saved = {"version": 1, "generation": generation, "incarnation": incarnation,
                 "sim_fingerprint": fingerprint, "kind": "unanswered",
                 "observed_at": stale_event["event_at"],
                 "event_key": stale_event["event_key"]}
        writes = []

        def write(_iid, value):
            writes.append(dict(value))
            saved.clear()
            saved.update(value)
            return True

        def read(_iid, _generation, _incarnation):
            return saved

        with self.base, patch.object(status.engine, "read_registration_evidence", read), \
                patch.object(status.engine, "write_registration_evidence", write), \
                patch.object(status.engine, "registration_state", return_value="Registered"):
            result = await status.compute(self.inst, None, runtime)
            repeated = await status.compute(self.inst, None, runtime)
        self.assertEqual(result["reason_code"], "ok")
        self.assertEqual(repeated["reason_code"], "ok")
        self.assertEqual(saved["kind"], "registered")
        self.assertEqual(saved["generation"], "generation-1")
        self.assertEqual(len(writes), 1)

        with self.base, patch.object(status.engine, "read_registration_evidence", read), \
                patch.object(status.engine, "write_registration_evidence", write), \
                patch.object(status.engine, "registration_state", return_value="Rejected"), \
                patch.object(status.engine, "logs", return_value=stale):
            rejected = await status.compute(self.inst, None, runtime)
        self.assertEqual(rejected["reason_code"], "reg_rejected")
        self.assertEqual(saved["kind"], "registered")
        self.assertEqual(len(writes), 1)

    def test_same_container_restart_rejects_old_asterisk_incarnation(self):
        old_runtime = {"container_id": "same-container", "started_at_epoch": 1000.0}
        new_runtime = {"container_id": "same-container", "started_at_epoch": 2000.0}
        generation, incarnation, fingerprint = status._registration_evidence_owner(
            self.inst, old_runtime)
        saved = {
            "version": 1, "generation": generation, "incarnation": incarnation,
            "sim_fingerprint": fingerprint, "kind": "unanswered",
            "observed_at": 1001.0, "event_key": "a" * 64,
        }
        with patch.object(status.engine, "read_registration_evidence", return_value=saved):
            self.assertEqual(status._validated_saved_registration_evidence(
                self.inst, old_runtime)["kind"], "unanswered")
            self.assertEqual(status._validated_saved_registration_evidence(
                self.inst, new_runtime), {"kind": "unknown"})

    def test_registered_write_failure_keeps_an_in_memory_success_fence(self):
        runtime = {"container_id": "generation-1", "started_at_epoch": 1000.0}
        generation, incarnation, fingerprint = status._registration_evidence_owner(
            self.inst, runtime)
        saved = {
            "version": 1, "generation": generation, "incarnation": incarnation,
            "sim_fingerprint": fingerprint, "kind": "unanswered",
            "observed_at": 1001.0, "event_key": "b" * 64,
        }
        with patch.object(status.engine, "read_registration_evidence", return_value=saved), \
                patch.object(status.engine, "write_registration_evidence",
                             side_effect=OSError("read only")):
            status._mark_registration_success(self.inst, runtime)
            self.assertEqual(status._validated_saved_registration_evidence(
                self.inst, runtime), {"kind": "unknown"})

    def test_failure_and_registered_updates_are_serialized(self):
        runtime = {"container_id": "generation-1", "started_at_epoch": 1000.0}
        shared = {}
        read_entered = threading.Event()
        release_read = threading.Event()
        first_read = True

        def read(_iid, _generation, _incarnation):
            nonlocal first_read
            if first_read:
                first_read = False
                read_entered.set()
                release_read.wait(2)
            return dict(shared)

        def write(_iid, value):
            shared.clear()
            shared.update(value)
            return True

        event = {
            "kind": "unanswered", "event_at": 1001.0, "event_key": "c" * 64,
        }
        with patch.object(status.engine, "read_registration_evidence", side_effect=read), \
                patch.object(status.engine, "write_registration_evidence", side_effect=write):
            failure = threading.Thread(
                target=status._saved_registration_evidence,
                args=(self.inst, runtime, event))
            success = threading.Thread(
                target=status._mark_registration_success, args=(self.inst, runtime))
            failure.start()
            self.assertTrue(read_entered.wait(1))
            success.start()
            time.sleep(0.02)
            release_read.set()
            failure.join(2)
            success.join(2)
        self.assertFalse(failure.is_alive())
        self.assertFalse(success.is_alive())
        self.assertEqual(shared["kind"], "registered")

    def test_saved_destructive_evidence_requires_strict_schema(self):
        runtime = {"container_id": "generation-1", "started_at_epoch": 1000.0}
        generation, incarnation, fingerprint = status._registration_evidence_owner(
            self.inst, runtime)
        valid = {
            "version": 1, "generation": generation, "incarnation": incarnation,
            "sim_fingerprint": fingerprint, "kind": "unanswered",
            "observed_at": 1001.0, "event_key": "d" * 64,
        }
        for field, invalid in (("version", True), ("observed_at", float("nan")),
                               ("event_key", "short"), ("sim_fingerprint", "")):
            malformed = {**valid, field: invalid}
            with self.subTest(field=field), patch.object(
                    status.engine, "read_registration_evidence", return_value=malformed):
                self.assertEqual(status._validated_saved_registration_evidence(
                    self.inst, runtime), {"kind": "unknown"})

    def test_newer_failure_supersedes_success_fence_and_survives_log_rollover(self):
        runtime = {"container_id": "generation-1", "started_at_epoch": 1000.0}
        shared = {}

        def read(_iid, _generation, _incarnation):
            return dict(shared)

        def write(_iid, value):
            shared.clear()
            shared.update(value)
            return True

        with patch.object(status.time, "time", return_value=2000.0), \
                patch.object(status.engine, "read_registration_evidence", side_effect=read), \
                patch.object(status.engine, "write_registration_evidence", side_effect=write):
            status._mark_registration_success(self.inst, runtime)
        later = {"kind": "unanswered", "event_at": 2001.0, "event_key": "e" * 64}
        with patch.object(status.time, "time", return_value=2002.0), \
                patch.object(status.engine, "read_registration_evidence", side_effect=read), \
                patch.object(status.engine, "write_registration_evidence", side_effect=write):
            self.assertEqual(status._saved_registration_evidence(
                self.inst, runtime, later)["kind"], "unanswered")
            self.assertEqual(status._validated_saved_registration_evidence(
                self.inst, runtime)["kind"], "unanswered")

    async def test_explicit_no_card_observation_remains_no_card(self):
        with patch.multiple(
                status.engine,
                is_running=lambda _iid: True,
                read_run_json=lambda _iid, _name: {"state": "NO_CARD"}):
            result = await status.compute(self.inst, None)

        self.assertEqual(result["state"], "NO_CARD")
        self.assertEqual(result["reason_code"], "no_card")


class AmiRegistrationTests(unittest.IsolatedAsyncioTestCase):
    async def test_registration_uses_bounded_command_without_detailed_action(self):
        client = AmiClient("1", "172.17.0.2", 5038, "user", "secret", "realm")
        client._mgr = object()
        client._connected = True
        client._action = AsyncMock(return_value=[{"Output": "volte_ims Registered"}])

        self.assertEqual(await client.registration_state(), "Registered")
        client._action.assert_awaited_once_with(
            {"Action": "Command", "Command": "pjsip show registrations"}, timeout=3.0)

    async def test_active_channels_uses_bounded_ami_command(self):
        client = AmiClient("1", "172.17.0.2", 5038, "user", "secret", "realm")
        client._mgr = object()
        client._connected = True
        client._action = AsyncMock(return_value=[{"Output": "2 active channels\n1 active call"}])

        self.assertEqual(await client.active_channel_count(), 2)
        client._action.assert_awaited_once_with(
            {"Action": "Command", "Command": "core show channels count"}, timeout=3.0)

    async def test_unreadable_active_channel_count_fails_closed(self):
        client = AmiClient("1", "172.17.0.2", 5038, "user", "secret", "realm")
        client._mgr = object()
        client._connected = True
        client._action = AsyncMock(return_value=[{"Output": "unexpected"}])

        self.assertIsNone(await client.active_channel_count())

    async def test_usim_recovery_accepts_only_exact_terminal_message_pseudo_channel(self):
        client = AmiClient("1", "172.17.0.2", 5038, "user", "secret", "realm")
        client._mgr = object()
        client._connected = True
        complete = {"Event": "CoreShowChannelsComplete", "ListItems": "1"}
        exact = {"Event": "CoreShowChannel", "Channel": "Message/ast_msg_queue",
                 "Context": "volte_ims_msg", "Application": "Hangup",
                 "ChannelStateDesc": "Up"}

        client._action = AsyncMock(return_value=[{"Response": "Success"}, exact, complete])
        self.assertTrue(await client.zero_usim_recovery_call_channels_complete())

        near_matches = (
            {**exact, "Channel": "Message/ast_msg_queue-1"},
            {**exact, "Context": "other"},
            {**exact, "Application": "MessageSend"},
            {**exact, "ChannelStateDesc": "Down"},
            {key: value for key, value in exact.items() if key != "ChannelStateDesc"},
        )
        for near in near_matches:
            with self.subTest(channel=near):
                client._action = AsyncMock(
                    return_value=[{"Response": "Success"}, near, complete])
                self.assertFalse(await client.zero_usim_recovery_call_channels_complete())

    async def test_usim_recovery_call_snapshot_keeps_other_channels_fail_closed(self):
        client = AmiClient("1", "172.17.0.2", 5038, "user", "secret", "realm")
        client._mgr = object()
        client._connected = True
        pseudo = {"Event": "CoreShowChannel", "Channel": "Message/ast_msg_queue",
                  "Context": "volte_ims_msg", "Application": "Hangup",
                  "ChannelStateDesc": "Up"}
        pjsip = {"Event": "CoreShowChannel", "Channel": "PJSIP/volte_ims-00000001",
                 "Context": "from-ims", "Application": "Dial",
                 "ChannelStateDesc": "Up"}

        cases = (
            ([{"Response": "Success"},
              {"Event": "CoreShowChannelsComplete", "ListItems": "0"}], True),
            ([{"Response": "Success"}, pseudo, pjsip,
              {"Event": "CoreShowChannelsComplete", "ListItems": "2"}], False),
            ([{"Response": "Success"}, pseudo, pseudo,
              {"Event": "CoreShowChannelsComplete", "ListItems": "2"}], False),
            ([{"Response": "Success"}, pjsip,
              {"Event": "CoreShowChannelsComplete", "ListItems": "1"}], False),
        )
        for messages, expected in cases:
            with self.subTest(messages=messages):
                client._action = AsyncMock(return_value=messages)
                self.assertIs(
                    await client.zero_usim_recovery_call_channels_complete(), expected)

    async def test_usim_recovery_call_snapshot_rejects_incomplete_or_inconsistent_lists(self):
        client = AmiClient("1", "172.17.0.2", 5038, "user", "secret", "realm")
        client._mgr = object()
        client._connected = True
        pseudo = {"Event": "CoreShowChannel", "Channel": "Message/ast_msg_queue",
                  "Context": "volte_ims_msg", "Application": "Hangup",
                  "ChannelStateDesc": "Up"}
        invalid = (
            [{"Response": "Success"}, pseudo],
            [{"Response": "Success"}, pseudo,
             {"Event": "CoreShowChannelsComplete", "ListItems": "0"}],
            [{"Response": "Success"},
             {"Event": "CoreShowChannelsComplete", "ListItems": "invalid"}],
            [{"Response": "Success"},
             {"Event": "CoreShowChannelsComplete", "ListItems": "0"},
             {"Event": "CoreShowChannelsComplete", "ListItems": "0"}],
            ([{"Response": "Success"}]
             + [{"Event": "CoreShowChannel", "Channel": f"PJSIP/test-{index}"}
                for index in range(513)]
             + [{"Event": "CoreShowChannelsComplete", "ListItems": "513"}]),
        )
        for messages in invalid:
            with self.subTest(length=len(messages)):
                client._action = AsyncMock(return_value=messages)
                self.assertIsNone(
                    await client.zero_usim_recovery_call_channels_complete())

    async def test_exact_echo_channel_rtp_counts_require_both_directions(self):
        client = AmiClient("1", "172.17.0.2", 5038, "user", "secret", "realm")
        client._mgr = object()
        client._connected = True
        client._action = AsyncMock(side_effect=[
            [{"Event": "CoreShowChannel", "Uniqueid": "171.9",
              "Channel": "PJSIP/webrtc-00000001"}],
            [{"Response": "Success",
              "Value": "ssrc=1;rxcount=12;txcount=15;lp=0"}],
        ])

        self.assertEqual(await client.channel_rtp_counts("171.9"), {
            "tx_packets": 15, "rx_packets": 12,
            "channel": "PJSIP/webrtc-00000001",
        })

    async def test_rtp_counts_fail_closed_for_wrong_channel_or_partial_value(self):
        client = AmiClient("1", "172.17.0.2", 5038, "user", "secret", "realm")
        client._mgr = object()
        client._connected = True
        client._action = AsyncMock(return_value=[{
            "Event": "CoreShowChannel", "Uniqueid": "other",
            "Channel": "PJSIP/webrtc-00000001"}])
        self.assertIsNone(await client.channel_rtp_counts("wanted"))

    async def test_disconnect_cleanup_hangs_up_only_matching_browser_token(self):
        client = AmiClient("1", "172.17.0.2", 5038, "user", "secret", "realm")
        client._mgr = object()
        client._connected = True
        token = "token-abcdefghijklmnopqrstuvwxyz123456"
        client._action = AsyncMock(side_effect=[
            [{"Channel": "PJSIP/webrtc-0001"}, {"Channel": "PJSIP/webrtc-0002"}],
            [{"Value": "another-token-abcdefghijklmnopqrstuvwxyz"}],
            [{"Value": token}],
            [{"Response": "Success"}],
        ])
        result = await client.hangup_channels_by_variable("MDD_MEDIA_TOKEN", {token})
        self.assertEqual(result, {"ok": True, "matched": 1})
        self.assertEqual(client._action.await_args_list[-1].args[0], {
            "Action": "Hangup", "Channel": "PJSIP/webrtc-0002", "Cause": "16"})

    async def test_exact_channel_safety_lease_renews_and_hangs_up_without_scanning_vars(self):
        client = AmiClient("1", "172.17.0.2", 5038, "user", "secret", "realm")
        client._mgr = object()
        client._connected = True
        channel = {"Event": "CoreShowChannel", "Uniqueid": "171.9",
                   "Channel": "PJSIP/webrtc-00000001"}
        client._action = AsyncMock(side_effect=[
            [channel], [{"Response": "Success"}],
            [channel], [{"Response": "Success"}],
        ])
        self.assertTrue(await client.renew_channel_absolute_timeout("171.9", 10))
        self.assertTrue(await client.hangup_channel("171.9"))
        self.assertEqual(client._action.await_args_list[1].args[0], {
            "Action": "Setvar", "Channel": "PJSIP/webrtc-00000001",
            "Variable": "TIMEOUT(absolute)", "Value": "10"})
        self.assertEqual(client._action.await_args_list[3].args[0], {
            "Action": "Hangup", "Channel": "PJSIP/webrtc-00000001", "Cause": "16"})


if __name__ == "__main__":
    unittest.main()
