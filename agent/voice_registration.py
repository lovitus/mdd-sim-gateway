"""Bounded, capability-driven maintenance of cellular voice registration.

The maintainer never dials, answers, sends SMS, selects an arbitrary carrier profile or
identifies hardware by marketing name.  Standard automatic registration is repaired first.
The vendor IMS recovery is available only when the modem proves the documented Quectel command
contract and its already-selected, already-activated MBN explicitly declares VoLTE support.
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
class VoiceRegistrationResult:
    ready: bool | None = None
    state: str = "unknown"
    reason: str = "voice registration has not been checked"
    bearer: str = ""
    action: str = ""
    restart_required: bool = False
    retry_after: int = 0
    diagnostics: dict = field(default_factory=dict)

    def public(self) -> dict:
        return asdict(self)


def _text(value: bytes | str) -> str:
    return value.decode("ascii", "replace") if isinstance(value, bytes) else str(value or "")


def _registration(value: bytes | str, family: str) -> int | None:
    match = re.search(rf"\+{family}:\s*(?:\d+\s*,\s*)?(\d+)", _text(value), re.I)
    return int(match.group(1)) if match else None


def _cops_mode(value: bytes | str) -> int | None:
    match = re.search(r"\+COPS:\s*(\d+)", _text(value), re.I)
    return int(match.group(1)) if match else None


def _ims(value: bytes | str) -> tuple[int | None, int | None]:
    match = re.search(r'\+QCFG:\s*"ims"\s*,\s*(\d+)\s*,\s*(\d+)', _text(value), re.I)
    return (int(match.group(1)), int(match.group(2))) if match else (None, None)


def _active_volte_mbn(value: bytes | str) -> str:
    for selected, activated, name in re.findall(
            r'\+QMBNCFG:\s*"List"\s*,\s*\d+\s*,\s*([01])\s*,\s*([01])\s*,\s*"([^"]+)"',
            _text(value), re.I):
        if selected == "1" and activated == "1" and "volte" in name.casefold():
            return name
    return ""


def _mbn_autoselect(value: bytes | str) -> int | None:
    match = re.search(
        r'\+QMBNCFG:\s*"AutoSel"\s*,\s*([01])', _text(value), re.I)
    return int(match.group(1)) if match else None


class VoiceRegistrationMaintainer:
    """Run one idempotent recovery stage per check and publish an auditable result."""

    def __init__(self, *, interval: float = 60.0, action_cooldown: float = 120.0,
                 clock: Callable[[], float] = time.monotonic,
                 state_path: Path | str | None = None):
        self.interval = float(interval)
        self.action_cooldown = float(action_cooldown)
        self.clock = clock
        self.lock = threading.RLock()
        self.checked_at = 0.0
        self.action_at = 0.0
        self.last = VoiceRegistrationResult()
        self.state_path = Path(state_path) if state_path else None
        self.state = TransactionalJsonState(self.state_path)
        self.context = ""

    def set_context(self, *values: str) -> None:
        self.context = "\0".join(str(value or "") for value in values)

    def _action_key(self, action: str, profile: str = "") -> str:
        material = f"{self.context}\0{action}\0{profile}".encode("utf-8")
        return hashlib.sha256(material).hexdigest()

    def _load_actions(self) -> dict:
        return self.state.load()

    def _record_action(self, action: str, profile: str = "") -> None:
        if not self.state_path:
            return
        key = self._action_key(action, profile)
        self.state.update(lambda value: value.__setitem__(key, int(time.time())))

    def _action_attempted(self, action: str, profile: str = "") -> bool:
        return self._action_key(action, profile) in self._load_actions()

    def _action_age(self, action: str, profile: str = "") -> float | None:
        value = self._load_actions().get(self._action_key(action, profile))
        try:
            return max(0.0, time.time() - float(value))
        except (TypeError, ValueError):
            return None

    @staticmethod
    def _query(at_command: Callable[[str], bytes], command: str) -> str:
        return _text(at_command(command))

    def check(self, at_command: Callable[[str], bytes], *, force: bool = False,
              allow_repair: bool = True) -> VoiceRegistrationResult:
        with self.lock:
            now = self.clock()
            if not force and self.checked_at and now - self.checked_at < self.interval:
                return self.last
            self.checked_at = now
            try:
                cs = _registration(self._query(at_command, "AT+CREG?"), "CREG")
                eps = _registration(self._query(at_command, "AT+CEREG?"), "CEREG")
            except Exception as exc:
                self.last = VoiceRegistrationResult(
                    reason=f"voice registration probe failed: {exc}")
                return self.last

            diagnostics = {"cs": cs, "eps": eps}
            if cs in {1, 5}:
                self.last = VoiceRegistrationResult(
                    ready=True, state="registered", reason="", bearer="cs",
                    diagnostics=diagnostics)
                return self.last

            try:
                ims_conf, volte_cap = _ims(self._query(at_command, 'AT+QCFG="ims"'))
            except Exception:
                ims_conf, volte_cap = None, None
            diagnostics.update({"ims_config": ims_conf, "volte_capability": volte_cap})
            if eps in {1, 5} and volte_cap == 1:
                self.last = VoiceRegistrationResult(
                    ready=True, state="registered", reason="", bearer="ims",
                    diagnostics=diagnostics)
                return self.last

            try:
                cfun_raw = self._query(at_command, "AT+CFUN?")
                cfun_match = re.search(r"\+CFUN:\s*(\d+)", cfun_raw, re.I)
                cfun = int(cfun_match.group(1)) if cfun_match else None
            except Exception:
                cfun = None
            try:
                cops = _cops_mode(self._query(at_command, "AT+COPS?"))
            except Exception:
                cops = None
            try:
                serving = self._query(at_command, 'AT+QENG="servingcell"')
            except Exception:
                serving = ""
            limited = bool(re.search(r'"(?:LIMSRV|SEARCH)"', serving, re.I))
            lte = bool(re.search(r'"LTE"', serving, re.I))
            diagnostics.update({
                "radio_function": cfun, "operator_mode": cops,
                "limited_service": limited, "access": "lte" if lte else "unknown",
            })

            cooldown = max(0, round(self.action_cooldown - (now - self.action_at)))
            if cfun is None:
                self.last = VoiceRegistrationResult(
                    ready=None, state="unknown",
                    reason="the modem did not expose an authoritative radio-function state",
                    diagnostics=diagnostics)
                return self.last
            if cfun != 1:
                self.last = VoiceRegistrationResult(
                    ready=False, state="radio_off",
                    reason="the modem radio is not in full-function mode",
                    diagnostics=diagnostics)
                return self.last

            # 3GPP TS 27.007 mode 2 is an explicit network deregistration. Restoring automatic
            # selection is standard, idempotent and precedes every vendor-specific repair.
            if cops == 2:
                if not allow_repair or cooldown:
                    self.last = VoiceRegistrationResult(
                        ready=None, state="deregistered",
                        reason="the modem is explicitly deregistered from the mobile network",
                        retry_after=cooldown, diagnostics=diagnostics)
                    return self.last
                try:
                    at_command("AT+COPS=0")
                    confirmed = _cops_mode(self._query(at_command, "AT+COPS?"))
                    if confirmed == 2:
                        raise RuntimeError("automatic operator mode was not retained")
                    diagnostics["operator_mode"] = confirmed
                except Exception as exc:
                    self.last = VoiceRegistrationResult(
                        ready=False, state="recovery_failed",
                        reason=f"automatic network registration recovery failed: {exc}",
                        action="automatic_registration", diagnostics=diagnostics)
                    return self.last
                self.action_at = now
                self.last = VoiceRegistrationResult(
                    ready=None, state="recovering",
                    reason="automatic mobile-network registration was restored",
                    action="automatic_registration", retry_after=round(self.action_cooldown),
                    diagnostics=diagnostics)
                return self.last

            # QCFG/MBNCFG is deliberately capability-selected. Config 0 delegates IMS to the
            # active MBN; only an already-selected and activated profile explicitly named
            # VoLTE proves that forcing the documented IMS function is consistent with that
            # profile. Config 2 is an explicit disable and is never overridden.
            active_mbn = ""
            autoselect = None
            if limited and lte and volte_cap == 0 and ims_conf != 2:
                try:
                    active_mbn = _active_volte_mbn(
                        self._query(at_command, 'AT+QMBNCFG="List"'))
                except Exception:
                    active_mbn = ""
                try:
                    autoselect = _mbn_autoselect(
                        self._query(at_command, 'AT+QMBNCFG="AutoSel"'))
                except Exception:
                    autoselect = None
            diagnostics.update({"voice_profile": active_mbn,
                                "profile_autoselect": autoselect})
            if (allow_repair and not cooldown and active_mbn and
                    ims_conf == 0):
                try:
                    at_command('AT+QCFG="ims",1')
                    confirmed_conf, _ = _ims(self._query(at_command, 'AT+QCFG="ims"'))
                    if confirmed_conf != 1:
                        raise RuntimeError("IMS enable was not retained")
                    diagnostics["ims_config"] = confirmed_conf
                    try:
                        at_command("AT+CFUN=1,1")
                    except Exception:
                        # A successful module reset commonly removes the serial function before
                        # it can return its final OK.
                        pass
                except Exception as exc:
                    self.last = VoiceRegistrationResult(
                        ready=False, state="recovery_failed",
                        reason=f"documented IMS recovery failed: {exc}",
                        action="enable_selected_volte_profile", diagnostics=diagnostics)
                    return self.last
                self.action_at = now
                self.last = VoiceRegistrationResult(
                    ready=None, state="restarting",
                    reason="IMS was enabled for the selected active VoLTE profile",
                    action="enable_selected_volte_profile", restart_required=True,
                    retry_after=round(self.action_cooldown), diagnostics=diagnostics)
                return self.last

            # Quectel documents Deactivate as an NVM write. It is therefore used only as the
            # second stage of the exact, already-selected VoLTE recovery: automatic profile
            # selection must remain enabled, IMS must already be forced on, and the current
            # profile must still be selected+activated while LTE remains limited. The durable
            # fingerprint prevents a service restart from repeating the NVM write/reset loop.
            reapply = "reapply_active_volte_profile"
            attempted = bool(active_mbn and self._action_attempted(reapply, active_mbn))
            reapply_age = self._action_age(reapply, active_mbn) if active_mbn else None
            durable_guard = bool(self.state_path and self.context)
            diagnostics["profile_reapply_attempted"] = attempted
            diagnostics["profile_reapply_age"] = (
                round(reapply_age) if reapply_age is not None else None)
            diagnostics["persistent_recovery_guard"] = durable_guard
            if (allow_repair and not cooldown and active_mbn and autoselect == 1 and
                    ims_conf == 1 and volte_cap == 0 and not attempted and durable_guard):
                try:
                    # Persist the one-shot guard before the NVM command. If the process or USB
                    # function disappears during the write, a service restart must not repeat
                    # an action whose outcome is unknown.
                    self._record_action(reapply, active_mbn)
                    at_command('AT+QMBNCFG="Deactivate"')
                    try:
                        at_command("AT+CFUN=1,1")
                    except Exception:
                        pass
                except Exception as exc:
                    self.last = VoiceRegistrationResult(
                        ready=False, state="recovery_failed",
                        reason=f"active VoLTE profile reapply failed: {exc}",
                        action=reapply, diagnostics=diagnostics)
                    return self.last
                self.action_at = now
                self.last = VoiceRegistrationResult(
                    ready=None, state="restarting",
                    reason="the active VoLTE profile is being reapplied after IMS enablement",
                    action=reapply, restart_required=True,
                    retry_after=round(self.action_cooldown), diagnostics=diagnostics)
                return self.last

            # If the process or USB ownership changed around the MBN reset, the persistent
            # reapply may have completed while the old SIM session was still held. After the
            # full cooldown, permit one plain module reset with no additional NVM/profile
            # mutation. Its own durable fingerprint makes this a terminal, non-looping stage.
            stabilize = "stabilize_after_profile_reapply"
            stabilized = bool(active_mbn and self._action_attempted(stabilize, active_mbn))
            diagnostics["post_reapply_restart_attempted"] = stabilized
            if (allow_repair and not cooldown and attempted and not stabilized and
                    reapply_age is not None and reapply_age >= self.action_cooldown and
                    active_mbn and autoselect == 1 and ims_conf == 1 and volte_cap == 0 and
                    durable_guard):
                try:
                    self._record_action(stabilize, active_mbn)
                    try:
                        at_command("AT+CFUN=1,1")
                    except Exception:
                        pass
                except Exception as exc:
                    self.last = VoiceRegistrationResult(
                        ready=False, state="recovery_failed",
                        reason=f"post-profile registration restart failed: {exc}",
                        action=stabilize, diagnostics=diagnostics)
                    return self.last
                self.action_at = now
                self.last = VoiceRegistrationResult(
                    ready=None, state="restarting",
                    reason="the modem is restarting once after the VoLTE profile reapply",
                    action=stabilize, restart_required=True,
                    retry_after=round(self.action_cooldown), diagnostics=diagnostics)
                return self.last

            reason = "neither circuit-switched registration nor an available IMS session exists"
            if limited:
                reason = "the serving network currently provides limited service without a voice bearer"
            if attempted and stabilized:
                reason = ("the documented VoLTE recovery was already attempted; the network "
                          "still exposes no voice bearer")
            self.last = VoiceRegistrationResult(
                ready=None, state="pending", reason=reason,
                retry_after=cooldown, diagnostics=diagnostics)
            return self.last
