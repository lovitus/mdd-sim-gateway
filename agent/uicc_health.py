"""Bounded, transport-neutral maintenance of a modem's UICC session.

The maintainer uses standard 3GPP registration, PIN and functionality commands.  A vendor
insertion-status query is advisory only: unsupported commands never make a healthy attachment
unavailable.  Recovery is allowed only after the host has already identified a SIM for this
modem and the modem explicitly reports a SIM failure while no domain is registered.
"""

from __future__ import annotations

from dataclasses import asdict, dataclass, field
import hashlib
import os
from pathlib import Path
import re
import threading
import time
from typing import Callable

try:
    from state_store import TransactionalJsonState
except ModuleNotFoundError:
    from .state_store import TransactionalJsonState


@dataclass(frozen=True)
class UiccHealthResult:
    ready: bool | None = None
    state: str = "unknown"
    reason: str = "UICC health has not been checked"
    action: str = ""
    retry_after: int = 0
    diagnostics: dict = field(default_factory=dict)

    def public(self) -> dict:
        return asdict(self)


def _text(value: bytes | str | BaseException) -> str:
    if isinstance(value, bytes):
        return value.decode("ascii", "replace")
    return str(value or "")


def _registration(value: bytes | str, family: str) -> int | None:
    match = re.search(rf"\+{family}:\s*(?:\d+\s*,\s*)?(\d+)", _text(value), re.I)
    return int(match.group(1)) if match else None


def _pin_state(value: bytes | str | BaseException) -> str:
    text = _text(value).upper()
    if re.search(r"\+CPIN:\s*READY", text):
        return "ready"
    if re.search(r"\+CPIN:\s*(?:SIM\s+)?P(?:IN|UK)", text):
        return "locked"
    if ("SIM FAILURE" in text or "SIM NOT INSERTED" in text or
            re.search(r"\+CME ERROR:\s*(?:10|13)(?:\D|$)", text)):
        return "failure"
    return "unknown"


def _inserted_state(value: bytes | str) -> int | None:
    match = re.search(r"\+QSIMSTAT:\s*\d+\s*,\s*([012])", _text(value), re.I)
    return int(match.group(1)) if match else None


class UiccHealthMaintainer:
    """Detect and recover one failed UICC initialization without reset loops."""

    ACTION = "reinitialize_uicc"
    RESTORE_ACTION = "restore_full_function"
    IDENTITIES = "known_sim_identities"

    def __init__(self, *, interval: float = 60.0, settle_timeout: float = 45.0,
                 poll_interval: float = 2.0,
                 clock: Callable[[], float] = time.monotonic,
                 sleeper: Callable[[float], None] = time.sleep,
                 state_path: Path | str | None = None):
        self.interval = float(interval)
        self.settle_timeout = float(settle_timeout)
        self.poll_interval = max(0.05, float(poll_interval))
        self.clock = clock
        self.sleeper = sleeper
        self.state_path = Path(state_path) if state_path else None
        self.state = TransactionalJsonState(self.state_path)
        self.context = ""
        self.has_known_sim = False
        self.checked_at = 0.0
        self.last = UiccHealthResult()
        self.lock = threading.RLock()

    def set_context(self, imei: str, iccid: str = "", firmware: str = "") -> None:
        self.context = "\0".join((str(imei or ""), str(iccid or ""), str(firmware or "")))
        self.has_known_sim = bool(str(imei or "") and str(iccid or ""))

    def remember_identity(self, imei: str, iccid: str) -> None:
        """Persist the last SIM identity only after a provider returned it successfully."""
        imei = "".join(character for character in str(imei or "") if character.isdigit())
        iccid = "".join(character for character in str(iccid or "") if character.isdigit())
        if not self.state_path or not (14 <= len(imei) <= 17 and 18 <= len(iccid) <= 22):
            return
        def remember(value):
            identities = value.get(self.IDENTITIES)
            identities = dict(identities) if isinstance(identities, dict) else {}
            identities[imei] = iccid
            value[self.IDENTITIES] = identities
        self.state.update(remember)

    def known_iccid(self, imei: str) -> str:
        imei = "".join(character for character in str(imei or "") if character.isdigit())
        identities = self._load().get(self.IDENTITIES)
        value = str(identities.get(imei) or "") if isinstance(identities, dict) else ""
        return value if 18 <= len(value) <= 22 and value.isdigit() else ""

    def _key(self) -> str:
        material = f"{self.context}\0{self.ACTION}".encode("utf-8")
        return hashlib.sha256(material).hexdigest()

    @staticmethod
    def _bootstrap_key(imei: str) -> str:
        material = f"{imei}\0{UiccHealthMaintainer.RESTORE_ACTION}".encode("utf-8")
        return hashlib.sha256(material).hexdigest()

    def _load(self) -> dict:
        return self.state.load()

    def _attempted(self) -> bool:
        return bool(self.context and self._key() in self._load())

    def _record_attempt(self) -> None:
        key = self._key()
        self.state.update(lambda value: value.__setitem__(key, int(time.time())))

    def _clear_attempt(self) -> None:
        if not self.state_path or not self.context:
            return
        key = self._key()
        self.state.update(lambda value: value.pop(key, None))

    def ensure_full_function(self, at_command: Callable[[str], bytes], imei: str) -> UiccHealthResult:
        """Resume an interrupted ``CFUN=0`` transition before Windows MBN is available.

        This bootstrap is deliberately narrower than :meth:`check`: it never changes a radio
        that is fully functional (1) or intentionally radio-disabled (commonly 4), and it never
        starts a 0/1 reset cycle.  It only finishes a transition that is already in minimum
        functionality.  A durable one-shot guard prevents a non-responsive modem from being
        reset on every discovery pass.
        """
        imei = str(imei or "")
        if not imei:
            return UiccHealthResult(
                ready=None, state="unknown",
                reason="modem identity is unavailable for full-function recovery")
        try:
            raw = self._query(at_command, "AT+CFUN?")
            match = re.search(r"\+CFUN:\s*(\d+)", raw, re.I)
            cfun = int(match.group(1)) if match else None
        except Exception as exc:
            return UiccHealthResult(
                ready=None, state="unknown",
                reason=f"the modem did not expose its radio function state: {exc}")

        diagnostics = {"radio_function": cfun}
        if cfun != 0:
            if self.state_path:
                key = self._bootstrap_key(imei)
                self.state.update(lambda value: value.pop(key, None))
            return UiccHealthResult(
                ready=True if cfun == 1 else None,
                state="ready" if cfun == 1 else "unchanged",
                reason="" if cfun == 1 else
                "the radio is not in full-function mode, but it is not an interrupted CFUN=0 transition",
                diagnostics=diagnostics)

        if not self.state_path:
            return UiccHealthResult(
                ready=False, state="failed",
                reason="durable recovery state is unavailable; refusing repeated CFUN recovery",
                diagnostics=diagnostics)
        key = self._bootstrap_key(imei)
        value = self._load()
        if key in value:
            diagnostics["recovery_attempted"] = True
            return UiccHealthResult(
                ready=False, state="failed",
                reason=("full-function recovery was already attempted; power-cycle the modem "
                        "before retrying"),
                action=self.RESTORE_ACTION, diagnostics=diagnostics)

        self.state.update(lambda current: current.__setitem__(key, int(time.time())))
        try:
            at_command("AT+CFUN=1")
        except Exception as exc:
            return UiccHealthResult(
                ready=False, state="recovery_failed",
                reason=f"full-function recovery failed: {exc}",
                action=self.RESTORE_ACTION, diagnostics=diagnostics)
        return UiccHealthResult(
            ready=None, state="restarting",
            reason="the modem is restoring full-function mode",
            action=self.RESTORE_ACTION, retry_after=10, diagnostics=diagnostics)

    @staticmethod
    def _query(at_command: Callable[[str], bytes], command: str) -> str:
        return _text(at_command(command))

    def _pin(self, at_command: Callable[[str], bytes]) -> tuple[str, str]:
        try:
            raw = self._query(at_command, "AT+CPIN?")
        except Exception as exc:
            raw = _text(exc)
        return _pin_state(raw), raw

    def _wait_ready(self, at_command: Callable[[str], bytes]) -> tuple[str, str]:
        deadline = self.clock() + self.settle_timeout
        state, raw = "unknown", ""
        while self.clock() < deadline:
            self.sleeper(min(self.poll_interval, max(0.0, deadline - self.clock())))
            state, raw = self._pin(at_command)
            if state in {"ready", "locked"}:
                break
        return state, raw

    def check(self, at_command: Callable[[str], bytes], *, force: bool = False,
              allow_repair: bool = True) -> UiccHealthResult:
        with self.lock:
            now = self.clock()
            if not force and self.checked_at and now - self.checked_at < self.interval:
                return self.last
            self.checked_at = now

            registered = {1, 5}
            registration = {}
            for command, family in (("AT+CREG?", "CREG"), ("AT+CEREG?", "CEREG")):
                try:
                    registration[family.casefold()] = _registration(
                        self._query(at_command, command), family)
                except Exception:
                    registration[family.casefold()] = None
            pin, pin_raw = self._pin(at_command)
            diagnostics = dict(registration)
            diagnostics["pin"] = pin
            if pin == "ready":
                self._clear_attempt()
                self.last = UiccHealthResult(
                    ready=True, state="ready", reason="", diagnostics=diagnostics)
                return self.last
            if pin == "locked":
                self.last = UiccHealthResult(
                    ready=False, state="locked",
                    reason="the SIM requires a PIN or PUK before network registration",
                    diagnostics=diagnostics)
                return self.last
            if pin != "failure":
                if any(value in registered for value in registration.values()):
                    # Some system-owned modem functions intentionally hide CPIN. Direct
                    # registration remains authoritative unless the modem also reports an
                    # explicit UICC failure below.
                    self._clear_attempt()
                    self.last = UiccHealthResult(
                        ready=True, state="ready", reason="", diagnostics=diagnostics)
                    return self.last
                self.last = UiccHealthResult(
                    ready=None, state="unknown",
                    reason="the modem did not expose an authoritative UICC state",
                    diagnostics=diagnostics)
                return self.last

            try:
                diagnostics["inserted"] = _inserted_state(
                    self._query(at_command, "AT+QSIMSTAT?"))
            except Exception:
                diagnostics["inserted"] = None
            if (any(value in registered for value in registration.values()) and
                    diagnostics["inserted"] != 0):
                # CPIN may be owned by Windows MBIM while call signalling remains healthy.
                # Only a corroborating "not inserted" result may override registration.
                self._clear_attempt()
                self.last = UiccHealthResult(
                    ready=True, state="ready", reason="", diagnostics=diagnostics)
                return self.last
            attempted = self._attempted()
            diagnostics["recovery_attempted"] = attempted
            diagnostics["persistent_recovery_guard"] = bool(
                self.state_path and self.context)
            if not self.has_known_sim:
                self.last = UiccHealthResult(
                    ready=False, state="failed",
                    reason=("the modem reports a SIM failure, but no prior SIM identity is "
                            "available to authorize an automatic radio reset"),
                    diagnostics=diagnostics)
                return self.last
            if not self.state_path or not self.context:
                self.last = UiccHealthResult(
                    ready=False, state="failed",
                    reason=("the modem reports a SIM failure, but durable recovery state is "
                            "unavailable; refusing an unbounded radio reset"),
                    diagnostics=diagnostics)
                return self.last
            if not allow_repair:
                self.last = UiccHealthResult(
                    ready=False, state="failed", reason="the modem reports a SIM failure",
                    diagnostics=diagnostics)
                return self.last

            try:
                cfun_raw = self._query(at_command, "AT+CFUN?")
                match = re.search(r"\+CFUN:\s*(\d+)", cfun_raw, re.I)
                cfun = int(match.group(1)) if match else None
            except Exception:
                cfun = None
            diagnostics["radio_function"] = cfun

            if attempted and cfun == 1:
                self.last = UiccHealthResult(
                    ready=False, state="failed",
                    reason=("the bounded UICC reinitialization was already attempted; "
                            "a cold power cycle, SIM reseat, or hardware inspection is required"),
                    diagnostics=diagnostics)
                return self.last

            try:
                if not attempted:
                    # Persist before changing CFUN. A process crash after CFUN=0 must resume
                    # with CFUN=1, never start an unbounded 0/1 cycle.
                    self._record_attempt()
                    at_command("AT+CFUN=0")
                    self.sleeper(1.0)
                at_command("AT+CFUN=1")
                pin, pin_raw = self._wait_ready(at_command)
            except Exception as exc:
                self.last = UiccHealthResult(
                    ready=False, state="recovery_failed",
                    reason=f"UICC reinitialization failed: {exc}", action=self.ACTION,
                    diagnostics=diagnostics)
                return self.last

            diagnostics["pin_after_recovery"] = pin
            if pin != "ready":
                reason = ("the SIM requires a PIN or PUK after radio reinitialization" if
                          pin == "locked" else
                          "the modem still reports a SIM failure after one bounded reinitialization")
                self.last = UiccHealthResult(
                    ready=False, state="locked" if pin == "locked" else "failed",
                    reason=reason, action=self.ACTION, diagnostics=diagnostics)
                return self.last

            self._clear_attempt()
            for command, family in (("AT+CREG?", "CREG"), ("AT+CEREG?", "CEREG")):
                try:
                    diagnostics[family.casefold()] = _registration(
                        self._query(at_command, command), family)
                except Exception:
                    pass
            self.last = UiccHealthResult(
                ready=True, state="recovered", reason="", action=self.ACTION,
                diagnostics=diagnostics)
            return self.last
