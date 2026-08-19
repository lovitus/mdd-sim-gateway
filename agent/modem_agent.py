#!/usr/bin/env python3
"""Generic remote 3GPP modem agent.

The agent discovers a standards-based AT port and publishes only capabilities that the modem
actually supports. Vendor/model quirks belong in optional backends; ICCID remains the stable SIM
identity and no host name, COM port, network-interface name or gateway address is hard-coded.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import hashlib
import json
import logging
import os
import re
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import uuid
from xml.sax.saxutils import escape

import serial
from serial.tools import list_ports
import websocket

try:
    from card_agent import (
        VPCD_CTRL_ATR, VPCD_CTRL_OFF, VPCD_CTRL_ON, VPCD_CTRL_RESET,
        agent_ws_path, connect_wss, get_agent_id, is_forbidden_apdu, load_pin_store,
        run_pcsc_reader_supervisor, verify_or_pin_fingerprint,
    )
    from embedded_socks import SocksServer, _encoded_address, _packet_address
    from cellular_isolation import IsolationGuard
    from call_audio import CallAudioController, CallAudioProbe, probe_call_audio
    from apn_providers import lookup_by_imsi
    import sms_history
    from modem_providers import (
        AuxiliaryAtProvider, CompositeModemProvider, GammuCliProvider, WindowsMbnProvider,
    )
except ModuleNotFoundError:  # Imported as agent.modem_agent by tests and packaging.
    from .card_agent import (
        VPCD_CTRL_ATR, VPCD_CTRL_OFF, VPCD_CTRL_ON, VPCD_CTRL_RESET,
        agent_ws_path, connect_wss, get_agent_id, is_forbidden_apdu, load_pin_store,
        run_pcsc_reader_supervisor, verify_or_pin_fingerprint,
    )
    from .embedded_socks import SocksServer, _encoded_address, _packet_address
    from .cellular_isolation import IsolationGuard
    from .call_audio import CallAudioController, CallAudioProbe, probe_call_audio
    from .apn_providers import lookup_by_imsi
    from . import sms_history
    from .modem_providers import (
        AuxiliaryAtProvider, CompositeModemProvider, GammuCliProvider, WindowsMbnProvider,
    )


ATR = bytes.fromhex("3B9F95801FC78031E073FE211B66D0017797020C000B")
CSIM_RE = re.compile(rb'\+CSIM:\s*(\d+)\s*,\s*"([0-9A-Fa-f]*)"')
ICCID_RE = re.compile(rb'(?<!\d)(89\d{17,20})(?!\d)')
IMEI_RE = re.compile(rb'(?<!\d)(\d{15})(?!\d)')
IMSI_RE = re.compile(rb'(?<!\d)(\d{14,15})(?!\d)')

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] [modem-agent] %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("mdd-modem-agent")
_PROCESS_LOCK = None


def acquire_process_lock(agent_id: str) -> bool:
    """Acquire one host-local Agent lease without requiring administrator privileges."""
    global _PROCESS_LOCK
    identity = hashlib.sha256(str(agent_id or "default").encode("utf-8")).hexdigest()[:24]
    if os.name == "nt":
        import ctypes
        kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel32.CreateMutexW.argtypes = [ctypes.c_void_p, ctypes.c_bool,
                                          ctypes.c_wchar_p]
        kernel32.CreateMutexW.restype = ctypes.c_void_p
        kernel32.CloseHandle.argtypes = [ctypes.c_void_p]
        kernel32.CloseHandle.restype = ctypes.c_bool
        handle = kernel32.CreateMutexW(None, False, f"Global\\MDDModemAgent-{identity}")
        if not handle:
            raise OSError(ctypes.get_last_error(), "CreateMutexW failed")
        if ctypes.get_last_error() == 183:  # ERROR_ALREADY_EXISTS
            kernel32.CloseHandle(handle)
            return False
        _PROCESS_LOCK = (kernel32, handle)
        return True

    import fcntl
    path = os.path.join(tempfile.gettempdir(), f"mdd-modem-agent-{identity}.lock")
    lock_file = open(path, "a+b")  # noqa: SIM115 -- retained for the process lifetime.
    try:
        fcntl.flock(lock_file.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        lock_file.close()
        return False
    _PROCESS_LOCK = lock_file
    return True


def windows_mbn_profile_xml(name: str, subscriber_id: str, apn: str,
                            auth: str = "NONE", username: str = "",
                            password: str = "") -> str:
    """Build a Windows MBN v1 profile without the application-owned SimIccID field."""
    auth = str(auth or "NONE").upper()
    if auth not in {"NONE", "PAP", "CHAP", "MSCHAPV2"}:
        raise ModemError("unsupported mobile-broadband authentication method")
    if not re.fullmatch(r"\d{14,15}", str(subscriber_id or "")):
        raise ModemError("SIM IMSI is required to create a Windows mobile-broadband profile")
    if not str(name or "").strip() or not str(apn or "").strip():
        raise ModemError("profile name and APN are required")
    windows_auth = "MsCHAPv2" if auth == "MSCHAPV2" else auth
    credentials = ""
    if username or password:
        credentials = ("<UserLogonCred><UserName>" + escape(username) +
                       "</UserName><Password>" + escape(password) +
                       "</Password></UserLogonCred>")
    return ("<?xml version=\"1.0\"?>"
            "<MBNProfile xmlns=\"http://www.microsoft.com/networking/WWAN/profile/v1\">"
            f"<Name>{escape(name.strip())}</Name><IsDefault>true</IsDefault>"
            "<ProfileCreationType>UserProvisioned</ProfileCreationType>"
            f"<SubscriberID>{subscriber_id}</SubscriberID>"
            "<AutoConnectOnInternet>false</AutoConnectOnInternet>"
            "<ConnectionMode>manual</ConnectionMode><Context>"
            f"<AccessString>{escape(apn.strip())}</AccessString>{credentials}"
            f"<Compression>DISABLE</Compression><AuthProtocol>{windows_auth}</AuthProtocol>"
            "</Context></MBNProfile>")


class ModemError(RuntimeError):
    pass


class ModemCard:
    def __init__(self, port: str, baud: int, timeout: float = 10.0, platform_provider=None,
                 gammu: str = "", gammu_port: str = "", call_audio_helper: str = ""):
        self.requested_port = port
        self.port_name = port
        self.baud = baud
        self.timeout = timeout
        self.serial = None
        self.iccid = ""
        self.imei = ""
        self.imsi = ""
        self.msisdn = ""
        self.operator = ""
        self.model = ""
        # The marketing model name is shared by incompatible hardware branches, so the exact
        # revision string is the only value a firmware compatibility check may key on.
        self.firmware = ""
        self.capabilities = {"sms": False, "call": False, "call_audio": False,
                             "cellular_data": False}
        self.sim_via_mbn = False
        self.platform_provider = platform_provider
        self.gammu = str(gammu or "")
        self.gammu_port = str(gammu_port or "")
        self.call_audio_helper = str(call_audio_helper or "")
        self.call_audio_probe = CallAudioProbe(reason="call audio has not been probed")
        self.call_audio_controller = None
        # The control thread must not publish a partially initialized capability snapshot.
        # connect() sets this only after identity, provider and all non-billable self-tests end.
        self.registration_ready = threading.Event()
        self.lock = threading.RLock()
        self._sms_readiness_cache = (0.0, {"ready": None, "reason": "not checked"})
        self._smsc_cache = (0.0, "")

    @property
    def connection(self):
        return self.serial

    def _at(self, command: str) -> bytes:
        with self.lock:
            if not self.serial:
                raise ModemError("modem is not connected")
            self.serial.reset_input_buffer()
            self.serial.write((command + "\r").encode("ascii"))
            self.serial.flush()
            return self._read_result(command)

    def _read_result(self, command: str) -> bytes:
        buffer = bytearray()
        deadline = time.monotonic() + self.timeout
        while time.monotonic() < deadline:
            chunk = self.serial.read(1024)
            if chunk:
                buffer.extend(chunk)
                lines = bytes(buffer).replace(b"\r", b"\n").splitlines()
                if any(line.strip() == b"OK" for line in lines):
                    return bytes(buffer)
                if any(line.strip() == b"ERROR" or line.strip().startswith(
                        (b"+CME ERROR:", b"+CMS ERROR:")) for line in lines):
                    detail = bytes(buffer).decode("ascii", "replace").strip()
                    raise ModemError(f"{command}: {detail}")
        raise ModemError(f"timeout waiting for {command}: {bytes(buffer[-200:])!r}")

    def sms_send(self, recipient: str, body: str) -> dict:
        platform_status = (self.platform_provider.status(refresh=True)
                           if self.platform_provider else {})
        authoritative = bool(platform_status.get("sms_readiness_authoritative"))
        if authoritative:
            if not platform_status.get("sms_ready"):
                raise ModemError(str(platform_status.get("sms_error") or
                                     "Platform SMS service is not ready"))
        else:
            readiness = self.sms_submit_readiness(force=True)
            if readiness.get("ready") is False:
                raise ModemError(str(readiness.get("reason") or "SMS bearer is unavailable"))
        # A modem can be registered for LTE/packet data while its legacy circuit-switched
        # domain is unavailable (common while roaming).  3GPP TS 27.007 defines CGSMS=2 as
        # packet-domain preferred with circuit-switched fallback.  Select it only when the
        # registration state proves that this is necessary; do not key this behaviour to a
        # modem model, operating system, carrier or country.
        if not authoritative:
            try:
                self._prepare_sms_bearer()
            except Exception as exc:
                # Some modems do not implement CGSMS and can still send SMS through their own
                # default/IMS path. Preparation must not turn that existing path into a failure.
                log.warning("Could not prepare the preferred SMS bearer: %s", exc)
        if self.platform_provider:
            try:
                result = self.platform_provider.sms_send(recipient, body)
                if result.get("ok") and self.iccid:
                    sms_history.record(self.iccid, self.service_centre())
                return result
            except Exception as exc:
                raise ModemError(self._submit_failure_detail(exc)) from exc
        if not re.fullmatch(r"\+?\d{1,32}", recipient):
            raise ModemError("invalid SMS recipient")
        try:
            result = self._sms_submit_at(recipient, body)
            if result.get("ok") and self.iccid:
                sms_history.record(self.iccid, self.service_centre())
            return result
        except ModemError as exc:
            raise ModemError(self._submit_failure_detail(exc)) from exc

    def _submit_failure_detail(self, exc: Exception) -> str:
        """Attach the submit preconditions an SMS failure never reports on its own.

        A rejected submit is reported by the network as an unspecified error, which is
        indistinguishable from a gateway defect.  Recording the SMS centre and registration
        that were in effect makes the difference visible without a second billable attempt.
        """
        detail = str(exc).strip() or exc.__class__.__name__
        try:
            centre = self.service_centre(force=True)
        except Exception:
            centre = ""
        context = [f"SMS centre {centre}" if centre else
                   "no SMS centre was readable through MBN or AT+CSCA"]
        readiness = dict(self._sms_readiness_cache[1] or {})
        bearer = str(readiness.get("bearer") or "")
        if bearer:
            context.append(f"bearer {bearer}")
        if readiness.get("cs") is not None:
            context.append(f"CREG {readiness['cs']}")
        return f"{detail} (at submit time: {', '.join(context)})"[:400]

    def _sms_submit_at(self, recipient: str, body: str) -> dict:
        with self.lock:
            self._at("AT+CMGF=1")
            unicode_text = any(ord(char) > 127 for char in body)
            if unicode_text:
                self._at('AT+CSCS="UCS2"')
                wire_recipient = recipient.encode("utf-16-be").hex().upper()
                wire_body = body.encode("utf-16-be").hex().upper().encode("ascii")
            else:
                self._at('AT+CSCS="GSM"')
                wire_recipient, wire_body = recipient, body.encode("ascii")
            self.serial.reset_input_buffer()
            self.serial.write(f'AT+CMGS="{wire_recipient}"\r'.encode("ascii"))
            self.serial.flush()
            prompt = bytearray()
            deadline = time.monotonic() + 8
            while time.monotonic() < deadline and b">" not in prompt:
                prompt.extend(self.serial.read(256))
            if b">" not in prompt:
                raise ModemError("SMS modem did not return a submit prompt")
            self.serial.write(wire_body + b"\x1a")
            self.serial.flush()
            raw = self._read_result("AT+CMGS")
        match = re.search(rb"\+CMGS:\s*(\d+)", raw)
        return {"ok": True, "status": "sent", "reference": int(match.group(1)) if match else None,
                "audio": False}

    def smsc_changed(self) -> bool:
        """Return True when the current SMSC differs from the last successful value.

        This is the only way to distinguish a missing SMSC from a changed/wrong one without
        another billable attempt.  A fresh change is advisory: it does not block the operator
        from trying, because the operator or the network may have updated the centre.
        """
        return sms_history.changed(self.iccid, self.service_centre())

    def service_centre(self, *, force: bool = False) -> str:
        """Read the SMS centre address the modem will submit through.

        This is observational and read-only: ``AT+CSCA?`` is answered from EF_SMSP, and the
        value is never written back. A wrong or absent centre makes ``AT+CMGS`` fail with an
        unspecified network error, so the address must appear in status and in failure
        reports instead of leaving the operator to guess.  Absence alone is not treated as
        proof of failure, because some modems keep the address below the AT interface.
        """
        checked_at, cached = self._smsc_cache
        now = time.monotonic()
        if not force and checked_at and now - checked_at < 60:
            return cached
        value = ""
        if self.platform_provider:
            value = str(self.platform_provider.status().get("sms_service_center") or "")
            if not value:
                # Verified on real hardware: the heartbeat status path leaves MBN's SMS
                # getters unsubscribed, so they answer E_PENDING and the address looks
                # absent. An empty platform field is therefore missing information, not an
                # absent centre; ask the subscribed reader before concluding anything.
                reader = getattr(self.platform_provider, "sms_configuration", None)
                if reader:
                    try:
                        config = reader()
                        if isinstance(config, dict):
                            value = str(config.get("service_center") or "")
                    except Exception as exc:
                        log.debug("MBN SMS configuration unavailable: %s", exc)
        if not value:
            try:
                raw = self._at("AT+CSCA?").decode("ascii", "replace")
                match = re.search(r'\+CSCA:\s*"([^"]*)"', raw)
                value = (match.group(1).strip() if match else "")
            except Exception:
                value = ""
        self._smsc_cache = (now, value)
        return value

    def sms_submit_readiness(self, *, force: bool = False) -> dict:
        """Report only authoritative SMS bearer failures; unknown remains usable.

        The signalling provider being installed proves that SMS can be read and submitted to
        the modem, not that the serving network currently offers an MO-SMS bearer.  In an
        LTE-only attachment, no CS registration plus an explicitly unavailable IMS session is
        a definitive precondition failure.  Cache the read-only AT probe so status heartbeats
        do not turn into a command flood; transient registration/searching remains ``None``
        rather than a false failure and is checked again later.
        """
        checked_at, cached = self._sms_readiness_cache
        now = time.monotonic()
        if not force and checked_at and now - checked_at < 60:
            return dict(cached)
        try:
            cs = self._registration_state(self._at("AT+CREG?"), "CREG")
            packet = []
            for command, name in (("AT+CGREG?", "CGREG"), ("AT+CEREG?", "CEREG")):
                try:
                    packet.append(self._registration_state(self._at(command), name))
                except Exception:
                    pass
            registered = {1, 5}
            if cs in registered:
                result = {"ready": True, "bearer": "cs", "cs": cs, "packet": packet}
            elif not any(value in registered for value in packet):
                # Searching, radio-off and reconnect transitions are not proof that a
                # configured bearer is permanently unavailable.
                result = {"ready": None, "reason": "mobile network registration is pending",
                          "cs": cs, "packet": packet}
            else:
                try:
                    raw = self._at('AT+QCFG="ims"')
                except Exception:
                    # QCFG is vendor-specific.  Other modems may expose IMS differently, so
                    # absence of this probe must not disable their existing send path.
                    result = {"ready": None, "reason": "IMS state is not exposed by this modem",
                              "cs": cs, "packet": packet}
                else:
                    match = re.search(rb'\+QCFG:\s*"ims"\s*,\s*(\d+)\s*,\s*(\d+)', raw)
                    ims = int(match.group(2)) if match else None
                    if ims == 1:
                        result = {"ready": True, "bearer": "ims", "cs": cs,
                                  "packet": packet, "ims": ims}
                    elif ims == 0:
                        result = {
                            "ready": False, "cs": cs, "packet": packet, "ims": ims,
                            "reason": ("SMS unavailable: LTE data is registered, but the modem "
                                       "has neither circuit-switched registration nor an available "
                                       "IMS session. Update/provision the modem firmware or carrier "
                                       "profile before retrying."),
                        }
                    else:
                        result = {"ready": None, "reason": "IMS state is inconclusive",
                                  "cs": cs, "packet": packet}
        except Exception as exc:
            result = {"ready": None, "reason": f"SMS bearer probe failed: {exc}"}
        self._sms_readiness_cache = (now, dict(result))
        return result

    @staticmethod
    def _revision_from(output) -> str:
        """Extract the firmware revision from ATI or AT+GMR output.

        Vendors place the revision on a ``Revision:`` line, as a bare token, or not at all.
        Return an empty string rather than guessing: an invented revision would be checked
        against the compatibility matrix and could produce a false verdict.
        """
        lines = output if isinstance(output, list) else str(output or "").replace(
            "\r", "\n").split("\n")
        candidates = [str(line).strip() for line in lines if str(line).strip()]
        for line in candidates:
            match = re.match(r"^(?:revision|firmware)\s*:\s*(\S+)$", line, re.I)
            if match:
                return match.group(1)[:100]
        for line in candidates:
            if line.upper() in {"OK", "ATI", "AT+GMR"}:
                continue
            if re.fullmatch(r"[A-Z0-9._\-]*R\d{2}[A-Z0-9._\-]*", line.upper()):
                return line[:100]
        return ""

    @staticmethod
    def _registration_state(raw: bytes, name: str) -> int | None:
        match = re.search(
            rb"\+" + re.escape(name.encode("ascii")) + rb":\s*\d+\s*,\s*(\d+)", raw)
        return int(match.group(1)) if match else None

    def _prepare_sms_bearer(self) -> dict:
        """Prefer packet-domain MO SMS only when packet service is the sole registered domain."""
        cs = self._registration_state(self._at("AT+CREG?"), "CREG")
        packet_states = []
        for command, name in (("AT+CGREG?", "CGREG"), ("AT+CEREG?", "CEREG")):
            try:
                packet_states.append(self._registration_state(self._at(command), name))
            except Exception:
                pass
        registered = {1, 5}
        if cs in registered or not any(value in registered for value in packet_states):
            return {"changed": False, "cs": cs, "packet": packet_states}
        supported = self._at("AT+CGSMS=?")
        support_match = re.search(rb"\+CGSMS:\s*\(([^\r\n)]*)\)", supported)
        support_tokens = re.findall(rb"\d+(?:\s*-\s*\d+)?",
                                    support_match.group(1) if support_match else b"")
        packet_preferred_supported = any(
            (int(token) == 2 if b"-" not in token else
             int(token.split(b"-", 1)[0]) <= 2 <= int(token.split(b"-", 1)[1]))
            for token in support_tokens)
        if not packet_preferred_supported:
            return {"changed": False, "cs": cs, "packet": packet_states,
                    "reason": "packet-preferred SMS is unsupported"}
        current_raw = self._at("AT+CGSMS?")
        current_match = re.search(rb"\+CGSMS:\s*(\d+)", current_raw)
        current = int(current_match.group(1)) if current_match else None
        if current != 2:
            self._at("AT+CGSMS=2")
        return {"changed": current != 2, "previous": current, "selected": 2,
                "cs": cs, "packet": packet_states}

    def sms_list(self) -> list[dict]:
        if self.platform_provider:
            return self.platform_provider.sms_list()
        self._at("AT+CMGF=1")
        self._at('AT+CSCS="GSM"')
        raw = self._at('AT+CMGL="ALL"').decode("utf-8", "replace")
        lines, messages, index = raw.replace("\r", "").split("\n"), [], 0
        while index < len(lines):
            line = lines[index].strip()
            match = re.match(r'^\+CMGL:\s*(\d+),"([^"]*)","([^"]*)"(.*)$', line)
            if match:
                body = lines[index + 1] if index + 1 < len(lines) else ""
                status = match.group(2).upper()
                identity = hashlib.sha256(f"{line}\0{body}".encode("utf-8")).hexdigest()
                messages.append({"id": match.group(1),
                                 "fingerprint": identity,
                                 "direction": "out" if status.startswith("STO") else "in",
                                 "peer": match.group(3), "body": body, "ts": int(time.time())})
                index += 1
            index += 1
        return messages

    def connect(self) -> bool:
        # An auto-discovered COM/tty name belongs only to the current USB attachment.  After
        # unplug/replug Windows commonly renumbers it, so never turn an `auto` preference into
        # a permanent pin to the first successful port.
        if self.requested_port and self.requested_port != "auto":
            candidates = [self.requested_port]
        else:
            ports = list(list_ports.comports())
            def priority(item):
                description = str(getattr(item, "description", "") or "")
                if re.search(r"\bAT\b", description, re.I):
                    return 0
                if re.search(r"modem|wwan|mobile broadband|cellular", description, re.I):
                    return 1
                if re.search(r"bluetooth|nmea|diagnostic|\bDM\b|\bgps\b", description, re.I):
                    return 3
                return 2
            candidates = [item.device for item in sorted(ports, key=priority)]
        for candidate in candidates:
            if self._connect_port(candidate):
                return True
        return False

    def _connect_port(self, candidate: str) -> bool:
        try:
            self.close()
            self.serial = serial.Serial(
                candidate, self.baud, timeout=0.25, write_timeout=2
            )
            # EC20 autobaud can reject the first command after the port is opened.
            for _ in range(3):
                try:
                    self._at("AT")
                    break
                except ModemError:
                    time.sleep(0.2)
            self._at("ATE0")
            self._at("AT+CMEE=2")
            self.sim_via_mbn = False
            raw = self._at("AT+CGSN")
            match = IMEI_RE.search(raw)
            if not match:
                raise ModemError("modem IMEI is unavailable")
            self.imei = match.group(1).decode("ascii")
            if os.name == "nt" and self.platform_provider is None:
                self.platform_provider = WindowsMbnProvider.discover(self.imei)
                if self.platform_provider is None:
                    raise ModemError(
                        "Windows MBN has not enumerated this modem; automatic Generic AT "
                        "ownership is disabled")
                configured_port = self.gammu_port or str(os.environ.get("MDD_GAMMU_PORT") or "")
                if configured_port:
                    signalling_ports = [configured_port]
                else:
                    discovered_ports = list(list_ports.comports())
                    def signalling_priority(item):
                        description = str(getattr(item, "description", "") or "")
                        if re.search(r"\bmodem\b", description, re.I):
                            return 0
                        if re.search(r"\bAT\b", description, re.I):
                            return 1
                        return 2
                    signalling_ports = [item.device for item in sorted(
                        discovered_ports, key=signalling_priority)
                        if item.device != candidate and not re.search(
                            r"bluetooth|nmea|diagnostic|\bDM\b|\bgps\b",
                            str(getattr(item, "description", "") or ""), re.I)]
                signalling = AuxiliaryAtProvider.discover(self.imei, signalling_ports)
                if not signalling:
                    signalling = GammuCliProvider.discover(
                        self.imei, signalling_ports, configured=self.gammu)
                if signalling:
                    apdu = signalling if signalling.capabilities.sim_apdu else None
                    self.platform_provider = CompositeModemProvider(
                        self.platform_provider, signalling, apdu)
                    log.info("%s signalling attached on %s", signalling.name, signalling.port)
                    if apdu:
                        log.info("Auxiliary SIM APDU access verified on %s", signalling.port)
            system_iccid = system_imsi = ""
            if self.platform_provider:
                # Once the operating-system provider owns this modem, it is authoritative for
                # every SIM/business operation.  Do not silently mix MBN data with AT SIM,
                # APDU, SMS or calling merely because a diagnostic COM port also responds.
                system_iccid, system_imsi = self.platform_provider.identity()
                self.iccid = system_iccid
                self.sim_via_mbn = True
            else:
                try:
                    raw = self._at("AT+CCID")
                    match = ICCID_RE.search(raw)
                    self.iccid = match.group(1).decode("ascii") if match else ""
                except Exception:
                    self.iccid = ""
            if not self.iccid and os.name == "nt" and not self.platform_provider:
                system_iccid, system_imsi = self._windows_mbn_identity()
                self.iccid = system_iccid
                self.sim_via_mbn = bool(system_iccid)
            if not self.iccid:
                raise ModemError("SIM ICCID is unavailable through AT or Windows MBIM")
            if self.platform_provider:
                self.imsi = system_imsi
                numbers = self.platform_provider.snapshot.get("telephone_numbers") or []
                self.msisdn = str(next((value for value in numbers if value), ""))
            else:
                try:
                    raw = self._at("AT+CIMI")
                    match = IMSI_RE.search(raw)
                    self.imsi = match.group(1).decode("ascii") if match else ""
                except Exception:
                    if os.name == "nt" and not system_imsi:
                        _, system_imsi = self._windows_mbn_identity()
                    self.imsi = system_imsi
                try:
                    raw = self._at("AT+CNUM").decode("ascii", "replace")
                    match = re.search(r'"(\+?\d{5,20})"', raw)
                    self.msisdn = match.group(1) if match else ""
                except Exception:
                    self.msisdn = ""
            self.port_name = candidate
            try:
                raw = self._at("ATI").decode("ascii", "replace")
                values = [line.strip() for line in raw.replace("\r", "").split("\n")
                          if line.strip() and line.strip() not in {"OK", "ATI"}]
                self.model = " ".join(values[:2])[:100]
                self.firmware = self._revision_from(values)
            except Exception:
                self.model = "3GPP modem"
            if not self.firmware:
                # 3GPP TS 27.007 AT+GMR is the authoritative revision request. ATI only
                # includes it on some modems, and never as a stable field position.
                try:
                    self.firmware = self._revision_from(
                        self._at("AT+GMR").decode("ascii", "replace"))
                except Exception:
                    self.firmware = ""
            # When Windows MBIM owns the SIM channel, accepting generic AT commands does not
            # mean that AT SMS, calls, or CSIM APDUs are usable.  Advertise only capabilities
            # that can actually reach the SIM in the current ownership mode.
            if self.platform_provider:
                provider_model = " ".join(filter(None, (
                    str(self.platform_provider.snapshot.get("manufacturer") or ""),
                    str(self.platform_provider.snapshot.get("model") or ""))))
                if provider_model:
                    self.model = provider_model[:100]
                provider_firmware = str(
                    self.platform_provider.snapshot.get("firmware") or "").strip()
                if provider_firmware:
                    self.firmware = provider_firmware[:100]
                platform_caps = self.platform_provider.capabilities
                # SMS is enabled when the provider implements the operations, not merely when
                # the physical driver reports an SMS capability bit.
                self.capabilities["sms"] = bool(
                    platform_caps.sms_list and platform_caps.sms_send and
                    hasattr(self.platform_provider, "sms_list") and
                    hasattr(self.platform_provider, "sms_send"))
                self.capabilities["call"] = bool(platform_caps.call_signalling)
                self.capabilities["sim_apdu"] = bool(platform_caps.sim_apdu)
                self.capabilities["cellular_data"] = bool(platform_caps.cellular_data)
            else:
                self.capabilities["sms"] = (not self.sim_via_mbn and
                                            self._supports("AT+CMGF=?"))
                self.capabilities["call"] = (not self.sim_via_mbn and
                                             self._supports("AT+CLCC"))
                self.capabilities["cellular_data"] = True
            # Voice signalling and media are separate capabilities. The startup media probe is
            # bounded and non-billable: it never dials, answers, or changes the default sound
            # device. Only an explicitly matched endpoint in this modem's hardware container
            # can make call_audio true.
            self.call_audio_probe = CallAudioProbe(
                reason="call signalling is unavailable on the selected provider")
            self.capabilities["call_audio"] = False
            if self.capabilities.get("call") and self.capabilities.get("cellular_data"):
                self.call_audio_probe = probe_call_audio(
                    candidate, self._at, helper=self.call_audio_helper)
                self.capabilities["call_audio"] = self.call_audio_probe.ready
                if self.call_audio_probe.ready:
                    self.call_audio_controller = CallAudioController(
                        self.call_audio_probe, self._at, helper=self.call_audio_helper)
                    log.info("Call audio self-test passed (%s)",
                             self.call_audio_probe.backend)
                else:
                    log.warning("Call audio unavailable: %s", self.call_audio_probe.reason)
            log.info("Connected to %s (%s; ICCID ending %s)", candidate,
                     self.model or "3GPP modem", self.iccid[-4:])
            self.registration_ready.set()
            return True
        except Exception as exc:
            log.warning("Cannot open modem %s: %s", candidate, exc)
            self.close()
            return False

    def _supports(self, command: str) -> bool:
        try:
            self._at(command)
            return True
        except Exception:
            return False

    def _windows_mbn_identity(self) -> tuple[str, str]:
        """Read SIM identity through Windows MBIM when the AT SIM channel is owned by WWAN."""
        if os.name != "nt" or not self.imei:
            return "", ""
        try:
            interfaces = subprocess.run(
                ["netsh", "mbn", "show", "interfaces"], capture_output=True,
                text=True, timeout=8, check=False).stdout
            blocks = re.split(r"\n\s*\n", interfaces)
            block = next((value for value in blocks if self.imei in value), "")
            match = re.search(r"^\s*Name\s*:\s*(.+?)\s*$", block, re.I | re.M)
            if not match:
                return "", ""
            ready = subprocess.run(
                ["netsh", "mbn", "show", "readyinfo", f"interface={match.group(1).strip()}"],
                capture_output=True, text=True, timeout=8, check=False).stdout
            iccid_match = re.search(r"^\s*SIM\s+ICC\s+Id\s*:\s*(\d{18,22})\s*$",
                                    ready, re.I | re.M)
            imsi_match = re.search(r"^\s*Subscriber\s+Id\s*:\s*(\d{14,15})\s*$",
                                   ready, re.I | re.M)
            return ((iccid_match.group(1) if iccid_match else ""),
                    (imsi_match.group(1) if imsi_match else ""))
        except Exception:
            return "", ""

    def transmit(self, apdu: bytes) -> bytes:
        if is_forbidden_apdu(apdu):
            return bytes.fromhex("6985")
        try:
            if (self.platform_provider and self.capabilities.get("sim_apdu") and
                    hasattr(self.platform_provider, "transmit_apdu")):
                return self.platform_provider.transmit_apdu(apdu)
            value = apdu.hex().upper()
            raw = self._at(f'AT+CSIM={len(value)},"{value}"')
            match = CSIM_RE.search(raw)
            if not match:
                raise ModemError(f"missing +CSIM response: {raw[-200:]!r}")
            return bytes.fromhex(match.group(2).decode("ascii"))
        except Exception as exc:
            log.warning("APDU failed: %s", exc)
            return bytes.fromhex("6F00")

    def reset(self):
        try:
            if (self.platform_provider and self.capabilities.get("sim_apdu") and
                    hasattr(self.platform_provider, "reset_apdu")):
                self.platform_provider.reset_apdu()
                return
            self._at("AT+CPIN?")
        except Exception:
            # Some Windows MBIM drivers own the SIM channel and return "SIM failure" for
            # CPIN/CCID while the physical AT port remains healthy.  A VPCD reset must not
            # tear down modem control in that case.
            try:
                self._at("AT")
            except Exception:
                self.close()

    def close(self):
        self.registration_ready.clear()
        self.capabilities["call_audio"] = False
        self.call_audio_probe = CallAudioProbe(reason="modem is disconnected")
        audio, self.call_audio_controller = self.call_audio_controller, None
        if audio:
            try:
                audio.close()
            except Exception:
                pass
        provider, self.platform_provider = self.platform_provider, None
        if provider:
            closer = getattr(provider, "close", None)
            if closer:
                try:
                    closer()
                except Exception:
                    pass
        current, self.serial = self.serial, None
        if current:
            try:
                current.close()
            except Exception:
                pass


def path_with_card_id(path: str, reader_name: str, card_id: str, imei: str) -> str:
    allocated = agent_ws_path(path, reader_name)
    split = urllib.parse.urlsplit(allocated)
    query = dict(urllib.parse.parse_qsl(split.query, keep_blank_values=True))
    query["card_id"] = card_id
    query["imei"] = imei
    return urllib.parse.urlunsplit(("", "", split.path, urllib.parse.urlencode(query), ""))


class ModemControl:
    """Versioned control channel; all business identity remains ICCID-based."""
    def __init__(self, args, modem: ModemCard):
        self.args, self.modem = args, modem
        self.stop = threading.Event()
        self.results = {}
        self.socks_server = None
        self.isolation = IsolationGuard(args.isolation_helper)
        self.acked_sms = set()
        self.allow_roaming = False
        self.selected_profile = ""
        self._isolation_armed = False
        self._source_miss_count = 0
        self.operation_lock = threading.Lock()
        self.reset_pin = bool(getattr(args, "reset_pin", False))
        # Windows WWAN can misreport an otherwise readable SIM as absent if a vendor driver
        # sees a USIM logical channel before it activates the saved data profile.  The control
        # plane establishes the desired data/off state first; only then may the VPCD bridge
        # open an auxiliary UICC session.
        self.data_reconciled = threading.Event()
        self.cellular_active = threading.Event()
        threading.Thread(target=self._watch_isolation, name="cellular-isolation-watch",
                         daemon=True).start()

    def _cellular_interface(self) -> str:
        if self.args.cellular_interface:
            return self.args.cellular_interface
        # Once isolation is armed, its verified interface is the stable attachment for this
        # data context. Re-running Windows MBN discovery on every status sample can return an
        # empty transient result and must not erase that known-good binding.
        guarded_interface = str(getattr(self.isolation, "interface", "") or "")
        if guarded_interface:
            return guarded_interface
        try:
            if os.name == "nt":
                raw = subprocess.run(["netsh", "mbn", "show", "interfaces"],
                                     capture_output=True, text=True, timeout=8, check=False).stdout
                blocks = re.split(r"\n\s*\n", raw)
                block = next((value for value in blocks if self.modem.imei in value), raw)
                match = re.search(r"^\s*Name\s*:\s*(.+?)\s*$", block, re.I | re.M)
                return match.group(1).strip() if match else ""
            if sys.platform == "darwin":
                raw = subprocess.run(["networksetup", "-listallhardwareports"],
                                     capture_output=True, text=True, timeout=8, check=False).stdout
                blocks = re.split(r"\n\s*\n", raw)
                block = next((value for value in blocks if re.search(
                    r"Hardware Port:.*(cellular|wwan|mobile|usb)", value, re.I)), "")
                match = re.search(r"Device:\s*(\S+)", block)
                return match.group(1) if match else ""
            names = os.listdir("/sys/class/net")
            preferred = [name for name in names if re.search(r"wwan|cdc|usb|rmnet", name, re.I)]
            return preferred[0] if preferred else ""
        except Exception:
            return ""

    def _advertise_host(self) -> str:
        if self.args.advertise_host:
            return self.args.advertise_host
        try:
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as probe:
                probe.connect((self.args.host, self.args.gateway_port))
                return probe.getsockname()[0]
        except OSError:
            return socket.gethostbyname(socket.gethostname())

    def _cellular_ip(self, interface: str) -> str:
        if not interface:
            return ""
        try:
            if os.name == "nt":
                command = ["powershell", "-NoProfile", "-Command",
                           f"(Get-NetIPAddress -InterfaceAlias '{interface.replace(chr(39), chr(39) * 2)}' -AddressFamily IPv4 -ErrorAction Stop | Where-Object {{$_.IPAddress -notlike '169.254.*'}} | Select-Object -First 1).IPAddress"]
                result = subprocess.run(
                    command, capture_output=True, text=True, timeout=8, check=False)
                candidates = result.stdout.strip().splitlines()
                def usable(value: str) -> bool:
                    try:
                        packed = socket.inet_aton(value.strip())
                    except OSError:
                        return False
                    first, second = packed[0], packed[1]
                    # A few WWAN drivers transiently expose a netmask-looking value through
                    # CIM. Only unicast source addresses are valid for a bound data socket.
                    return (first not in (0, 127) and first < 224 and
                            not (first == 169 and second == 254))
                # Some WWAN miniports make Get-NetIPAddress fail with a generic CIM error
                # while netsh and the actual adapter both have a valid address. Use the
                # independent Windows IPv4 store as a deterministic fallback.
                candidates = [value.strip() for value in candidates if usable(value)]
                if not candidates:
                    fallback = subprocess.run(
                        ["netsh", "interface", "ipv4", "show", "addresses",
                         f"name={interface}"],
                        capture_output=True, text=True, timeout=8, check=False)
                    candidates = re.findall(
                        r"(?<![\d.])(?:\d{1,3}\.){3}\d{1,3}(?![\d.])",
                        fallback.stdout)
                for value in candidates:
                    value = value.strip()
                    if usable(value):
                        return value
                return ""
            elif sys.platform == "darwin":
                command = ["ipconfig", "getifaddr", interface]
            else:
                command = ["sh", "-c", "ip -4 -o addr show dev \"$1\" | awk '{print $4}' | cut -d/ -f1 | head -1", "sh", interface]
            result = subprocess.run(command, capture_output=True, text=True, timeout=8, check=False)
            value = result.stdout.strip().splitlines()[0] if result.stdout.strip() else ""
            socket.inet_aton(value)
            return value
        except Exception:
            return ""

    def _modem_transport_present(self) -> bool:
        """Check the already-open serial attachment without guessing a replacement port."""
        port = str(getattr(self.modem, "port_name", "") or "").strip()
        if not port or port.casefold() == "auto" or list_ports is None:
            return True
        try:
            expected = os.path.normcase(port)
            return any(os.path.normcase(str(item.device)) == expected
                       for item in list_ports.comports())
        except Exception:
            # Enumeration failure is not proof of unplug; the serial I/O path will make the
            # final decision. This check may revoke, but must never manufacture, presence.
            return True

    def _status(self) -> dict:
        values = {"sim": "ready" if self.modem.connection else "offline",
                  "data": "disconnected", "data_active": False,
                  "proxy": {"ready": bool(self.socks_server and self.socks_server.ready)},
                  "roaming_allowed": self.allow_roaming,
                  "interface": "", "registration": "unknown", "operator": "",
                  "signal": None, "radio_enabled": None}
        audio_probe = getattr(self.modem, "call_audio_probe", CallAudioProbe())
        modem_capabilities = getattr(self.modem, "capabilities", {})
        values["call_audio_ready"] = bool(
            modem_capabilities.get("call_audio") and audio_probe.ready)
        values["call_audio_backend"] = audio_probe.backend
        values["call_audio_error"] = "" if values["call_audio_ready"] else audio_probe.reason
        values["profile"] = self.selected_profile
        source_lost = False
        try:
            interface = self._cellular_interface()
            values["interface"] = interface
            # A running SOCKS server was created only after this address existed and the WFP
            # guard verified the same interface. Reuse that established data-context binding
            # for status; repeated Windows address queries are advisory and can fail inside a
            # packaged process even while the bound socket and MBN connection remain healthy.
            raw_established_ip = getattr(self.socks_server, "source_ip", "")
            established_ip = raw_established_ip if isinstance(raw_established_ip, str) else ""
            if established_ip:
                observed_ip = self._cellular_ip(interface)
                if observed_ip == established_ip:
                    self._source_miss_count = 0
                else:
                    self._source_miss_count += 1
                if self._source_miss_count >= 3:
                    # One failed Windows query is common; three consecutive independent
                    # samples are a revoked attachment. Tear down only the data-plane objects
                    # created by this Agent generation, then let desired-state reconciliation
                    # rebuild them if the bearer returns.
                    server, self.socks_server = self.socks_server, None
                    if server:
                        server.close()
                    self.isolation.close()
                    self._isolation_armed = False
                    self._source_miss_count = 0
                    source_lost = True
                    established_ip = ""
                    if not self._modem_transport_present():
                        self.modem.close()
            else:
                self._source_miss_count = 0
            source_ip = established_ip or ("" if source_lost else self._cellular_ip(interface))
            if source_ip:
                values["data"] = "connected"
                values["data_active"] = True
                values["ip"] = source_ip
            elif self.socks_server:
                # Status collection is observational. A transient Windows query failure must
                # not mutate the data plane; report fail-closed and let the idempotent desired-
                # state reconciler decide whether to reconnect. The WFP guard and source-bound
                # socket continue to prevent fallback to another interface in the meantime.
                values["proxy"] = {"ready": False}
                values["cellular"] = {
                    "ok": False, "status": "unavailable",
                    "error": "The cellular address could not be confirmed.",
                    "proxy": {"ready": False},
                }
            platform_provider = getattr(self.modem, "platform_provider", None)
            if platform_provider:
                platform = platform_provider.status()
                values.update({key: platform[key] for key in (
                    "registration", "operator", "signal", "radio_enabled",
                    "data", "data_active", "profile", "provider", "owner",
                ) if key in platform})
                platform_sms_ready = platform.get("sms_ready")
                values["sms_ready"] = (platform_sms_ready
                                       if isinstance(platform_sms_ready, bool) else None)
                values["sms_error"] = str(platform.get("sms_error") or "")
                values["sms_service_center"] = str(platform.get("sms_service_center") or "")
                values["sms_service_center_changed"] = self.modem.smsc_changed()
                values["sms_service_center_advisory"] = (
                    "The SMS centre differs from the last successful send; check with your "
                    "carrier if sends fail." if values["sms_service_center_changed"] else "")
                values["sms_provider"] = str(platform.get("sms_provider") or "")
                values["call_ready"] = False
                values["call_error"] = "Cellular call bearer is unavailable"
                authoritative_sms = bool(platform.get("sms_readiness_authoritative"))
                if ((not authoritative_sms and self.modem.capabilities.get("sms")) or
                        self.modem.capabilities.get("call")):
                    readiness = self.modem.sms_submit_readiness()
                    # Voice signalling needs the same serving-network prerequisite as
                    # mobile-originated SMS: a registered CS domain or a usable IMS session.
                    # Merely finding ATD/CLCC in an installed adapter is not runtime readiness.
                    values["call_ready"] = bool(
                        self.modem.capabilities.get("call") and
                        readiness.get("ready") is True)
                    call_reason = str(
                        readiness.get("reason") or "Cellular call bearer is unavailable")
                    if call_reason.startswith("SMS unavailable:"):
                        call_reason = "Cellular call unavailable:" + call_reason[len("SMS unavailable:"):]
                    values["call_error"] = "" if values["call_ready"] else call_reason
                    if not authoritative_sms and readiness.get("ready") is False:
                        values["sms_ready"] = False
                        values["sms_error"] = str(
                            readiness.get("reason") or "SMS bearer unavailable")
                    elif not authoritative_sms and readiness.get("ready") is not False:
                        # Unknown means the platform must decide during the explicit user
                        # submission; it is not proof of unavailability and is never retried.
                        values["sms_ready"] = True
            else:
                for command in ("AT+CEREG?", "AT+CREG?"):
                    try:
                        raw = self.modem._at(command).decode("ascii", "replace")
                        match = re.search(r"\+(?:CE|C)REG:\s*\d+\s*,\s*(\d+)", raw)
                        if match:
                            values["registration"] = {
                                "0": "unregistered", "1": "home", "2": "searching",
                                "3": "denied", "4": "unknown", "5": "roaming",
                            }.get(match.group(1), "unknown")
                            break
                    except Exception:
                        continue
                try:
                    raw = self.modem._at("AT+COPS?").decode("ascii", "replace")
                    match = re.search(r'\+COPS:\s*\d+(?:,\d+,"([^"]*)")?', raw)
                    values["operator"] = match.group(1) if match and match.group(1) else ""
                except Exception:
                    pass
                try:
                    raw = self.modem._at("AT+CSQ").decode("ascii", "replace")
                    match = re.search(r"\+CSQ:\s*(\d+)", raw)
                    if match and int(match.group(1)) <= 31:
                        values["signal"] = round(int(match.group(1)) * 100 / 31)
                except Exception:
                    pass
                try:
                    raw = self.modem._at("AT+CFUN?").decode("ascii", "replace")
                    match = re.search(r"\+CFUN:\s*(\d+)", raw)
                    if match:
                        values["radio_enabled"] = match.group(1) == "1"
                except Exception:
                    pass
                # The SMS centre is the one submit precondition a status page cannot infer
                # from registration, so publish it for every provider, not only Windows MBN.
                values["sms_service_center"] = self.modem.service_centre()
                values["sms_service_center_changed"] = self.modem.smsc_changed()
                values["sms_service_center_advisory"] = (
                    "The SMS centre differs from the last successful send; check with your "
                    "carrier if sends fail." if values["sms_service_center_changed"] else "")
            if os.name == "nt":
                if (self.modem.sim_via_mbn and interface and
                        not getattr(self.modem, "platform_provider", None)):
                    radio = subprocess.run(
                        ["netsh", "mbn", "show", "radio", f"interface={interface}"],
                        capture_output=True, text=True, timeout=8, check=False).stdout
                    software = re.search(r"Software\s+radio\s+state\s*:\s*(On|Off)",
                                         radio, re.I)
                    if software:
                        values["radio_enabled"] = software.group(1).casefold() == "on"
                command = ["netsh", "mbn", "show", "interfaces"]
                if interface:
                    command = ["netsh", "mbn", "show", "interface", f"name={interface}"]
                result = subprocess.run(command,
                                        capture_output=True, text=True, timeout=8, check=False)
                text = result.stdout
                if re.search(r"State\s*:\s*connected", text, re.I):
                    values["data"] = "connected"
                    values["data_active"] = True
        except Exception:
            pass
        if source_lost:
            # Native providers may retain a last-known "connected" snapshot briefly after USB
            # removal. The revoked source binding is authoritative for this status sample.
            values.update(data="disconnected", data_active=False,
                          proxy={"ready": False})
            values.pop("ip", None)
            values["cellular"] = {
                "ok": False, "status": "unavailable",
                "error": "The cellular attachment disappeared.",
                "proxy": {"ready": False},
            }
        static_apdu = bool(modem_capabilities.get("sim_apdu"))
        apdu_paused = bool(getattr(self.modem, "sim_via_mbn", False) and
                           values.get("data_active"))
        values["sim_apdu_ready"] = bool(static_apdu and not apdu_paused)
        values["sim_apdu_error"] = (
            "SIM APDU access is paused while Windows cellular data owns the SIM"
            if static_apdu and apdu_paused else
            "SIM APDU access is unavailable" if not static_apdu else "")
        self.modem.operator = str(values.get("operator") or "")
        return values

    @staticmethod
    def _windows_profile_names(output: str) -> list[str]:
        names = [value.strip() for value in re.findall(
            r"^\s*(?:All User Profile|所有用户配置文件)\s*:\s*(.+?)\s*$",
            output or "", re.I | re.M) if value.strip()]
        if names:
            return names
        after_separator = False
        for line in str(output or "").splitlines():
            value = line.strip()
            if value and set(value) == {"-"}:
                after_separator = True
            elif after_separator and value and value.lower() not in {"<none>", "<无>"}:
                names.append(value)
        return names

    def _modem_profile_candidates(self) -> list[dict]:
        """Read non-service PDP contexts and any active network-assigned APN.

        If the modem reports no stored contexts, also query ``AT+CGCONTRDP`` for any
        currently active context assigned by the network.  This is a read-only fallback that
        lets the operator see the APN the network chose, which is often enough to create a
        matching profile without guessing.  3GPP TS 27.007 mandates the first three fields
        are ``cid``, ``pdp_type`` and ``apn``; the rest is variable and ignored here.
        """
        reserved = {"ims", "sos", "emergency", "mms", "supl", "xcap"}
        values: list[dict] = []
        try:
            raw = self.modem._at("AT+CGDCONT?").decode("utf-8", "replace")
        except Exception:
            raw = ""
        for match in re.finditer(
                r'^\s*\+CGDCONT:\s*(\d+)\s*,\s*"([^"]*)"\s*,\s*"([^"]*)"',
                raw, re.I | re.M):
            cid, pdp_type, apn = int(match.group(1)), match.group(2).strip(), match.group(3).strip()
            leaf = apn.lower().split(".", 1)[0]
            if apn and leaf not in reserved and not any(item["apn"] == apn for item in values):
                values.append({"id": f"pdp-{cid}", "source": "modem", "cid": cid,
                               "name": f"{apn} (CID {cid})", "apn": apn,
                               "pdp_type": pdp_type or "IP", "auth": "NONE",
                               "username": ""})
        if values:
            return values
        try:
            raw = self.modem._at("AT+CGCONTRDP?").decode("utf-8", "replace")
        except Exception:
            return values
        for match in re.finditer(
                r'^\s*\+CGCONTRDP:\s*(\d+)\s*,\s*"([^"]*)"\s*,\s*"([^"]*)"',
                raw, re.I | re.M):
            cid, pdp_type, apn = int(match.group(1)), match.group(2).strip(), match.group(3).strip()
            leaf = apn.lower().split(".", 1)[0]
            if apn and leaf not in reserved and not any(item["apn"] == apn for item in values):
                values.append({"id": f"network-{cid}", "source": "network", "cid": cid,
                               "name": f"{apn} (network assigned)", "apn": apn,
                               "pdp_type": pdp_type or "IP", "auth": "NONE",
                               "username": ""})
        return values

    def _modem_apn_candidates(self) -> list[str]:
        return [item["apn"] for item in self._modem_profile_candidates()]

    def _provider_apn_candidates(self) -> list[dict]:
        """Look up public-domain APN candidates for the current SIM's MCC/MNC.

        mobile-broadband-provider-info is a Creative Commons public-domain dataset used by
        NetworkManager/ModemManager.  Entries are keyed by MCC/MNC and limited to APNs whose
        usage is ``internet``.  The returned list is advisory: it is never used to auto-
        provision, only to offer the operator a starting point.
        """
        imsi = str(getattr(self.modem, "imsi", "") or "")
        return lookup_by_imsi(imsi) if imsi else []

    def _apn_guidance(self, names: list[str]) -> str:
        """Explain exactly why no data profile could be selected.

        "No mobile-broadband profile is configured" is a dead end for a SIM whose operator is
        not in any local APN database: it neither says whether the modem offered candidates
        nor which network is being attached.  Report the observed facts instead, so the
        operator can supply the APN once rather than retry blindly.  This uses only values
        already known to this Agent; a connect attempt must not add fresh AT round trips.
        """
        candidates = self._modem_apn_candidates()
        imsi = str(getattr(self.modem, "imsi", "") or "")
        operator = str(getattr(self.modem, "operator", "") or "")
        # MCC plus the first MNC digits identify the network, not the subscriber.  The MNC
        # length is carrier specific, so present the prefix without asserting a split.
        network = f"MCC/MNC {imsi[:5]}" if len(imsi) >= 5 else "an unidentified network"
        if operator:
            network = f"{operator} ({network})"
        if len(names) > 1:
            return ("More than one mobile-broadband profiles exist "
                    f"({', '.join(names[:5])}) for {network}; select one in MDD.")[:300]
        if len(candidates) > 1:
            return (f"This modem reports {len(candidates)} APN candidates "
                    f"({', '.join(candidates[:5])}) for {network}; select or enter one under "
                    "4G network / APN in MDD.")[:300]
        provider = [item["apn"] for item in self._provider_apn_candidates()]
        if not candidates and provider:
            return (f"No mobile-broadband profile is configured for {network}, but the public "
                    f"APN database suggests {', '.join(provider[:5])}. Select one or enter the "
                    "APN supplied by this SIM's carrier under 4G network / APN in MDD.")[:300]
        return ("No mobile-broadband profile is configured, and this modem reports no usable "
                f"APN for {network}. Enter the APN supplied by this SIM's carrier under "
                "4G network / APN in MDD.")[:300]

    def _cellular_profiles(self) -> dict:
        interface = self._cellular_interface()
        if not interface:
            raise ModemError("No mobile-broadband interface matches this modem.")
        suggested_profiles = self._modem_profile_candidates()
        existing_apns = {item["apn"] for item in suggested_profiles}
        for item in self._provider_apn_candidates():
            if item["apn"] not in existing_apns:
                suggested_profiles.append({
                    "id": f"provider-{item['apn']}",
                    "source": "provider",
                    "name": f"{item['name']} ({item['apn']})",
                    "apn": item["apn"],
                    "pdp_type": "IP",
                    "auth": "NONE",
                    "username": "",
                })
                existing_apns.add(item["apn"])
        suggested = [item["apn"] for item in suggested_profiles]
        if os.name == "nt":
            result = subprocess.run(
                ["netsh", "mbn", "show", "profiles", f"interface={interface}"],
                capture_output=True, text=True, timeout=10, check=False)
            names = self._windows_profile_names(result.stdout)
            # Windows returns exit code 1 with completely empty output when the interface is
            # valid but has no profiles, and may also return 1 while printing a valid list.
            # Parsed profiles are the authoritative postcondition; only unparseable diagnostic
            # output is a real failure.
            if result.returncode and not names and str(result.stderr or result.stdout).strip():
                raise ModemError(str(result.stderr or result.stdout).strip()[:300])
        elif sys.platform == "darwin":
            return {"ok": True, "platform": "macos", "supported": False,
                    "system_managed": True, "suggested_apns": suggested,
                    "suggested_profiles": suggested_profiles, "profiles": [],
                    "error": "This macOS adapter must be provisioned by its system or vendor network service."}
        else:
            result = subprocess.run(
                ["nmcli", "-t", "-f", "NAME,TYPE", "connection", "show"],
                capture_output=True, text=True, timeout=10, check=False)
            if result.returncode:
                raise ModemError(str(result.stderr or result.stdout).strip()[:300])
            names = [line.rsplit(":", 1)[0].replace("\\:", ":") for line in
                     result.stdout.splitlines() if line.rsplit(":", 1)[-1] == "gsm"]
        return {"ok": True, "platform": "windows" if os.name == "nt" else "linux",
                "supported": True, "selected": self.selected_profile,
                "suggested_apns": suggested, "suggested_profiles": suggested_profiles,
                "profiles": [{"name": name} for name in names]}

    def _save_cellular_profile(self, params: dict) -> dict:
        interface = self._cellular_interface()
        name = str(params.get("name") or "").strip()
        apn = str(params.get("apn") or "").strip()
        auth = str(params.get("auth") or "NONE").upper()
        username = str(params.get("username") or "")
        password = str(params.get("password") or "")
        if not interface:
            raise ModemError("No mobile-broadband interface matches this modem.")
        if not name or len(name) > 100 or not apn or len(apn) > 100:
            raise ModemError("profile name and APN are required and must not exceed 100 characters")
        if auth not in {"NONE", "PAP", "CHAP", "MSCHAPV2"}:
            raise ModemError("unsupported mobile-broadband authentication method")
        if os.name == "nt":
            content = windows_mbn_profile_xml(
                name, self.modem.imsi, apn, auth, username, password)
            path = ""
            try:
                with tempfile.NamedTemporaryFile(
                        mode="w", suffix=".xml", encoding="utf-8", delete=False) as handle:
                    handle.write(content)
                    path = handle.name
                result = subprocess.run(
                    ["netsh", "mbn", "add", "profile", f"interface={interface}",
                     f"name={path}"], capture_output=True, text=True, timeout=20, check=False)
            finally:
                if path:
                    try:
                        os.unlink(path)
                    except OSError:
                        pass
        elif sys.platform == "darwin":
            raise ModemError("Mobile-broadband profile management is not available for this macOS adapter.")
        else:
            existing = {item["name"] for item in self._cellular_profiles()["profiles"]}
            command = (["nmcli", "connection", "modify", name] if name in existing else
                       ["nmcli", "connection", "add", "type", "gsm", "ifname", "*",
                        "con-name", name])
            command += ["gsm.apn", apn, "gsm.username", username,
                        "gsm.password", password, "gsm.auth-type",
                        {"NONE": "none", "PAP": "pap", "CHAP": "chap",
                         "MSCHAPV2": "mschapv2"}[auth]]
            result = subprocess.run(command, capture_output=True, text=True,
                                    timeout=20, check=False)
        if result.returncode:
            # netsh can create/update the MBN profile and still exit 1 with no output. Verify
            # the postcondition before reporting failure so a successful side effect is never
            # presented as safe to retry.
            if os.name == "nt":
                current = {item["name"] for item in self._cellular_profiles()["profiles"]}
                if name not in current:
                    detail = str(result.stderr or result.stdout).strip()
                    raise ModemError((detail or
                                      f"netsh mbn add profile exited {result.returncode} without diagnostic output")[:300])
            else:
                raise ModemError(str(result.stderr or result.stdout).strip()[:300])
        self.selected_profile = name
        return {"ok": True, "name": name, "apn": apn,
                "platform": "windows" if os.name == "nt" else "linux"}

    def _connect_cellular(self, interface: str) -> str:
        """Ask the platform to activate an existing operator-approved data profile."""
        try:
            if os.name == "nt":
                # Windows keeps data enablement and roaming permission outside the MBN
                # profile.  A valid APN profile can therefore still fail with 0x139f when
                # the Settings app left Internet roaming at "Home carrier only".
                for command in (
                    ["netsh", "mbn", "set", "dataenablement", f"interface={interface}",
                     "profileset=internet", "mode=yes"],
                    ["netsh", "mbn", "set", "dataroamcontrol", f"interface={interface}",
                     "profileset=internet", "state=all" if self.allow_roaming else "state=none"],
                ):
                    policy = subprocess.run(command, capture_output=True, text=True,
                                            timeout=15, check=False)
                    # Like profile creation, current Windows netsh can apply these policy
                    # changes and still exit 1 with no output.  A real diagnostic remains a
                    # failure; an empty one is followed by the connect postcondition below.
                    detail = str(policy.stderr or policy.stdout).strip()
                    if policy.returncode and detail:
                        return detail[:300]
                listing = subprocess.run(
                    ["netsh", "mbn", "show", "profiles", f"interface={interface}"],
                    capture_output=True, text=True, timeout=10, check=False)
                names = self._windows_profile_names(listing.stdout)
                profile = self.selected_profile or (names[0] if len(names) == 1 else "")
                if not profile and not names:
                    candidates = self._modem_apn_candidates()
                    if len(candidates) == 1:
                        profile = f"MDD-Auto-{self.modem.iccid[-4:]}"
                        self._save_cellular_profile({"name": profile, "apn": candidates[0],
                                                     "auth": "NONE"})
                if not profile:
                    return self._apn_guidance(names)
                platform_provider = getattr(self.modem, "platform_provider", None)
                if platform_provider:
                    native = platform_provider.connect(profile, interface)
                    if native.get("ok"):
                        self.selected_profile = profile
                        return ""
                    detail = ["Windows MBN connection failed"]
                    if native.get("hresult"):
                        detail.append(f"HRESULT {native['hresult']}")
                    if native.get("network_error"):
                        detail.append(f"network cause {native['network_error']}")
                    if native.get("activation_state"):
                        detail.append(str(native["activation_state"]))
                    if native.get("error"):
                        detail.append(str(native["error"]))
                    return ": ".join(detail)[:300]
                result = subprocess.run(
                    ["netsh", "mbn", "connect", f"interface={interface}",
                     "connmode=name", f"name={profile}"],
                    capture_output=True, text=True, timeout=30, check=False)
            elif sys.platform == "darwin":
                return "Automatic cellular activation is not available for this macOS adapter."
            else:
                result = subprocess.run(["nmcli", "device", "connect", interface],
                                        capture_output=True, text=True, timeout=30, check=False)
            return "" if result.returncode == 0 else str(result.stderr or result.stdout).strip()[:300]
        except (OSError, subprocess.TimeoutExpired) as exc:
            return str(exc)[:300]

    def _disconnect_cellular(self, interface: str) -> str:
        try:
            if os.name == "nt":
                platform_provider = getattr(self.modem, "platform_provider", None)
                if platform_provider:
                    current = platform_provider.status()
                    if not current.get("data_active"):
                        return ""
                    native = platform_provider.disconnect()
                    if native.get("ok"):
                        return ""
                    detail = str(native.get("error") or native.get("hresult") or
                                 "Windows MBN disconnect failed")
                    if not platform_provider.status().get("data_active"):
                        return ""
                    return detail[:300]
                result = subprocess.run(
                    ["netsh", "mbn", "disconnect", f"interface={interface}"],
                    capture_output=True, text=True, timeout=20, check=False)
            elif sys.platform == "darwin":
                result = subprocess.run(["networksetup", "-setnetworkserviceenabled",
                                         interface, "off"], capture_output=True, text=True,
                                        timeout=20, check=False)
            else:
                result = subprocess.run(["nmcli", "device", "disconnect", interface],
                                        capture_output=True, text=True, timeout=20, check=False)
            if result.returncode == 0:
                return ""
            detail = str(result.stderr or result.stdout).strip()[:300]
            # Windows reports "Context Not Activated" when disconnecting an already-off
            # profile.  The missing interface address is the authoritative postcondition.
            if os.name == "nt" and not self._cellular_ip(interface):
                return ""
            return detail
        except (OSError, subprocess.TimeoutExpired) as exc:
            return str(exc)[:300]

    def _watch_isolation(self):
        """Tear down every managed data socket if the privileged guard disappears."""
        while not self.stop.wait(0.5):
            if self.isolation.active:
                self._isolation_armed = True
                # The native guard and source-bound sockets are the security authorities.
                # Re-running platform interface discovery here is both redundant and unsafe:
                # Windows MBN can return an empty list during routine provider refreshes,
                # which used to flap a perfectly guarded bearer. If the interface genuinely
                # disappears, its bound source address cannot fall back to another route and
                # the regular status path closes the proxy when the address is gone.
                continue
            elif not self._isolation_armed:
                continue
            server, self.socks_server = self.socks_server, None
            if server:
                server.close()
            interface = self.isolation.interface or self._cellular_interface()
            if interface:
                self._disconnect_cellular(interface)
            self.isolation.close()
            self._isolation_armed = False

    def _cellular_failure(self, interface: str, error: str, isolation: dict) -> dict:
        server, self.socks_server = self.socks_server, None
        if server:
            server.close()
        self._disconnect_cellular(interface)
        self.isolation.close()
        self._isolation_armed = False
        return {"ok": False, "status": "unavailable", "unavailable": True,
                "error": error, "isolation": {**isolation, "ready": False},
                "proxy": {"ready": False}}

    def _cellular_ensure(self, params: dict) -> dict:
        if "allow_roaming" in params:
            self.allow_roaming = bool(params.get("allow_roaming"))
        port = int(params.get("port") or self.args.socks_port)
        interface = self._cellular_interface()
        if not interface:
            return {"ok": False, "status": "unavailable", "unavailable": True,
                    "error": "No mobile-broadband interface matches this modem.",
                    "proxy": {"ready": False}}
        network = self._status()
        if network.get("registration") == "roaming" and not self.allow_roaming:
            self._disconnect_cellular(interface)
            return {"ok": False, "status": "unavailable", "unavailable": True,
                    "error": "Data roaming is disabled for this SIM.",
                    "registration": "roaming", "roaming_allowed": False,
                    "proxy": {"ready": False}}
        # Isolation must be installed before the OS is allowed to establish a default route;
        # otherwise there is a leak window between connection and policy installation.
        already_isolated = self.isolation.active
        isolation = self.isolation.ensure(interface, "")
        if not isolation.get("ready"):
            return {"ok": False, "status": "unavailable", "unavailable": True,
                    "error": isolation.get("error") or "Cellular isolation is not ready.",
                    "isolation": isolation, "proxy": {"ready": False}}
        # Connections that existed before MDD took ownership are not grandfathered in.  Cycle
        # the data context after WFP/netns/pf is active so every new flow is classified.
        if self._status()["data"] == "connected" and not already_isolated:
            problem = self._disconnect_cellular(interface)
            if problem:
                self.isolation.close()
                return {"ok": False, "status": "unavailable", "unavailable": True,
                        "error": f"Cannot reset the pre-existing cellular connection: {problem}",
                        "isolation": {"ready": False, "mode": "strict"},
                        "proxy": {"ready": False}}
            self._isolation_armed = True
        if self._status()["data"] != "connected":
            problem = self._connect_cellular(interface)
            if problem:
                return self._cellular_failure(interface, problem, isolation)
        source_ip = self._cellular_ip(interface)
        if not source_ip:
            return self._cellular_failure(
                interface, "The cellular interface has no usable IPv4 address.", isolation)
        isolation = self.isolation.ensure(interface, source_ip)
        if not isolation.get("ready"):
            return self._cellular_failure(
                interface, isolation.get("error") or "Cellular isolation is not ready.",
                isolation)
        if not self.socks_server or not self.socks_server.ready:
            try:
                self.socks_server = SocksServer("0.0.0.0", port, source_ip)
                self.socks_server.start()
            except OSError as exc:
                return self._cellular_failure(interface, str(exc)[:300], isolation)
        ready = self.socks_server.ready
        if not ready:
            return self._cellular_failure(
                interface, "Embedded SOCKS server failed", isolation)
        return {"ok": True, "status": "ready",
                "proxy": {"ready": ready, "host": self._advertise_host(),
                          "port": self.socks_server.port,
                          "udp": True}, "isolation": isolation,
                "error": None}

    def _cellular_disable(self) -> dict:
        server, self.socks_server = self.socks_server, None
        if server:
            server.close()
        interface = self.isolation.interface or self._cellular_interface()
        problem = self._disconnect_cellular(interface) if interface else ""
        self.isolation.close()
        self._isolation_armed = False
        return {"ok": not bool(problem), "status": "off", "data": "disconnected",
                "proxy": {"ready": False}, "error": problem or None}

    def _reverse_tunnel_source_ip(self) -> str:
        """Return the source address already admitted by the fail-closed data plane.

        The isolation watcher owns continuous liveness checking.  Re-running the Windows MBN
        and IP discovery commands for every SOCKS connection is both redundant and slow enough
        to race the gateway's tunnel handshake timeout.  The established SOCKS listener and
        guard therefore form one immutable admission snapshot; a dead guard tears both down,
        while a stale source address simply makes the bound outbound connect fail closed.
        """
        interface = self.isolation.interface
        server = self.socks_server
        if not server or not server.ready:
            raise OSError("cellular proxy is not enabled")
        if not self.isolation.active or not interface:
            raise OSError("cellular isolation is not active for this interface")
        source_ip = str(server.source_ip or "")
        if not source_ip:
            raise OSError("cellular proxy has no isolated source address")
        return source_ip

    def _open_reverse_tunnel(self, message: dict, session_id: str) -> None:
        tunnel_id = str(message.get("id") or "")
        mode = str(message.get("mode") or "tcp")
        host = str(message.get("host") or "")
        port = int(message.get("port") or 0)
        if not tunnel_id or mode not in {"tcp", "udp"}:
            return

        def bridge():
            local = tunnel = None
            try:
                source_ip = self._reverse_tunnel_source_ip()
                if mode == "tcp":
                    target = socket.getaddrinfo(host, port, socket.AF_INET,
                                                socket.SOCK_STREAM)[0][-1]
                    local = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
                    local.bind((source_ip, 0))
                    local.settimeout(15)
                    local.connect(target)
                    local.settimeout(None)
                else:
                    local = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                    local.bind((source_ip, 0))
                query = urllib.parse.urlencode({"token": self.args.token,
                                                "session_id": session_id,
                                                "tunnel_id": tunnel_id})
                url = (f"wss://{self.args.host}:{self.args.gateway_port}"
                       f"/mdd/api/agent/modem/tunnel?{query}")
                tunnel = websocket.create_connection(url, timeout=20,
                                                      sslopt={"cert_reqs": 0})
                if mode == "tcp":
                    def upload():
                        try:
                            while True:
                                data = local.recv(65536)
                                if not data:
                                    break
                                tunnel.send_binary(data)
                        except Exception:
                            pass
                        finally:
                            try:
                                tunnel.close()
                            except Exception:
                                pass

                    threading.Thread(target=upload, name="modem-tunnel-upload", daemon=True).start()
                    while True:
                        data = tunnel.recv()
                        if not isinstance(data, bytes) or not data:
                            break
                        local.sendall(data)
                else:
                    def upload_udp():
                        try:
                            while True:
                                payload, remote = local.recvfrom(65535)
                                tunnel.send_binary(
                                    b"\x00\x00\x00" + _encoded_address(remote[0], remote[1]) + payload)
                        except Exception:
                            pass

                    threading.Thread(target=upload_udp, name="modem-tunnel-udp", daemon=True).start()
                    while True:
                        packet = tunnel.recv()
                        if not isinstance(packet, bytes) or len(packet) < 10 or packet[:3] != b"\0\0\0":
                            break
                        target_host, target_port, offset = _packet_address(packet)
                        local.sendto(packet[offset:], (target_host, target_port))
            except Exception as exc:
                log.warning("Reverse cellular tunnel failed: %s", exc)
            finally:
                if local:
                    try:
                        local.close()
                    except OSError:
                        pass
                if tunnel:
                    try:
                        tunnel.close()
                    except Exception:
                        pass

        threading.Thread(target=bridge, name="modem-reverse-tunnel", daemon=True).start()

    def execute(self, method: str, params: dict) -> dict:
        if method.startswith("call.") and not self.modem.capabilities["call"]:
            return {"ok": False, "unavailable": True, "status": "unsupported",
                    "error": "The selected modem provider exposes no call-signalling capability."}
        if method in {"call.dial", "call.answer", "call.dtmf"}:
            readiness = self.modem.sms_submit_readiness(force=True)
            if readiness.get("ready") is False:
                return {"ok": False, "unavailable": True, "status": "unavailable",
                        "error": str(readiness.get("reason") or
                                     "Cellular call bearer is unavailable")}
        if method == "sms.list":
            return {"ok": True, "messages": self.modem.sms_list()}
        if method == "sms.send":
            if not self.modem.capabilities["sms"]:
                return {"ok": False, "unavailable": True,
                        "error": "No installed modem provider currently exposes SMS."}
            return self.modem.sms_send(str(params.get("to") or ""), str(params.get("body") or ""))
        if method == "sms.ack":
            sms_id = str(params.get("id") or "")
            if not sms_id:
                raise ModemError("SMS identifier is required")
            fingerprint = str(params.get("fingerprint") or "")
            ack_key = f"{sms_id}:{fingerprint}"
            if ack_key not in self.acked_sms:
                current = next((item for item in self.modem.sms_list()
                                if item["id"] == sms_id), None)
                if not current:
                    self.acked_sms.add(ack_key)
                    return {"ok": True, "already_absent": True}
                if fingerprint and current.get("fingerprint") != fingerprint:
                    return {"ok": False, "status": "stale",
                            "error": "SMS index now identifies a different message"}
                if self.modem.platform_provider:
                    result = self.modem.platform_provider.sms_delete(sms_id)
                    if not result.get("ok"):
                        raise ModemError(str(result.get("hresult") or "SMS delete failed"))
                else:
                    self.modem._at(f"AT+CMGD={sms_id}")
                self.acked_sms.add(ack_key)
            return {"ok": True}
        if method == "call.dial":
            number = str(params.get("to") or "")
            if not re.fullmatch(r"\+?\d{1,32}", number):
                raise ModemError("invalid telephone number")
            if self.modem.platform_provider and hasattr(
                    self.modem.platform_provider, "call_dial"):
                return self.modem.platform_provider.call_dial(number)
            self.modem._at(f"ATD{number};")
            return {"ok": True, "status": "dialing", "audio": False,
                    "audio_error": "This modem exposes no usable USB audio endpoint."}
        if method == "call.answer":
            if self.modem.platform_provider and hasattr(
                    self.modem.platform_provider, "call_answer"):
                return self.modem.platform_provider.call_answer()
            self.modem._at("ATA")
            return {"ok": True, "status": "active", "audio": False}
        if method == "call.hangup":
            if self.modem.platform_provider and hasattr(
                    self.modem.platform_provider, "call_hangup"):
                return self.modem.platform_provider.call_hangup()
            self.modem._at("ATH")
            return {"ok": True, "status": "ended", "audio": False}
        if method == "call.status":
            if self.modem.platform_provider and hasattr(
                    self.modem.platform_provider, "call_status"):
                return self.modem.platform_provider.call_status()
            raw = self.modem._at("AT+CLCC").decode("ascii", "replace")
            match = re.search(r'\+CLCC:\s*\d+,(\d+),(\d+),\d+,\d+(?:,"([^"]*)")?', raw)
            states = {0: "active", 1: "held", 2: "dialing", 3: "ringing-out",
                      4: "ringing-in", 5: "waiting"}
            audio_ready = bool(self.modem.capabilities.get("call_audio") and
                               self.modem.call_audio_probe.ready)
            return {"ok": True, "status": states.get(int(match.group(2)), "unknown") if match else "idle",
                    "direction": "out" if match and match.group(1) == "0" else "in",
                    "number": match.group(3) if match and match.group(3) else "",
                    "audio": audio_ready,
                    "audio_error": "" if audio_ready else self.modem.call_audio_probe.reason}
        if method == "call.dtmf":
            digits = str(params.get("digits") or "")
            if not re.fullmatch(r"[0-9A-D*#]+", digits, re.I):
                raise ModemError("invalid DTMF digits")
            if self.modem.platform_provider and hasattr(
                    self.modem.platform_provider, "call_dtmf"):
                return self.modem.platform_provider.call_dtmf(digits)
            for digit in digits:
                self.modem._at(f"AT+VTS={digit}")
            return {"ok": True}
        if method == "audio.open":
            controller = getattr(self.modem, "call_audio_controller", None)
            if not self.modem.capabilities.get("call_audio") or not controller:
                return {"ok": False, "unavailable": True, "status": "unsupported",
                        "error": self.modem.call_audio_probe.reason or
                                 "No compatible call-audio backend was detected."}
            call_id = str(params.get("call_id") or "")
            token = str(params.get("token") or "")
            query = urllib.parse.urlencode({"call_id": call_id})
            media_url = (f"wss://{self.args.host}:{self.args.gateway_port}"
                         f"/mdd/api/agent/modem/media?{query}")
            tls_pin = str(getattr(self.args, "pin", "") or
                          load_pin_store().get(self.args.host) or "")
            return controller.open(call_id, media_url, token, tls_pin)
        if method == "audio.close":
            controller = getattr(self.modem, "call_audio_controller", None)
            return (controller.close(str(params.get("call_id") or "")) if controller else
                    {"ok": True, "closed": False})
        if method == "cellular.status":
            return {"ok": True, **self._status()}
        if method == "cellular.profile.list":
            return self._cellular_profiles()
        if method == "cellular.profile.save":
            return self._save_cellular_profile(params)
        if method == "cellular.ensure":
            return self._cellular_ensure(params)
        if method == "cellular.disable":
            return self._cellular_disable()
        if method == "cellular.roaming.set":
            self.allow_roaming = bool(params.get("enabled"))
            if os.name == "nt":
                interface = self._cellular_interface()
                if not interface:
                    return {"ok": False, "status": "unavailable",
                            "error": "No mobile-broadband interface matches this modem."}
                policy = subprocess.run(
                    ["netsh", "mbn", "set", "dataroamcontrol",
                     f"interface={interface}", "profileset=internet",
                     "state=all" if self.allow_roaming else "state=none"],
                    capture_output=True, text=True, timeout=15, check=False)
                detail = str(policy.stderr or policy.stdout).strip()
                if policy.returncode and detail:
                    return {"ok": False, "status": "unavailable", "error": detail[:300],
                            "roaming_allowed": self.allow_roaming}
            status = self._status()
            if status.get("registration") == "roaming" and not self.allow_roaming:
                result = self._cellular_disable()
                result.update({"registration": "roaming", "roaming_allowed": False})
                return result
            return {"ok": True, "status": "on" if self.allow_roaming else "off",
                    "roaming_allowed": self.allow_roaming,
                    "registration": status.get("registration")}
        if method == "radio.set":
            enabled = bool(params.get("enabled"))
            if not enabled:
                self._cellular_disable()
            if os.name == "nt" and self.modem.sim_via_mbn:
                interface = self._cellular_interface()
                if not interface:
                    return {"ok": False, "status": "unavailable",
                            "error": "No mobile-broadband interface matches this modem."}
                result = subprocess.run(
                    ["netsh", "mbn", "set", "powerstate", f"interface={interface}",
                     "state=on" if enabled else "state=off"],
                    capture_output=True, text=True, timeout=20, check=False)
                detail = str(result.stderr or result.stdout).strip()
                if result.returncode and detail:
                    return {"ok": False, "status": "unavailable", "error": detail[:300],
                            "radio_enabled": not enabled}
                return {"ok": True, "status": "on" if enabled else "off",
                        "radio_enabled": enabled}
            self.modem._at("AT+CFUN=1" if enabled else "AT+CFUN=4")
            return {"ok": True, "status": "on" if enabled else "off",
                    "radio_enabled": enabled}
        raise ModemError(f"unsupported method {method}")

    def run(self):
        while not self.stop.is_set():
            registration_ready = getattr(self.modem, "registration_ready", None)
            if (not self.modem.connection or not self.modem.iccid or
                    (registration_ready is not None and
                     not registration_ready.is_set())):
                time.sleep(1)
                continue
            session_id = ""
            ws = None
            try:
                url = (f"wss://{self.args.host}:{self.args.gateway_port}"
                       f"{self.args.control_path}?token={urllib.parse.quote(self.args.token)}")
                ws = websocket.create_connection(url, timeout=20,
                                                 sslopt={"cert_reqs": 0})
                transport = getattr(ws, "sock", None)
                tls_socket = getattr(transport, "sock", transport)
                certificate = (tls_socket.getpeercert(binary_form=True)
                               if tls_socket and hasattr(tls_socket, "getpeercert") else None)
                if not certificate:
                    raise ModemError("modem control WSS did not expose its peer certificate")
                verify_or_pin_fingerprint(
                    self.args.host, certificate, explicit_pin=self.args.pin,
                    reset_pin=self.reset_pin)
                self.reset_pin = False
                ws.send(json.dumps({"version": 1, "type": "hello",
                                    "agent_id": self.args.agent_id, "modem_id": self.modem.imei,
                                    "imei": self.modem.imei, "iccid": self.modem.iccid,
                                    "imsi": self.modem.imsi,
                                    "phone": self.modem.msisdn,
                                    "model": self.modem.model,
                                    "firmware": self.modem.firmware,
                                    "capabilities": {
                                        "sms": self.modem.capabilities["sms"],
                                        # A callable voice service requires both the control
                                        # function and a locally opened media transport. Keep
                                        # call_control for diagnostics, but do not register a
                                        # signalling-only attachment as usable voice.
                                        "call_control": self.modem.capabilities["call"],
                                        "call_signalling": bool(
                                            self.modem.capabilities["call"] and
                                            self.modem.capabilities["call_audio"]),
                                        "call_audio": self.modem.capabilities["call_audio"],
                                        "sim_apdu": bool(self.modem.capabilities.get("sim_apdu") or
                                                         not self.modem.sim_via_mbn),
                                        "cellular_data": self.modem.capabilities["cellular_data"],
                                        "socks5_udp": True,
                                    },
                                    "status": self._status()}))
                ack = json.loads(ws.recv())
                session_id = ack["session_id"]
                ws.settimeout(15)
                last_status = time.monotonic()
                log.info("Modem control online (session %s)", session_id[:8])
                send_lock = threading.Lock()

                def send(value: dict):
                    with send_lock:
                        ws.send(json.dumps(value))

                status_pending = threading.Event()

                def publish_status():
                    try:
                        # Platform providers and mutating RPCs may share one native helper or
                        # device handle. Build the snapshot behind the operation lock, but do
                        # it on this worker so the WebSocket receive loop remains responsive.
                        with self.operation_lock:
                            snapshot = self._status()
                        send({"version": 1, "type": "status",
                              "session_id": session_id,
                              "modem_id": self.modem.imei,
                              "status": snapshot})
                    except Exception:
                        # The control loop owns reconnects. A late status result from an old
                        # transport must not interfere with the next session.
                        pass
                    finally:
                        status_pending.clear()

                def schedule_status(status_executor):
                    nonlocal last_status
                    # Windows MBN and vendor status providers can occasionally take seconds.
                    # Keep that work off the only thread receiving RPC and tunnel.open frames;
                    # one in-flight sample is enough and prevents an unbounded stale queue.
                    if not status_pending.is_set():
                        status_pending.set()
                        status_executor.submit(publish_status)
                    last_status = time.monotonic()

                def perform(message: dict):
                    operation_id = str(message.get("operation_id") or "")
                    method = str(message.get("method") or "")
                    try:
                        with self.operation_lock:
                            if operation_id and operation_id in self.results:
                                result = self.results[operation_id]
                            else:
                                result = self.execute(method, message.get("params") or {})
                                if operation_id:
                                    self.results[operation_id] = result
                                    if len(self.results) > 256:
                                        self.results.pop(next(iter(self.results)))
                        response = {"version": 1, "type": "rpc.result", "id": message.get("id"),
                                    "session_id": session_id, "modem_id": self.modem.imei,
                                    "ok": True, "result": result}
                    except Exception as exc:
                        detail = str(exc).strip() or f"{type(exc).__name__} without diagnostic details"
                        response = {"version": 1, "type": "rpc.result", "id": message.get("id"),
                                    "session_id": session_id, "modem_id": self.modem.imei,
                                    "ok": False, "error": detail}
                    if method in {"cellular.ensure", "cellular.disable"}:
                        self.data_reconciled.set()
                        if (method == "cellular.ensure" and response.get("ok") and
                                (response.get("result") or {}).get("ok") and
                                ((response.get("result") or {}).get("proxy") or {}).get("ready")):
                            self.cellular_active.set()
                        elif method == "cellular.disable":
                            self.cellular_active.clear()
                    try:
                        send(response)
                    except Exception:
                        # The operation may have completed after this transport session ended.
                        # Keep its operation_id result in memory; never resend a paid action.
                        pass

                with concurrent.futures.ThreadPoolExecutor(
                        max_workers=1, thread_name_prefix="modem-rpc") as executor, \
                        concurrent.futures.ThreadPoolExecutor(
                            max_workers=1, thread_name_prefix="modem-status") as status_executor:
                    while self.modem.connection and not self.stop.is_set():
                        try:
                            message = json.loads(ws.recv())
                        except websocket.WebSocketTimeoutException:
                            schedule_status(status_executor)
                            continue
                        if (message.get("type") == "rpc.request" and
                                message.get("session_id") == session_id):
                            executor.submit(perform, message)
                        elif (message.get("type") == "tunnel.open" and
                              message.get("session_id") == session_id):
                            self._open_reverse_tunnel(message, session_id)
                        if time.monotonic() - last_status >= 15:
                            schedule_status(status_executor)
            except Exception as exc:
                log.warning("Modem control connection failed: %s", exc)
            finally:
                if ws:
                    ws.close()
            time.sleep(self.args.retry)


def run(args):
    if not args.no_pcsc:
        threading.Thread(
            target=run_pcsc_reader_supervisor,
            args=(args.host, args.gateway_port),
            kwargs={
                "token": args.token,
                "use_wss": True,
                "ws_path": args.path,
                "explicit_pin": args.pin,
                "reset_pin": args.reset_pin,
                "retry_delay": args.retry,
                "reader_filter": args.pcsc_reader,
            },
            name="pcsc-supervisor",
            daemon=True,
        ).start()
    modem = ModemCard(
        args.port, args.baud,
        gammu=getattr(args, "gammu", ""),
        gammu_port=getattr(args, "gammu_port", ""),
        call_audio_helper=getattr(args, "call_audio_helper", ""),
    )
    control = ModemControl(args, modem)
    threading.Thread(target=control.run, name="modem-control", daemon=True).start()
    reset_pin = args.reset_pin
    while True:
        while not modem.connection:
            if modem.connect():
                break
            time.sleep(args.retry)
        reader_name = args.name or f"3GPP modem {modem.imei[-6:]}"
        if modem.sim_via_mbn and not modem.capabilities.get("sim_apdu"):
            log.info("Windows WWAN owns the SIM; modem control remains online without VPCD APDU bridging")
            while (modem.connection and modem.sim_via_mbn and
                   not modem.capabilities.get("sim_apdu")):
                try:
                    modem._at("AT")
                except Exception:
                    modem.close()
                    break
                time.sleep(max(args.retry, 5.0))
            continue
        if (modem.sim_via_mbn and modem.capabilities.get("sim_apdu") and
                not control.data_reconciled.is_set()):
            log.info("Waiting for Windows cellular desired state before exposing SIM APDUs")
            if not control.data_reconciled.wait(90):
                log.warning("Cellular desired-state wait timed out; exposing SIM APDUs offline")
        if modem.sim_via_mbn and control.cellular_active.is_set():
            # This is a driver ownership boundary, not a missing feature.  The EC20 Windows
            # WWAN miniport invalidates its active MBN context when an auxiliary function opens
            # a USIM logical channel.  Keep the data exit stable and expose card APDUs only
            # after the persisted cellular switch has been turned off.
            log.info("Windows cellular data owns the SIM; VPCD APDUs are paused")
            while (modem.connection and control.cellular_active.is_set() and
                   not control.stop.is_set()):
                time.sleep(1)
            continue
        client = None
        try:
            client = connect_wss(
                args.host,
                args.gateway_port,
                path_with_card_id(args.path, reader_name, modem.iccid, modem.imei),
                token=args.token,
                explicit_pin=args.pin,
                reset_pin=reset_pin,
            )
            reset_pin = False
            log.info("Bridge online; forwarding AT+CSIM APDUs")
            while True:
                payload = client.recv_frame()
                if payload is None:
                    break
                if len(payload) == 1:
                    if payload[0] == VPCD_CTRL_ATR:
                        client.send_frame(ATR)
                    elif payload[0] in (VPCD_CTRL_OFF, VPCD_CTRL_ON, VPCD_CTRL_RESET):
                        modem.reset()
                    continue
                client.send_frame(modem.transmit(payload))
        except Exception as exc:
            log.warning("Gateway connection failed: %s", exc)
        finally:
            if client:
                client.close()
            if not modem.connection:
                modem.close()
        time.sleep(args.retry)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", default="10.44.0.14:8443")
    parser.add_argument("--port", default="auto", help="AT serial port, or auto")
    parser.add_argument("--baud", type=int, default=115200)
    parser.add_argument("--token", default="")
    parser.add_argument("--name", default="")
    parser.add_argument("--path", default="/mdd/api/vpcd/ws")
    parser.add_argument("--pin", default="")
    parser.add_argument("--reset-pin", action="store_true")
    parser.add_argument("--retry", type=float, default=3.0)
    parser.add_argument("--control-path", default="/mdd/api/agent/modem/ws")
    parser.add_argument("--agent-id", default=get_agent_id())
    parser.add_argument("--cellular-interface", default="",
                        help="mobile-broadband interface override (normally auto-detected by IMEI)")
    parser.add_argument("--advertise-host", default="",
                        help="gateway-reachable address override (normally route-detected)")
    parser.add_argument("--socks-port", type=int, default=0,
                        help="SOCKS listen port; 0 chooses a collision-free ephemeral port")
    parser.add_argument("--isolation-helper", default="",
                        help="bundled privileged cellular isolation guard override")
    parser.add_argument("--gammu", default="",
                        help="Gammu executable override (or set MDD_GAMMU)")
    parser.add_argument("--gammu-port", default="",
                        help="separate Gammu AT/Modem port override (or set MDD_GAMMU_PORT)")
    parser.add_argument("--call-audio-helper", default="",
                        help="bundled call-audio helper override (or set MDD_CALL_AUDIO_HELPER)")
    parser.add_argument("--pcsc-reader", default="",
                        help="optional name filter for external PC/SC readers; default manages all")
    parser.add_argument("--no-pcsc", action="store_true",
                        help="disable external PC/SC reader discovery")
    args = parser.parse_args()
    host, separator, port = args.server.rpartition(":")
    args.host = host if separator else args.server
    args.gateway_port = int(port) if separator else 8443
    if not acquire_process_lock(args.agent_id):
        log.error("Another MDD modem Agent with this agent-id is already running; exiting")
        return
    run(args)


if __name__ == "__main__":
    main()
