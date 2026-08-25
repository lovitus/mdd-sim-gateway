"""
ami.py - Async Asterisk AMI client (per engine instance).

The manager keeps one AmiClient per running instance to: read IMS registration state,
send SMS (AMI MessageSend to the volte_ims endpoint), place calls (Originate), and
receive live events. Incoming call/SMS are primarily delivered via the engine's
notify.py HTTP hooks; AMI events supplement call state.
"""
from __future__ import annotations

import asyncio
import logging
import re
from collections.abc import Awaitable, Callable

from panoramisk import Manager

log = logging.getLogger("vowifi.ami")


class StaleAmiGeneration(RuntimeError):
    """The exact Engine identity changed during a one-shot AMI transaction."""


def browser_media_canary_action(engine_sid: str, channel_id: str) -> dict:
    """Build the only AMI action E1 may send; no caller-controlled dial field exists."""
    if not re.fullmatch(r"[A-Za-z0-9_-]{24,32}", str(engine_sid or "")):
        raise ValueError("invalid media WebSocket session id")
    if not re.fullmatch(r"mddcanary-[0-9a-f-]{36}", str(channel_id or ""), re.I):
        raise ValueError("invalid media canary channel id")
    channel = f"WebSocket/mdd_control_media/c(slin)nf(json)v(sid={engine_sid})"
    if len(channel) > 160:
        raise ValueError("media WebSocket dial string is too long")
    return {
        "Action": "Originate",
        "Channel": channel,
        "Context": "browser-media-canary",
        "Exten": "echo",
        "Priority": "1",
        "CallerID": "mdd-media-canary",
        "ChannelId": channel_id,
        "Timeout": "5000",
        "Async": "true",
    }


def _complete_channel_snapshot(messages) -> dict:
    """Validate one CoreShowChannels response without treating a partial list as empty."""
    items = messages if isinstance(messages, list) else [messages]
    if len(items) > 514:  # response + at most 512 channels + completion
        return {"ok": False, "error": "channel snapshot exceeds safety limit"}
    complete = [item for item in items
                if str(item.get("Event") or "").casefold()
                == "coreshowchannelscomplete"]
    if len(complete) != 1:
        return {"ok": False, "error": "channel snapshot is incomplete"}
    channels = [item for item in items
                if str(item.get("Event") or "").casefold() == "coreshowchannel"]
    raw_count = complete[0].get("ListItems")
    if type(raw_count) is int:
        declared = raw_count
    elif isinstance(raw_count, str) and raw_count.isdigit():
        declared = int(raw_count)
    else:
        return {"ok": False, "error": "channel snapshot count is invalid"}
    if declared != len(channels) or declared > 512:
        return {"ok": False, "error": "channel snapshot count is inconsistent"}
    return {"ok": True, "channels": channels, "count": declared}


class OneShotAmiSession:
    """Non-reconnecting AMI transaction for destructive exact-call operations.

    Panoramisk normally queues unanswered actions and replays them after reconnect. That is
    useful for telemetry but unsafe for Hangup: an old Asterisk channel name can be reused by a
    new Engine generation. This session permits exactly one underlying TCP connection, never
    enters the shared Hub cache, and fails closed if that socket or the Engine identity changes.
    """

    CONNECT_TIMEOUT = 4.0
    LOGIN_TIMEOUT = 3.0
    ACTION_TIMEOUT = 2.0
    TRANSACTION_TIMEOUT = 12.0

    def __init__(self, instance_id: str, host: str, port: int, username: str, secret: str,
                 generation_current: Callable[[], Awaitable[bool]], *,
                 transaction_timeout: float | None = None,
                 manager_factory=Manager):
        self.instance_id = str(instance_id)
        self.host = str(host)
        self.port = int(port)
        self.username = str(username)
        self.secret = str(secret)
        self._generation_current = generation_current
        self._transaction_timeout = float(
            transaction_timeout or self.TRANSACTION_TIMEOUT)
        self._manager_factory = manager_factory
        self._mgr: Manager | None = None
        self._protocol = None
        self._deadline = 0.0
        self._connect_used = False
        self._disconnected = asyncio.Event()
        self._closed = False

    def _on_disconnect(self, *_args) -> None:
        self._disconnected.set()

    def _remaining(self, cap: float | None = None) -> float:
        remaining = self._deadline - asyncio.get_running_loop().time()
        if remaining <= 0:
            raise asyncio.TimeoutError("one-shot AMI transaction deadline exceeded")
        return min(remaining, float(cap)) if cap is not None else remaining

    async def _bounded(self, awaitable, cap: float | None = None):
        try:
            timeout = self._remaining(cap)
        except BaseException:
            # Callers may already have created a coroutine/Future before the total deadline is
            # checked. Dispose it here so budget exhaustion cannot leak an un-awaited identity
            # probe or leave an AMI action future eligible for later completion/replay.
            if asyncio.iscoroutine(awaitable):
                awaitable.close()
            elif asyncio.isfuture(awaitable) and not awaitable.done():
                awaitable.cancel()
            raise
        return await asyncio.wait_for(awaitable, timeout=timeout)

    async def _identity_current(self) -> bool:
        try:
            return bool(await self._bounded(self._generation_current(), 2.0))
        except Exception:
            return False

    def _assert_live_socket(self) -> None:
        protocol = self._protocol
        if (self._closed or self._disconnected.is_set() or not self._mgr
                or self._mgr.protocol is not protocol or protocol is None
                or getattr(protocol, "closed", False)
                or not getattr(self._mgr, "authenticated", False)):
            raise ConnectionError("one-shot AMI socket is no longer authoritative")

    async def __aenter__(self):
        self._deadline = (asyncio.get_running_loop().time()
                          + self._transaction_timeout)
        if not await self._identity_current():
            raise StaleAmiGeneration("engine generation changed before AMI connect")
        self._mgr = self._manager_factory(
            host=self.host, port=self.port, username=self.username, secret=self.secret,
            ping_delay=60, reconnect_timeout=5, on_disconnect=self._on_disconnect)
        original_connect = self._mgr.connect

        # Install the guard before the very first connect. Both initial TCP failure and later
        # connection_lost schedule self.connect(); every such scheduled call becomes a no-op.
        def connect_once(*args, **kwargs):
            if self._connect_used or self._closed:
                return None
            self._connect_used = True
            return original_connect(*args, **kwargs)

        self._mgr.connect = connect_once
        try:
            connect_task = self._mgr.connect()
            if connect_task is None:
                raise ConnectionError("one-shot AMI connection was not started")
            await self._bounded(connect_task, self.CONNECT_TIMEOUT)
            for _ in range(32):
                if self._mgr.authenticated_future is not None:
                    break
                await asyncio.sleep(0)
            auth_future = self._mgr.authenticated_future
            if auth_future is None:
                raise ConnectionError("one-shot AMI login did not start")
            response = await self._bounded(asyncio.shield(auth_future), self.LOGIN_TIMEOUT)
            await asyncio.sleep(0)  # allow Manager.login() to publish authenticated=True
            if not getattr(response, "success", False) or not self._mgr.authenticated:
                raise PermissionError("one-shot AMI login failed")
            self._protocol = self._mgr.protocol
            self._assert_live_socket()
            if not await self._identity_current():
                raise StaleAmiGeneration("engine generation changed during AMI connect")
            return self
        except BaseException:
            await self.close()
            raise

    async def __aexit__(self, _exc_type, _exc, _tb):
        await self.close()

    async def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        manager = self._mgr
        if not manager:
            return
        pending: dict[int, object] = {}
        protocol = getattr(manager, "protocol", None)
        for action in (getattr(protocol, "responses", {}) or {}).values():
            pending[id(action)] = action
        try:
            manager.close()
        except Exception:
            pass
        for action in list(getattr(manager, "awaiting_actions", ())):
            pending[id(action)] = action
        manager.awaiting_actions.clear()
        for action in pending.values():
            future = getattr(action, "future", None)
            if future is not None and not future.done():
                future.set_exception(ConnectionError("one-shot AMI session closed"))

    async def action(self, payload: dict, timeout: float | None = None):
        if not await self._identity_current():
            raise StaleAmiGeneration("engine generation changed before AMI action")
        self._assert_live_socket()
        try:
            result = await self._bounded(
                self._mgr.send_action(dict(payload)), timeout or self.ACTION_TIMEOUT)
        except BaseException:
            await self.close()
            raise
        self._assert_live_socket()
        if not await self._identity_current():
            raise StaleAmiGeneration("engine generation changed after AMI action")
        return result

    async def _snapshot(self) -> dict:
        return _complete_channel_snapshot(await self.action(
            {"Action": "CoreShowChannels"}, timeout=2.0))

    async def _sleep(self, delay: float) -> None:
        if delay:
            await self._bounded(asyncio.sleep(delay), delay + 0.1)
        if not await self._identity_current():
            raise StaleAmiGeneration("engine generation changed during terminal verification")

    async def hangup_channels_by_linkedid(self, linkedid: str) -> dict:
        """Terminate all legs of one linkedid without allowing cross-generation replay."""
        linkedid = str(linkedid or "")
        if not re.fullmatch(r"[A-Za-z0-9_.-]{1,160}", linkedid):
            return {"ok": False, "outcome": "unknown", "attempted": 0,
                    "remaining": None, "error": "invalid linkedid"}

        def matching(result: dict) -> list[dict]:
            return [item for item in result.get("channels", [])
                    if str(item.get("Linkedid") or "") == linkedid]

        attempted = 0
        malformed_channel = False
        try:
            initial = await self._snapshot()
            if not initial.get("ok"):
                return {"ok": False, "outcome": "unknown", "attempted": 0,
                        "remaining": None,
                        "error": initial.get("error", "channel snapshot unavailable")}
            matches = matching(initial)
            if len(matches) > 16:
                return {"ok": False, "outcome": "unknown", "attempted": 0,
                        "remaining": len(matches),
                        "error": "linked call exceeds safety leg limit"}
            if not matches:
                await self._sleep(0.1)
                confirmed = await self._snapshot()
                if confirmed.get("ok") and not matching(confirmed):
                    return {"ok": True, "terminal_confirmed": True,
                            "outcome": "already_terminal", "attempted": 0, "remaining": 0}
                return {"ok": False, "outcome": "unknown", "attempted": 0,
                        "remaining": (len(matching(confirmed)) if confirmed.get("ok") else None),
                        "error": confirmed.get("error", "call appeared during terminal check")}

            for message in matches:
                channel = str(message.get("Channel") or "")
                if not channel or len(channel) > 240:
                    malformed_channel = True
                    continue
                try:
                    await self.action({
                        "Action": "Hangup", "Channel": channel, "Cause": "16",
                    }, timeout=2.0)
                except StaleAmiGeneration:
                    raise
                except Exception:
                    # A disappearing old-generation leg does not prevent bounded attempts on
                    # the remaining legs. The same one-shot socket cannot reconnect/replay.
                    pass
                finally:
                    attempted += 1

            last_remaining = None
            for delay in (0.0, 0.25, 0.75):
                await self._sleep(delay)
                verified = await self._snapshot()
                if not verified.get("ok"):
                    return {"ok": False, "outcome": "unknown", "attempted": attempted,
                            "remaining": None,
                            "error": verified.get("error", "terminal verification unavailable")}
                remaining = matching(verified)
                last_remaining = len(remaining)
                if not remaining:
                    return {"ok": True, "terminal_confirmed": True,
                            "outcome": "terminated", "attempted": attempted, "remaining": 0}
            outcome = "unknown" if malformed_channel else "partial"
            return {"ok": False, "terminal_confirmed": False, "outcome": outcome,
                    "attempted": attempted, "remaining": last_remaining,
                    "error": ("channel snapshot contains invalid identity"
                              if malformed_channel else "exact call still has active channels")}
        except StaleAmiGeneration as exc:
            return {"ok": False, "outcome": "stale", "attempted": attempted,
                    "remaining": None, "error": str(exc)}
        except Exception as exc:  # noqa
            return {"ok": False, "outcome": "unknown", "attempted": attempted,
                    "remaining": None, "error": repr(exc)}


class AmiClient:
    # Hard bounds so a wedged AMI connection can never hang the status poller / API.
    CONNECT_TIMEOUT = 6.0    # login handshake
    ACTION_TIMEOUT = 8.0     # any single AMI action (send_action) response

    def __init__(self, instance_id: str, host: str, port: int, username: str, secret: str,
                 realm: str, msisdn: str = "", smsc: str = ""):
        self.instance_id = str(instance_id)
        self.host = host
        self.port = port
        self.username = username
        self.secret = secret
        self.realm = realm
        self.msisdn = msisdn
        self.smsc = smsc
        self._mgr: Manager | None = None
        self._connected = False
        self._closed = False
        self._event_cb = None

    async def connect(self):
        self._mgr = Manager(host=self.host, port=self.port,
                            username=self.username, secret=self.secret,
                            ping_delay=15, reconnect_timeout=5)
        # panoramisk auto-reconnects on connection loss/refusal by scheduling
        # loop.call_later(reconnect_timeout, self.connect); its close() only cancels the
        # pinger, NOT that pending timer — so a Manager whose target container was stopped
        # or recreated with a NEW AMI secret keeps reconnecting forever and floods the new
        # Asterisk with "failed to authenticate as 'vowifi'" every few seconds. Wrap connect()
        # so that once we close() this client, any queued/scheduled reconnect becomes a no-op.
        self._closed = False
        _orig_connect = self._mgr.connect

        def _guarded_connect(*a, **k):
            if self._closed:
                return None            # client closed -> stop the reconnect loop dead
            return _orig_connect(*a, **k)

        self._mgr.connect = _guarded_connect
        try:
            # Bound the login handshake: a half-open TCP (e.g. the container was just
            # recreated on the same IP) must not block the caller indefinitely.
            await asyncio.wait_for(self._mgr.connect(), timeout=self.CONNECT_TIMEOUT)
            self._connected = True
            log.info("AMI connected instance=%s %s:%s", self.instance_id, self.host, self.port)
        except Exception as e:  # noqa  (asyncio.TimeoutError included)
            self._connected = False
            log.warning("AMI connect failed instance=%s: %r", self.instance_id, e)

    async def _action(self, action: dict, timeout: float | None = None):
        """Send an AMI action with a hard timeout. panoramisk's send_action awaits a Future
        that resolves when the matching AMI response arrives; if the connection is wedged
        (socket up but Asterisk not answering, or a reconnect orphaned the in-flight future)
        that Future never resolves. Without this bound a single stuck action hangs the status
        poller AND the /api/instances handler forever. On timeout we mark the client
        disconnected so ami_for() rebuilds it on the next call, and re-raise TimeoutError."""
        try:
            return await asyncio.wait_for(self._mgr.send_action(action),
                                          timeout=timeout or self.ACTION_TIMEOUT)
        except asyncio.TimeoutError:
            log.warning("AMI action timed out instance=%s action=%s -> marking disconnected",
                        self.instance_id, action.get("Action"))
            self._connected = False
            raise

    async def close(self):
        # Mark closed FIRST so any reconnect that panoramisk already scheduled
        # (loop.call_later -> self.connect) turns into a no-op via the guard installed in
        # connect(). Otherwise the Manager keeps dialing the (now stopped / re-secreted)
        # engine forever, flooding its Asterisk with AMI auth failures.
        self._closed = True
        if self._mgr:
            try:
                self._mgr.close()
            except Exception:
                pass
        self._connected = False

    @property
    def connected(self):
        return self._connected and self._mgr is not None and not self._closed

    async def registration_state(self) -> str:
        """Return 'Registered' | 'Rejected' | 'Unregistered' | 'unknown'."""
        if not self.connected:
            return "unknown"
        # Some IMS-patched Asterisk builds never finish PJSIPShowRegistrationsDetailed even
        # while registration is healthy. AMI Command uses the same reliable CLI view as
        # ``asterisk -rx`` without creating a Docker exec process for every status sample.
        try:
            res = await self._action(
                {"Action": "Command", "Command": "pjsip show registrations"}, timeout=3.0)
            text = ""
            for m in (res if isinstance(res, list) else [res]):
                text += str(m.get("Output") or m.get("content") or "")
            if "Registered" in text:
                return "Registered"
            if "Rejected" in text:
                return "Rejected"
            if "Unregistered" in text:
                return "Unregistered"
        except Exception as e:  # noqa
            log.debug("reg state error: %r", e)
        return "unknown"

    async def active_channel_count(self) -> int | None:
        """Return the number of live Asterisk channels, or ``None`` when unreadable.

        A stale IMS registration does not necessarily tear down an established call: its RTP
        can still be flowing through the otherwise-live ESP tunnel.  Automatic recovery must
        therefore fail closed when checking for calls.  AMI Command is used for the same reason
        as registration_state(): it is bounded and reliable on the supported IMS-patched build.
        """
        if not self.connected:
            return None
        try:
            res = await self._action(
                {"Action": "Command", "Command": "core show channels count"}, timeout=3.0)
            text = ""
            for message in (res if isinstance(res, list) else [res]):
                text += str(message.get("Output") or message.get("content") or "") + "\n"
            match = re.search(r"\b(\d+)\s+active channels?\b", text, re.I)
            return int(match.group(1)) if match else None
        except Exception as exc:  # noqa
            log.debug("active channel count error: %r", exc)
            return None

    async def zero_channels_complete(self, timeout: float = 2.0) -> bool | None:
        """Prove a complete CoreShowChannels snapshot contains no live channels.

        Recovery uses the completion event rather than a textual count so a partial AMI reply
        can never be mistaken for an idle line. ``None`` is fail-closed/unknown.
        """
        if not self.connected:
            return None
        try:
            messages = await self._action({"Action": "CoreShowChannels"}, timeout=timeout)
            items = messages if isinstance(messages, list) else [messages]
            complete = next((item for item in items
                             if str(item.get("Event") or "").casefold()
                             == "coreshowchannelscomplete"), None)
            if complete is None:
                return None
            observed = sum(1 for item in items
                           if str(item.get("Event") or "").casefold() == "coreshowchannel")
            raw_count = complete.get("ListItems")
            if type(raw_count) is int:
                declared = raw_count
            elif isinstance(raw_count, str) and raw_count.isdigit():
                declared = int(raw_count)
            else:
                return None
            if declared != observed:
                return None
            return declared == 0
        except Exception as exc:  # noqa
            log.debug("complete channel snapshot failed instance=%s: %r", self.instance_id, exc)
            return None

    async def zero_usim_recovery_call_channels_complete(
            self, timeout: float = 2.0) -> bool | None:
        """Prove local-USIM recovery cannot interrupt a voice call.

        Asterisk keeps one internal ``Message/ast_msg_queue`` channel for dialplan text
        processing.  It is not an active voice call, but it is still returned by
        CoreShowChannels after the SMS dialplan reaches Hangup.  Ignore only that exact,
        terminal pseudo-channel; every incomplete snapshot, near match or other channel
        remains fail-closed.
        """
        if not self.connected:
            return None
        try:
            snapshot = _complete_channel_snapshot(await self._action(
                {"Action": "CoreShowChannels"}, timeout=timeout))
            if snapshot.get("ok") is not True:
                return None
            channels = snapshot["channels"]
            if not channels:
                return True
            if len(channels) != 1:
                return False
            channel = channels[0]
            return (channel.get("Channel") == "Message/ast_msg_queue"
                    and channel.get("Context") == "volte_ims_msg"
                    and channel.get("Application") == "Hangup"
                    and channel.get("ChannelStateDesc") == "Up")
        except Exception as exc:  # noqa
            log.debug("USIM recovery call snapshot failed instance=%s: %r",
                      self.instance_id, exc)
            return None

    async def channel_rtp_counts(self, uniqueid: str) -> dict | None:
        """Return exact-channel RTP tx/rx counters, or ``None`` when not authoritative.

        The browser's getStats report is useful evidence but is client supplied.  Paid-call
        admission additionally requires Asterisk to observe packets on the exact no-charge
        Echo channel identified by its own Uniqueid.
        """
        if not self.connected or not uniqueid or len(str(uniqueid)) > 160:
            return None
        try:
            channels = await self._action({"Action": "CoreShowChannels"}, timeout=3.0)
            channel = ""
            for message in channels if isinstance(channels, list) else [channels]:
                if (str(message.get("Uniqueid") or "") == str(uniqueid)
                        or str(message.get("Linkedid") or "") == str(uniqueid)):
                    candidate = str(message.get("Channel") or "")
                    if candidate:
                        channel = candidate
                        break
            if not channel:
                return None
            response = await self._action({
                "Action": "Getvar", "Channel": channel,
                "Variable": "CHANNEL(rtpqos,audio,all)",
            }, timeout=3.0)
            value = ""
            for message in response if isinstance(response, list) else [response]:
                if message.get("Value") is not None:
                    value = str(message.get("Value") or "")
                    break
            tx = re.search(r"(?:^|;)txcount=(\d+)(?:;|$)", value, re.I)
            rx = re.search(r"(?:^|;)rxcount=(\d+)(?:;|$)", value, re.I)
            if not tx or not rx:
                return None
            return {"tx_packets": int(tx.group(1)), "rx_packets": int(rx.group(1)),
                    "channel": channel}
        except Exception as exc:  # noqa
            log.debug("RTP counter read failed instance=%s: %r", self.instance_id, exc)
            return None

    async def hangup_channels_by_variable(self, variable: str, values: set[str]) -> dict:
        """Hang up only browser channels carrying one of the exact admission tokens."""
        if (not self.connected or variable != "MDD_MEDIA_TOKEN" or not values
                or len(values) > 32):
            return {"ok": False, "matched": 0, "error": "invalid targeted hangup"}
        expected = {str(value) for value in values if 20 <= len(str(value)) <= 160}
        if not expected:
            return {"ok": False, "matched": 0, "error": "invalid targeted hangup"}
        try:
            channels = await self._action({"Action": "CoreShowChannels"}, timeout=3.0)
            matched = 0
            for message in (channels if isinstance(channels, list) else [channels])[:64]:
                channel = str(message.get("Channel") or "")
                if not channel.startswith("PJSIP/"):
                    continue
                response = await self._action({
                    "Action": "Getvar", "Channel": channel, "Variable": variable,
                }, timeout=2.0)
                observed = ""
                for item in response if isinstance(response, list) else [response]:
                    if item.get("Value") is not None:
                        observed = str(item.get("Value") or "")
                        break
                if observed not in expected:
                    continue
                result = await self._action({
                    "Action": "Hangup", "Channel": channel, "Cause": "16",
                }, timeout=3.0)
                first = result[0] if isinstance(result, list) and result else result
                if str((first or {}).get("Response") or "").casefold() == "success":
                    matched += 1
            return {"ok": True, "matched": matched}
        except Exception as exc:  # noqa
            return {"ok": False, "matched": 0, "error": repr(exc)}

    async def _exact_channel(self, uniqueid: str) -> str:
        if not self.connected or not re.fullmatch(r"[A-Za-z0-9_.-]{1,160}", str(uniqueid)):
            return ""
        channels = await self._action({"Action": "CoreShowChannels"}, timeout=3.0)
        for message in channels if isinstance(channels, list) else [channels]:
            if str(message.get("Uniqueid") or "") == str(uniqueid):
                return str(message.get("Channel") or "")
        return ""

    async def renew_channel_absolute_timeout(self, uniqueid: str, seconds: int = 10) -> bool:
        """Renew the Asterisk-local safety lease on one exact browser channel."""
        if type(seconds) is not int or not 5 <= seconds <= 30:
            return False
        try:
            channel = await self._exact_channel(uniqueid)
            if not channel:
                return False
            result = await self._action({
                "Action": "Setvar", "Channel": channel,
                "Variable": "TIMEOUT(absolute)", "Value": str(seconds),
            }, timeout=2.0)
            first = result[0] if isinstance(result, list) and result else result
            return str((first or {}).get("Response") or "").casefold() == "success"
        except Exception:
            return False

    async def hangup_channel(self, uniqueid: str) -> bool:
        """Hang up one exact Asterisk channel; never falls back to a broad hangup."""
        try:
            channel = await self._exact_channel(uniqueid)
            if not channel:
                return True
            result = await self._action({
                "Action": "Hangup", "Channel": channel, "Cause": "16",
            }, timeout=3.0)
            first = result[0] if isinstance(result, list) and result else result
            return str((first or {}).get("Response") or "").casefold() == "success"
        except Exception:
            return False

    async def complete_channel_snapshot(self) -> dict:
        """Return one bounded, internally consistent CoreShowChannels snapshot."""
        messages = await self._action({"Action": "CoreShowChannels"}, timeout=3.0)
        return _complete_channel_snapshot(messages)

    async def send_sms(self, to: str, body: str) -> dict:
        if not self.connected:
            return {"ok": False, "error": "AMI not connected"}
        dest = f"pjsip:volte_ims/{to}@volte_ims"
        frm = f"sip:{self.msisdn or to}@{self.realm}"
        try:
            res = await self._action(
                {"Action": "MessageSend", "To": dest, "From": frm, "Body": body})
            msg = res[0] if isinstance(res, list) else res
            ok = (msg.get("Response") == "Success")
            return {"ok": ok, "detail": msg.get("Message", "")}
        except Exception as e:  # noqa
            return {"ok": False, "error": repr(e)}

    async def originate(self, to: str, from_endpoint: str,
                        caller_id: str = "") -> dict:
        """Place a call: ring from_endpoint (a local endpoint / softphone) and bridge to
        the dialed number over the IMS. Uses a Local channel into from-local."""
        if not self.connected:
            return {"ok": False, "error": "AMI not connected"}
        try:
            res = await self._action({
                "Action": "Originate",
                "Channel": f"PJSIP/{from_endpoint}",
                "Exten": to,
                "Context": "from-local",
                "Priority": "1",
                "CallerID": caller_id or self.msisdn or "gateway",
                "Async": "true",
            }, timeout=12.0)
            msg = res[0] if isinstance(res, list) else res
            return {"ok": msg.get("Response") == "Success", "detail": msg.get("Message", "")}
        except Exception as e:  # noqa
            return {"ok": False, "error": repr(e)}

    async def originate_browser_media_canary(self, engine_sid: str,
                                             channel_id: str) -> dict:
        """Start the fixed, non-carrier media-WebSocket Echo path used by the WSS PCM probe.

        Every dialable AMI field is generated here.  The caller can select neither a number nor
        a context, and malformed identities fail before an AMI action is queued.
        """
        if not self.connected:
            return {"ok": False, "error": "AMI not connected"}
        try:
            response = await self._action(
                browser_media_canary_action(engine_sid, channel_id), timeout=4.0)
            message = response[0] if isinstance(response, list) else response
            return {"ok": message.get("Response") == "Success",
                    "detail": message.get("Message", "")}
        except Exception as exc:  # noqa
            return {"ok": False, "error": repr(exc)}

    async def command(self, value: str) -> dict:
        """Run one bounded Asterisk CLI command through the existing authenticated AMI."""
        if not self.connected:
            return {"ok": False, "error": "AMI not connected"}
        try:
            response = await self._action({"Action": "Command", "Command": str(value)},
                                          timeout=4.0)
            messages = response if isinstance(response, list) else [response]
            output = "\n".join(str(item.get("Output") or item.get("content") or "")
                               for item in messages)
            return {"ok": True, "output": output}
        except Exception as exc:  # noqa
            return {"ok": False, "error": repr(exc)}

    async def hangup_all(self) -> dict:
        if not self.connected:
            return {"ok": False, "error": "AMI not connected"}
        try:
            await self._action({"Action": "Command", "Command": "channel request hangup all"})
            return {"ok": True}
        except Exception as e:  # noqa
            return {"ok": False, "error": repr(e)}
