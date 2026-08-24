"""Capability-driven cellular call-audio discovery.

This module does not select a default sound card and does not identify a modem by its product
name.  A Windows UAC endpoint is usable only when it belongs to the same PnP container as the
already verified modem control port and the bundled helper can actually open both directions.
"""

from __future__ import annotations

from dataclasses import dataclass, field
import base64
import json
import os
import plistlib
from pathlib import Path
import queue
import re
import shutil
import subprocess
import sys
import threading
from typing import Callable


@dataclass(frozen=True)
class CallAudioProbe:
    ready: bool = False
    backend: str = ""
    activation: str = ""
    reason: str = ""
    details: dict = field(default_factory=dict)

    def public(self) -> dict:
        return {
            "ready": self.ready,
            "backend": self.backend,
            "activation": self.activation,
            "reason": self.reason,
        }


@dataclass(frozen=True)
class CallAudioUsbPreparation:
    """Result of a bounded, capability-driven USB audio composition check."""

    supported: bool = False
    enabled: bool = False
    changed: bool = False
    restart_required: bool = False
    reason: str = ""
    original: str = ""
    configured: str = ""


def _helper_command(configured: str = "") -> list[str]:
    value = str(configured or os.environ.get("MDD_CALL_AUDIO_HELPER") or "").strip()
    if value:
        return [value]
    names = (("mdd-call-audio-helper.exe", "mdd-call-audio-helper")
             if os.name == "nt" else ("mdd-call-audio-helper",))
    candidates: list[Path] = []
    executable = Path(sys.executable).resolve()
    module_dir = Path(__file__).resolve().parent
    bundle = Path(str(getattr(sys, "_MEIPASS", "") or module_dir))
    for name in names:
        candidates.extend((executable.with_name(name), module_dir / name, bundle / name))
    for candidate in candidates:
        if candidate.is_file():
            return [str(candidate)]
    found = next((shutil.which(name) for name in names if shutil.which(name)), None)
    return [found] if found else []


def _powershell_json(script: str, runner=subprocess.run) -> dict:
    encoded = base64.b64encode(script.encode("utf-16-le")).decode("ascii")
    result = runner(
        ["powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", encoded],
        capture_output=True, text=True, timeout=15, check=False,
    )
    if result.returncode:
        detail = str(result.stderr or result.stdout or "PowerShell inventory failed").strip()
        raise RuntimeError(detail[:500])
    try:
        value = json.loads(str(result.stdout or "").strip())
    except (TypeError, json.JSONDecodeError) as exc:
        raise RuntimeError("Windows audio inventory returned invalid JSON") from exc
    if not isinstance(value, dict):
        raise RuntimeError("Windows audio inventory returned a non-object result")
    return value


def _windows_audio_inventory(port: str, runner=subprocess.run) -> dict:
    match = re.fullmatch(r"(?:\\\\\.\\)?(COM\d+)", str(port or "").strip(), re.I)
    if not match:
        raise RuntimeError("Windows call-audio discovery requires a concrete COM port")
    port_name = match.group(1).upper()
    # The COM value has been reduced to COM + digits above, so it is safe as a literal. Device
    # names and localized labels are never parsed; only PnP instance IDs and ContainerId matter.
    script = rf"""
$ErrorActionPreference = 'Stop'
$portName = '{port_name}'
$serial = Get-PnpDevice -Class Ports -PresentOnly | Where-Object {{
    $_.FriendlyName -match ('\(' + [regex]::Escape($portName) + '\)$')
}} | Select-Object -First 1
if ($null -eq $serial) {{ throw ('PnP serial port not found: ' + $portName) }}
$container = (Get-PnpDeviceProperty -InstanceId $serial.InstanceId -KeyName 'DEVPKEY_Device_ContainerId').Data
$endpoints = @()
Get-PnpDevice -Class AudioEndpoint -PresentOnly | ForEach-Object {{
    try {{
        $candidate = (Get-PnpDeviceProperty -InstanceId $_.InstanceId -KeyName 'DEVPKEY_Device_ContainerId').Data
        if ($candidate -eq $container) {{
            $kind = if ($_.InstanceId -match '\\{{0\.0\.0\.') {{ 'playback' }} elseif ($_.InstanceId -match '\\{{0\.0\.1\.') {{ 'capture' }} else {{ 'unknown' }}
            $endpoints += [pscustomobject]@{{ kind = $kind; instance_id = $_.InstanceId; status = [string]$_.Status }}
        }}
    }} catch {{}}
}}
[pscustomobject]@{{
    port_instance_id = $serial.InstanceId
    container_id = [string]$container
    endpoints = @($endpoints)
}} | ConvertTo-Json -Depth 5 -Compress
"""
    return _powershell_json(script, runner=runner)


def _decode_miniaudio_id(value: str) -> str:
    try:
        raw = bytes.fromhex(str(value or ""))
        # miniaudio's textual device ID trims the final zero byte from a Windows WCHAR
        # buffer. Restore only that UTF-16 alignment byte; never fuzzy-match a partial ID.
        if len(raw) % 2:
            raw += b"\0"
        return raw.decode("utf-16-le").rstrip("\0")
    except (ValueError, UnicodeDecodeError):
        return ""


def _decode_utf8_miniaudio_id(value: str) -> str:
    try:
        return bytes.fromhex(str(value or "")).decode("utf-8").rstrip("\0")
    except (ValueError, UnicodeDecodeError):
        return ""


def _mac_audio_inventory(port: str, runner=subprocess.run) -> dict[str, str]:
    match = re.fullmatch(
        r"usb:([0-9a-f]{4}):([0-9a-f]{4}):(\d+):(\d+):([^:]+)",
        str(port or ""), re.I)
    if not match:
        raise RuntimeError("macOS call-audio discovery requires a raw USB attachment identity")
    vid, pid, _bus, _address, serial = match.groups()
    result = runner(
        ["ioreg", "-r", "-c", "AppleUSBAudioEngine", "-a", "-l"],
        capture_output=True, timeout=15, check=False)
    if result.returncode:
        raise RuntimeError("macOS IORegistry audio inventory failed")
    try:
        values = plistlib.loads(result.stdout)
    except Exception as exc:
        raise RuntimeError("macOS IORegistry audio inventory is invalid") from exc
    wanted = {}
    for item in values if isinstance(values, list) else []:
        if (int(item.get("idVendor") or -1) != int(vid, 16) or
                int(item.get("idProduct") or -1) != int(pid, 16)):
            continue
        unique_id = str(item.get("IOAudioEngineGlobalUniqueID") or "")
        if not unique_id or serial not in unique_id.split(":"):
            continue
        directions = {
            int(child.get("IOAudioStreamDirection"))
            for child in item.get("IORegistryEntryChildren") or []
            if child.get("IOObjectClass") == "AppleUSBAudioStream" and
            child.get("IOAudioStreamDirection") is not None
        }
        kind = "capture" if 1 in directions else "playback" if 0 in directions else ""
        if kind:
            if kind in wanted:
                raise RuntimeError("macOS modem exposes multiple ambiguous audio endpoints")
            wanted[kind] = unique_id
    if not wanted.get("playback") or not wanted.get("capture"):
        raise RuntimeError("the modem USB attachment has no full-duplex CoreAudio endpoints")
    return wanted


def _mac_microphone_permission(*, request: bool = False, timeout: float = 30.0) -> str:
    """Inspect TCC and optionally request it from the current interactive process identity."""
    try:
        import AVFoundation  # type: ignore[import-not-found]
    except Exception as exc:
        raise RuntimeError(
            "macOS microphone permission cannot be inspected; use the packaged MDD Agent") from exc
    status = int(AVFoundation.AVCaptureDevice.authorizationStatusForMediaType_(
        AVFoundation.AVMediaTypeAudio))
    if request and status == int(AVFoundation.AVAuthorizationStatusNotDetermined):
        completed = threading.Event()

        def completion(_granted):
            completed.set()

        AVFoundation.AVCaptureDevice.requestAccessForMediaType_completionHandler_(
            AVFoundation.AVMediaTypeAudio, completion)
        completed.wait(max(0.0, timeout))
        status = int(AVFoundation.AVCaptureDevice.authorizationStatusForMediaType_(
            AVFoundation.AVMediaTypeAudio))
    values = {
        int(AVFoundation.AVAuthorizationStatusNotDetermined): "permission_required",
        int(AVFoundation.AVAuthorizationStatusRestricted): "permission_restricted",
        int(AVFoundation.AVAuthorizationStatusDenied): "permission_denied",
        int(AVFoundation.AVAuthorizationStatusAuthorized): "authorized",
    }
    return values.get(status, "permission_unknown")


def _invoke_helper(command: list[str], *arguments: str,
                   runner=subprocess.run, timeout: int = 15) -> dict:
    result = runner(
        [*command, *arguments], capture_output=True, text=True,
        timeout=timeout, check=False,
    )
    try:
        value = json.loads(str(result.stdout or "").strip())
    except (TypeError, json.JSONDecodeError) as exc:
        detail = str(result.stderr or result.stdout or "call-audio helper failed").strip()
        raise RuntimeError(detail[:500]) from exc
    if not isinstance(value, dict):
        raise RuntimeError("call-audio helper returned a non-object result")
    if result.returncode or not value.get("ok"):
        raise RuntimeError(str(value.get("error") or "call-audio helper failed")[:500])
    return value


def _qpcmv_activation(at_command: Callable[[str], bytes]) -> str:
    try:
        response = at_command("AT+QPCMV=?").decode("ascii", "replace")
    except Exception as exc:
        raise RuntimeError(f"modem exposes no supported call-audio activation: {exc}") from exc
    # The command is selected by an observed capability response, never by vendor/model name.
    # Mode 2 is UAC and mode 0 is the serial PCM fallback in the documented command family.
    if not re.search(r"\(0\s*-\s*2\)|(?:^|[,()\s])2(?:[,()\s]|$)", response):
        raise RuntimeError("the modem call-audio command does not advertise UAC mode")
    return "qpcmv-uac"


_USBCFG_PATTERN = re.compile(
    r'\+QCFG:\s*"usbcfg"\s*,\s*'
    r'(0x[0-9a-f]+|\d+)\s*,\s*(0x[0-9a-f]+|\d+)\s*,\s*'
    r'([01])\s*,\s*([01])\s*,\s*([01])\s*,\s*([01])\s*,\s*'
    r'([01])\s*,\s*([01])\s*,\s*([01])(?:\s*(?:\r?\n|$))', re.I)


def prepare_call_audio_usb(at_command: Callable[[str], bytes]) -> CallAudioUsbPreparation:
    """Enable a documented UAC function only when the modem proves the exact schema.

    This is intentionally fail-closed: product names are ignored, all existing VID/PID and
    function flags are preserved, and only the seventh documented function bit may change.
    Re-running the check is read-only once that bit is enabled.
    """
    try:
        _qpcmv_activation(at_command)
        raw = at_command('AT+QCFG="USBCFG"').decode("ascii", "replace")
    except Exception as exc:
        return CallAudioUsbPreparation(reason=f"USB audio configuration is unavailable: {exc}")
    match = _USBCFG_PATTERN.search(raw)
    if not match:
        return CallAudioUsbPreparation(
            reason="the modem did not expose the supported seven-flag USBCFG schema")
    vid, pid, *flags = match.groups()
    original = ",".join((vid, pid, *flags))
    if flags[-1] == "1":
        return CallAudioUsbPreparation(
            supported=True, enabled=True, original=original, configured=original)
    configured_flags = [*flags[:-1], "1"]
    configured = ",".join((vid, pid, *configured_flags))
    command = f'AT+QCFG="USBCFG",{configured}'
    try:
        at_command(command)
    except Exception as exc:
        return CallAudioUsbPreparation(
            supported=True, original=original, configured=configured,
            reason=f"enabling the modem UAC function failed: {exc}")
    # USBCFG is persistent but takes effect only after the module restarts.  A readback before
    # restart proves that the single intended bit was accepted; never reboot on ambiguity.
    try:
        confirmed_raw = at_command('AT+QCFG="USBCFG"').decode("ascii", "replace")
        confirmed = _USBCFG_PATTERN.search(confirmed_raw)
        if not confirmed or list(confirmed.groups()) != [vid, pid, *configured_flags]:
            return CallAudioUsbPreparation(
                supported=True, changed=True, original=original, configured=configured,
                reason="the modem did not confirm the requested UAC composition")
    except Exception as exc:
        return CallAudioUsbPreparation(
            supported=True, changed=True, original=original, configured=configured,
            reason=f"the modem UAC composition could not be verified: {exc}")
    try:
        at_command("AT+CFUN=1,1")
    except Exception:
        # A successful reset normally removes the serial function before it can return OK.
        pass
    return CallAudioUsbPreparation(
        supported=True, enabled=True, changed=True, restart_required=True,
        original=original, configured=configured)


def probe_call_audio(port: str, at_command: Callable[[str], bytes], *,
                     helper: str = "", runner=subprocess.run,
                     allow_permission_prompt: bool = False) -> CallAudioProbe:
    """Run a bounded, non-billable startup probe and return a fail-closed capability."""
    if os.name != "nt" and sys.platform != "darwin":
        return CallAudioProbe(reason="call audio has not been validated on this platform")
    command = _helper_command(helper)
    if not command:
        return CallAudioProbe(reason="the bundled call-audio helper is not installed")
    try:
        activation = _qpcmv_activation(at_command)
        if os.name != "nt" and sys.platform == "darwin":
            permission = _mac_microphone_permission(request=allow_permission_prompt)
            if permission != "authorized":
                raise RuntimeError(
                    f"macOS microphone {permission}; open MDD Agent in the logged-in user "
                    "session and grant microphone access")
            inventory = _mac_audio_inventory(port, runner=runner)
            listed = _invoke_helper(command, "-mode", "list", runner=runner)
            wanted = {}
            for item in listed.get("devices") or []:
                endpoint = _decode_utf8_miniaudio_id(str(item.get("id") or ""))
                kind = str(item.get("kind") or "")
                if inventory.get(kind) == endpoint:
                    wanted[kind] = str(item.get("id") or "")
            container_id = f"usb:{port.rsplit(':', 1)[-1]}"
        else:
            inventory = _windows_audio_inventory(port, runner=runner)
            endpoints = inventory.get("endpoints") or []
            listed = _invoke_helper(command, "-mode", "list", runner=runner)
            wanted = {}
            by_instance = {
                str(item.get("instance_id") or "").split("\\", 2)[-1].casefold():
                str(item.get("kind") or "")
                for item in endpoints if str(item.get("status") or "").casefold() == "ok"
            }
            for item in listed.get("devices") or []:
                endpoint = _decode_miniaudio_id(str(item.get("id") or "")).casefold()
                pnp_kind = by_instance.get(endpoint)
                if pnp_kind and pnp_kind == str(item.get("kind") or ""):
                    wanted[pnp_kind] = str(item.get("id") or "")
            container_id = str(inventory.get("container_id") or "")
        if not wanted.get("playback") or not wanted.get("capture"):
            raise RuntimeError("the modem PnP container has no matching full-duplex UAC endpoints")
        checked = _invoke_helper(
            command, "-mode", "probe",
            "-playback-id", wanted["playback"],
            "-capture-id", wanted["capture"],
            "-duration-ms", "500", runner=runner,
        )
        helper_version = checked.get("version")
        if type(helper_version) is not int or helper_version < 2:
            raise RuntimeError(
                "the bundled call-audio helper is too old; audio telemetry version 2 is required")
        if (int(checked.get("sample_rate") or 0) != 8000 or
                int(checked.get("capture_channels") or 0) != 1 or
                int(checked.get("playback_channels") or 0) != 1):
            raise RuntimeError("the UAC endpoint did not negotiate 8 kHz mono duplex PCM")
        return CallAudioProbe(
            ready=True, backend="uac", activation=activation,
            details={
                "container_id": container_id,
                "playback_id": wanted["playback"], "capture_id": wanted["capture"],
                "helper_version": helper_version,
            },
        )
    except Exception as exc:
        return CallAudioProbe(reason=str(exc).strip() or "call-audio self-test failed")


class CallAudioController:
    """Own exactly one call-scoped helper and restore the modem media route on every exit."""

    def __init__(self, probe: CallAudioProbe, at_command: Callable[[str], bytes], *,
                 helper: str = "", process_factory=subprocess.Popen):
        self.probe = probe
        self.at_command = at_command
        self.command = _helper_command(helper)
        self.process_factory = process_factory
        self.process = None
        self.call_id = ""
        self.lock = threading.RLock()

    def open(self, call_id: str, media_url: str, token: str, tls_pin: str) -> dict:
        if not self.probe.ready or not self.command:
            raise RuntimeError(self.probe.reason or "call audio is unavailable")
        probe_version = self.probe.details.get("helper_version")
        if type(probe_version) is not int or probe_version < 2:
            raise RuntimeError(
                "the call-audio helper does not support required audio telemetry")
        if not re.fullmatch(r"[0-9a-f]{32}", str(call_id or "")):
            raise RuntimeError("invalid call media session id")
        if not str(media_url or "").startswith(("wss://", "ws://")) or not token:
            raise RuntimeError("call media allocation is incomplete")
        with self.lock:
            if self.process and self.process.poll() is None:
                if self.call_id == call_id:
                    return {"ok": True, "ready": True, "call_id": call_id,
                            "backend": self.probe.backend}
                raise RuntimeError("another call already owns the modem audio transport")
            self._stop_locked(restore=False)
            if self.probe.activation != "qpcmv-uac":
                raise RuntimeError("no implemented activation strategy for this audio backend")
            self.at_command("AT+QPCMV=1,2")
            environment = dict(os.environ)
            environment.update({
                "MDD_MEDIA_URL": str(media_url), "MDD_MEDIA_TOKEN": str(token),
                "MDD_MEDIA_TLS_PIN": str(tls_pin or ""),
            })
            try:
                self.process = self.process_factory(
                    [*self.command, "-mode", "bridge",
                     "-playback-id", self.probe.details["playback_id"],
                     "-capture-id", self.probe.details["capture_id"]],
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                    encoding="utf-8", errors="replace", env=environment,
                )
                self.call_id = str(call_id)
                lines: queue.Queue = queue.Queue(maxsize=1)

                def read_ready():
                    try:
                        lines.put(self.process.stdout.readline(4096), timeout=1)
                    except Exception:
                        pass

                threading.Thread(target=read_ready, name="call-audio-ready", daemon=True).start()
                try:
                    line = lines.get(timeout=12)
                    ready = json.loads(str(line or ""))
                except (queue.Empty, TypeError, json.JSONDecodeError) as exc:
                    raise RuntimeError("call-audio helper did not become ready") from exc
                if not isinstance(ready, dict) or not ready.get("ok"):
                    raise RuntimeError(str((ready or {}).get("error") or
                                           "call-audio helper failed to start"))
                ready_version = ready.get("version")
                if type(ready_version) is not int or ready_version < 2:
                    raise RuntimeError(
                        "the running call-audio helper does not support required telemetry")
                process = self.process
                threading.Thread(target=self._watch, args=(process, self.call_id),
                                 name="call-audio-watch", daemon=True).start()
                return {"ok": True, "ready": True, "call_id": self.call_id,
                        "backend": self.probe.backend, "sample_rate": 8000,
                        "channels": 1, "format": "s16le"}
            except Exception:
                self._stop_locked(restore=True)
                raise

    def _watch(self, process, call_id: str) -> None:
        process.wait()
        with self.lock:
            if self.process is process and self.call_id == call_id:
                self.process = None
                self.call_id = ""
                try:
                    self.at_command("AT+QPCMV=0")
                except Exception:
                    pass

    def close(self, call_id: str = "") -> dict:
        with self.lock:
            if call_id and self.call_id and str(call_id) != self.call_id:
                return {"ok": True, "closed": False, "stale": True}
            active = bool(self.process or self.call_id)
            self._stop_locked(restore=True)
            return {"ok": True, "closed": active}

    def _stop_locked(self, *, restore: bool) -> None:
        process, self.process = self.process, None
        self.call_id = ""
        if process and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)
        if restore:
            try:
                self.at_command("AT+QPCMV=0")
            except Exception:
                pass
