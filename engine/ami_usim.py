#!/usr/bin/env python3
"""
ami_usim.py - Bridge Asterisk (ims_aka) SIP authentication to the physical USIM via PC/SC.

Derived from phcoder/asterisk-docker (jolly). Changes:
  - VERIFY CHV1 (PIN) after selecting ADF.USIM, before AUTHENTICATE (reference didn't).
  - Clean type-annotation bug from the original (undefined Hexstr/Optional names).
  - Emit status/heartbeat JSON to $MDD_RUNDIR/usim_status.json for the manager FSM.

On Asterisk 'AuthRequest' it runs USIM AUTHENTICATE and returns RES/CK/IK (or AUTS on
sync failure). Triggers registration on FullyBooted and confirms dedicated bearers.
"""
import asyncio
import configparser
from contextlib import contextmanager
import fcntl
import json
import os
import re
import sys
import time

from panoramisk import Manager
from smartcard.System import readers
from smartcard.util import toHexString, toBytes
from smartcard.CardConnection import CardConnection
from smartcard.Exceptions import CardConnectionException
from smartcard.scard import (
    SCardBeginTransaction, SCardEndTransaction, SCARD_LEAVE_CARD,
    SCARD_E_NO_SERVICE, SCARD_W_RESET_CARD,
)

_orig_transmit = CardConnection.transmit
def _safe_transmit(self, bytes, protocol=None):
    if protocol is None:
        try:
            protocol = self.getProtocol()
        except Exception:
            pass
    return _orig_transmit(self, bytes, protocol=protocol)
CardConnection.transmit = _safe_transmit

_orig_disconnect = CardConnection.disconnect
def _safe_disconnect(self, disposition=SCARD_LEAVE_CARD):
    return _orig_disconnect(self, disposition=disposition)
CardConnection.disconnect = _safe_disconnect

RUNDIR = os.environ.get("MDD_RUNDIR", "/run/mdd-sim-gateway")
USIM_PIN = os.environ.get("USIM_PIN", "")
ENGINE_RUN_ID = os.environ.get("MDD_ENGINE_RUN_ID", "").strip()
_AUTH_SEQ = 0
USIM_RECOVERY_FENCE_NAME = "usim-auth-recovery.fence"
PCSC_RECOVERY_CAUSES = frozenset({"pcsc_service_unavailable", "pcsc_card_reset"})


# --- Reader binding by physical USB port -----------------------------------------------------
# Resolve a reader by its STABLE physical USB port path (USIM_READER_PORT, e.g. "3-2") so the SIP
# IMS-AKA path addresses the same physical reader as swu_ike/pin_keeper — even when pcscd flips
# two identical (serial-less) readers' enumeration order. Mirrors control/app/usbreader.py.
try:
    from smartcard.scard import (
        SCardEstablishContext as _SCEstablish, SCardReleaseContext as _SCRelease,
        SCardConnect as _SCConnect,
        SCardGetAttrib as _SCGetAttrib, SCardDisconnect as _SCDisconnect,
        SCARD_SCOPE_USER as _SC_SCOPE_USER, SCARD_SHARE_DIRECT as _SC_SHARE_DIRECT,
        SCARD_LEAVE_CARD as _SC_LEAVE, SCARD_PROTOCOL_T0 as _SC_T0,
        SCARD_PROTOCOL_T1 as _SC_T1, SCARD_ATTR_CHANNEL_ID as _SC_CHANNEL_ID,
        SCARD_S_SUCCESS as _SC_OK,
    )
    _SC_PORT_OK = True
except Exception:                        # pragma: no cover
    _SC_PORT_OK = False


def _reader_bus_dev(reader_name):
    if not _SC_PORT_OK:
        return None
    hctx = hcard = None
    try:
        hr, hctx = _SCEstablish(_SC_SCOPE_USER)
        if hr != _SC_OK:
            return None
        hr, hcard, _p = _SCConnect(hctx, reader_name, _SC_SHARE_DIRECT, _SC_T0 | _SC_T1)
        if hr != _SC_OK:
            return None
        hr, val = _SCGetAttrib(hcard, _SC_CHANNEL_ID)
        if hr != _SC_OK or not val or len(val) < 4:
            return None
        v = val[0] | (val[1] << 8) | (val[2] << 16) | (val[3] << 24)
        if (v >> 16) != 0x0020:
            return None
        return (v >> 8) & 0xff, v & 0xff
    except Exception:
        return None
    finally:
        if hcard is not None:
            try:
                _SCDisconnect(hcard, _SC_LEAVE)
            except Exception:
                pass
        if hctx is not None:
            try:
                _SCRelease(hctx)
            except Exception:
                pass


def _usb_port_path(bus, devnum):
    import glob as _glob
    try:
        entries = _glob.glob("/sys/bus/usb/devices/*/")
    except Exception:
        return None
    for d in entries:
        try:
            with open(d + "busnum") as f:
                b = int(f.read())
            with open(d + "devnum") as f:
                n = int(f.read())
        except Exception:
            continue
        if b == bus and n == devnum:
            return os.path.basename(d.rstrip("/"))
    return None


def index_for_port(port):
    if not port:
        return None
    try:
        rlist = readers()
    except Exception:
        return None
    for i, r in enumerate(rlist):
        bd = _reader_bus_dev(str(r))
        if bd and _usb_port_path(bd[0], bd[1]) == port:
            return i
    return None


def _hcard(conn):
    obj = conn
    for _ in range(5):
        if hasattr(obj, "hcard"):
            return obj.hcard
        if hasattr(obj, "component") and obj.component is not None:
            obj = obj.component
            continue
        break
    return None


class _Tx:
    """Best-effort PC/SC transaction: exclusive card access for the auth sequence, so it
    cannot interleave with pin_keeper / swu_ike APDUs on the shared card."""
    def __init__(self, conn):
        self.conn = conn
        self.hcard = None

    def __enter__(self):
        self.hcard = _hcard(self.conn)
        if self.hcard is not None:
            try:
                SCardBeginTransaction(self.hcard)
            except Exception:
                self.hcard = None
        return self.conn

    def __exit__(self, *a):
        if self.hcard is not None:
            try:
                SCardEndTransaction(self.hcard, SCARD_LEAVE_CARD)
            except Exception:
                pass


@contextmanager
def _runtime_state_locked():
    """Serialize run-id publication, USIM status and recovery fence across processes."""
    os.makedirs(RUNDIR, exist_ok=True)
    path = os.path.join(RUNDIR, ".pcscf-rebind.lock")
    with open(path, "a+", encoding="utf-8") as handle:
        os.chmod(path, 0o600)
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)


def _current_run_id_unlocked():
    try:
        with open(os.path.join(RUNDIR, "engine-run-id"), encoding="utf-8") as handle:
            return handle.read(257).strip()
    except OSError:
        return ""


def _valid_recovery_fence(value):
    return (isinstance(value, dict) and set(value) == {
        "version", "engine_run_id", "auth_seq", "cause_class", "created_at"}
        and type(value.get("version")) is int and value["version"] == 1
        and isinstance(value.get("engine_run_id"), str) and bool(value["engine_run_id"])
        and type(value.get("auth_seq")) is int and value["auth_seq"] > 0
        and value.get("cause_class") in PCSC_RECOVERY_CAUSES
        and isinstance(value.get("created_at"), (int, float))
        and not isinstance(value.get("created_at"), bool)
        and value["created_at"] > 0)


def _read_recovery_fence_unlocked():
    try:
        with open(_recovery_fence_path(), encoding="utf-8") as handle:
            value = json.load(handle)
        return value if _valid_recovery_fence(value) else None
    except (OSError, ValueError, TypeError):
        return None


def write_status(**kw):
    """Publish only for the exact live run, atomically ordered with its outage fence."""
    with _runtime_state_locked():
        if not ENGINE_RUN_ID or _current_run_id_unlocked() != ENGINE_RUN_ID:
            return False
        now = time.time()
        state = kw.get("state")
        current_fence = _read_recovery_fence_unlocked()
        if current_fence and current_fence.get("engine_run_id") == ENGINE_RUN_ID:
            if state == "AUTH_OK":
                if (type(kw.get("auth_seq")) is not int
                        or kw["auth_seq"] < current_fence["auth_seq"]):
                    return False
            elif state == "AUTH_UNAVAILABLE":
                # One outage epoch retains the first exact local cause. A later recoverable
                # symptom may update diagnostics, but cannot change the recovery identity.
                kw["cause_class"] = current_fence["cause_class"]
            elif state != "AUTH_UNAVAILABLE":
                # Intermediate/rejected attempts are diagnostics, not proof that the PC/SC
                # outage ended. Preserve the stable recovery epoch until a real AUTH_OK.
                kw["latest_state"] = str(state or "")
                kw["latest_auth_seq"] = kw.get("auth_seq")
                kw.update(state="AUTH_UNAVAILABLE",
                          cause_class=current_fence["cause_class"],
                          auth_seq=current_fence["auth_seq"],
                          latest_ts=int(now))
                state = "AUTH_UNAVAILABLE"
        if (state == "AUTH_UNAVAILABLE"
                and kw.get("cause_class") in PCSC_RECOVERY_CAUSES):
            requested_seq = kw.get("auth_seq")
            if type(requested_seq) is not int or requested_seq <= 0:
                return False
            fence = current_fence
            if fence is None or fence.get("engine_run_id") != ENGINE_RUN_ID:
                fence = {
                    "version": 1, "engine_run_id": ENGINE_RUN_ID,
                    "auth_seq": requested_seq,
                    "cause_class": kw["cause_class"], "created_at": now,
                }
                _atomic_recovery_fence(fence)
            # One PC/SC outage has one stable authorization key and freshness deadline. Later
            # AuthRequests update diagnostics only; they cannot authorize another REGISTER.
            if type(kw.get("latest_auth_seq")) is not int:
                kw["latest_auth_seq"] = requested_seq
            kw["auth_seq"] = fence["auth_seq"]
            kw.setdefault("latest_ts", int(now))
            status_ts = int(fence["created_at"])
        else:
            status_ts = int(now)
        kw["version"] = 2
        kw["engine_run_id"] = ENGINE_RUN_ID
        kw["ts"] = status_ts
        tmp = os.path.join(RUNDIR, f"usim_status.json.tmp.{os.getpid()}.{time.time_ns()}")
        try:
            with open(tmp, "x", encoding="utf-8") as handle:
                json.dump(kw, handle)
                handle.flush()
                os.fsync(handle.fileno())
            os.chmod(tmp, 0o600)
            os.replace(tmp, os.path.join(RUNDIR, "usim_status.json"))
        finally:
            try:
                os.unlink(tmp)
            except FileNotFoundError:
                pass
        # AUTH_OK is durable evidence, not permission for this producer to clear the fence.
        # Control consumes it only after matching the one serializer dispatch receipt, rearms
        # the deferred timer, commits recovered, and then releases the fence.
        return True


def _recovery_fence_path():
    return os.path.join(RUNDIR, USIM_RECOVERY_FENCE_NAME)


def _atomic_recovery_fence(value):
    os.makedirs(RUNDIR, exist_ok=True)
    path = _recovery_fence_path()
    tmp = f"{path}.tmp.{os.getpid()}.{time.time_ns()}"
    try:
        with open(tmp, "x", encoding="utf-8") as handle:
            json.dump(value, handle, sort_keys=True, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(tmp, 0o600)
        os.replace(tmp, path)
        directory = os.open(RUNDIR, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass


def _clear_recovery_fence_unlocked(auth_seq):
    """Release a fence only after this exact live Engine run proves successful AKA.

    The bind-mounted run directory survives Docker's ``unless-stopped`` restart. A new
    supervisor run may therefore retire its predecessor's fence, but only after init-run has
    atomically published this process's exact run id and this process has completed AUTH_OK.
    A delayed old process sees a different current run id and cannot clear the new owner's fence.
    """
    if type(auth_seq) is not int or auth_seq <= 0 or not ENGINE_RUN_ID:
        return False
    path = _recovery_fence_path()
    value = _read_recovery_fence_unlocked()
    if (_current_run_id_unlocked() != ENGINE_RUN_ID or value is None
            or (value["engine_run_id"] == ENGINE_RUN_ID
                and value["auth_seq"] > auth_seq)):
        return False
    try:
        os.unlink(path)
    except FileNotFoundError:
        return False
    directory = os.open(RUNDIR, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(directory)
    finally:
        os.close(directory)
    return True


def _clear_recovery_fence(auth_seq):
    with _runtime_state_locked():
        return _clear_recovery_fence_unlocked(auth_seq)


def _pcsc_unavailable_cause(exc):
    """Return an exact local PC/SC outage that may authorize bounded re-registration.

    A carrier 401, an absent card, a PIN failure and an arbitrary pyscard exception must never
    enter this path.  Some pyscard versions preserve SCARD_E_NO_SERVICE in ``hresult``; the
    pinned version used by the Engine only preserves pcsc-lite's exact transmit message.
    """
    if not isinstance(exc, CardConnectionException):
        return ""
    if getattr(exc, "hresult", None) == SCARD_E_NO_SERVICE:
        return "pcsc_service_unavailable"
    hresult = getattr(exc, "hresult", None)
    if (type(hresult) is int
            and (hresult & 0xFFFFFFFF) == (SCARD_W_RESET_CARD & 0xFFFFFFFF)):
        return "pcsc_card_reset"
    message = " ".join(str(exc).split())
    if re.fullmatch(
            r"Failed to transmit with protocol T[01]\. Service not available\.", message):
        return "pcsc_service_unavailable"
    if message == "Service not available.":
        return "pcsc_service_unavailable"
    if message in {
            "Failed to transmit with protocol T0. Card was reset.",
            "Failed to transmit with protocol T1. Card was reset.",
    }:
        return "pcsc_card_reset"
    return ""


def swap_nibbles(s):
    return "".join([x + y for x, y in zip(s[1::2], s[0::2])])


def dec_imsi(ef):
    if len(ef) < 4:
        return None
    l = int(ef[0:2], 16) * 2 - 1
    swapped = swap_nibbles(ef[2:]).rstrip("f")
    if len(swapped) < 1:
        return None
    return swapped[1:]


# 3GPP USIM AID prefix. EF_DIR record 1 is NOT always the USIM (China Telecom cards
# list CSIM first), so scan the records and pick the USIM by AID.
USIM_AID_PREFIX = "A0000000871002"


def _usim_aid_from_dir(connection):
    """Scan EF_DIR records for the USIM AID; prefer 3GPP USIM, fall back to the first
    application. EF.DIR must be selectable from the current DF. Returns (len, hex) or None."""
    data, sw1, sw2 = connection.transmit(toBytes("00a40004022f0000"))  # SELECT EF.DIR
    if sw1 != 0x61:
        return None
    fcp, sw1, sw2 = connection.transmit(toBytes("00C00000") + [sw2])
    if sw1 != 0x90 or len(fcp) < 8:
        return None
    record_length = fcp[7]
    first = None
    for rec in range(1, 11):
        data, sw1, sw2 = connection.transmit(toBytes("00b2") + [rec, 0x04, record_length])
        if sw1 != 0x90 or len(data) < 5 or data[0] != 0x61 or data[2] != 0x4F:
            break
        aid_length = data[3]
        aid = "".join("%02X" % b for b in data[4:4 + aid_length])
        if len(aid) < aid_length * 2:
            break
        if aid.startswith(USIM_AID_PREFIX):
            return aid_length, aid
        if first is None:
            first = (aid_length, aid)
    return first


def make_connection_index(reader_index):
    r = readers()
    if reader_index >= len(r):
        return None
    connection = r[reader_index].createConnection()
    connection.connect()
    connection.transmit(toBytes("00a40004023f0000"))
    got = _usim_aid_from_dir(connection)
    if got is None:
        print("Failed to find USIM AID in EF.DIR")
        return None
    aid_length, aid = got
    print(f"Using aid={aid}")
    data, sw1, sw2 = connection.transmit(toBytes("00a40404") + [aid_length] + toBytes(aid))
    if sw1 not in (0x90, 0x61):
        print("Failed to select AID")
        return None
    return connection


def make_connection_name(reader_name):
    if isinstance(reader_name, str) and reader_name.startswith("imsi:"):
        target_imsi = reader_name[5:]
        for idx in range(len(readers())):
            connection = make_connection_index(idx)
            if connection is None:
                continue
            data, sw1, sw2 = connection.transmit(toBytes("00a40004026f0700"))
            if sw1 != 0x61:
                continue
            data, sw1, sw2 = connection.transmit(toBytes("00b0000009"))
            if (sw1, sw2) != (0x90, 0x00):
                continue
            imsi = dec_imsi(bytes(data).hex())
            if imsi == target_imsi:
                print(f"Found target SIM on reader {idx}")
                # re-select ADF.USIM after reading IMSI (IMSI read left EF selected)
                make_reselect_adf(connection)
                return connection
        print("Target SIM not found")
        return None
    return make_connection_index(int(reader_name))


def make_reselect_adf(connection):
    connection.transmit(toBytes("00a40004023f0000"))
    got = _usim_aid_from_dir(connection)
    if got is None:
        return
    aid_length, aid = got
    connection.transmit(toBytes("00a40404") + [aid_length] + toBytes(aid))


_USIM_CONN = None
_USIM_AID = None


def select_adf_usim(connection):
    """SELECT MF -> EF.DIR -> USIM AID -> ADF.USIM. Returns True on success."""
    global _USIM_AID
    if _USIM_AID is None:
        connection.transmit(toBytes("00a40004023f0000"))
        _USIM_AID = _usim_aid_from_dir(connection)
    if _USIM_AID is None:
        return False
    aid_length, aid = _USIM_AID
    data, sw1, sw2 = connection.transmit(toBytes("00a40404") + [aid_length] + toBytes(aid))
    return sw1 in (0x90, 0x61)


def get_usim_connection(reader_spec):
    global _USIM_CONN
    if _USIM_CONN is not None:
        return _USIM_CONN
    _USIM_CONN = open_usim(reader_spec)
    return _USIM_CONN


def open_usim(reader_spec):
    """Return an open connection positioned at ADF.USIM."""
    rlist = readers()
    if not rlist:
        return None
    port = os.environ.get("USIM_READER_PORT", "").strip()
    if port and len(rlist) > 1:
        pidx = index_for_port(port)
        if pidx is not None and pidx < len(rlist):
            try:
                conn = rlist[pidx].createConnection()
                conn.connect()
                return conn
            except Exception:
                pass
    if isinstance(reader_spec, str) and reader_spec.startswith("imsi:") and len(rlist) > 1:
        target = reader_spec[5:]
        for r in rlist:
            try:
                conn = r.createConnection()
                conn.connect()
            except Exception:
                continue
            with _Tx(conn):
                if select_adf_usim(conn) and verify_pin(conn):
                    conn.transmit(toBytes("00a40004026f0700"))
                    d, s1, s2 = conn.transmit(toBytes("00b0000009"))
                    if s1 == 0x90 and dec_imsi(bytes(d).hex()) == target:
                        return conn
            try:
                conn.disconnect()
            except Exception:
                pass
        return None
    # single reader, or explicit index
    idx = 0
    if isinstance(reader_spec, str) and reader_spec.isdigit():
        idx = int(reader_spec)
    if idx >= len(rlist):
        idx = 0
    conn = rlist[idx].createConnection()
    conn.connect()
    return conn


def verify_pin(connection):
    """Verify CHV1 if a PIN is configured. Idempotent: skips if already verified (9000)."""
    if not USIM_PIN or USIM_PIN.lower() in ("none", "disabled", ""):
        return True
    d, s1, s2 = connection.transmit(toBytes("0020000100"))
    if (s1, s2) == (0x90, 0x00):
        return True  # already verified in this card session
    if s1 == 0x63 and (s2 & 0x0F) < 2:
        print(f"Refusing PIN verify: only {s2 & 0x0F} tries left", flush=True)
        return False
    body = [ord(c) for c in USIM_PIN] + [0xFF] * (8 - len(USIM_PIN))
    d, s1, s2 = connection.transmit(toBytes("00200001") + [0x08] + body)
    if (s1, s2) == (0x90, 0x00):
        return True
    print(f"PIN verify failed sw={s1:02x}{s2:02x}", flush=True)
    return False


def read_res_ck_ik(reader_spec, rand, autn, auth_seq=0):
    global _USIM_CONN
    res = ck = ik = auts = None
    try:
        conn = get_usim_connection(reader_spec)
        if conn is None:
            write_status(state="NO_CARD", auth_seq=auth_seq)
            return res, ck, ik, auts
        with _Tx(conn):
            if not select_adf_usim(conn):
                write_status(state="NO_CARD", auth_seq=auth_seq,
                             detail="ADF.USIM select failed")
                return res, ck, ik, auts
            if not verify_pin(conn):
                write_status(state="PIN_FAIL", auth_seq=auth_seq)
                return res, ck, ik, auts
            data, sw1, sw2 = conn.transmit(
                toBytes("008800812210" + rand.upper() + "10" + autn.upper()))
            if sw1 == 0x61:
                data, sw1, sw2 = conn.transmit(toBytes("00C00000") + [sw2])
                result = toHexString(data).replace(" ", "")
                rc = result[0:2]
                if rc == "DB":  # success
                    res_length = data[1]
                    res = result[4:(4 + res_length * 2)]
                    ck_length = data[2 + res_length]
                    ck = result[(6 + res_length * 2):(6 + res_length * 2 + ck_length * 2)]
                    ik_length = data[2 + res_length + 1 + ck_length]
                    ik = result[(8 + res_length * 2 + ck_length * 2):
                                (8 + res_length * 2 + ck_length * 2 + ik_length * 2)]
                    write_status(state="AUTH_OK", auth_seq=auth_seq)
                elif rc == "DC":  # sync failure -> AUTS
                    auts = result[4:32]
                    write_status(state="AUTH_SYNC", auth_seq=auth_seq)
            else:
                print(f"Authentication failed sw={sw1:02x}{sw2:02x}", flush=True)
                write_status(state="AUTH_FAIL", auth_seq=auth_seq,
                             detail=f"sw={sw1:02x}{sw2:02x}")
    except Exception as e:
        print(f"Exception in read_res_ck_ik: {e!r}", flush=True)
        cause = _pcsc_unavailable_cause(e)
        old_connection = _USIM_CONN
        _USIM_CONN = None
        if old_connection is not None:
            try:
                old_connection.disconnect()
            except Exception:
                pass
        write_status(
            state="AUTH_UNAVAILABLE" if cause else "AUTH_ERROR",
            auth_seq=auth_seq,
            cause_class=cause or "unexpected_local_auth_error",
        )
    return res, ck, ik, auts


def main():
    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)
    if len(sys.argv) != 2:
        print(f"Usage: python {sys.argv[0]} <ini-file>")
        sys.exit(1)
    config = configparser.ConfigParser()
    config.read(sys.argv[1])
    cfg_endpoint = config.sections()[0]
    cfg_reader = config.get(cfg_endpoint, "reader")
    cfg_host = config.get(cfg_endpoint, "host")
    cfg_username = config.get(cfg_endpoint, "username")
    cfg_secret = config.get(cfg_endpoint, "secret")
    print(f"Endpoint={cfg_endpoint} reader={cfg_reader} host={cfg_host} user={cfg_username}")
    write_status(state="STARTING")

    try:
        conn = get_usim_connection(cfg_reader)
        if conn is not None:
            with _Tx(conn):
                select_adf_usim(conn)
                verify_pin(conn)
            print(f"USIM connection pre-warmed on reader={cfg_reader}, aid={_USIM_AID}")
    except Exception as e:
        print(f"Pre-warm notice: {e}")

    manager = Manager(loop=asyncio.get_event_loop(), host=cfg_host,
                      username=cfg_username, secret=cfg_secret)

    @manager.register_event("FullyBooted")
    def on_booted(manager, message):
        recovery_files = (
            USIM_RECOVERY_FENCE_NAME, "usim-auth-recovery.json",
            "usim-registration-permit.json", "usim-registration-dispatch.json")
        if any(os.path.lexists(os.path.join(RUNDIR, name)) for name in recovery_files):
            print("Asterisk ready; automatic registration remains fenced by USIM recovery")
            return
        print("Asterisk ready, triggering registration...")
        manager.send_action({"Action": "PJSIPRegister", "Registration": cfg_endpoint})

    @manager.register_event("AuthRequest")
    def on_auth(manager, message):
        global _AUTH_SEQ
        _AUTH_SEQ += 1
        auth_seq = _AUTH_SEQ
        algo = message.Algorithm
        rand = message.RAND
        autn = message.AUTN
        print(f"AuthRequest: Algorithm={algo}")
        write_status(state="AUTH_IN_PROGRESS", auth_seq=auth_seq)
        res, ck, ik, auts = read_res_ck_ik(cfg_reader, rand, autn, auth_seq=auth_seq)
        if res is not None:
            manager.send_action({"Action": "AuthResponse", "Registration": cfg_endpoint,
                                 "RES": res, "CK": ck, "IK": ik})
        elif auts is not None:
            manager.send_action({"Action": "AuthResponse", "Registration": cfg_endpoint,
                                 "AUTS": auts})
        else:
            manager.send_action({"Action": "AuthResponse", "Registration": cfg_endpoint})
        print("AuthResponse sent")

    @manager.register_event("Newchannel")
    def on_newchannel(manager, message):
        context = message.Context
        channel = message.Channel
        time.sleep(0.5)
        if context == cfg_endpoint:
            manager.send_action({"Action": "DedicatedBearerStatus", "Channel": channel,
                                 "Status": "Up"})
            print(f"DedicatedBearerStatus sent: Channel={channel}")

    try:
        manager.connect(run_forever=True)
    except KeyboardInterrupt:
        manager.loop.close()


if __name__ == "__main__":
    main()
