#!/usr/bin/env python3
"""No-network fake-incoming E2E for app_mdd_answer_bridged on Asterisk 20.7."""

from __future__ import annotations

import json
import os
import queue
import re
import socket
import sys
import threading
import time
import uuid

sys.path.insert(0, "/e3")
from e2base import AdmissionServer, Ami, MediaServer, wait_for  # noqa: E402


class FakeIncomingSip:
    def __init__(self):
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.bind(("127.0.0.1", 0))
        self.sock.settimeout(0.2)
        self.local_port = self.sock.getsockname()[1]
        self.call_id = uuid.uuid4().hex + "@mdd.invalid"
        self.from_tag = uuid.uuid4().hex[:16]
        self.branch = "z9hG4bK-" + uuid.uuid4().hex
        self.to_value = "<sip:15550000000@127.0.0.1>"
        self.responses = queue.Queue()
        self.stop = threading.Event()
        self.ok_count = 0

    def _request(self, method, cseq_method=None, to_value=None, branch=None):
        cseq_method = cseq_method or method
        branch = branch or self.branch
        body = ""
        if method == "INVITE":
            body = (
                "v=0\r\no=mdd 1 1 IN IP4 127.0.0.1\r\ns=mdd\r\n"
                "c=IN IP4 127.0.0.1\r\nt=0 0\r\n"
                f"m=audio {self.local_port + 1} RTP/AVP 0 101\r\n"
                "a=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\n")
        headers = [
            f"{method} sip:15550000000@127.0.0.1:15061 SIP/2.0",
            f"Via: SIP/2.0/UDP 127.0.0.1:{self.local_port};branch={branch};rport",
            "Max-Forwards: 70",
            f"From: <sip:15551112222@127.0.0.1>;tag={self.from_tag}",
            f"To: {to_value or self.to_value}",
            f"Call-ID: {self.call_id}", f"CSeq: 1 {cseq_method}",
            f"Contact: <sip:mdd@127.0.0.1:{self.local_port}>",
            "Content-Type: application/sdp" if body else "User-Agent: MDD isolated fake caller",
            f"Content-Length: {len(body.encode())}", "", body,
        ]
        return "\r\n".join(headers).encode()

    def start(self):
        def run():
            while not self.stop.is_set():
                try:
                    raw, address = self.sock.recvfrom(65535)
                except socket.timeout:
                    continue
                message = raw.decode("utf-8", errors="replace")
                if message.startswith("SIP/2.0"):
                    match = re.match(r"SIP/2.0\s+(\d{3})", message)
                    if not match:
                        continue
                    status = int(match.group(1))
                    to_match = re.search(r"^To:\s*(.+)\r?$", message, re.I | re.M)
                    if to_match:
                        self.to_value = to_match.group(1).strip()
                    self.responses.put((status, message))
                    if status == 200:
                        self.ok_count += 1
                        self.sock.sendto(self._request(
                            "ACK", cseq_method="ACK", to_value=self.to_value,
                            branch="z9hG4bK-" + uuid.uuid4().hex), address)
                    continue
                if message.startswith("BYE ") or message.startswith("CANCEL "):
                    response = self._response(message, "200 OK")
                    self.sock.sendto(response, address)

        self.thread = threading.Thread(target=run, daemon=True)
        self.thread.start()
        self.sock.sendto(self._request("INVITE"), ("127.0.0.1", 15061))

    @staticmethod
    def _response(request, status):
        def header(name):
            match = re.search(rf"^{name}:\s*(.+)\r?$", request, re.I | re.M)
            return match.group(1).strip()
        values = [
            f"SIP/2.0 {status}", f"Via: {header('Via')}", f"From: {header('From')}",
            f"To: {header('To')}", f"Call-ID: {header('Call-ID')}",
            f"CSeq: {header('CSeq')}", "Content-Length: 0", "", "",
        ]
        return "\r\n".join(values).encode()

    def wait_status(self, wanted, timeout=5):
        deadline = time.monotonic() + timeout
        held = []
        try:
            while time.monotonic() < deadline:
                status, message = self.responses.get(timeout=deadline - time.monotonic())
                if status == wanted:
                    return message
                held.append((status, message))
        finally:
            for value in held:
                self.responses.put(value)
        raise AssertionError(f"SIP status {wanted} not received")

    def close(self):
        self.stop.set()
        self.thread.join(1)
        self.sock.close()


def response_value(response, key):
    return next((item.get(key) for item in response if item.get(key) is not None), "")


def exact_channel(ami, uniqueid):
    return next((item for item in ami.channels() if item.get("Uniqueid") == uniqueid), None)


def getvar(ami, channel, variable):
    return response_value(ami.action({
        "Action": "Getvar", "Channel": channel, "Variable": variable}), "Value")


def setvar(ami, channel, variable, value):
    ami.success({"Action": "Setvar", "Channel": channel,
                 "Variable": variable, "Value": value})
    observed = getvar(ami, channel, variable)
    if variable == "TIMEOUT(absolute)":
        try:
            remaining = float(observed)
        except ValueError:
            remaining = 0
        if not 8 <= remaining <= 10:
            raise AssertionError(f"Set/Get mismatch for {variable}: {observed!r}")
        return
    if observed != value:
        raise AssertionError(f"Set/Get mismatch for {variable}")


def bridge_id(ami):
    rows = ami.action({"Action": "BridgeList"}, "BridgeListComplete")
    bridges = [row for row in rows if row.get("Event") == "BridgeListItem"
               and row.get("BridgeNumChannels") == "2"]
    if len(bridges) != 1:
        return ""
    return bridges[0].get("BridgeUniqueid", "")


def expect_error(ami, values):
    response = ami.action(values)[0]
    if response.get("Response") != "Error":
        raise AssertionError("unsafe MddAnswerBridged request was not rejected")


def prepare_bridged_call(ami, media, sid, winner_id, operation, epoch, marker):
    caller = FakeIncomingSip(); caller.start(); caller.wait_status(180)
    ims = wait_for(lambda: next((row for row in ami.channels()
                                if row.get("Context") == "e3-incoming"), None),
                   detail=f"{marker} unanswered IMS")
    ims_channel, ims_id = ims["Channel"], ims["Uniqueid"]
    ami.success({
        "Action": "Originate",
        "Channel": f"WebSocket/mdd_control_media/c(slin)nf(json)v(sid={sid})",
        "Context": "e3-warmup", "Exten": "echo", "Priority": "1",
        "ChannelId": winner_id, "Async": "true",
    })
    media.next().send_pcm_echo(marker)
    winner = wait_for(lambda: exact_channel(ami, winner_id), detail=f"{marker} WSS")
    winner_channel = winner["Channel"]
    for channel, values in {
        ims_channel: {
            "MDD_INBOUND_ARMED": "1", "MDD_INBOUND_OPERATION": operation,
            "MDD_MEDIA_EPOCH": epoch, "MDD_INBOUND_WINNER_ID": winner_id,
            "MDD_INBOUND_ANSWER_RESULT": "", "MDD_INBOUND_ATTACH": "1",
            "MDD_INBOUND_SOURCE_ID": ims_id,
            "MDD_INBOUND_WINNER_CHANNEL": winner_channel,
        },
        winner_channel: {
            "MDD_INBOUND_WINNER": "1", "MDD_INBOUND_OPERATION": operation,
            "MDD_MEDIA_EPOCH": epoch, "MDD_INBOUND_SOURCE_ID": ims_id,
            "TIMEOUT(absolute)": "10",
        },
    }.items():
        for name, value in values.items():
            setvar(ami, channel, name, value)
    ami.success({"Action": "Redirect", "Channel": ims_channel,
                 "Context": "e3-attach", "Exten": "s", "Priority": "1"})
    exact_bridge = wait_for(lambda: bridge_id(ami), detail=f"{marker} bridge")
    if caller.ok_count:
        raise AssertionError(f"{marker} Bridge prematurely answered IMS")
    setvar(ami, winner_channel, "TIMEOUT(absolute)", "10")
    return caller, ims_channel, ims_id, winner_channel, exact_bridge


def recorded_inbound_statuses(path="/logs/events.jsonl"):
    try:
        rows = [json.loads(line) for line in open(path, encoding="utf-8")]
    except (FileNotFoundError, ValueError):
        return []
    return [str(row.get("args", ["", "", ""])[2]).upper()
            for row in rows if row.get("event") == "call_result"
            and len(row.get("args") or []) >= 3 and row["args"][0] == "in"]


def main():
    config = json.load(open("/config/instance.json", encoding="utf-8"))
    try:
        os.unlink("/logs/events.jsonl")
    except FileNotFoundError:
        pass
    admission = AdmissionServer(); admission.start()
    media = MediaServer(); media.start()
    caller = None
    caller2 = None
    extra_callers = []
    ami = Ami(config.get("ami_user", "vowifi"), config["ami_secret"])
    winner_id = "mddcanary-00000000-0000-4000-8000-000000000101"
    operation = "a" * 32
    epoch = "B" * 24
    try:
        unattended = FakeIncomingSip(); unattended.start(); unattended.wait_status(180)
        unattended_ims = wait_for(lambda: next((row for row in ami.channels()
                                                if row.get("Context") == "e3-incoming"), None),
                                  detail="unattached IMS")
        ami.success({"Action": "Hangup", "Channel": unattended_ims["Channel"],
                     "Cause": "16"})
        wait_for(lambda: not ami.channels(), timeout=5, detail="unattached cleanup")
        wait_for(lambda: recorded_inbound_statuses().count("NOANSWER") == 1,
                 detail="one NOANSWER result")
        unattended.close()

        caller = FakeIncomingSip(); caller.start()
        caller.wait_status(180)
        ims = wait_for(lambda: next((row for row in ami.channels()
                                    if row.get("Context") == "e3-incoming"), None),
                       detail="unanswered fake IMS channel")
        ims_channel, ims_id = ims["Channel"], ims["Uniqueid"]
        if ims.get("ChannelStateDesc") == "Up" or caller.ok_count:
            raise AssertionError("incoming IMS was answered before media winner")

        ami.success({
            "Action": "Originate",
            "Channel": "WebSocket/mdd_control_media/c(slin)nf(json)v(sid=e3winner)",
            "Context": "e3-warmup", "Exten": "echo", "Priority": "1",
            "ChannelId": winner_id, "Async": "true",
        })
        winner_media = media.next()
        winner_media.send_pcm_echo(7)
        winner = wait_for(lambda: exact_channel(ami, winner_id), detail="winner WSS channel")
        winner_channel = winner["Channel"]
        variables = {
            ims_channel: {
                "MDD_INBOUND_ARMED": "1", "MDD_INBOUND_OPERATION": operation,
                "MDD_MEDIA_EPOCH": epoch, "MDD_INBOUND_WINNER_ID": winner_id,
                "MDD_INBOUND_ANSWER_RESULT": "", "MDD_INBOUND_ATTACH": "1",
                "MDD_INBOUND_SOURCE_ID": ims_id,
                "MDD_INBOUND_WINNER_CHANNEL": winner_channel,
            },
            winner_channel: {
                "MDD_INBOUND_WINNER": "1", "MDD_INBOUND_OPERATION": operation,
                "MDD_MEDIA_EPOCH": epoch, "MDD_INBOUND_SOURCE_ID": ims_id,
                "TIMEOUT(absolute)": "10",
            },
        }
        for channel, values in variables.items():
            for name, value in values.items():
                setvar(ami, channel, name, value)

        ami.success({"Action": "Redirect", "Channel": ims_channel,
                     "Context": "e3-attach", "Exten": "s", "Priority": "1"})
        exact_bridge = wait_for(lambda: bridge_id(ami), detail="exact two-party bridge")
        if caller.ok_count:
            raise AssertionError("AMI Bridge prematurely answered IMS")
        # Bridge() yanks the winner out of Echo and clears its prior absolute timeout. Control
        # must reinstall/read it after both BridgeEnter events and before the Answer action.
        setvar(ami, winner_channel, "TIMEOUT(absolute)", "10")
        ims_timeout = float(getvar(ami, ims_channel, "TIMEOUT(absolute)") or 0)
        winner_timeout = float(getvar(ami, winner_channel, "TIMEOUT(absolute)") or 0)
        if not (1 <= ims_timeout <= 10 and 1 <= winner_timeout <= 10):
            raise AssertionError(
                f"attach timeouts invalid: ims={ims_timeout} winner={winner_timeout}")

        base_action = {
            "Action": "MddAnswerBridged", "Channel": ims_channel,
            "IMSUniqueid": ims_id, "WinnerChannel": winner_channel,
            "WinnerUniqueid": winner_id, "BridgeUniqueid": exact_bridge,
            "OperationID": operation, "MediaEpoch": epoch,
        }
        expect_error(ami, {**base_action, "MediaEpoch": "Z" * 24})
        expect_error(ami, {**base_action, "UnknownHeader": "forbidden"})
        if caller.ok_count:
            raise AssertionError("invalid answer action reached fake caller")

        ami.success(base_action)
        result = ami.wait_event(
            "MddAnswerBridgedResult",
            lambda item: item.get("OperationID") == operation, timeout=5)
        caller.wait_status(200)
        if result.get("Result") != "answered" or caller.ok_count != 1:
            raise AssertionError("exact bridged answer did not emit one answered result")
        wait_for(lambda: (exact_channel(ami, ims_id) or {}).get("ChannelStateDesc") == "Up",
                 detail="IMS Up after exact answer")
        if getvar(ami, ims_channel, "MDD_INBOUND_ANSWER_RESULT") != "answered" \
                or getvar(ami, ims_channel, "MDD_INBOUND_ARMED") != "0":
            raise AssertionError("answer result/armed linearization did not persist")
        expect_error(ami, base_action)
        time.sleep(0.3)
        if caller.ok_count != 1:
            raise AssertionError("duplicate action emitted a second SIP 200")

        ami.success({"Action": "Hangup", "Channel": winner_channel, "Cause": "16"})
        wait_for(lambda: not ami.channels(), timeout=5, detail="bridged call cleanup")
        wait_for(lambda: recorded_inbound_statuses().count("ANSWER") == 1,
                 detail="one ANSWER result")

        # A second call proves that a winner disappearing after BridgeEnter but before the
        # custom Answer action cannot produce a SIP 200.
        caller.close()
        caller2 = FakeIncomingSip(); caller2.start()
        caller2.wait_status(180)
        ims2 = wait_for(lambda: next((row for row in ami.channels()
                                     if row.get("Context") == "e3-incoming"), None),
                        detail="second unanswered IMS channel")
        ims2_channel, ims2_id = ims2["Channel"], ims2["Uniqueid"]
        winner2_id = "mddcanary-00000000-0000-4000-8000-000000000102"
        operation2 = "c" * 32
        epoch2 = "D" * 24
        ami.success({
            "Action": "Originate",
            "Channel": "WebSocket/mdd_control_media/c(slin)nf(json)v(sid=e3loser)",
            "Context": "e3-warmup", "Exten": "echo", "Priority": "1",
            "ChannelId": winner2_id, "Async": "true",
        })
        media.next().send_pcm_echo(8)
        winner2 = wait_for(lambda: exact_channel(ami, winner2_id), detail="second WSS")
        winner2_channel = winner2["Channel"]
        for channel, values in {
            ims2_channel: {
                "MDD_INBOUND_ARMED": "1", "MDD_INBOUND_OPERATION": operation2,
                "MDD_MEDIA_EPOCH": epoch2, "MDD_INBOUND_WINNER_ID": winner2_id,
                "MDD_INBOUND_ANSWER_RESULT": "", "MDD_INBOUND_ATTACH": "1",
                "MDD_INBOUND_SOURCE_ID": ims2_id,
                "MDD_INBOUND_WINNER_CHANNEL": winner2_channel,
            },
            winner2_channel: {
                "MDD_INBOUND_WINNER": "1", "MDD_INBOUND_OPERATION": operation2,
                "MDD_MEDIA_EPOCH": epoch2, "MDD_INBOUND_SOURCE_ID": ims2_id,
                "TIMEOUT(absolute)": "10",
            },
        }.items():
            for name, value in values.items():
                setvar(ami, channel, name, value)
        ami.success({"Action": "Redirect", "Channel": ims2_channel,
                     "Context": "e3-attach", "Exten": "s", "Priority": "1"})
        bridge2 = wait_for(lambda: bridge_id(ami), detail="second exact bridge")
        if caller2.ok_count:
            raise AssertionError("second Bridge prematurely answered IMS")
        setvar(ami, winner2_channel, "TIMEOUT(absolute)", "10")
        ami.success({"Action": "Hangup", "Channel": winner2_channel, "Cause": "16"})
        wait_for(lambda: exact_channel(ami, winner2_id) is None,
                 detail="winner leaves before answer")
        expect_error(ami, {
            "Action": "MddAnswerBridged", "Channel": ims2_channel,
            "IMSUniqueid": ims2_id, "WinnerChannel": winner2_channel,
            "WinnerUniqueid": winner2_id, "BridgeUniqueid": bridge2,
            "OperationID": operation2, "MediaEpoch": epoch2,
        })
        if caller2.ok_count:
            raise AssertionError("departed winner allowed a SIP 200")
        remaining_ims = exact_channel(ami, ims2_id)
        if remaining_ims:
            ami.success({"Action": "Hangup", "Channel": remaining_ims["Channel"],
                         "Cause": "16"})
        wait_for(lambda: not ami.channels(), timeout=5, detail="second cleanup")
        wait_for(lambda: recorded_inbound_statuses().count("NOANSWER") == 2,
                 detail="winner-left NOANSWER result")

        # Wrong bridge identity is rejected in the bridge-thread callback, consumes the exact
        # owner arm, and cannot later be retried into an answer.
        operation3, epoch3 = "e" * 32, "F" * 24
        winner3_id = "mddcanary-00000000-0000-4000-8000-000000000103"
        caller3, ims3_channel, ims3_id, winner3_channel, bridge3 = prepare_bridged_call(
            ami, media, "e3wrong", winner3_id, operation3, epoch3, 9)
        extra_callers.append(caller3)
        ami.success({
            "Action": "MddAnswerBridged", "Channel": ims3_channel,
            "IMSUniqueid": ims3_id, "WinnerChannel": winner3_channel,
            "WinnerUniqueid": winner3_id, "BridgeUniqueid": "wrong-bridge",
            "OperationID": operation3, "MediaEpoch": epoch3,
        })
        failed = ami.wait_event(
            "MddAnswerBridgedResult",
            lambda item: item.get("OperationID") == operation3, timeout=5)
        if (failed.get("Result") != "failed" or caller3.ok_count
                or getvar(ami, ims3_channel, "MDD_INBOUND_ARMED") != "0"
                or getvar(ami, ims3_channel, "MDD_INBOUND_ANSWER_RESULT") != "failed"):
            raise AssertionError("wrong bridge did not consume the arm fail-closed")
        expect_error(ami, {**{
            "Action": "MddAnswerBridged", "Channel": ims3_channel,
            "IMSUniqueid": ims3_id, "WinnerChannel": winner3_channel,
            "WinnerUniqueid": winner3_id, "BridgeUniqueid": bridge3,
            "OperationID": operation3, "MediaEpoch": epoch3,
        }})
        ami.success({"Action": "Hangup", "Channel": winner3_channel, "Cause": "16"})
        wait_for(lambda: not ami.channels(), timeout=5, detail="wrong bridge cleanup")
        wait_for(lambda: recorded_inbound_statuses().count("NOANSWER") == 3,
                 detail="failed-module NOANSWER result")

        # The admission service flips between the Action check and the queued callback check.
        # The callback must consume the arm as denied and emit no SIP 200.
        operation4, epoch4 = "1" * 32, "2" * 24
        winner4_id = "mddcanary-00000000-0000-4000-8000-000000000104"
        caller4, ims4_channel, ims4_id, winner4_channel, bridge4 = prepare_bridged_call(
            ami, media, "e3deny", winner4_id, operation4, epoch4, 10)
        extra_callers.append(caller4)
        admission.decisions.put(True)
        admission.decisions.put(False)
        ami.success({
            "Action": "MddAnswerBridged", "Channel": ims4_channel,
            "IMSUniqueid": ims4_id, "WinnerChannel": winner4_channel,
            "WinnerUniqueid": winner4_id, "BridgeUniqueid": bridge4,
            "OperationID": operation4, "MediaEpoch": epoch4,
        })
        denied = ami.wait_event(
            "MddAnswerBridgedResult",
            lambda item: item.get("OperationID") == operation4, timeout=5)
        if (denied.get("Result") != "denied" or caller4.ok_count
                or getvar(ami, ims4_channel, "MDD_INBOUND_ARMED") != "0"
                or getvar(ami, ims4_channel, "MDD_INBOUND_ANSWER_RESULT") != "denied"):
            raise AssertionError("callback admission deny did not consume the arm")
        ami.success({"Action": "Hangup", "Channel": winner4_channel, "Cause": "16"})
        wait_for(lambda: not ami.channels(), timeout=5, detail="admission deny cleanup")
        wait_for(lambda: recorded_inbound_statuses().count("NOANSWER") == 4,
                 detail="denied-module NOANSWER result")

        operation5, epoch5 = "3" * 32, "4" * 24
        winner5_id = "mddcanary-00000000-0000-4000-8000-000000000105"
        caller5, ims5_channel, ims5_id, winner5_channel, bridge5 = prepare_bridged_call(
            ami, media, "e3predeny", winner5_id, operation5, epoch5, 11)
        extra_callers.append(caller5)
        admission.allowed = False
        expect_error(ami, {
            "Action": "MddAnswerBridged", "Channel": ims5_channel,
            "IMSUniqueid": ims5_id, "WinnerChannel": winner5_channel,
            "WinnerUniqueid": winner5_id, "BridgeUniqueid": bridge5,
            "OperationID": operation5, "MediaEpoch": epoch5,
        })
        predenied = ami.wait_event(
            "MddAnswerBridgedResult",
            lambda item: item.get("OperationID") == operation5, timeout=5)
        admission.allowed = True
        if (predenied.get("Result") != "denied" or caller5.ok_count
                or getvar(ami, ims5_channel, "MDD_INBOUND_ARMED") != "0"
                or getvar(ami, ims5_channel, "MDD_INBOUND_ANSWER_RESULT") != "denied"):
            raise AssertionError("prequeue admission deny did not consume the arm")
        ami.success({"Action": "Hangup", "Channel": winner5_channel, "Cause": "16"})
        wait_for(lambda: not ami.channels(), timeout=5, detail="prequeue deny cleanup")
        wait_for(lambda: recorded_inbound_statuses().count("NOANSWER") == 5,
                 detail="prequeue-denied NOANSWER result")

        owner_flips = (
            ("MDD_INBOUND_ATTACH", "0", "5" * 32, "6" * 24, 12),
            ("MDD_INBOUND_SOURCE_ID", "wrong-source-id", "7" * 32, "8" * 24, 13),
            ("MDD_INBOUND_WINNER_CHANNEL",
             "WebSocket/mdd_control_media/0xdeadbeef", "9" * 32, "A" * 24, 14),
        )
        for offset, (field, changed, owner_operation, owner_epoch, marker) in enumerate(
                owner_flips, start=1):
            owner_winner_id = (
                f"mddcanary-00000000-0000-4000-8000-{106 + offset:012d}")
            owner_caller, owner_ims_channel, owner_ims_id, owner_winner_channel, owner_bridge = \
                prepare_bridged_call(
                    ami, media, f"e3owner{offset}", owner_winner_id,
                    owner_operation, owner_epoch, marker)
            extra_callers.append(owner_caller)
            setvar(ami, owner_ims_channel, field, changed)
            expect_error(ami, {
                "Action": "MddAnswerBridged", "Channel": owner_ims_channel,
                "IMSUniqueid": owner_ims_id, "WinnerChannel": owner_winner_channel,
                "WinnerUniqueid": owner_winner_id, "BridgeUniqueid": owner_bridge,
                "OperationID": owner_operation, "MediaEpoch": owner_epoch,
            })
            revoked = ami.wait_event(
                "MddAnswerBridgedResult",
                lambda item, expected=owner_operation: item.get("OperationID") == expected,
                timeout=5)
            if (revoked.get("Result") != "failed" or owner_caller.ok_count
                    or getvar(ami, owner_ims_channel, "MDD_INBOUND_ARMED") != "0"
                    or getvar(ami, owner_ims_channel,
                              "MDD_INBOUND_ANSWER_RESULT") != "failed"):
                raise AssertionError(f"owner revocation did not fail closed: {field}")
            ami.success({"Action": "Hangup", "Channel": owner_winner_channel,
                         "Cause": "16"})
            wait_for(lambda: not ami.channels(), timeout=5,
                     detail=f"owner revoke cleanup {field}")
            wait_for(lambda: recorded_inbound_statuses().count("NOANSWER") == 5 + offset,
                     detail=f"owner revoke NOANSWER {field}")

        rejected = FakeIncomingSip(); rejected.start(); rejected.wait_status(180)
        extra_callers.append(rejected)
        rejected_ims = wait_for(lambda: next((row for row in ami.channels()
                                             if row.get("Context") == "e3-incoming"), None),
                                detail="explicit reject IMS")
        setvar(ami, rejected_ims["Channel"], "DIALSTATUS", "BUSY")
        ami.success({"Action": "Hangup", "Channel": rejected_ims["Channel"],
                     "Cause": "21"})
        wait_for(lambda: not ami.channels(), timeout=5, detail="reject cleanup")
        wait_for(lambda: recorded_inbound_statuses().count("BUSY") == 1,
                 detail="one BUSY result")
        if recorded_inbound_statuses() != [
                "NOANSWER", "ANSWER", "NOANSWER", "NOANSWER", "NOANSWER",
                "NOANSWER", "NOANSWER", "NOANSWER", "NOANSWER", "BUSY"]:
            raise AssertionError("inbound hangup handler emitted duplicate or wrong results")
        print(json.dumps({
            "network": "none", "bridge_did_not_answer": True,
            "exact_bridge_legs": 2, "manager_result": "answered",
            "sip_200_count": caller.ok_count, "duplicate_answer_rejected": True,
            "wrong_bridge_rejected": True, "wrong_epoch_rejected": True,
            "unknown_header_rejected": True, "armed_consumed": True,
            "winner_left_before_answer": True,
            "wrong_bridge_consumed_failed": True,
            "callback_admission_denied": True,
            "prequeue_admission_denied": True,
            "owner_revocations_failed": [field for field, *_rest in owner_flips],
            "ims_attach_timeout10": True, "winner_timeout10": True,
            "call_result_statuses": recorded_inbound_statuses(),
            "final_channels": 0,
        }, sort_keys=True))
    finally:
        ami.close()
        if caller2 is not None:
            caller2.close()
        elif caller is not None:
            caller.close()
        for extra in extra_callers:
            extra.close()
        media.close(); admission.close()


if __name__ == "__main__":
    main()
