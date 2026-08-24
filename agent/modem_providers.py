"""Platform modem providers with a small, transport-neutral contract.

The business agent never parses platform CLI output or chooses a provider by modem model.
Providers are selected from an actually enumerated platform control plane and report only the
operations that control plane implements.
"""

from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import uuid
from typing import Callable

try:
    from sms_pdu import decode_deliver, encode_submit
except ModuleNotFoundError:
    from .sms_pdu import decode_deliver, encode_submit


class ProviderError(RuntimeError):
    """A structured platform-provider operation failed."""


class ProviderTimeout(ProviderError):
    """An operation may have reached the modem, but its result was not observed."""


_VOICE_CALL_STATES = {
    0: "active", 1: "held", 2: "dialing", 3: "ringing-out",
    4: "ringing-in", 5: "waiting",
}


def parse_clcc_voice(raw: bytes | str) -> dict:
    """Parse one fresh 3GPP +CLCC sample without relying on cached provider state."""
    text = raw.decode("ascii", "replace") if isinstance(raw, bytes) else str(raw or "")
    records = []
    for match in re.finditer(
            r'\+CLCC:\s*\d+,(\d+),(\d+),(\d+),\d+'
            r'(?:,"([^"]*)",\d+)?', text):
        direction, state, mode = map(int, match.group(1, 2, 3))
        if mode == 0:
            records.append((direction, state, match.group(4) or ""))
    observed_at = time.time()
    if not records:
        return {"ok": True, "status": "idle", "direction": "", "number": "",
                "fresh": True, "authoritative": True, "observed_at": observed_at,
                "state_source": "3gpp_clcc"}
    priority = {0: 0, 1: 1, 5: 2, 4: 3, 3: 4, 2: 5}
    direction, state, number = min(records, key=lambda item: priority.get(item[1], 99))
    return {"ok": True, "status": _VOICE_CALL_STATES.get(state, "unknown"),
            "direction": "out" if direction == 0 else "in", "number": number,
            "fresh": True, "authoritative": True, "observed_at": observed_at,
            "state_source": "3gpp_clcc"}


def verified_at_hangup(at: Callable[[str, float], bytes], *,
                       sleeper: Callable[[float], None] = time.sleep,
                       polls: int = 5, interval: float = 0.4,
                       terminal_samples: int = 2,
                       total_timeout: float = 35.0) -> dict:
    """Hang up and require fresh +CLCC terminal evidence, never just command ``OK``."""
    attempts = []
    deadline = time.monotonic() + max(0.1, float(total_timeout))

    def remaining(limit: float) -> float:
        return max(0.0, min(float(limit), deadline - time.monotonic()))

    def status() -> dict:
        budget = remaining(5.0)
        if budget <= 0:
            return {"ok": False, "status": "unknown", "fresh": False,
                    "authoritative": False, "state_source": "3gpp_clcc",
                    "error": "hangup verification deadline expired"}
        try:
            return parse_clcc_voice(at("AT+CLCC", budget))
        except Exception as exc:
            return {"ok": False, "status": "unknown", "fresh": False,
                    "authoritative": False, "state_source": "3gpp_clcc",
                    "error": str(exc)}

    def confirm_idle(first: dict) -> dict | None:
        if not (first.get("fresh") and first.get("status") == "idle"):
            return None
        last_idle = first
        for _ in range(max(1, terminal_samples) - 1):
            pause = remaining(interval)
            if pause <= 0:
                return None
            sleeper(pause)
            last_idle = status()
            if not (last_idle.get("fresh") and last_idle.get("status") == "idle"):
                return None
        return last_idle

    initial = status()
    confirmed = confirm_idle(initial)
    if confirmed:
        return {**confirmed, "terminal_confirmed": True, "strategy": "already_idle",
                "attempts": attempts, "audio": False}
    last = initial
    for command, strategy in (("AT+CHUP", "chup"), ("ATH", "chup_ath")):
        budget = remaining(15.0)
        if budget <= 0:
            break
        try:
            at(command, budget)
            attempts.append({"command": command, "accepted": True})
        except Exception as exc:
            attempts.append({"command": command, "accepted": False, "error": str(exc)})
        for _ in range(max(1, polls)):
            if remaining(5.0) <= 0:
                break
            last = status()
            confirmed = confirm_idle(last)
            if confirmed:
                return {**confirmed, "terminal_confirmed": True, "strategy": strategy,
                        "attempts": attempts, "audio": False}
            pause = remaining(interval)
            if pause <= 0:
                break
            sleeper(pause)
    return {"ok": False, "status": str(last.get("status") or "unknown"),
            "fresh": bool(last.get("fresh")),
            "authoritative": bool(last.get("authoritative")),
            "observed_at": last.get("observed_at"),
            "state_source": str(last.get("state_source") or "3gpp_clcc"),
            "terminal_confirmed": False, "strategy": "chup_ath",
            "attempts": attempts, "audio": False,
            "error": "The modem did not confirm that the physical call ended."}


class GammuCliProvider:
    """Process-isolated Gammu adapter for one otherwise unclaimed AT/Modem function.

    Gammu is GPLv2, so it remains a separately installed executable.  This adapter does not
    link or copy libGammu; it creates an ephemeral configuration and invokes the official CLI
    with argv-only inputs.  A later persistent companion can implement the same methods without
    changing the MDD domain contract.
    """

    name = "gammu"
    owner = "function_managed"

    def __init__(self, command: list[str], port: str, device_id: str,
                 runner: Callable[..., subprocess.CompletedProcess] = subprocess.run):
        self.command = list(command)
        self.port = str(port)
        self.device_id = str(device_id or "")
        self.runner = runner
        self.identity_snapshot: dict = {}
        self.sms_supported = False
        self.call_supported = False
        self._call_state = "idle"
        self._display_status_supported: bool | None = None
        self._sms_cache: list[dict] = []
        self._sms_cache_at = 0.0
        # Gammu opens the auxiliary AT function for each operation. Other providers that use
        # that same function (notably SIM APDU forwarding) share this lock, so Windows keeps
        # owning the independent MBN data function without two processes racing one COM port.
        self.operation_lock = threading.RLock()

    @staticmethod
    def command_path(configured: str = "") -> list[str]:
        value = str(configured or os.environ.get("MDD_GAMMU") or "").strip()
        if value:
            return [value]
        names = ("gammu.exe", "gammu") if os.name == "nt" else ("gammu",)
        candidates: list[Path] = []
        executable = Path(sys.executable).resolve()
        for name in names:
            candidates.append(executable.with_name(name))
        bundle = getattr(sys, "_MEIPASS", "")
        if bundle:
            for name in names:
                candidates.append(Path(bundle) / name)
        for path in candidates:
            if path.is_file():
                return [str(path)]
        found = next((shutil.which(name) for name in names if shutil.which(name)), None)
        return [found] if found else []

    @classmethod
    def discover(cls, device_id: str, ports: list[str], configured: str = "",
                 runner=subprocess.run):
        command = cls.command_path(configured)
        if not command:
            return None
        for port in dict.fromkeys(str(value) for value in ports if value):
            provider = cls(command, port, device_id, runner)
            try:
                identity = provider.identify()
            except ProviderError:
                continue
            if str(identity.get("imei") or "") != str(device_id or ""):
                continue
            try:
                provider._probe_features()
            except ProviderError:
                # Identity is authoritative for attachment; feature probing is allowed to
                # degrade without making the MBN data function disappear.
                pass
            return provider
        return None

    def _config_text(self) -> str:
        device = self.port + (":" if os.name == "nt" and not self.port.endswith(":") else "")
        return ("[gammu]\n"
                f"device = {device}\n"
                "connection = at115200\n"
                "synchronizetime = no\n"
                "logformat = nothing\n")

    def _invoke(self, *arguments: str, timeout: int = 45,
                input_text: str | None = None) -> subprocess.CompletedProcess:
        with self.operation_lock:
            return self._invoke_unlocked(*arguments, timeout=timeout, input_text=input_text)

    def _invoke_unlocked(self, *arguments: str, timeout: int = 45,
                         input_text: str | None = None) -> subprocess.CompletedProcess:
        config_path = ""
        try:
            with tempfile.NamedTemporaryFile("w", encoding="utf-8", suffix=".ini",
                                             prefix="mdd-gammu-", delete=False) as config:
                config.write(self._config_text())
                config_path = config.name
            environment = dict(os.environ)
            environment.update({"LANG": "C", "LC_ALL": "C"})
            result = self.runner(
                [*self.command, "-c", config_path, *arguments],
                capture_output=True, text=True, encoding="utf-8", errors="replace",
                input=input_text, timeout=timeout, check=False, env=environment,
            )
        except subprocess.TimeoutExpired as exc:
            raise ProviderTimeout(
                f"Gammu operation timed out: {arguments[0] if arguments else ''}") from exc
        except OSError as exc:
            raise ProviderError(f"Gammu executable is unavailable: {exc}") from exc
        finally:
            if config_path:
                try:
                    os.unlink(config_path)
                except OSError:
                    pass
        if result.returncode:
            streams = [str(value).strip() for value in (result.stdout, result.stderr)
                       if str(value or "").strip()]
            detail = "\n".join(streams) or f"exit {result.returncode}"
            raise ProviderError(f"Gammu {arguments[0] if arguments else 'operation'} failed: {detail[:300]}")
        return result

    @staticmethod
    def _fields(output: str) -> dict[str, str]:
        values = {}
        for line in str(output or "").splitlines():
            match = re.match(r"^\s*([^:]+?)\s*:\s*(.*?)\s*$", line)
            if match:
                values[match.group(1).strip().casefold()] = match.group(2).strip()
        return values

    def identify(self) -> dict:
        values = self._fields(self._invoke("identify", timeout=20).stdout)
        imei = values.get("imei", "")
        if not re.fullmatch(r"\d{14,17}", imei):
            raise ProviderError("Gammu did not return a valid modem IMEI")
        self.identity_snapshot = {
            "imei": imei,
            "imsi": values.get("sim imsi", ""),
            "manufacturer": values.get("manufacturer", ""),
            "model": values.get("model", ""),
            "firmware": values.get("firmware", ""),
            "port": self.port,
        }
        return dict(self.identity_snapshot)

    def _probe_features(self) -> None:
        output = str(self._invoke("monitor", "1", timeout=30).stdout or "")
        self.sms_supported = bool(re.search(
            r"Enabling info about incoming SMS\s*:\s*No error", output, re.I))
        self.call_supported = bool(re.search(
            r"Enabling info about calls\s*:\s*No error", output, re.I))

    @property
    def capabilities(self) -> ProviderCapabilities:
        return ProviderCapabilities(
            sms_list=self.sms_supported, sms_send=self.sms_supported,
            call_signalling=self.call_supported,
        )

    @staticmethod
    def _parse_messages(output: str) -> list[dict]:
        starts = list(re.finditer(
            r'^Location\s+(\d+),\s+folder\s+"([^"]*)"(.*)$', output or "", re.I | re.M))
        messages = []
        for position, match in enumerate(starts):
            end = starts[position + 1].start() if position + 1 < len(starts) else len(output)
            block = output[match.end():end]
            location = match.group(1)
            folder_name = match.group(2)
            folder = 1 if "inbox" in folder_name.casefold() else 2
            remote = re.search(r'^\s*Remote numbers?\s*:\s*"([^"]*)"', block, re.I | re.M)
            status = re.search(r'^\s*Status\s*:\s*(\S+)', block, re.I | re.M)
            sent = re.search(r'^\s*(?:Sent|Saved)\s*:\s*(.+?)\s*$', block, re.I | re.M)
            # Gammu prints decoded text after the metadata and a blank line.  Keep the final
            # paragraph only; metadata never becomes message content.
            paragraphs = [value.strip() for value in re.split(r"\r?\n\s*\r?\n", block)
                          if value.strip()]
            body = paragraphs[-1] if paragraphs else ""
            if re.search(r"^(?:SMS message|\d+ SMS parts)", body, re.I):
                body = ""
            state = (status.group(1).casefold() if status else "")
            direction = "out" if state in {"sent", "unsent"} or folder == 2 else "in"
            raw_id = f"gammu:{folder}:{location}"
            fingerprint = hashlib.sha256(
                f"{raw_id}\0{remote.group(1) if remote else ''}\0{body}".encode("utf-8")
            ).hexdigest()
            messages.append({
                "id": raw_id, "fingerprint": fingerprint, "direction": direction,
                "peer": remote.group(1) if remote else "", "body": body,
                "ts": int(time.time()), "timestamp_text": sent.group(1) if sent else "",
            })
        return messages

    def sms_list(self) -> list[dict]:
        now = time.monotonic()
        if self._sms_cache_at and now - self._sms_cache_at < 15:
            return [dict(item) for item in self._sms_cache]
        messages = self._parse_messages(self._invoke("geteachsms", timeout=90).stdout)
        self._sms_cache = [dict(item) for item in messages]
        self._sms_cache_at = now
        return messages

    def sms_send(self, recipient: str, body: str) -> dict:
        if not re.fullmatch(r"\+?\d{1,32}", str(recipient or "")):
            raise ProviderError("invalid SMS recipient")
        if not body:
            raise ProviderError("SMS body is empty")
        try:
            result = self._invoke(
                "sendsms", "TEXT", recipient, "-textutf8", body, timeout=190)
        except ProviderTimeout as exc:
            return {"ok": False, "status": "unknown", "retryable": False,
                    "error": str(exc), "provider": self.name}
        output = str(result.stdout or result.stderr or "")
        self._sms_cache_at = 0.0
        reference = re.search(r"reference[=: ]+(\d+)", output, re.I)
        return {"ok": True, "status": "sent",
                "reference": int(reference.group(1)) if reference else None,
                "provider": self.name}

    def sms_delete(self, message_id: str) -> dict:
        match = re.fullmatch(r"gammu:(\d+):(\d+)", str(message_id or ""))
        if not match:
            raise ProviderError("invalid Gammu SMS identifier")
        self._invoke("deletesms", match.group(1), match.group(2), timeout=45)
        self._sms_cache_at = 0.0
        return {"ok": True}

    def call_dial(self, number: str) -> dict:
        if not re.fullmatch(r"\+?\d{1,32}", str(number or "")):
            raise ProviderError("invalid telephone number")
        try:
            self._invoke("dialvoice", number, timeout=45)
        except ProviderTimeout as exc:
            self._call_state = "unknown"
            return {"ok": False, "status": "unknown", "retryable": False,
                    "error": str(exc), "audio": False}
        self._call_state = "dialing"
        return {"ok": True, "status": "dialing", "audio": False}

    def call_answer(self) -> dict:
        try:
            self._invoke("answercall", timeout=45)
        except ProviderTimeout as exc:
            self._call_state = "unknown"
            return {"ok": False, "status": "unknown", "retryable": False,
                    "error": str(exc), "audio": False}
        self._call_state = "active"
        return {"ok": True, "status": "active", "audio": False}

    def call_hangup(self, timeout: float = 45.0) -> dict:
        try:
            self._invoke("cancelcall", timeout=max(1, min(45, int(timeout))))
        except ProviderTimeout as exc:
            self._call_state = "unknown"
            return {"ok": False, "status": "unknown", "retryable": False,
                    "error": str(exc), "audio": False}
        self._call_state = "unknown"
        return {"ok": False, "status": "unknown", "audio": False,
                "terminal_confirmed": False, "command_accepted": True,
                "state_source": "gammu_operation",
                "error": "Gammu accepted call cancellation but cannot prove physical termination."}

    def call_status(self, timeout: float = 30.0) -> dict:
        if self._display_status_supported is False:
            return {"ok": True, "status": self._call_state, "audio": False,
                    "fresh": False, "authoritative": False,
                    "state_source": "gammu_operation"}
        try:
            output = str(self._invoke(
                "getdisplaystatus", timeout=max(1, min(30, int(timeout)))).stdout or "")
        except ProviderError as exc:
            if not re.search(r"not implemented|not supported", str(exc), re.I):
                raise
            # Many standards-based AT modems implement Gammu call control and unsolicited
            # call notifications but not the legacy handset display-status API.  Preserve the
            # last state observed through this provider instead of failing the entire status
            # endpoint or bypassing Gammu with a vendor command.
            self._display_status_supported = False
            return {"ok": True, "status": self._call_state, "audio": False,
                    "fresh": False, "authoritative": False,
                    "state_source": "gammu_operation"}
        self._display_status_supported = True
        active = bool(re.search(r"^Call active\s*$", output, re.I | re.M))
        self._call_state = "active" if active else "idle"
        return {"ok": True, "status": self._call_state, "audio": False,
                "fresh": True, "authoritative": False, "observed_at": time.time(),
                "state_source": "gammu_display"}

    def call_dtmf(self, digits: str) -> dict:
        if not re.fullmatch(r"[0-9A-D*#]+", str(digits or ""), re.I):
            raise ProviderError("invalid DTMF digits")
        self._invoke("senddtmf", digits, timeout=45)
        return {"ok": True}


class AuxiliaryAtProvider:
    """Persistent owner of one auxiliary 3GPP AT/Modem function.

    Windows keeps its independent MBN function for data. This provider holds the signalling
    function once and serializes APDU, SMS and call operations internally, avoiding both the
    Gammu process race and the SIM-session resets caused by reopening the COM port per APDU.
    """

    name = "auxiliary_at"
    owner = "function_managed"

    def __init__(self, port: str, connection, baud: int = 115200):
        self.port = str(port)
        self.connection = connection
        self.baud = int(baud)
        self.operation_lock = threading.RLock()
        self.identity_snapshot: dict = {}
        self.sms_supported = False
        self.call_supported = False
        self.sim_apdu_supported = False
        self._call_state = "idle"
        self._call_direction = ""
        self._call_number = ""
        self.usim_aid = ""
        self.directory_records: list[bytes] = []
        self.logical_channel: int | None = None
        self._synthetic_response: bytes | None = None
        self._directory_selected = False

    @classmethod
    def discover(cls, device_id: str, ports: list[str], baud: int = 115200,
                 *, probe_sim_apdu: bool = True):
        try:
            import serial
        except ImportError:
            return None
        for port in dict.fromkeys(str(value) for value in ports if value):
            connection = None
            try:
                connection = serial.Serial(port, baud, timeout=0.1, write_timeout=2)
                provider = cls(port, connection, baud)
                provider._at("AT")
                provider._at("ATE0")
                provider._at("AT+CMEE=2")
                imei_raw = provider._at("AT+CGSN")
                imei_match = re.search(rb'(?<!\d)(\d{14,17})(?!\d)', imei_raw)
                if not imei_match or imei_match.group(1).decode() != str(device_id or ""):
                    provider.close()
                    continue
                provider._probe_capabilities(probe_sim_apdu=probe_sim_apdu)
                if not (provider.sms_supported or provider.call_supported or
                        provider.sim_apdu_supported):
                    provider.close()
                    continue
                provider.identity_snapshot = {"imei": str(device_id), "port": port}
                return provider
            except Exception:
                if connection:
                    try:
                        connection.close()
                    except Exception:
                        pass
        return None

    def _probe_capabilities(self, *, probe_sim_apdu: bool = True) -> None:
        """Probe independent functions without requiring ownership of the SIM channel.

        Windows MBN may legitimately own CPIN/CUAD while the auxiliary function still owns
        3GPP call signalling.  A failure in one function must therefore not suppress the
        others.  Every probe is non-mutating and each advertised capability has succeeded on
        this exact attachment.
        """
        try:
            self._at("AT+CLCC")
            self.call_supported = True
        except ProviderError:
            self.call_supported = False
        try:
            self._at("AT+CMGF=?")
            self.sms_supported = True
        except ProviderError:
            self.sms_supported = False
        if not probe_sim_apdu:
            self.sim_apdu_supported = False
            return
        try:
            directory = self._at("AT+CUAD").decode("ascii", "replace")
            records = [bytes.fromhex(value) for value in re.findall(
                r'"([0-9A-Fa-f]+)"', directory) if value.startswith("61")]
            aids = []
            for record in records:
                match = re.search(rb"\x4f(.)(.+)", record, re.S)
                if match:
                    length = match.group(1)[0]
                    aids.append(match.group(2)[:length].hex().upper())
            usim = next((value for value in aids if value.startswith("A0000000871002")), "")
            if usim:
                self.directory_records = records
                self.usim_aid = usim
                self.sim_apdu_supported = True
        except (ProviderError, ValueError):
            self.sim_apdu_supported = False

    @property
    def capabilities(self):
        return ProviderCapabilities(
            sms_list=self.sms_supported, sms_send=self.sms_supported,
            call_signalling=self.call_supported, sim_apdu=self.sim_apdu_supported)

    def _at(self, command: str, timeout: float = 8.0) -> bytes:
        with self.operation_lock:
            if not self.connection or not self.connection.is_open:
                raise ProviderError("auxiliary AT port is closed")
            self.connection.reset_input_buffer()
            self.connection.write((command + "\r").encode("ascii"))
            self.connection.flush()
            value = bytearray()
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline:
                chunk = self.connection.read(1024)
                if chunk:
                    value.extend(chunk)
                    lines = bytes(value).replace(b"\r", b"\n").splitlines()
                    if any(line.strip() == b"OK" for line in lines):
                        return bytes(value)
                    if any(line.strip() == b"ERROR" or line.strip().startswith(
                            (b"+CME ERROR:", b"+CMS ERROR:")) for line in lines):
                        break
            raise ProviderError(
                f"auxiliary AT command failed: {command}: "
                f"{bytes(value[-200:]).decode('ascii', 'replace').strip()}")

    def voice_command(self, command: str) -> bytes:
        """Run maintenance on the same function that owns call signalling."""
        return self._at(command)

    def transmit(self, apdu: bytes) -> bytes:
        if not self.sim_apdu_supported:
            raise ProviderError("SIM APDU is unavailable on this auxiliary AT function")
        if not apdu:
            raise ProviderError("SIM APDU is empty")
        with self.operation_lock:
            instruction = apdu[1] if len(apdu) > 1 else -1
            data = b""
            if len(apdu) >= 5 and apdu[4] and len(apdu) >= 5 + apdu[4]:
                data = apdu[5:5 + apdu[4]]

            if instruction == 0xA4 and data == b"\x3f\x00":
                self._close_logical_channel()
                self._directory_selected = False
                self._synthetic_response = None
                return b"\x90\x00"

            if instruction == 0xA4 and data == b"\x2f\x00":
                self._directory_selected = True
                record_length = max((len(value) for value in self.directory_records), default=1)
                # sim.py needs only byte 7 (record length) from this compact FCP response.
                self._synthetic_response = bytes.fromhex("6206820278218000")[:-1] + bytes([record_length])
                return b"\x61" + bytes([len(self._synthetic_response)])

            if instruction == 0xC0 and self._synthetic_response is not None:
                value, self._synthetic_response = self._synthetic_response, None
                return value + b"\x90\x00"

            if instruction == 0xB2 and self._directory_selected:
                record = int(apdu[2]) if len(apdu) > 2 else 0
                if 1 <= record <= len(self.directory_records):
                    width = max(len(value) for value in self.directory_records)
                    return self.directory_records[record - 1].ljust(width, b"\xff") + b"\x90\x00"
                return b"\x6a\x83"

            if instruction == 0xA4 and data.hex().upper().startswith("A0000000871002"):
                self._directory_selected = False
                self._synthetic_response = None
                self._open_logical_channel(data.hex().upper())
                # Existing PC/SC consumers only require proof that ADF.USIM was selected;
                # subsequent file commands use CGLA on the opened logical channel.
                return b"\x61\x00"

            if self.logical_channel is not None:
                return self._logical_exchange(apdu)

            value = apdu.hex().upper()
            response = self._at(f'AT+CSIM={len(value)},"{value}"', timeout=12)
            return self._parse_apdu_response(response, "CSIM")

    @staticmethod
    def _parse_apdu_response(response: bytes, label: str) -> bytes:
        match = re.search(
            rb'\+' + label.encode("ascii") + rb':\s*(\d+)\s*,\s*"([0-9A-Fa-f]*)"',
            response)
        if not match:
            raise ProviderError(f"auxiliary AT port returned no {label} response")
        payload = bytes.fromhex(match.group(2).decode("ascii"))
        if int(match.group(1)) != len(payload) * 2:
            raise ProviderError(f"auxiliary AT port returned an invalid {label} length")
        return payload

    def _open_logical_channel(self, aid: str) -> None:
        self._close_logical_channel()
        response = self._at(f'AT+CCHO="{aid}"')
        match = re.search(rb"\+CCHO:\s*(\d+)", response)
        if not match:
            raise ProviderError("modem did not open a USIM logical channel")
        self.logical_channel = int(match.group(1))

    def _close_logical_channel(self) -> None:
        channel, self.logical_channel = self.logical_channel, None
        if channel is not None:
            try:
                self._at(f"AT+CCHC={channel}")
            except ProviderError:
                pass

    def _logical_exchange(self, apdu: bytes) -> bytes:
        value = apdu.hex().upper()
        response = self._at(
            f'AT+CGLA={self.logical_channel},{len(value)},"{value}"', timeout=12)
        return self._parse_apdu_response(response, "CGLA")

    def reset(self) -> None:
        if not self.sim_apdu_supported:
            raise ProviderError("SIM APDU is unavailable on this auxiliary AT function")
        self._close_logical_channel()
        self._directory_selected = False
        self._synthetic_response = None
        self._at("AT+CPIN?", timeout=3)

    def sms_list(self) -> list[dict]:
        self._at("AT+CMGF=1")
        self._at('AT+CSCS="GSM"')
        raw = self._at('AT+CMGL="ALL"', timeout=30).decode("utf-8", "replace")
        lines, messages, index = raw.replace("\r", "").split("\n"), [], 0
        while index < len(lines):
            match = re.match(r'^\+CMGL:\s*(\d+),"([^"]*)","([^"]*)"(.*)$',
                             lines[index].strip())
            if match:
                body = lines[index + 1] if index + 1 < len(lines) else ""
                state = match.group(2).upper()
                fingerprint = hashlib.sha256(
                    f"{match.group(0)}\0{body}".encode("utf-8")).hexdigest()
                messages.append({
                    "id": f"at:{match.group(1)}", "fingerprint": fingerprint,
                    "direction": "out" if state.startswith("STO") else "in",
                    "peer": match.group(3), "body": body, "ts": int(time.time()),
                })
                index += 1
            index += 1
        return messages

    def sms_send(self, recipient: str, body: str) -> dict:
        if not re.fullmatch(r"\+?\d{1,32}", recipient) or not body:
            raise ProviderError("invalid SMS submission")
        self._at("AT+CMGF=1")
        self._at('AT+CSCS="GSM"')
        with self.operation_lock:
            self.connection.reset_input_buffer()
            self.connection.write(f'AT+CMGS="{recipient}"\r'.encode("ascii"))
            self.connection.flush()
            prompt = bytearray()
            deadline = time.monotonic() + 8
            while time.monotonic() < deadline and b">" not in prompt:
                prompt.extend(self.connection.read(256))
            if b">" not in prompt:
                raise ProviderError("SMS modem did not return a submit prompt")
            self.connection.write(body.encode("utf-8") + b"\x1a")
            self.connection.flush()
            raw = self._read_pending("AT+CMGS", 190)
        reference = re.search(rb"\+CMGS:\s*(\d+)", raw)
        return {"ok": True, "status": "sent",
                "reference": int(reference.group(1)) if reference else None,
                "provider": self.name}

    def _read_pending(self, command: str, timeout: float) -> bytes:
        value = bytearray()
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            chunk = self.connection.read(1024)
            if chunk:
                value.extend(chunk)
                lines = bytes(value).replace(b"\r", b"\n").splitlines()
                if any(line.strip() == b"OK" for line in lines):
                    return bytes(value)
                if any(line.strip() == b"ERROR" or line.strip().startswith(
                        (b"+CME ERROR:", b"+CMS ERROR:")) for line in lines):
                    break
        raise ProviderError(
            f"auxiliary AT command failed: {command}: "
            f"{bytes(value[-300:]).decode('utf-8', 'replace').strip()}")

    def sms_delete(self, message_id: str) -> dict:
        match = re.fullmatch(r"at:(\d+)", str(message_id or ""))
        if not match:
            raise ProviderError("invalid auxiliary SMS identifier")
        self._at(f"AT+CMGD={match.group(1)}")
        return {"ok": True}

    def call_dial(self, number: str) -> dict:
        if not re.fullmatch(r"\+?\d{1,32}", number):
            raise ProviderError("invalid telephone number")
        self._at(f"ATD{number};", timeout=15)
        self._call_state = "dialing"
        self._call_direction = "out"
        self._call_number = number
        return {"ok": True, "status": "dialing", "direction": "out",
                "number": number, "audio": False}

    def call_answer(self) -> dict:
        self._at("ATA", timeout=15); self._call_state = "active"
        return {"ok": True, "status": "active", "audio": False}

    def call_hangup(self, timeout: float = 35.0) -> dict:
        result = verified_at_hangup(
            lambda command, command_timeout: self._at(command, timeout=command_timeout),
            total_timeout=timeout)
        self._call_state = str(result.get("status") or "unknown")
        return result

    def call_status(self, timeout: float = 5.0) -> dict:
        try:
            result = parse_clcc_voice(self._at(
                "AT+CLCC", timeout=max(0.1, min(5.0, float(timeout)))))
            self._call_state = str(result["status"])
            self._call_direction = str(result.get("direction") or self._call_direction)
            self._call_number = str(result.get("number") or self._call_number)
            return {**result, "audio": False,
                    "direction": self._call_direction, "number": self._call_number}
        except ProviderError as exc:
            return {"ok": False, "status": "unknown", "audio": False,
                    "fresh": False, "authoritative": False,
                    "direction": self._call_direction, "number": self._call_number,
                    "state_source": "3gpp_clcc", "error": str(exc)}

    def call_dtmf(self, digits: str) -> dict:
        if not re.fullmatch(r"[0-9A-D*#]+", digits, re.I):
            raise ProviderError("invalid DTMF digits")
        for digit in digits:
            self._at(f"AT+VTS={digit}")
        return {"ok": True}

    def close(self) -> None:
        with self.operation_lock:
            self._close_logical_channel()
            if self.connection:
                try:
                    self.connection.close()
                finally:
                    self.connection = None


class CompositeModemProvider:
    """One coordinator over independent OS-data and signalling functions."""

    name = "composite"
    owner = "function_coordinated"

    def __init__(self, data_provider, signalling_provider,
                 apdu_provider: AuxiliaryAtProvider | None = None):
        self.data = data_provider
        self.signalling = signalling_provider
        self.apdu = apdu_provider
        self._sms_backend = data_provider if self._native_sms() else signalling_provider

    @property
    def snapshot(self):
        return self.data.snapshot

    @property
    def capabilities(self) -> ProviderCapabilities:
        data_caps = self.data.capabilities
        signal_caps = self.signalling.capabilities
        native_sms = bool(data_caps.sms_list and data_caps.sms_send and
                          hasattr(self.data, "sms_list") and hasattr(self.data, "sms_send"))
        return ProviderCapabilities(
            cellular_data=data_caps.cellular_data,
            sms_list=data_caps.sms_list if native_sms else signal_caps.sms_list,
            sms_send=data_caps.sms_send if native_sms else signal_caps.sms_send,
            call_signalling=signal_caps.call_signalling,
            call_audio=signal_caps.call_audio,
            sim_apdu=bool(self.apdu) or data_caps.sim_apdu,
        )

    def _native_sms(self) -> bool:
        capabilities = self.data.capabilities
        return bool(capabilities.sms_list and capabilities.sms_send and
                    hasattr(self.data, "sms_list") and hasattr(self.data, "sms_send"))

    def _signalling_sms(self) -> bool:
        capabilities = self.signalling.capabilities
        return bool(capabilities.sms_list and capabilities.sms_send and
                    hasattr(self.signalling, "sms_list") and
                    hasattr(self.signalling, "sms_send"))

    def _select_sms_backend(self, data_status: dict):
        """Prefer native SMS only while its runtime service is usable.

        Static MBN capability bits describe what the driver implements, not whether the
        current SIM/firmware combination has initialized that service.  An independently
        owned signalling function remains a valid 3GPP SMS path when the OS explicitly
        reports its native service unavailable.
        """
        native_unavailable = bool(
            data_status.get("sms_readiness_authoritative") and
            data_status.get("sms_ready") is False)
        if self._native_sms() and not (native_unavailable and self._signalling_sms()):
            self._sms_backend = self.data
        elif self._signalling_sms():
            self._sms_backend = self.signalling
        return self._sms_backend

    def identity(self):
        return self.data.identity()

    def transmit_apdu(self, apdu: bytes) -> bytes:
        if not self.apdu:
            raise ProviderError("auxiliary SIM APDU access is unavailable")
        return self.apdu.transmit(apdu)

    def reset_apdu(self) -> None:
        if not self.apdu:
            raise ProviderError("auxiliary SIM APDU access is unavailable")
        self.apdu.reset()

    def status(self, refresh: bool = True):
        value = self.data.status(refresh=refresh)
        native_sms = self._native_sms()
        sms_backend = self._select_sms_backend(value)
        native_selected = native_sms and sms_backend is self.data
        native_error = str(value.get("sms_error") or "")
        value.update({
            "provider": self.name,
            "owner": self.owner,
            "sms_provider": self.data.name if native_selected else self.signalling.name,
            "sms_readiness_authoritative": bool(
                native_selected and value.get("sms_readiness_authoritative")),
        })
        if not native_selected:
            value.update({"sms_ready": self.signalling.sms_supported,
                          "sms_error": "" if self.signalling.sms_supported else
                                       "auxiliary_sms_unavailable"})
            if native_error:
                value["native_sms_error"] = native_error
        return value

    def connect(self, profile: str, interface: str = ""):
        return self.data.connect(profile, interface)

    def disconnect(self):
        return self.data.disconnect()

    def sms_configuration(self):
        """Always ask the OS data function: it owns the SIM's SMS configuration."""
        reader = getattr(self.data, "sms_configuration", None)
        if not reader:
            raise ProviderError("this provider cannot read the SMS configuration")
        return reader()

    def sms_list(self):
        return self._sms_backend.sms_list()

    def sms_send(self, recipient: str, body: str):
        return self._sms_backend.sms_send(recipient, body)

    def sms_delete(self, message_id: str):
        if str(message_id or "").startswith(("at:", "gammu:")):
            return self.signalling.sms_delete(message_id)
        return self._sms_backend.sms_delete(message_id)

    def call_dial(self, number: str):
        return self.signalling.call_dial(number)

    def call_answer(self):
        return self.signalling.call_answer()

    def call_hangup(self, timeout: float = 35.0):
        return self.signalling.call_hangup(timeout=timeout)

    def call_status(self, timeout: float = 5.0):
        return self.signalling.call_status(timeout=timeout)

    def call_dtmf(self, digits: str):
        return self.signalling.call_dtmf(digits)

    def voice_command(self, command: str) -> bytes:
        """Route self-checks through the provider that executes ATD/ATA/ATH."""
        runner = getattr(self.signalling, "voice_command", None)
        if not callable(runner):
            raise ProviderError(
                "the selected call-signalling provider exposes no maintenance channel")
        return runner(command)

    def close(self):
        closer = getattr(self.signalling, "close", None)
        if closer:
            closer()


@dataclass(frozen=True)
class ProviderCapabilities:
    cellular_data: bool = False
    sms_list: bool = False
    sms_send: bool = False
    call_signalling: bool = False
    call_audio: bool = False
    sim_apdu: bool = False


def _ps_literal(value: str) -> str:
    return "'" + str(value).replace("'", "''") + "'"


class WindowsPnpLease:
    """Reversible, per-interface ownership lease for a Windows WWAN PnP function."""

    def __init__(self, interface: str, pnp_device_id: str,
                 runner: Callable[..., subprocess.CompletedProcess] = subprocess.run,
                 process_factory=subprocess.Popen, helper: str = ""):
        self.interface = interface
        self.pnp_device_id = pnp_device_id
        self.runner = runner
        self.process_factory = process_factory
        self.helper = helper
        self.process = None
        self.release_event = ""
        self.acquired = False

    @classmethod
    def discover(cls, interface: str, runner=subprocess.run,
                 process_factory=subprocess.Popen, helper: str = ""):
        script = (f"Get-NetAdapter -Name {_ps_literal(interface)} -ErrorAction Stop | "
                  "Select-Object -First 1 Name,PnPDeviceID | ConvertTo-Json -Compress")
        result = runner(["powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script],
                        capture_output=True, text=True, timeout=15, check=False)
        try:
            value = json.loads(str(result.stdout or "").strip())
        except (TypeError, json.JSONDecodeError):
            return None
        pnp_id = str((value or {}).get("PnPDeviceID") or "")
        return cls(str((value or {}).get("Name") or interface), pnp_id, runner,
                   process_factory, helper) if pnp_id else None

    def _helper(self) -> str:
        if self.helper:
            return self.helper
        executable = Path(sys.executable).resolve()
        candidate = executable.with_name("mdd-network-guard.exe")
        return str(candidate) if candidate.is_file() else ""

    def acquire(self) -> dict:
        if self.acquired:
            return {"status": "leased", "problem_code": 22}
        helper = self._helper()
        if not helper:
            raise ProviderError("the native Windows PnP lease helper is not installed")
        try:
            self.release_event = f"MDDCellularLease-{uuid.uuid4().hex}"
            self.process = self.process_factory(
                [helper, "--lease-device", self.pnp_device_id, "--pid", str(os.getpid()),
                 "--release-event", self.release_event],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                text=True)
            line = self.process.stdout.readline(4096) if self.process.stdout else ""
            value = json.loads(line or "{}")
            if value.get("ready") is not True or self.process.poll() is not None:
                raise ProviderError(str(value.get("error") or "PnP lease helper stopped"))
            self.acquired = True
            return value
        except Exception as exc:
            try:
                if self.process and self.process.stdin:
                    self.process.stdin.close()
                if self.process:
                    self.process.wait(timeout=10)
            except Exception:
                pass
            self.process = None
            self.release_event = ""
            if isinstance(exc, ProviderError):
                raise
            raise ProviderError(f"Windows PnP ownership transition failed: {exc}") from exc

    def release(self) -> dict:
        process, self.process = self.process, None
        if process:
            signal = self.runner(
                [self._helper(), "--signal-event", self.release_event],
                capture_output=True, text=True, timeout=10, check=False)
            if signal.returncode:
                raise ProviderError(str(signal.stderr or signal.stdout or
                                        "cannot signal Windows PnP lease release")[:300])
            try:
                code = process.wait(timeout=30)
            except subprocess.TimeoutExpired as exc:
                raise ProviderError("Windows PnP lease helper did not restore the device") from exc
            if code:
                detail = process.stderr.read(300) if process.stderr else ""
                raise ProviderError(f"Windows PnP device restore failed: {detail or code}")
        self.acquired = False
        self.release_event = ""
        return {"status": "released", "problem_code": 0}


class WindowsMbnProvider:
    """Windows WWAN/MBN owner accessed through the native MBN helper."""

    owner = "system_managed"
    name = "windows_mbn"

    def __init__(self, command: list[str], device_id: str,
                 runner: Callable[..., subprocess.CompletedProcess] = subprocess.run):
        self.command = list(command)
        self.device_id = str(device_id or "")
        self.runner = runner
        self.interface_id = ""
        self.snapshot: dict = {}

    @classmethod
    def discover(cls, device_id: str, runner=subprocess.run, wait_seconds: float = 60.0):
        command = _windows_mbn_helper_command()
        if not command:
            return None
        deadline = time.monotonic() + max(0.0, wait_seconds)
        while True:
            value = cls(command, device_id, runner)
            try:
                value.refresh()
                return value
            except ProviderError:
                # USB composite devices commonly publish their diagnostic COM port before
                # WwanSvc has enumerated the matching MBN interface.  A bounded ownership
                # arbitration window prevents Generic AT from stealing a system-managed
                # modem during that normal startup race.
                if time.monotonic() >= deadline:
                    return None
                time.sleep(min(1.0, max(0.0, deadline - time.monotonic())))

    def _invoke(self, *args: str, timeout: int = 60, payload: dict | None = None) -> dict:
        try:
            result = self.runner(
                [*self.command, *args], capture_output=True, text=True,
                timeout=timeout, check=False,
                input=json.dumps(payload) if payload is not None else None,
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise ProviderError(f"Windows MBN helper unavailable: {exc}") from exc
        output = str(result.stdout or "").strip()
        try:
            payload = json.loads(output)
        except (TypeError, json.JSONDecodeError) as exc:
            detail = str(result.stderr or output or f"exit {result.returncode}").strip()
            raise ProviderError(f"Windows MBN helper returned invalid output: {detail[:300]}") from exc
        if not isinstance(payload, dict):
            raise ProviderError("Windows MBN helper returned a non-object result")
        return payload

    def refresh(self) -> dict:
        payload = self._invoke("probe", timeout=20)
        interfaces = payload.get("interfaces") if payload.get("ok") else []
        match = next((item for item in interfaces or []
                      if str(item.get("device_id") or "") == self.device_id), None)
        if not match:
            raise ProviderError("Windows MBN did not enumerate this modem")
        self.snapshot = dict(match)
        self.interface_id = str(match.get("interface_id") or "")
        return self.snapshot

    @property
    def capabilities(self) -> ProviderCapabilities:
        sms_caps = int(self.snapshot.get("sms_caps") or 0)
        # MBN_SMS_CAPS_PDU_RECEIVE=1 and MBN_SMS_CAPS_PDU_SEND=2. The helper will
        # expose operations only after their native implementation is present.
        return ProviderCapabilities(
            cellular_data=bool(int(self.snapshot.get("data_class") or 0)),
            sms_list=bool(sms_caps & 1),
            # PDU_SEND is static hardware/driver support. Runtime readiness is reported by
            # status() and enforced again in sms_send(); keeping these separate lets a device
            # become ready after Windows finishes probing without rebuilding the Provider.
            sms_send=bool(sms_caps & 2),
            call_signalling=False,
            call_audio=False,
            sim_apdu=False,
        )

    def identity(self) -> tuple[str, str]:
        if str(self.snapshot.get("ready_state") or "") != "MBN_READY_STATE_INITIALIZED":
            return ("", "")
        return (str(self.snapshot.get("sim_iccid") or ""),
                str(self.snapshot.get("subscriber_id") or ""))

    def status(self, refresh: bool = True) -> dict:
        value = self.refresh() if refresh else self.snapshot
        registration = {
            "MBN_REGISTER_STATE_HOME": "home",
            "MBN_REGISTER_STATE_ROAMING": "roaming",
            "MBN_REGISTER_STATE_PARTNER": "roaming",
            "MBN_REGISTER_STATE_SEARCHING": "searching",
            "MBN_REGISTER_STATE_DENIED": "denied",
            "MBN_REGISTER_STATE_DEREGISTERED": "unregistered",
        }.get(str(value.get("registration") or ""), "unknown")
        active = str(value.get("activation_state") or "").endswith("_ACTIVATED")
        radio = str(value.get("software_radio") or "").endswith("_ON")
        return {
            "sim": "ready" if str(value.get("ready_state") or "") ==
                    "MBN_READY_STATE_INITIALIZED" else "unavailable",
            "registration": registration,
            "operator": str(value.get("provider_name") or ""),
            "operator_id": str(value.get("provider_id") or ""),
            "signal": value.get("signal"),
            "radio_enabled": radio,
            "data": "connected" if active else "disconnected",
            "data_active": active,
            "profile": str(value.get("profile_name") or ""),
            "provider": self.name,
            "owner": self.owner,
            "sms_ready": value.get("sms_ready") if isinstance(
                value.get("sms_ready"), bool) else None,
            "sms_configured": bool(value.get("sms_configured")),
            "sms_error": str(value.get("sms_error") or ""),
            "sms_service_center": str(value.get("sms_service_center") or ""),
            "sms_provider": self.name,
            # The public MBN COM API exposes configuration, message-store state and
            # asynchronous send completion, but no pre-submit network-ready flag. A nullable
            # helper value must therefore be resolved by the bearer probe in ModemCard.
            "sms_readiness_authoritative": isinstance(value.get("sms_ready"), bool),
        }

    def sms_configuration(self) -> dict:
        """Read the SMS configuration through a subscribed MBN session.

        Verified on real hardware (2026-08-19): the MBN SMS getters answer E_PENDING until a
        client has subscribed to ``IMbnSmsEvents``.  ``probe`` deliberately stays
        unsubscribed and non-blocking because it backs every status heartbeat, so the service
        centre has to be read through this separate, bounded call and cached by the caller.
        """
        if not self.interface_id:
            self.refresh()
        return self._invoke("sms-config", self.interface_id, timeout=20)

    def connect(self, profile: str, interface: str = "") -> dict:
        if not self.interface_id:
            self.refresh()
        native = self._invoke("connect", self.interface_id, str(profile), timeout=45)
        if native.get("ok") or not interface:
            return native
        # Some older Windows WWAN miniports acknowledge legacy IMbnConnection.Connect but
        # never transition out of DEACTIVATED.  Let the OS compatibility command trigger the
        # same stored profile, then use native MBN state as the sole postcondition; its exit
        # code and localized output are intentionally ignored.
        if (str(native.get("hresult") or "").lower() == "0x00000000" and
                str(native.get("activation_state") or "").endswith("_DEACTIVATED")):
            try:
                self.runner(
                    ["netsh", "mbn", "connect", f"interface={interface}",
                     "connmode=name", f"name={profile}"],
                    capture_output=True, text=True, timeout=30, check=False)
            except (OSError, subprocess.TimeoutExpired):
                pass
            deadline = time.monotonic() + 35
            while time.monotonic() < deadline:
                try:
                    current = self.status(refresh=True)
                except ProviderError:
                    current = {}
                if current.get("data_active"):
                    return {"ok": True, "status": "completed",
                            "activation_state": "MBN_ACTIVATION_STATE_ACTIVATED",
                            "compatibility_trigger": "windows_mbn_system_command"}
                time.sleep(.5)
            native = {**native, "compatibility_trigger": "windows_mbn_system_command"}
        return native

    def disconnect(self) -> dict:
        if not self.interface_id:
            self.refresh()
        return self._invoke("disconnect", self.interface_id, timeout=40)

    def sms_list(self) -> list[dict]:
        if not self.interface_id:
            self.refresh()
        result = self._invoke("sms-read", self.interface_id, timeout=55)
        if not result.get("ok"):
            raise ProviderError(f"Windows MBN SMS read failed ({result.get('hresult') or 'unknown'})")
        values = []
        for message in result.get("messages") or []:
            try:
                values.append(decode_deliver(message.get("pdu") or "", message.get("index") or 0,
                                             str(message.get("status") or "")))
            except (TypeError, ValueError):
                continue
        return values

    def sms_send(self, recipient: str, body: str) -> dict:
        # Readiness changes independently of registration and data. Refresh immediately before
        # every billable submission and fail before encoding/submitting if Windows has not
        # initialized SMS yet.
        self.refresh()
        if self.snapshot.get("sms_ready") is False:
            detail = str(self.snapshot.get("sms_error") or "pending")
            return {"ok": False, "unavailable": True, "status": "unavailable",
                    "error": f"Windows mobile-broadband SMS is not ready ({detail})."}
        pdu, size = encode_submit(recipient, body)
        result = self._invoke("sms-send", self.interface_id, timeout=190,
                              payload={"pdu": pdu, "size": size})
        if not result.get("ok") and not result.get("error"):
            result["error"] = ("Windows mobile-broadband SMS submission failed"
                               f" ({result.get('hresult') or 'unknown status'}).")
        return result

    def sms_delete(self, index: int | str) -> dict:
        if not self.interface_id:
            self.refresh()
        return self._invoke("sms-delete", self.interface_id, timeout=55,
                            payload={"index": int(index)})


def _windows_mbn_helper_command() -> list[str]:
    if os.name != "nt":
        return []
    configured = str(os.environ.get("MDD_WINDOWS_MBN_HELPER") or "").strip()
    if configured:
        return [configured]
    candidates = [Path(sys.executable).with_name("mdd-windows-mbn.exe")]
    bundle = getattr(sys, "_MEIPASS", "")
    if bundle:
        candidates.append(Path(bundle) / "mdd-windows-mbn.exe")
    candidates.append(Path(__file__).resolve().parent / "windows" / "mdd-windows-mbn.exe")
    match = next((path for path in candidates if path.is_file()), None)
    return [str(match)] if match else []
