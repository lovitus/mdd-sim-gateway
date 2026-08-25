"""
store.py - SQLite persistence for SMS threads/messages and the call log.

One DB per manager at $MDD_DATA/mdd-sim-gateway.sqlite. Messages carry the instance id so a
multi-SIM setup keeps separate conversations. New rows are broadcast to the WebSocket
layer by the caller (main.py).
"""
from __future__ import annotations

import os
import json
import re
import shutil
import sqlite3
import threading
import time

DATA_DIR = os.environ.get("MDD_DATA", os.path.join(os.getcwd(), "data"))
DB_PATH = os.path.join(DATA_DIR, "mdd-sim-gateway.sqlite")
PREVIOUS_DB_PATH = os.path.join(DATA_DIR, "vowifi.sqlite")
_lock = threading.Lock()

# Connectivity timeline. Line state is sampled every few seconds, so it is stored as merged
# segments instead of one row per sample: two days of history stays a handful of rows.
LINE_STATES = ("up", "down", "off")
# The longest silence still treated as one continuous observation. Anything longer is a hole
# in the record (control plane restarted / host powered off) and must stay visible as one
# instead of being interpolated into a healthy stretch.
LINE_STATE_CONTINUITY_SECONDS = 90
LINE_STATE_RETENTION_SECONDS = 3 * 24 * 3600
# A create reply normally arrives within 30 seconds and the scanner runs every five seconds.
# Keep a wider recovery window for a service restart, but do not let an old timed-out draft hide
# an unrelated, manually-created SMS with the same recipient and body for an entire day.
LOCAL_MODEM_SMS_CLAIM_SECONDS = 5 * 60
LOCAL_MODEM_SMS_RETENTION_SECONDS = 24 * 3600

BROWSER_CALL_STATES = frozenset({
    "ringing", "claiming", "attach_submitted_unknown",
    "answer_submitted_unknown", "active", "ending", "unknown", "terminal",
})
BROWSER_CALL_PAID_STATES = frozenset({
    "claiming", "attach_submitted_unknown", "answer_submitted_unknown",
    "active", "ending", "unknown",
})
BROWSER_CALL_TRANSITIONS = {
    "ringing": frozenset({"claiming", "ending"}),
    "claiming": frozenset({"attach_submitted_unknown", "ending"}),
    "attach_submitted_unknown": frozenset({"answer_submitted_unknown", "ending"}),
    "answer_submitted_unknown": frozenset({"active", "ending"}),
    "active": frozenset({"ending"}),
    "unknown": frozenset({"ending"}),
    "ending": frozenset(),
    "terminal": frozenset(),
}


class BrowserCallConflict(RuntimeError):
    pass


_KEEP = object()


def _conn():
    os.makedirs(DATA_DIR, exist_ok=True)
    c = sqlite3.connect(DB_PATH)
    c.row_factory = sqlite3.Row
    return c


def init():
    with _lock:
        # Preserve call/SMS history when upgrading an installation that used the former
        # database filename. Copy once and keep the source as a rollback artifact.
        if not os.path.exists(DB_PATH) and os.path.isfile(PREVIOUS_DB_PATH):
            os.makedirs(DATA_DIR, exist_ok=True)
            shutil.copy2(PREVIOUS_DB_PATH, DB_PATH)
        with _conn() as c:
            c.executescript(
                """
                CREATE TABLE IF NOT EXISTS messages (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    instance TEXT NOT NULL,
                    direction TEXT NOT NULL,        -- 'in' | 'out'
                    peer TEXT NOT NULL,             -- phone number / address
                    body TEXT NOT NULL,
                    status TEXT DEFAULT 'ok',       -- ok|pending|sent|delivered|unknown|failed
                    ts INTEGER NOT NULL
                );
                CREATE INDEX IF NOT EXISTS idx_msg_inst_peer ON messages(instance, peer, ts);
                CREATE TABLE IF NOT EXISTS sms_submission_guards (
                    instance TEXT PRIMARY KEY,
                    operation_id TEXT NOT NULL UNIQUE,
                    payload_hash TEXT NOT NULL,
                    transport TEXT NOT NULL,
                    state TEXT NOT NULL,
                    message_id INTEGER,
                    result_json TEXT,
                    started_ts INTEGER NOT NULL,
                    updated_ts INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS sms_submission_receipts (
                    operation_id TEXT PRIMARY KEY,
                    instance TEXT NOT NULL,
                    payload_hash TEXT NOT NULL,
                    transport TEXT NOT NULL,
                    message_id INTEGER,
                    result_json TEXT,
                    acknowledged_ts INTEGER NOT NULL
                );
                CREATE INDEX IF NOT EXISTS idx_sms_submission_receipt_instance
                    ON sms_submission_receipts(instance, acknowledged_ts);
                CREATE TABLE IF NOT EXISTS message_imports (
                    fingerprint TEXT PRIMARY KEY,
                    instance TEXT NOT NULL,
                    imported_ts INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS local_modem_sms (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    instance TEXT NOT NULL,
                    iccid TEXT NOT NULL,
                    daemon_epoch TEXT NOT NULL DEFAULT '',
                    message_id INTEGER,
                    modem_path TEXT,
                    sms_path TEXT,
                    content_hash TEXT NOT NULL,
                    created_ts INTEGER NOT NULL,
                    bound_ts INTEGER,
                    cancelled INTEGER NOT NULL DEFAULT 0
                );
                CREATE TABLE IF NOT EXISTS calls (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    instance TEXT NOT NULL,
                    direction TEXT NOT NULL,        -- 'in' | 'out'
                    peer TEXT NOT NULL,
                    status TEXT DEFAULT '',         -- ringing|answered|ended|missed|failed
                    start_ts INTEGER NOT NULL,
                    end_ts INTEGER,
                    source_call_id TEXT NOT NULL DEFAULT '',
                    engine_run_id TEXT NOT NULL DEFAULT '',
                    browser_state TEXT,
                    browser_revision INTEGER,
                    browser_owner_session TEXT NOT NULL DEFAULT '',
                    browser_operation TEXT NOT NULL DEFAULT '',
                    browser_epoch TEXT NOT NULL DEFAULT ''
                );
                CREATE TABLE IF NOT EXISTS instance_call_fences (
                    instance TEXT PRIMARY KEY,
                    created_ts INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS cellular_call_leases (
                    call_id TEXT PRIMARY KEY,
                    instance TEXT NOT NULL,
                    iccid TEXT NOT NULL,
                    direction TEXT NOT NULL,
                    state TEXT NOT NULL,
                    started_ts INTEGER NOT NULL,
                    updated_ts INTEGER NOT NULL,
                    terminal_ts INTEGER
                );
                CREATE INDEX IF NOT EXISTS idx_cellular_call_leases_open
                    ON cellular_call_leases(iccid,state,updated_ts);
                CREATE TABLE IF NOT EXISTS legacy_history_imports (
                    kind TEXT NOT NULL,
                    source_id INTEGER NOT NULL,
                    imported_ts INTEGER NOT NULL,
                    PRIMARY KEY(kind, source_id)
                );
                CREATE TABLE IF NOT EXISTS line_states (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    instance TEXT NOT NULL,
                    state TEXT NOT NULL,        -- 'up' | 'down' | 'off'
                    start_ts INTEGER NOT NULL,
                    end_ts INTEGER NOT NULL,
                    reason TEXT NOT NULL DEFAULT '',  -- reason_code when the segment began
                    detail TEXT NOT NULL DEFAULT ''   -- evidence behind it (names, resolvers…)
                );
                CREATE INDEX IF NOT EXISTS idx_line_states ON line_states(instance, start_ts);
                CREATE TABLE IF NOT EXISTS line_allowances (
                    instance TEXT PRIMARY KEY,
                    balance TEXT NOT NULL DEFAULT '',
                    valid_until TEXT NOT NULL DEFAULT '',
                    sms_remaining TEXT NOT NULL DEFAULT '',
                    data_remaining TEXT NOT NULL DEFAULT '',
                    voice_remaining TEXT NOT NULL DEFAULT '',
                    activated_at TEXT NOT NULL DEFAULT '',
                    updated_ts INTEGER,
                    source TEXT NOT NULL DEFAULT 'manual'
                );
                CREATE TABLE IF NOT EXISTS allowance_query_rules (
                    instance TEXT PRIMARY KEY,
                    recipient TEXT NOT NULL,
                    body TEXT NOT NULL,
                    updated_ts INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS allowance_queries (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    instance TEXT NOT NULL,
                    recipient TEXT NOT NULL,
                    body TEXT NOT NULL,
                    carrier_key TEXT NOT NULL DEFAULT '',
                    started_ts INTEGER NOT NULL,
                    transport TEXT NOT NULL DEFAULT 'auto',
                    status TEXT NOT NULL DEFAULT 'pending'
                );
                CREATE INDEX IF NOT EXISTS idx_allowance_queries_inst
                    ON allowance_queries(instance, started_ts);
                CREATE TABLE IF NOT EXISTS allowance_reminders (
                    instance TEXT NOT NULL,
                    expiry_date TEXT NOT NULL,
                    days_before INTEGER NOT NULL,
                    sent_ts INTEGER NOT NULL,
                    PRIMARY KEY(instance, expiry_date, days_before)
                );
                CREATE TABLE IF NOT EXISTS soft_deleted_instances (
                    instance_id TEXT PRIMARY KEY,
                    iccid TEXT DEFAULT '',
                    imsi TEXT DEFAULT '',
                    name TEXT DEFAULT '',
                    deleted_ts INTEGER NOT NULL
                );
                CREATE TABLE IF NOT EXISTS esim_notifications (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    seq_number INTEGER NOT NULL,
                    operation TEXT NOT NULL,
                    iccid TEXT NOT NULL,
                    reader_name TEXT NOT NULL DEFAULT '',
                    aid TEXT NOT NULL DEFAULT '',
                    status TEXT NOT NULL DEFAULT 'pending',
                    created_ts INTEGER NOT NULL,
                    last_replayed_ts INTEGER,
                    replay_count INTEGER NOT NULL DEFAULT 0,
                    last_error TEXT NOT NULL DEFAULT ''
                );
                CREATE UNIQUE INDEX IF NOT EXISTS idx_esim_notif_uniq ON esim_notifications(seq_number, iccid);
                """
            )

            # executescript() finishes in autocommit mode. Every following schema/data migration
            # must share one explicit transaction so a mid-ALTER failure cannot leave a partial
            # browser-call schema that later maintenance mistakes for a safe database.
            c.execute("BEGIN IMMEDIATE")
            # migration: per-message failure detail (added later)
            try:
                c.execute("ALTER TABLE messages ADD COLUMN error TEXT")
            except Exception:
                pass
            try:
                c.execute("ALTER TABLE messages ADD COLUMN transport TEXT DEFAULT 'vowifi'")
            except Exception:
                pass
            try:
                c.execute("ALTER TABLE calls ADD COLUMN transport TEXT DEFAULT 'vowifi'")
            except Exception:
                pass
            try:
                c.execute("ALTER TABLE calls ADD COLUMN source_call_id TEXT NOT NULL DEFAULT ''")
            except Exception:
                pass
            try:
                c.execute("ALTER TABLE calls ADD COLUMN engine_run_id TEXT NOT NULL DEFAULT ''")
            except Exception:
                pass
            browser_schema = {
                "browser_state": "TEXT",
                "browser_revision": "INTEGER",
                "browser_owner_session": "TEXT NOT NULL DEFAULT ''",
                "browser_operation": "TEXT NOT NULL DEFAULT ''",
                "browser_epoch": "TEXT NOT NULL DEFAULT ''",
            }
            call_columns = {str(row[1]) for row in c.execute(
                "PRAGMA table_info(calls)").fetchall()}
            for name, declaration in browser_schema.items():
                if name not in call_columns:
                    c.execute(f"ALTER TABLE calls ADD COLUMN {name} {declaration}")
            call_columns = {str(row[1]) for row in c.execute(
                "PRAGMA table_info(calls)").fetchall()}
            if not set(browser_schema).issubset(call_columns):
                raise sqlite3.OperationalError("browser call schema migration is incomplete")
            if c.execute(
                    "SELECT 1 FROM sqlite_master WHERE type='table' "
                    "AND name='instance_call_fences'").fetchone() is None:
                raise sqlite3.OperationalError("instance call fence schema is missing")
            # An open pre-migration inbound call is occupied/unknown, never a fresh ringing call.
            c.execute(
                "UPDATE calls SET browser_state='unknown',browser_revision=1 "
                "WHERE direction='in' AND transport='vowifi' AND end_ts IS NULL "
                "AND browser_state IS NULL")
            c.execute(
                "CREATE INDEX IF NOT EXISTS idx_calls_browser_state "
                "ON calls(instance,browser_state,end_ts)")
            c.execute("DROP INDEX IF EXISTS idx_calls_source_call")
            c.execute(
                "CREATE UNIQUE INDEX IF NOT EXISTS idx_calls_source_call_generation "
                "ON calls(instance,direction,transport,source_call_id,engine_run_id) "
                "WHERE source_call_id <> '' AND engine_run_id <> ''")
            try:
                c.execute("ALTER TABLE line_allowances "
                          "ADD COLUMN activated_at TEXT NOT NULL DEFAULT ''")
            except Exception:
                pass
            try:
                c.execute("ALTER TABLE local_modem_sms "
                          "ADD COLUMN daemon_epoch TEXT NOT NULL DEFAULT ''")
            except Exception:
                pass
            try:
                c.execute("ALTER TABLE local_modem_sms ADD COLUMN message_id INTEGER")
            except Exception:
                pass
            try:
                c.execute("ALTER TABLE local_modem_sms "
                          "ADD COLUMN cancelled INTEGER NOT NULL DEFAULT 0")
            except Exception:
                pass
            # The daemon generation is part of an SMS object's identity: numeric paths restart
            # at zero whenever ModemManager itself restarts.
            c.execute("DROP INDEX IF EXISTS idx_local_modem_sms_path")
            c.execute("DROP INDEX IF EXISTS idx_local_modem_sms_pending")
            c.execute(
                "DELETE FROM local_modem_sms WHERE sms_path IS NOT NULL AND id NOT IN ("
                "SELECT MAX(id) FROM local_modem_sms WHERE sms_path IS NOT NULL "
                "GROUP BY daemon_epoch,iccid,sms_path)")
            c.execute(
                "CREATE UNIQUE INDEX IF NOT EXISTS idx_local_modem_sms_path "
                "ON local_modem_sms(daemon_epoch,iccid,sms_path) "
                "WHERE sms_path IS NOT NULL")
            c.execute(
                "CREATE INDEX IF NOT EXISTS idx_local_modem_sms_pending "
                "ON local_modem_sms(daemon_epoch,iccid,content_hash,created_ts) "
                "WHERE sms_path IS NULL AND cancelled=0")
            duplicate_message = c.execute(
                "SELECT message_id FROM local_modem_sms WHERE message_id IS NOT NULL "
                "GROUP BY message_id HAVING COUNT(*)>1 LIMIT 1").fetchone()
            if duplicate_message:
                raise RuntimeError(
                    "ambiguous local SMS reservations reference the same message")
            c.execute(
                "CREATE UNIQUE INDEX IF NOT EXISTS idx_local_modem_sms_message "
                "ON local_modem_sms(message_id) WHERE message_id IS NOT NULL")
            # A process exit after ModemManager accepted Create/Send leaves a pending row. On
            # startup its delivery outcome is unknowable, so preserve it and discourage retry.
            c.execute(
                "UPDATE messages SET status='unknown', "
                "error='Cellular SMS submission was interrupted; delivery is unknown.' "
                "WHERE transport='cellular' AND status='pending'")
            c.execute(
                "UPDATE sms_submission_guards SET state='orphaned',updated_ts=? "
                "WHERE state='active'", (int(time.time()),))
            # migration: why a down segment began (added later)
            try:
                c.execute("ALTER TABLE line_states ADD COLUMN reason TEXT NOT NULL DEFAULT ''")
            except Exception:
                pass
            try:
                c.execute("ALTER TABLE line_states ADD COLUMN detail TEXT NOT NULL DEFAULT ''")
            except Exception:
                pass


def migrate_legacy_history(instance_aliases: dict[str, str]) -> dict:
    """Merge legacy history into current line ids once, without exposing its contents."""
    if not instance_aliases or not os.path.isfile(PREVIOUS_DB_PATH):
        return {"calls": 0, "messages": 0}
    imported = {"calls": 0, "messages": 0}
    with _lock, sqlite3.connect(PREVIOUS_DB_PATH) as source, _conn() as dest:
        source.row_factory = sqlite3.Row
        for kind, table in (("call", "calls"), ("message", "messages")):
            try:
                rows = source.execute(f"SELECT * FROM {table}").fetchall()
            except sqlite3.Error:
                continue
            for row in rows:
                old = dict(row)
                target = instance_aliases.get(str(old.get("instance") or "").lower())
                if not target:
                    continue
                source_id = int(old["id"])
                marker = dest.execute(
                    "INSERT OR IGNORE INTO legacy_history_imports(kind,source_id,imported_ts) "
                    "VALUES(?,?,?)", (kind, source_id, int(time.time())))
                if marker.rowcount == 0:
                    continue
                if kind == "call":
                    duplicate = dest.execute(
                        "SELECT 1 FROM calls WHERE instance=? AND direction=? AND peer=? "
                        "AND start_ts=? LIMIT 1",
                        (target, old.get("direction", ""), old.get("peer", ""),
                         int(old.get("start_ts") or 0))).fetchone()
                    if not duplicate:
                        dest.execute(
                            "INSERT INTO calls(instance,direction,peer,status,start_ts,end_ts) "
                            "VALUES(?,?,?,?,?,?)",
                            (target, old.get("direction", ""), old.get("peer", ""),
                             old.get("status", ""), int(old.get("start_ts") or 0),
                             old.get("end_ts")))
                        imported["calls"] += 1
                else:
                    duplicate = dest.execute(
                        "SELECT 1 FROM messages WHERE instance=? AND direction=? AND peer=? "
                        "AND body=? AND ts=? LIMIT 1",
                        (target, old.get("direction", ""), old.get("peer", ""),
                         old.get("body", ""), int(old.get("ts") or 0))).fetchone()
                    if not duplicate:
                        dest.execute(
                            "INSERT INTO messages(instance,direction,peer,body,status,ts,error,transport) "
                            "VALUES(?,?,?,?,?,?,?,?)",
                            (target, old.get("direction", ""), old.get("peer", ""),
                             old.get("body", ""), old.get("status", "ok"),
                             int(old.get("ts") or 0), old.get("error"),
                             old.get("transport") or "vowifi"))
                        imported["messages"] += 1
    return imported


def set_message_status(mid: int, status: str, error: str | None = None):
    with _lock, _conn() as c:
        c.execute("UPDATE messages SET status=?, error=? WHERE id=?", (status, error, mid))


def add_message(instance: str, direction: str, peer: str, body: str, status: str = "ok",
                transport: str = "vowifi", ts: int | None = None) -> dict:
    ts = int(ts or time.time())
    with _lock, _conn() as c:
        cur = c.execute(
            "INSERT INTO messages(instance,direction,peer,body,status,ts,transport) VALUES(?,?,?,?,?,?,?)",
            (str(instance), direction, peer, body, status, ts, transport),
        )
        mid = cur.lastrowid
    return {"id": mid, "instance": str(instance), "direction": direction,
            "peer": peer, "body": body, "status": status, "error": None, "ts": ts,
            "transport": transport}


def begin_sms_submission(instance: str, operation_id: str, payload_hash: str,
                         transport: str) -> dict:
    """Reserve one per-line SMS operation or return its exact durable prior outcome."""
    now = int(time.time())
    with _lock, _conn() as c:
        receipt_row = c.execute(
            "SELECT operation_id,instance,payload_hash,transport,message_id,result_json,"
            "acknowledged_ts FROM sms_submission_receipts WHERE operation_id=?",
            (str(operation_id),),
        ).fetchone()
        if receipt_row:
            receipt = dict(receipt_row)
            exact = (receipt["instance"] == str(instance)
                     and receipt["payload_hash"] == str(payload_hash)
                     and receipt["transport"] == str(transport))
            result = None
            if exact and receipt.get("result_json"):
                try:
                    result = json.loads(receipt["result_json"])
                except (TypeError, ValueError):
                    result = None
            return {"created": False, "conflict": not exact,
                    "acknowledged": exact, "state": "acknowledged", **receipt,
                    "result": result if isinstance(result, dict) else None}
        row = c.execute(
            "SELECT * FROM sms_submission_guards WHERE instance=?",
            (str(instance),),
        ).fetchone()
        if row:
            current = dict(row)
            if (current["operation_id"] == str(operation_id)
                    and current["payload_hash"] == str(payload_hash)
                    and current["transport"] == str(transport)):
                result = None
                if current.get("result_json"):
                    try:
                        result = json.loads(current["result_json"])
                    except (TypeError, ValueError):
                        result = None
                return {"created": False, "conflict": False, **current,
                        "result": result if isinstance(result, dict) else None}
            return {"created": False, "conflict": True, **current, "result": None}
        c.execute(
            "INSERT INTO sms_submission_guards"
            "(instance,operation_id,payload_hash,transport,state,started_ts,updated_ts) "
            "VALUES(?,?,?,?,?,?,?)",
            (str(instance), str(operation_id), str(payload_hash), str(transport),
             "active", now, now),
        )
        return {"created": True, "conflict": False, "instance": str(instance),
                "operation_id": str(operation_id), "payload_hash": str(payload_hash),
                "transport": str(transport), "state": "active", "message_id": None,
                "result": None, "started_ts": now, "updated_ts": now}


def finish_sms_submission(instance: str, operation_id: str, state: str,
                          result: dict | None = None, message_id: int | None = None) -> bool:
    if state not in {"completed", "orphaned"}:
        raise ValueError("invalid SMS submission terminal state")
    payload = (json.dumps(result, sort_keys=True, separators=(",", ":"))
               if isinstance(result, dict) else None)
    if payload is not None and len(payload.encode("utf-8")) > 65536:
        raise ValueError("SMS submission result is too large")
    with _lock, _conn() as c:
        cur = c.execute(
            "UPDATE sms_submission_guards SET state=?,message_id=?,result_json=?,updated_ts=? "
            "WHERE instance=? AND operation_id=? AND state IN ('active','orphaned')",
            (state, int(message_id) if message_id is not None else None, payload,
             int(time.time()), str(instance), str(operation_id)),
        )
        return cur.rowcount == 1


def acknowledge_sms_submission(instance: str, operation_id: str) -> bool:
    """Release the per-line gate while retaining a permanent idempotency receipt."""
    with _lock, _conn() as c:
        receipt = c.execute(
            "SELECT instance FROM sms_submission_receipts WHERE operation_id=?",
            (str(operation_id),),
        ).fetchone()
        if receipt:
            return receipt["instance"] == str(instance)
        row = c.execute(
            "SELECT instance,operation_id,payload_hash,transport,state,message_id,result_json "
            "FROM sms_submission_guards WHERE instance=? AND operation_id=?",
            (str(instance), str(operation_id)),
        ).fetchone()
        if not row or row["state"] not in {"completed", "orphaned"}:
            return False
        c.execute(
            "INSERT INTO sms_submission_receipts"
            "(operation_id,instance,payload_hash,transport,message_id,result_json,acknowledged_ts) "
            "VALUES(?,?,?,?,?,?,?)",
            (row["operation_id"], row["instance"], row["payload_hash"], row["transport"],
             row["message_id"], row["result_json"], int(time.time())),
        )
        cur = c.execute(
            "DELETE FROM sms_submission_guards WHERE instance=? AND operation_id=? "
            "AND state IN ('completed','orphaned')",
            (str(instance), str(operation_id)),
        )
        if cur.rowcount != 1:
            raise RuntimeError("SMS submission acknowledgement lost its line guard")
        return True


def sms_submission_guard(instance: str) -> dict | None:
    with _lock, _conn() as c:
        row = c.execute(
            "SELECT instance,operation_id,payload_hash,transport,state,message_id,"
            "started_ts,updated_ts FROM sms_submission_guards WHERE instance=?",
            (str(instance),),
        ).fetchone()
    return dict(row) if row else None


def save_cellular_call_lease(call_id: str, instance: str, iccid: str,
                             direction: str, state: str) -> dict:
    """Persist paid-call lifecycle transitions; high-rate heartbeats stay in memory."""
    now = int(time.time())
    terminal = state in {"terminal_confirmed", "cancelled"}
    with _lock, _conn() as c:
        c.execute(
            "INSERT INTO cellular_call_leases(call_id,instance,iccid,direction,state,"
            "started_ts,updated_ts,terminal_ts) VALUES(?,?,?,?,?,?,?,?) "
            "ON CONFLICT(call_id) DO UPDATE SET state=excluded.state,"
            "updated_ts=excluded.updated_ts,terminal_ts=excluded.terminal_ts",
            (str(call_id), str(instance), str(iccid), str(direction), str(state),
             now, now, now if terminal else None),
        )
        row = c.execute("SELECT * FROM cellular_call_leases WHERE call_id=?",
                        (str(call_id),)).fetchone()
    return dict(row)


def mark_cellular_call_terminating(call_id: str) -> bool:
    """CAS an existing paid-call lease without overwriting a later terminal fact."""
    now = int(time.time())
    with _lock, _conn() as c:
        cur = c.execute(
            "UPDATE cellular_call_leases SET state='terminating',updated_ts=? "
            "WHERE call_id=? AND state NOT IN ('terminal_confirmed','cancelled')",
            (now, str(call_id)),
        )
        return cur.rowcount == 1


def open_cellular_call_lease(iccid: str) -> dict | None:
    with _lock, _conn() as c:
        row = c.execute(
            "SELECT * FROM cellular_call_leases WHERE iccid=? AND "
            "state NOT IN ('terminal_confirmed','cancelled') "
            "ORDER BY updated_ts DESC LIMIT 1", (str(iccid),)).fetchone()
    return dict(row) if row else None


def list_open_cellular_call_leases() -> list[dict]:
    with _lock, _conn() as c:
        rows = c.execute(
            "SELECT * FROM cellular_call_leases WHERE "
            "state NOT IN ('terminal_confirmed','cancelled') ORDER BY updated_ts"
        ).fetchall()
    return [dict(row) for row in rows]


def add_imported_message(fingerprint: str, instance: str, direction: str, peer: str,
                         body: str, ts: int, transport: str = "cellular") -> dict | None:
    """Atomically import one external message once. The marker survives UI deletion so an
    old SMS still retained by the modem is not resurrected on every polling cycle."""
    with _lock, _conn() as c:
        marker = c.execute(
            "INSERT OR IGNORE INTO message_imports(fingerprint,instance,imported_ts) VALUES(?,?,?)",
            (fingerprint, str(instance), int(time.time())),
        )
        if marker.rowcount == 0:
            return None
        cur = c.execute(
            "INSERT INTO messages(instance,direction,peer,body,status,ts,transport) "
            "VALUES(?,?,?,?,?,?,?)",
            (str(instance), direction, peer, body, "ok", int(ts), transport),
        )
        mid = cur.lastrowid
    return {"id": mid, "instance": str(instance), "direction": direction,
            "peer": peer, "body": body, "status": "ok", "error": None,
            "ts": int(ts), "transport": transport}


ALLOWANCE_FIELDS = ("balance", "valid_until", "sms_remaining", "data_remaining",
                    "voice_remaining", "activated_at")


def get_allowance(instance: str) -> dict:
    with _lock, _conn() as c:
        row = c.execute("SELECT * FROM line_allowances WHERE instance=?",
                        (str(instance),)).fetchone()
    if row:
        return dict(row)
    return {"instance": str(instance), **{key: "" for key in ALLOWANCE_FIELDS},
            "updated_ts": None, "source": "manual"}


def save_allowance(instance: str, values: dict, source: str = "manual",
                   updated_ts: int | None = None) -> dict:
    """Replace a line's allowance snapshot. Values stay as display strings so currencies,
    carrier units and plan-specific wording are not silently normalized away."""
    iid = str(instance)
    clean = {key: str(values.get(key) or "").strip() for key in ALLOWANCE_FIELDS}
    stamp = int(updated_ts or time.time())
    with _lock, _conn() as c:
        c.execute(
            "INSERT INTO line_allowances(instance,balance,valid_until,sms_remaining,"
            "data_remaining,voice_remaining,activated_at,updated_ts,source) "
            "VALUES(?,?,?,?,?,?,?,?,?) "
            "ON CONFLICT(instance) DO UPDATE SET balance=excluded.balance,"
            "valid_until=excluded.valid_until,sms_remaining=excluded.sms_remaining,"
            "data_remaining=excluded.data_remaining,voice_remaining=excluded.voice_remaining,"
            "activated_at=excluded.activated_at,"
            "updated_ts=excluded.updated_ts,source=excluded.source",
            (iid, clean["balance"], clean["valid_until"], clean["sms_remaining"],
             clean["data_remaining"], clean["voice_remaining"], clean["activated_at"],
             stamp, str(source)),
        )
    return get_allowance(iid)


def get_allowance_query_rule(instance: str) -> dict | None:
    with _lock, _conn() as c:
        row = c.execute("SELECT recipient,body,updated_ts FROM allowance_query_rules "
                        "WHERE instance=?", (str(instance),)).fetchone()
    return dict(row) if row else None


def save_allowance_query_rule(instance: str, recipient: str, body: str) -> dict:
    stamp = int(time.time())
    with _lock, _conn() as c:
        c.execute(
            "INSERT INTO allowance_query_rules(instance,recipient,body,updated_ts) "
            "VALUES(?,?,?,?) ON CONFLICT(instance) DO UPDATE SET "
            "recipient=excluded.recipient,body=excluded.body,updated_ts=excluded.updated_ts",
            (str(instance), str(recipient), str(body), stamp),
        )
    return get_allowance_query_rule(instance)


def delete_allowance_query_rule(instance: str) -> bool:
    with _lock, _conn() as c:
        cur = c.execute("DELETE FROM allowance_query_rules WHERE instance=?",
                        (str(instance),))
        return cur.rowcount > 0


def clear_allowance_data(instance: str) -> None:
    """Remove SIM-specific cached usage and query settings before a reusable line id is freed."""
    with _lock, _conn() as c:
        for table in ("line_allowances", "allowance_query_rules", "allowance_queries",
                      "allowance_reminders"):
            # Some recovery/tests open a minimal legacy DB before init() has created these
            # optional tables. Line deletion must still succeed in that degraded state.
            try:
                c.execute(f"DELETE FROM {table} WHERE instance=?", (str(instance),))
            except sqlite3.OperationalError:
                pass


def claim_allowance_reminder(instance: str, expiry_date: str, days_before: int,
                              sent_ts: int | None = None) -> bool:
    """Atomically reserve one reminder so restarts and overlapping pollers cannot duplicate it."""
    with _lock, _conn() as c:
        cur = c.execute(
            "INSERT OR IGNORE INTO allowance_reminders"
            "(instance,expiry_date,days_before,sent_ts) VALUES(?,?,?,?)",
            (str(instance), str(expiry_date), int(days_before),
             int(sent_ts or time.time())),
        )
        return cur.rowcount == 1


def start_allowance_query(instance: str, recipient: str, body: str, carrier_key: str,
                          transport: str, started_ts: int | None = None) -> dict:
    stamp = int(started_ts or time.time())
    with _lock, _conn() as c:
        cur = c.execute(
            "INSERT INTO allowance_queries(instance,recipient,body,carrier_key,started_ts,"
            "transport,status) VALUES(?,?,?,?,?,?, 'pending')",
            (str(instance), str(recipient), str(body), str(carrier_key), stamp,
             str(transport)),
        )
        qid = int(cur.lastrowid)
        # Query history is useful for debugging, but has no reason to grow without bound.
        c.execute("DELETE FROM allowance_queries WHERE instance=? AND id NOT IN "
                  "(SELECT id FROM allowance_queries WHERE instance=? ORDER BY id DESC LIMIT 20)",
                  (str(instance), str(instance)))
    return {"id": qid, "instance": str(instance), "recipient": str(recipient),
            "body": str(body), "carrier_key": str(carrier_key), "started_ts": stamp,
            "transport": str(transport), "status": "pending"}


def set_allowance_query_status(query_id: int, status: str) -> None:
    with _lock, _conn() as c:
        c.execute("UPDATE allowance_queries SET status=? WHERE id=?",
                  (str(status), int(query_id)))


def latest_allowance_query(instance: str) -> dict | None:
    with _lock, _conn() as c:
        row = c.execute("SELECT * FROM allowance_queries WHERE instance=? "
                        "ORDER BY id DESC LIMIT 1", (str(instance),)).fetchone()
    return dict(row) if row else None


def allowance_query_replies(instance: str, recipient: str, started_ts: int,
                            until_ts: int) -> list[dict]:
    """Read only replies belonging to an explicit, recent query attempt."""
    with _lock, _conn() as c:
        rows = c.execute(
            "SELECT id,peer,body,ts FROM messages WHERE instance=? AND direction='in' "
            "AND peer=? AND ts>=? AND ts<=? ORDER BY ts,id",
            (str(instance), str(recipient), int(started_ts), int(until_ts)),
        ).fetchall()
    return [dict(row) for row in rows]


def reserve_local_modem_sms(instance: str, iccid: str, content_hash: str,
                            daemon_epoch: str, recipient: str, body: str,
                            existing_message_id: int | None = None) -> int | dict:
    """Durably reserve one local ModemManager create operation before it starts.

    The tracking row retains only ``content_hash``; the normal message-history row stores the
    recipient and body for the UI. A committed reservation lets the receive poller fail closed
    if the process exits after ModemManager creates an object but before its path can be bound.
    """
    now = int(time.time())
    with _lock, _conn() as c:
        # An unbound reservation can only cover a create operation interrupted before its path
        # was returned. Keep one day of fail-closed protection, then bound disk growth. Markers
        # for older daemon generations can never match a current object and are also disposable.
        c.execute("DELETE FROM local_modem_sms WHERE daemon_epoch<>? AND created_ts<?",
                  (str(daemon_epoch), now - LOCAL_MODEM_SMS_RETENTION_SECONDS))
        c.execute("DELETE FROM local_modem_sms WHERE sms_path IS NULL AND created_ts<?",
                  (now - LOCAL_MODEM_SMS_RETENTION_SECONDS,))
        if existing_message_id is None:
            message = c.execute(
                "INSERT INTO messages(instance,direction,peer,body,status,ts,transport) "
                "VALUES(?,?,?,?,?,?,?)",
                (str(instance), "out", str(recipient), str(body), "pending", now,
                 "cellular"),
            )
            message_id = int(message.lastrowid)
        else:
            message_id = int(existing_message_id)
            message = c.execute(
                "SELECT instance,direction,peer,body,status,transport FROM messages WHERE id=?",
                (message_id,),
            ).fetchone()
            expected = (str(instance), "out", str(recipient), str(body),
                        "pending", "cellular")
            if not message or tuple(message) != expected:
                raise ValueError("existing cellular SMS message does not match reservation")
            existing = c.execute(
                "SELECT id,instance,iccid,daemon_epoch,content_hash,cancelled "
                "FROM local_modem_sms WHERE message_id=?",
                (message_id,),
            ).fetchone()
            if existing:
                exact = (str(existing["instance"]) == str(instance)
                         and str(existing["iccid"]) == str(iccid)
                         and str(existing["daemon_epoch"]) == str(daemon_epoch)
                         and str(existing["content_hash"]) == str(content_hash)
                         and not int(existing["cancelled"] or 0))
                if not exact:
                    raise ValueError("cellular SMS message has a conflicting reservation")
                return {"id": int(existing["id"]), "created": False}
        cur = c.execute(
            "INSERT INTO local_modem_sms"
            "(instance,iccid,daemon_epoch,message_id,content_hash,created_ts) "
            "VALUES(?,?,?,?,?,?)",
            (str(instance), str(iccid), str(daemon_epoch), message_id,
             str(content_hash), now),
        )
        reservation_id = int(cur.lastrowid)
        if existing_message_id is not None:
            return {"id": reservation_id, "created": True}
        return reservation_id


def bind_local_modem_sms(reservation_id: int, daemon_epoch: str,
                         modem_path: str, sms_path: str) -> bool:
    """Atomically bind a reservation to the ModemManager object it created.

    ModemManager may reuse numeric object paths after a restart. Replacing an older marker for
    the same SIM/path is safe because the new marker carries the new content hash.
    """
    with _lock, _conn() as c:
        row = c.execute(
            "SELECT iccid,modem_path,sms_path,cancelled FROM local_modem_sms "
            "WHERE id=? AND daemon_epoch=?",
            (int(reservation_id), str(daemon_epoch)),
        ).fetchone()
        if not row or row["cancelled"]:
            return False
        # A scanner in another worker may have claimed the object between Create and this bind.
        # Treat an exact prior binding as success; a different binding remains a hard stop.
        if row["sms_path"] is not None:
            return (str(row["modem_path"] or "") == str(modem_path)
                    and str(row["sms_path"]) == str(sms_path))
        c.execute(
            "DELETE FROM local_modem_sms "
            "WHERE daemon_epoch=? AND iccid=? AND sms_path=? AND id<>?",
            (str(daemon_epoch), str(row["iccid"]), str(sms_path), int(reservation_id)),
        )
        cur = c.execute(
            "UPDATE local_modem_sms SET modem_path=?,sms_path=?,bound_ts=? "
            "WHERE id=? AND daemon_epoch=? AND sms_path IS NULL",
            (str(modem_path), str(sms_path), int(time.time()), int(reservation_id),
             str(daemon_epoch)),
        )
        return cur.rowcount == 1


def cancel_local_modem_sms(reservation_id: int) -> None:
    """Deactivate a reservation when ModemManager definitely created no SMS object.

    Keep the row briefly so the caller can still resolve its atomically-created history row;
    the cancelled flag prevents the scanner treating it as a live unbound reservation.
    """
    with _lock, _conn() as c:
        c.execute("UPDATE local_modem_sms SET cancelled=1 "
                  "WHERE id=? AND sms_path IS NULL", (int(reservation_id),))


def local_modem_sms_message(reservation_id: int) -> dict | None:
    """Return the history row atomically created with a local SMS reservation."""
    with _lock, _conn() as c:
        row = c.execute(
            "SELECT m.* FROM local_modem_sms l JOIN messages m ON m.id=l.message_id "
            "WHERE l.id=? LIMIT 1", (int(reservation_id),),
        ).fetchone()
    return dict(row) if row else None


def is_local_modem_sms(daemon_epoch: str, iccid: str, modem_path: str, sms_path: str,
                       content_hash: str, sms_ts: int = 0) -> bool:
    """Return whether an outgoing ModemManager object was created by this application.

    Exact path markers survive control-plane restarts. The content hash prevents a different
    object imported after ModemManager reuses a numeric path from being hidden.

    A recent unbound reservation covers the narrow crash/failure window between object creation
    and binding. When ModemManager supplies an object timestamp it must be close to the reserve
    time; objects without one get only a short claim window. Thus a failed attempt cannot hide a
    separately-created outgoing message with identical content for the full marker retention.
    """
    now = int(time.time())
    with _lock, _conn() as c:
        row = c.execute(
            "SELECT 1 FROM local_modem_sms "
            "WHERE daemon_epoch=? AND iccid=? AND modem_path=? AND sms_path=? "
            "AND content_hash=? LIMIT 1",
            (str(daemon_epoch), str(iccid), str(modem_path), str(sms_path),
             str(content_hash)),
        ).fetchone()
        if row:
            return True
        object_ts = int(sms_ts or 0)
        if object_ts > 0:
            lower = object_ts - LOCAL_MODEM_SMS_CLAIM_SECONDS
            upper = object_ts + LOCAL_MODEM_SMS_CLAIM_SECONDS
        else:
            lower = now - LOCAL_MODEM_SMS_CLAIM_SECONDS
            upper = now + LOCAL_MODEM_SMS_CLAIM_SECONDS
        pending = c.execute(
            "SELECT id FROM local_modem_sms WHERE daemon_epoch=? AND iccid=? "
            "AND sms_path IS NULL AND cancelled=0 AND content_hash=? "
            "AND created_ts BETWEEN ? AND ? ORDER BY created_ts DESC,id DESC LIMIT 1",
            (str(daemon_epoch), str(iccid), str(content_hash), lower, upper),
        ).fetchone()
        if not pending:
            return False
        # A Create reply may be lost or the process may exit before bind_local_modem_sms().
        # Claim the matching live object now so subsequent restarts remain deduplicated.
        c.execute(
            "DELETE FROM local_modem_sms WHERE daemon_epoch=? AND iccid=? AND sms_path=? "
            "AND id<>?",
            (str(daemon_epoch), str(iccid), str(sms_path), int(pending["id"])),
        )
        claimed = c.execute(
            "UPDATE local_modem_sms SET modem_path=?,sms_path=?,bound_ts=? "
            "WHERE id=? AND sms_path IS NULL AND cancelled=0",
            (str(modem_path), str(sms_path), now, int(pending["id"])),
        )
        return claimed.rowcount == 1


def prune_local_modem_sms(daemon_epoch: str, iccid: str, modem_path: str,
                          live_sms_paths: set[str] | list[str]) -> int:
    """Bound durable-marker growth after a verified ModemManager path listing.

    Current objects retain their marker indefinitely. Missing paths, cancelled reservations and
    markers from older daemon generations get a one-day grace period so an in-flight HTTP caller
    can still resolve the history row that was committed with its reservation.
    """
    now = int(time.time())
    cutoff = now - LOCAL_MODEM_SMS_RETENTION_SECONDS
    live = {str(path) for path in live_sms_paths}
    with _lock, _conn() as c:
        removed = c.execute(
            "DELETE FROM local_modem_sms WHERE created_ts<? "
            "AND (cancelled=1 OR daemon_epoch<>? OR sms_path IS NULL)",
            (cutoff, str(daemon_epoch)),
        ).rowcount
        rows = c.execute(
            "SELECT id,sms_path FROM local_modem_sms WHERE daemon_epoch=? AND iccid=? "
            "AND modem_path=? AND sms_path IS NOT NULL "
            "AND COALESCE(bound_ts,created_ts)<?",
            (str(daemon_epoch), str(iccid), str(modem_path), cutoff),
        ).fetchall()
        stale_ids = [(int(row["id"]),) for row in rows if str(row["sms_path"]) not in live]
        if stale_ids:
            c.executemany("DELETE FROM local_modem_sms WHERE id=?", stale_ids)
            removed += len(stale_ids)
        return int(removed)


def list_threads(instance: str) -> list:
    with _lock, _conn() as c:
        rows = c.execute(
            """SELECT peer, MAX(ts) AS last_ts,
                      (SELECT body FROM messages m2 WHERE m2.instance=m.instance AND m2.peer=m.peer
                       ORDER BY ts DESC, id DESC LIMIT 1) AS last_body,
                      COUNT(*) AS n
               FROM messages m WHERE instance=? GROUP BY peer ORDER BY last_ts DESC, peer ASC""",
            (str(instance),),
        ).fetchall()
    return [dict(r) for r in rows]


def list_messages(instance: str, peer: str, limit: int = 200) -> list:
    with _lock, _conn() as c:
        rows = c.execute(
            "SELECT * FROM messages WHERE instance=? AND peer=? ORDER BY ts ASC, id ASC LIMIT ?",
            (str(instance), peer, limit),
        ).fetchall()
    return [dict(r) for r in rows]


def recent_messages(instance: str, limit: int = 10) -> list:
    """The newest messages of a line across every peer, newest first. The WebUI reads one
    conversation at a time; a chat/bot view wants the tail of the whole line."""
    with _lock, _conn() as c:
        rows = c.execute(
            "SELECT * FROM messages WHERE instance=? ORDER BY ts DESC, id DESC LIMIT ?",
            (str(instance), max(1, int(limit))),
        ).fetchall()
    return [dict(r) for r in rows]


def _placeholders(n: int) -> str:
    return ",".join("?" * n)


def delete_messages(instance: str, ids: list[int]) -> int:
    """Delete specific messages of this instance by id. Returns the number removed."""
    ids = [int(i) for i in ids]
    if not ids:
        return 0
    with _lock, _conn() as c:
        cur = c.execute(
            f"DELETE FROM messages WHERE instance=? AND id IN ({_placeholders(len(ids))})",
            (str(instance), *ids),
        )
        return cur.rowcount


def delete_thread(instance: str, peer: str) -> int:
    """Delete every message in one conversation (instance + peer). Returns rows removed."""
    with _lock, _conn() as c:
        cur = c.execute("DELETE FROM messages WHERE instance=? AND peer=?",
                        (str(instance), peer))
        return cur.rowcount


def clear_messages(instance: str) -> int:
    """Delete ALL messages for this instance. Returns rows removed."""
    with _lock, _conn() as c:
        cur = c.execute("DELETE FROM messages WHERE instance=?", (str(instance),))
        return cur.rowcount


def _validate_browser_call_row(row: dict | sqlite3.Row) -> None:
    value = dict(row)
    state = value.get("browser_state")
    is_browser_incoming = (value.get("direction") == "in"
                           and value.get("transport") == "vowifi")
    if state is None:
        if is_browser_incoming and value.get("end_ts") is None:
            raise BrowserCallConflict("open incoming call has no browser state")
        return
    if not is_browser_incoming or state not in BROWSER_CALL_STATES:
        raise BrowserCallConflict("browser call state is invalid")
    revision = value.get("browser_revision")
    if type(revision) is not int or revision < 0:
        raise BrowserCallConflict("browser call revision is invalid")
    if (state == "terminal") != (value.get("end_ts") is not None):
        raise BrowserCallConflict("browser call terminal fields are inconsistent")
    fields = tuple(str(value.get(name) or "") for name in (
        "browser_owner_session", "browser_operation", "browser_epoch"))
    complete_owner = all(fields)
    if any(fields) and not complete_owner:
        raise BrowserCallConflict("browser call owner fields are partial")
    if state in {"claiming", "attach_submitted_unknown",
                 "answer_submitted_unknown", "active"} and not complete_owner:
        raise BrowserCallConflict("browser call owner is missing")
    if state == "ringing" and complete_owner:
        raise BrowserCallConflict("ringing browser call unexpectedly has an owner")
    if complete_owner:
        owner, operation, epoch = fields
        if (not re.fullmatch(r"[A-Za-z0-9_-]{16,128}", owner)
                or not re.fullmatch(r"[0-9a-f]{32}", operation)
                or not re.fullmatch(r"[A-Za-z0-9_-]{20,64}", epoch)):
            raise BrowserCallConflict("browser call owner identity is invalid")


def _browser_call_rows(connection, instance: str, ids: list[int] | None = None):
    values: list[object] = [str(instance)]
    query = ("SELECT * FROM calls WHERE instance=? AND direction='in' "
             "AND transport='vowifi' AND (browser_state IS NOT NULL OR end_ts IS NULL)")
    if ids is not None:
        if not ids:
            return []
        query += f" AND id IN ({_placeholders(len(ids))})"
        values.extend(int(value) for value in ids)
    return connection.execute(query, values).fetchall()


def _guard_browser_call_deletion(connection, instance: str,
                                  ids: list[int] | None = None) -> None:
    for row in _browser_call_rows(connection, instance, ids):
        _validate_browser_call_row(row)
        if row["browser_state"] != "terminal":
            raise BrowserCallConflict("an incoming browser call is not terminal")


def browser_call_paid_work_count() -> int:
    """Count durable owner/unknown states; corrupt combinations fail closed."""
    with _lock, _conn() as connection:
        count = 0
        rows = connection.execute(
            "SELECT * FROM calls WHERE direction='in' AND transport='vowifi' "
            "AND (browser_state IS NOT NULL OR end_ts IS NULL)").fetchall()
        for row in rows:
            _validate_browser_call_row(row)
            if row["browser_state"] in BROWSER_CALL_PAID_STATES:
                count += 1
        return count


def begin_instance_call_fence(instance: str) -> None:
    """Atomically guard instance deletion and block any later call start."""
    with _lock, _conn() as connection:
        _guard_browser_call_deletion(connection, str(instance))
        try:
            connection.execute(
                "INSERT INTO instance_call_fences(instance,created_ts) VALUES(?,?)",
                (str(instance), int(time.time())))
        except sqlite3.IntegrityError as exc:
            raise BrowserCallConflict("instance call deletion is already fenced") from exc


def end_instance_call_fence(instance: str) -> None:
    with _lock, _conn() as connection:
        connection.execute(
            "DELETE FROM instance_call_fences WHERE instance=?", (str(instance),))


def _instance_call_creation_fenced(connection, instance: str) -> bool:
    return bool(connection.execute(
        "SELECT 1 FROM instance_call_fences WHERE instance=? "
        "UNION ALL SELECT 1 FROM soft_deleted_instances WHERE instance_id=? LIMIT 1",
        (str(instance), str(instance))).fetchone())


def transition_browser_call(
        instance: str, call_id: int | str, source_call_id: str, engine_run_id: str,
        *, expected_state: str, expected_revision: int, new_state: str,
        expected_owner: str | None = None, owner_session=_KEEP,
        operation=_KEEP, epoch=_KEEP, status=_KEEP) -> dict | None:
    """One exact, monotonic durable state transition; ``None`` means another actor won."""
    if (expected_state not in BROWSER_CALL_STATES
            or new_state not in BROWSER_CALL_STATES
            or new_state not in BROWSER_CALL_TRANSITIONS[expected_state]
            or type(expected_revision) is not int or expected_revision < 0):
        raise ValueError("invalid browser call transition")
    try:
        numeric_id = int(call_id)
    except (TypeError, ValueError) as exc:
        raise ValueError("invalid browser call id") from exc
    with _lock, _conn() as connection:
        row = connection.execute(
            "SELECT * FROM calls WHERE id=? AND instance=? AND source_call_id=? "
            "AND engine_run_id=? AND direction='in' AND transport='vowifi' LIMIT 1",
            (numeric_id, str(instance), str(source_call_id), str(engine_run_id))).fetchone()
        if not row:
            return None
        _validate_browser_call_row(row)
        current = dict(row)
        if (current["end_ts"] is not None or current["browser_state"] != expected_state
                or current["browser_revision"] != expected_revision
                or (expected_owner is not None
                    and current["browser_owner_session"] != str(expected_owner))):
            return None
        updates = {
            "browser_state": new_state,
            "browser_revision": expected_revision + 1,
            "browser_owner_session": (current["browser_owner_session"]
                                      if owner_session is _KEEP else str(owner_session or "")),
            "browser_operation": (current["browser_operation"]
                                  if operation is _KEEP else str(operation or "")),
            "browser_epoch": (current["browser_epoch"]
                              if epoch is _KEEP else str(epoch or "")),
            "status": current["status"] if status is _KEEP else str(status),
        }
        candidate = {**current, **updates}
        _validate_browser_call_row(candidate)
        result = connection.execute(
            "UPDATE calls SET browser_state=?,browser_revision=?,browser_owner_session=?,"
            "browser_operation=?,browser_epoch=?,status=? WHERE id=? AND instance=? "
            "AND source_call_id=? AND engine_run_id=? AND end_ts IS NULL "
            "AND browser_state=? AND browser_revision=?",
            (updates["browser_state"], updates["browser_revision"],
             updates["browser_owner_session"], updates["browser_operation"],
             updates["browser_epoch"], updates["status"], numeric_id, str(instance),
             str(source_call_id), str(engine_run_id), expected_state, expected_revision),
        )
        if result.rowcount != 1:
            return None
        return dict(connection.execute(
            "SELECT * FROM calls WHERE id=?", (numeric_id,)).fetchone())


def terminal_browser_call_exact(
        instance: str, call_id: int | str, source_call_id: str, engine_run_id: str,
        *, expected_state: str, expected_revision: int,
        expected_owner: str | None = None, status: str = "ended") -> dict | None:
    """Terminal CAS used only after external same-generation absence proof."""
    if (expected_state not in BROWSER_CALL_STATES - {"terminal"}
            or type(expected_revision) is not int or expected_revision < 0):
        raise ValueError("invalid browser call terminal transition")
    try:
        numeric_id = int(call_id)
    except (TypeError, ValueError) as exc:
        raise ValueError("invalid browser call id") from exc
    now = int(time.time())
    with _lock, _conn() as connection:
        row = connection.execute(
            "SELECT * FROM calls WHERE id=? AND instance=? AND source_call_id=? "
            "AND engine_run_id=? AND direction='in' AND transport='vowifi' LIMIT 1",
            (numeric_id, str(instance), str(source_call_id), str(engine_run_id))).fetchone()
        if not row:
            return None
        _validate_browser_call_row(row)
        current = dict(row)
        if (current["end_ts"] is not None or current["browser_state"] != expected_state
                or current["browser_revision"] != expected_revision
                or (expected_owner is not None
                    and current["browser_owner_session"] != str(expected_owner))):
            return None
        result = connection.execute(
            "UPDATE calls SET status=?,end_ts=?,browser_state='terminal',"
            "browser_revision=? WHERE id=? AND instance=? AND source_call_id=? "
            "AND engine_run_id=? AND end_ts IS NULL AND browser_state=? "
            "AND browser_revision=?",
            (str(status), now, expected_revision + 1, numeric_id, str(instance),
             str(source_call_id), str(engine_run_id), expected_state, expected_revision))
        if result.rowcount != 1:
            return None
        return dict(connection.execute(
            "SELECT * FROM calls WHERE id=?", (numeric_id,)).fetchone())


def get_browser_call_exact(instance: str, call_id: int | str,
                           source_call_id: str, engine_run_id: str) -> dict | None:
    try:
        numeric_id = int(call_id)
    except (TypeError, ValueError):
        return None
    with _lock, _conn() as connection:
        row = connection.execute(
            "SELECT * FROM calls WHERE id=? AND instance=? AND source_call_id=? "
            "AND engine_run_id=? AND direction='in' AND transport='vowifi' LIMIT 1",
            (numeric_id, str(instance), str(source_call_id), str(engine_run_id))).fetchone()
        if not row:
            return None
        _validate_browser_call_row(row)
        return dict(row)


def add_call(instance: str, direction: str, peer: str, status: str = "ringing",
             transport: str = "vowifi", source_call_id: str = "",
             engine_run_id: str = "") -> dict:
    ts = int(time.time())
    with _lock, _conn() as c:
        if _instance_call_creation_fenced(c, str(instance)):
            raise BrowserCallConflict("instance call creation is fenced")
        cur = c.execute(
            "INSERT INTO calls(instance,direction,peer,status,start_ts,transport,"
            "source_call_id,engine_run_id) VALUES(?,?,?,?,?,?,?,?)",
            (str(instance), direction, peer, status, ts, str(transport),
             str(source_call_id or ""), str(engine_run_id or "")),
        )
        cid = cur.lastrowid
    return {"id": cid, "instance": str(instance), "direction": direction,
            "peer": peer, "status": status, "start_ts": ts, "transport": str(transport),
            "source_call_id": str(source_call_id or ""),
            "engine_run_id": str(engine_run_id or "")}


def record_call_start(instance: str, direction: str, peer: str, status: str,
                      source_call_id: str, transport: str = "vowifi",
                      engine_run_id: str = "") -> tuple[dict, bool]:
    """Idempotently record a correlated call start without reopening a terminal row."""
    source_call_id = str(source_call_id or "")
    engine_run_id = str(engine_run_id or "")
    if not source_call_id:
        if direction == "in":
            return add_call_deduped(instance, direction, peer, status=status), True
        return add_call(instance, direction, peer, status=status, transport=transport,
                        engine_run_id=engine_run_id), True
    now = int(time.time())
    with _lock, _conn() as c:
        if _instance_call_creation_fenced(c, str(instance)):
            raise BrowserCallConflict("instance call creation is fenced")
        if engine_run_id:
            row = c.execute(
                "SELECT * FROM calls WHERE instance=? AND direction=? AND transport=? "
                "AND source_call_id=? AND engine_run_id=? LIMIT 1",
                (str(instance), direction, str(transport), source_call_id,
                 engine_run_id)).fetchone()
        else:
            row = c.execute(
                "SELECT * FROM calls WHERE instance=? AND direction=? AND transport=? "
                "AND source_call_id=? LIMIT 1",
                (str(instance), direction, str(transport), source_call_id)).fetchone()
        if row:
            current = dict(row)
            if current.get("browser_state") is not None:
                _validate_browser_call_row(current)
            if peer and not current.get("peer"):
                c.execute("UPDATE calls SET peer=? WHERE id=?", (peer, current["id"]))
                current["peer"] = peer
            return current, False
        native_incoming = bool(
            direction == "in" and str(transport) == "vowifi"
            and source_call_id and engine_run_id)
        cur = c.execute(
            "INSERT INTO calls(instance,direction,peer,status,start_ts,transport,"
            "source_call_id,engine_run_id,browser_state,browser_revision) "
            "VALUES(?,?,?,?,?,?,?,?,?,?)",
            (str(instance), direction, peer, status, now, str(transport), source_call_id,
             engine_run_id, "ringing" if native_incoming else None,
             0 if native_incoming else None))
        row = c.execute("SELECT * FROM calls WHERE id=?", (cur.lastrowid,)).fetchone()
        return dict(row), True


def record_call_result(instance: str, direction: str, peer: str, status: str,
                       source_call_id: str, transport: str = "vowifi",
                       engine_run_id: str = "") -> tuple[dict | None, bool]:
    """Idempotently finalize a call, including result-before-start delivery."""
    source_call_id = str(source_call_id or "")
    engine_run_id = str(engine_run_id or "")
    now = int(time.time())
    if not source_call_id:
        record = update_last_call(instance, direction, peer, status)
        if not record and peer:
            record = update_last_call(instance, direction, None, status)
        return record, False
    with _lock, _conn() as c:
        if engine_run_id:
            row = c.execute(
                "SELECT * FROM calls WHERE instance=? AND direction=? AND transport=? "
                "AND source_call_id=? AND engine_run_id=? LIMIT 1",
                (str(instance), direction, str(transport), source_call_id,
                 engine_run_id)).fetchone()
        else:
            row = c.execute(
                "SELECT * FROM calls WHERE instance=? AND direction=? AND transport=? "
                "AND source_call_id=? LIMIT 1",
                (str(instance), direction, str(transport), source_call_id)).fetchone()
        if row:
            current = dict(row)
            if current.get("end_ts") is None:
                next_peer = peer or current.get("peer") or ""
                if current.get("browser_state") is not None:
                    _validate_browser_call_row(current)
                    revision = int(current["browser_revision"]) + 1
                    c.execute(
                        "UPDATE calls SET peer=?,status=?,end_ts=?,browser_state='terminal',"
                        "browser_revision=? WHERE id=? AND end_ts IS NULL",
                        (next_peer, status, now, revision, current["id"]))
                    current.update({"peer": next_peer, "status": status, "end_ts": now,
                                    "browser_state": "terminal",
                                    "browser_revision": revision})
                else:
                    c.execute("UPDATE calls SET peer=?,status=?,end_ts=? WHERE id=?",
                              (next_peer, status, now, current["id"]))
                    current.update({"peer": next_peer, "status": status, "end_ts": now})
            return current, False
        if _instance_call_creation_fenced(c, str(instance)):
            return None, False
        native_incoming = bool(
            direction == "in" and str(transport) == "vowifi"
            and source_call_id and engine_run_id)
        cur = c.execute(
            "INSERT INTO calls(instance,direction,peer,status,start_ts,end_ts,transport,"
            "source_call_id,engine_run_id,browser_state,browser_revision) "
            "VALUES(?,?,?,?,?,?,?,?,?,?,?)",
            (str(instance), direction, peer, status, now, now, str(transport),
             source_call_id, engine_run_id, "terminal" if native_incoming else None,
             0 if native_incoming else None))
        row = c.execute("SELECT * FROM calls WHERE id=?", (cur.lastrowid,)).fetchone()
        return dict(row), True


def get_open_call_for_transport(instance: str, transport: str) -> dict | None:
    with _lock, _conn() as c:
        row = c.execute(
            "SELECT * FROM calls WHERE instance=? AND direction='out' AND transport=? "
            "AND end_ts IS NULL ORDER BY start_ts DESC,id DESC LIMIT 1",
            (str(instance), str(transport))).fetchone()
    return dict(row) if row else None


def get_open_call(instance: str, direction: str, within_s: int | None = None) -> dict | None:
    """The most recent still-open (not yet finalized) call for (instance, direction), or None.

    A softphone handles one call at a time, so an inbound INVITE that the IMS delivers more
    than once (VoLTE preconditions / GRUU fork / retransmit) fires `call_in` several times
    for the SAME call. Reusing the open record instead of inserting a new one keeps one row
    per call — otherwise every extra `call_in` leaves a ghost 'ringing' entry that the single
    `call_result` never finalizes. `within_s` bounds how old the open record may be so a
    genuinely new call (after a stale unfinalized one) still starts fresh."""
    with _lock, _conn() as c:
        row = c.execute(
            "SELECT * FROM calls WHERE instance=? AND direction=? AND end_ts IS NULL "
            "ORDER BY start_ts DESC LIMIT 1", (str(instance), direction)).fetchone()
        if not row:
            return None
        if within_s is not None and int(time.time()) - row["start_ts"] > within_s:
            return None
        return dict(row)


def get_open_call(instance: str, direction: str, within_s: int | None = None) -> dict | None:
    """The most recent still-open (not yet finalized, end_ts IS NULL) call for (instance,
    direction), or None. `within_s` bounds how old the open record may be so a genuinely new
    call after a stale unfinalized one still starts fresh."""
    with _lock, _conn() as c:
        row = c.execute(
            "SELECT * FROM calls WHERE instance=? AND direction=? AND end_ts IS NULL "
            "ORDER BY start_ts DESC LIMIT 1", (str(instance), direction)).fetchone()
        if not row:
            return None
        if within_s is not None and int(time.time()) - row["start_ts"] > within_s:
            return None
        return dict(row)


def get_call_by_id(instance: str, call_id: int | str) -> dict | None:
    try:
        cid = int(call_id)
    except (TypeError, ValueError):
        return None
    with _lock, _conn() as c:
        row = c.execute(
            "SELECT * FROM calls WHERE instance=? AND id=? LIMIT 1",
            (str(instance), cid)).fetchone()
    return dict(row) if row else None


def add_call_deduped(instance: str, direction: str, peer: str, status: str = "ringing",
                     open_within_s: int = 90) -> dict:
    """Insert an inbound-call record, coalescing concurrent duplicate `call_in` events for the
    SAME still-ringing call into ONE record.

    The IMS can deliver a call_in more than once while the call is still being set up (VoLTE
    preconditions / GRUU fork): those extra events all arrive BEFORE the call_result, so the
    record is still open (end_ts IS NULL) and is simply reused — no ghost row, and no reliance
    on any time heuristic that could swallow a genuine call-back.

    (The other historical source of duplicates — the dialplan 'h' hangup handler falling
    through to the broad `_.` pattern and firing a SECOND call_in AFTER finalization — is fixed
    at the source in extensions.conf.j2 with `h => …,Return()`, so no post-finalize dedupe is
    needed here.)

    An anonymous first call_in ('') whose number arrives on a later duplicate is filled in."""
    open_rec = get_open_call(instance, direction, within_s=open_within_s)
    # Only coalesce into an open record with a compatible peer: an anonymous dup ('') matches
    # anything; a numbered dup must match (or fill) the open record's peer. A different number
    # is a distinct call and starts its own record.
    if open_rec:
        rp = open_rec.get("peer") or ""
        if not peer or not rp or peer == rp:
            if peer and not rp:
                with _lock, _conn() as c:
                    c.execute("UPDATE calls SET peer=? WHERE id=?", (peer, open_rec["id"]))
                open_rec["peer"] = peer
            return open_rec
    return add_call(instance, direction, peer, status)


def update_call(cid: int, status: str, ended: bool = False):
    with _lock, _conn() as c:
        if ended:
            c.execute("UPDATE calls SET status=?, end_ts=? WHERE id=?",
                      (status, int(time.time()), cid))
        else:
            c.execute("UPDATE calls SET status=? WHERE id=?", (status, cid))


def finalize_exact_call(instance: str, call_id: int | str, source_call_id: str,
                        engine_run_id: str, status: str = "ended") -> dict | None:
    """Finalize only the still-open row matching all exact call identity fields."""
    try:
        numeric_id = int(call_id)
    except (TypeError, ValueError):
        return None
    with _lock, _conn() as c:
        row = c.execute(
            "SELECT * FROM calls WHERE id=? AND instance=? AND source_call_id=? "
            "AND engine_run_id=? LIMIT 1",
            (numeric_id, str(instance), str(source_call_id), str(engine_run_id)),
        ).fetchone()
        if not row:
            return None
        current = dict(row)
        if current.get("browser_state") is not None:
            _validate_browser_call_row(current)
        if current.get("end_ts") is not None:
            return current
        now = int(time.time())
        if current.get("browser_state") is not None:
            revision = int(current["browser_revision"])
            result = c.execute(
                "UPDATE calls SET status=?,end_ts=?,browser_state='terminal',"
                "browser_revision=? WHERE id=? AND instance=? AND source_call_id=? "
                "AND engine_run_id=? AND end_ts IS NULL AND browser_state=? "
                "AND browser_revision=?",
                (str(status), now, revision + 1, numeric_id, str(instance),
                 str(source_call_id), str(engine_run_id), current["browser_state"], revision))
        else:
            result = c.execute(
                "UPDATE calls SET status=?,end_ts=? WHERE id=? AND instance=? "
                "AND source_call_id=? AND engine_run_id=? AND end_ts IS NULL",
                (str(status), now, numeric_id, str(instance),
                 str(source_call_id), str(engine_run_id)))
        if result.rowcount != 1:
            return None
        return dict(c.execute("SELECT * FROM calls WHERE id=?", (numeric_id,)).fetchone())


def update_last_call(instance: str, direction: str, peer: str | None, status: str) -> dict | None:
    """Finalize the most recent still-open call for (instance, direction[, peer]).

    peer may be None/empty: Asterisk's 'h' hangup handler loses pre-Dial channel variables
    (incl. the dialled number) when the caller hangs up mid-Dial and the channel is
    masqueraded, so the disposition callback can arrive with no peer. Since a softphone
    handles one call at a time, finalizing the most-recent OPEN call of that direction is
    unambiguous and correct in that case."""
    with _lock, _conn() as c:
        if peer:
            row = c.execute(
                "SELECT id FROM calls WHERE instance=? AND direction=? AND peer=? AND end_ts IS NULL "
                "ORDER BY start_ts DESC LIMIT 1", (str(instance), direction, peer)).fetchone()
        else:
            row = c.execute(
                "SELECT id FROM calls WHERE instance=? AND direction=? AND end_ts IS NULL "
                "ORDER BY start_ts DESC LIMIT 1", (str(instance), direction)).fetchone()
        if not row:
            return None
        c.execute("UPDATE calls SET status=?, end_ts=? WHERE id=?",
                  (status, int(time.time()), row["id"]))
        r = c.execute("SELECT * FROM calls WHERE id=?", (row["id"],)).fetchone()
        return dict(r) if r else None



def list_calls(instance: str, limit: int = 100) -> list:
    with _lock, _conn() as c:
        rows = c.execute(
            "SELECT * FROM calls WHERE instance=? ORDER BY start_ts DESC LIMIT ?",
            (str(instance), limit),
        ).fetchall()
    return [dict(r) for r in rows]


def list_open_incoming_calls(instance: str, transport: str = "vowifi",
                             limit: int = 32) -> list:
    """Return open inbound calls without depending on their history-window position."""
    with _lock, _conn() as c:
        rows = c.execute(
            "SELECT * FROM calls WHERE instance=? AND direction='in' AND transport=? "
            "AND end_ts IS NULL ORDER BY start_ts DESC LIMIT ?",
            (str(instance), str(transport), max(1, min(int(limit), 32))),
        ).fetchall()
    return [dict(r) for r in rows]


def delete_calls(instance: str, ids: list[int]) -> int:
    """Delete specific call-log entries of this instance by id. Returns rows removed."""
    ids = [int(i) for i in ids]
    if not ids:
        return 0
    with _lock, _conn() as c:
        _guard_browser_call_deletion(c, str(instance), ids)
        cur = c.execute(
            f"DELETE FROM calls WHERE instance=? AND id IN ({_placeholders(len(ids))})",
            (str(instance), *ids),
        )
        return cur.rowcount


def clear_calls(instance: str) -> int:
    """Delete the ENTIRE call log for this instance. Returns rows removed."""
    with _lock, _conn() as c:
        _guard_browser_call_deletion(c, str(instance))
        cur = c.execute("DELETE FROM calls WHERE instance=?", (str(instance),))
        return cur.rowcount


def record_line_state(instance: str, state: str, ts: int | None = None,
                      reason: str = "", detail: str = "") -> None:
    """Append one connectivity observation for a line to its timeline.

    A sample that continues the current state only moves the segment's end. A different
    state starts a new segment where the previous one ended, so an uninterrupted record has
    no artificial holes; a silence longer than the continuity window deliberately leaves
    one, because the control plane cannot claim to know what happened while it was down.

    ``reason`` is the status reason_code that came with the sample, ``detail`` the evidence
    behind it (the name that failed to resolve, the resolvers it was tried against…). A
    segment keeps the first non-empty reason it saw — an outage's cause is what broke it,
    not the "registering…" it passes through on the way back up — and detail travels with
    the reason it belongs to.
    """
    state = state if state in LINE_STATES else "down"
    ts = int(ts if ts is not None else time.time())
    reason, detail = str(reason or ""), str(detail or "")
    with _lock, _conn() as c:
        row = c.execute(
            "SELECT id, state, end_ts, reason FROM line_states WHERE instance=? "
            "ORDER BY end_ts DESC, id DESC LIMIT 1", (str(instance),)).fetchone()
        if row and ts < row["end_ts"]:
            # A clock stepping backwards (NTP sync after boot) must never produce a segment
            # that ends before it starts.
            ts = int(row["end_ts"])
        continuous = bool(row) and ts - int(row["end_ts"]) <= LINE_STATE_CONTINUITY_SECONDS
        if continuous and row["state"] == state:
            # The first symptom can be above the actual fault: an IMS registration may become
            # Rejected seconds before swu_ike proves that the ePDG ignored every CHILD_SA rekey.
            # Replace only with a strictly stronger causal observation; recovery's generic
            # "registering" state must never erase the fault that began the outage.
            cause_priority = {
                "": 0, "registering": 5, "tunnel_setup": 10, "reg_temporary": 15,
                "reg_rejected": 20,
                "reg_unanswered": 30, "tunnel_network": 35,
                "tunnel_child_rekey_timeout": 50, "tunnel_ike_rekey_timeout": 50,
                "tunnel_rekey_send_error": 50, "tunnel_sim_auth": 50,
                "tunnel_not_authorized": 50, "tunnel_proposal": 50,
                "reg_reauth_failed": 50, "maintenance_rebuild": 60,
                "client_engine_failure": 60,
            }
            stronger = (reason and cause_priority.get(reason, 25)
                        > cause_priority.get(str(row["reason"] or ""), 25))
            if reason and (not row["reason"] or stronger):
                c.execute("UPDATE line_states SET end_ts=?, reason=?, detail=? WHERE id=?",
                          (ts, reason, detail, row["id"]))
            else:
                c.execute("UPDATE line_states SET end_ts=? WHERE id=?", (ts, row["id"]))
            return
        start = int(row["end_ts"]) if continuous else ts
        c.execute("INSERT INTO line_states(instance,state,start_ts,end_ts,reason,detail) "
                  "VALUES(?,?,?,?,?,?)", (str(instance), state, start, ts, reason, detail))


def line_states(instance: str, since_ts: int) -> list[dict]:
    """Recorded segments of one line that overlap [since_ts, now], oldest first."""
    with _lock, _conn() as c:
        rows = c.execute(
            "SELECT state, start_ts, end_ts, reason, detail FROM line_states "
            "WHERE instance=? AND end_ts>=? "
            "ORDER BY start_ts ASC, id ASC", (str(instance), int(since_ts))).fetchall()
    return [dict(r) for r in rows]


def line_state_timeline(instance: str, start_ts: int, end_ts: int) -> list[dict]:
    """Gap-aware timeline for a window: recorded segments clipped to it, every hole in the
    record reported as `unknown`.

    A hole shorter than the continuity window is sampling jitter and is absorbed by the
    neighbouring segment. A longer one is left as `unknown` on purpose: the control plane
    was not watching, so drawing that period as either healthy or failed would be a claim
    it cannot make.
    """
    start, end = int(start_ts), int(end_ts)
    result: list[dict] = []
    cursor = start
    for row in line_states(instance, start):
        segment_start = max(int(row["start_ts"]), start)
        segment_end = min(int(row["end_ts"]), end)
        # A segment holding a single sample is still zero-length; keep it so the newest
        # state appears immediately instead of after the second sample of that state.
        if segment_end < segment_start:
            continue
        hole = segment_start - cursor
        if hole > 0:
            if result and hole <= LINE_STATE_CONTINUITY_SECONDS:
                result[-1]["end"] = segment_start
            else:
                result.append({"state": "unknown", "start": cursor, "end": segment_start})
        if result and result[-1]["state"] == row["state"] and result[-1]["end"] >= segment_start:
            result[-1]["end"] = max(result[-1]["end"], segment_end)
            if not result[-1].get("reason"):
                # Reason and detail describe the same sample; they move as a pair.
                result[-1]["reason"] = str(row.get("reason") or "")
                result[-1]["detail"] = str(row.get("detail") or "")
        else:
            result.append({"state": str(row["state"]), "start": segment_start,
                           "end": segment_end, "reason": str(row.get("reason") or ""),
                           "detail": str(row.get("detail") or "")})
        cursor = result[-1]["end"]
    if end > cursor:
        if result and end - cursor <= LINE_STATE_CONTINUITY_SECONDS:
            result[-1]["end"] = end
        else:
            result.append({"state": "unknown", "start": cursor, "end": end})
    return result


def line_state_summary(segments: list[dict]) -> dict:
    """Totals for a timeline. Availability counts observed time only, never assumed time."""
    totals = {"up": 0, "down": 0, "off": 0, "unknown": 0}
    outages, longest = 0, 0
    for segment in segments:
        length = max(0, int(segment["end"]) - int(segment["start"]))
        totals[segment["state"]] = totals.get(segment["state"], 0) + length
        if segment["state"] == "down":
            outages += 1
            longest = max(longest, length)
    observed = totals["up"] + totals["down"]
    return {**totals, "observed_seconds": observed, "outages": outages,
            "longest_outage_seconds": longest,
            "uptime_ratio": (totals["up"] / observed) if observed else None}


def line_state_recorded_since(instance: str) -> int | None:
    """Oldest retained observation for this line, or None when nothing was ever recorded."""
    with _lock, _conn() as c:
        row = c.execute("SELECT MIN(start_ts) AS first_ts FROM line_states WHERE instance=?",
                        (str(instance),)).fetchone()
    return int(row["first_ts"]) if row and row["first_ts"] is not None else None


def prune_line_states(before_ts: int) -> int:
    """Drop history that has aged past retention. Returns rows removed."""
    with _lock, _conn() as c:
        cur = c.execute("DELETE FROM line_states WHERE end_ts < ?", (int(before_ts),))
        return cur.rowcount


def clear_line_states(instance: str) -> int:
    """Delete the connectivity timeline of one line. Returns rows removed."""
    with _lock, _conn() as c:
        cur = c.execute("DELETE FROM line_states WHERE instance=?", (str(instance),))
        return cur.rowcount


# --- Soft-deleted instances & restoration ----------------------------------------------------

def soft_delete_instance(instance_id: str, iccid: str = "", imsi: str = "", name: str = "") -> None:
    """Record a soft-deleted SIM line so it can be restored anytime without losing history."""
    ts = int(time.time())
    with _lock, _conn() as c:
        _guard_browser_call_deletion(c, str(instance_id))
        c.execute(
            """
            INSERT OR REPLACE INTO soft_deleted_instances (instance_id, iccid, imsi, name, deleted_ts)
            VALUES (?, ?, ?, ?, ?)
            """,
            (str(instance_id), str(iccid), str(imsi), str(name), ts),
        )


def restore_instance(instance_id: str) -> bool:
    """Remove soft-delete record to restore a SIM line."""
    with _lock, _conn() as c:
        cur = c.execute("DELETE FROM soft_deleted_instances WHERE instance_id=?", (str(instance_id),))
        return cur.rowcount > 0


def is_instance_soft_deleted(instance_id: str) -> bool:
    """Check if an instance is in the soft-deleted trash/recycle bin."""
    with _lock, _conn() as c:
        row = c.execute(
            "SELECT 1 FROM soft_deleted_instances WHERE instance_id=?", (str(instance_id),)
        ).fetchone()
        return bool(row)


def list_soft_deleted_instances() -> list[dict]:
    """List all soft-deleted SIM lines currently in trash."""
    with _lock, _conn() as c:
        rows = c.execute(
            "SELECT instance_id, iccid, imsi, name, deleted_ts FROM soft_deleted_instances ORDER BY deleted_ts DESC"
        ).fetchall()
        return [dict(r) for r in rows]


# --- eSIM SGP.22 Notification Ledger --------------------------------------------------------

def save_esim_notifications(notifications: list[dict], reader_name: str = "", aid: str = "") -> int:
    """Persist eSIM notifications to the durable ledger for auditing and replay."""
    now = int(time.time())
    saved = 0
    with _lock, _conn() as c:
        for n in notifications:
            seq = n.get("seqNumber") if n.get("seqNumber") is not None else n.get("seq")
            if seq is None:
                continue
            op = str(n.get("profileManagementOperation") or n.get("operation") or "unknown")
            iccid = str(n.get("iccid") or "")
            c.execute(
                """
                INSERT INTO esim_notifications (seq_number, operation, iccid, reader_name, aid, status, created_ts)
                VALUES (?, ?, ?, ?, ?, 'pending', ?)
                ON CONFLICT(seq_number, iccid) DO UPDATE SET
                    operation=excluded.operation,
                    reader_name=CASE WHEN excluded.reader_name != '' THEN excluded.reader_name ELSE esim_notifications.reader_name END,
                    aid=CASE WHEN excluded.aid != '' THEN excluded.aid ELSE esim_notifications.aid END
                """,
                (int(seq), op, iccid, str(reader_name), str(aid), now),
            )
            saved += 1
    return saved


def list_esim_notifications(iccid: str | None = None) -> list[dict]:
    """Retrieve eSIM notifications ledger."""
    with _lock, _conn() as c:
        if iccid:
            rows = c.execute(
                "SELECT * FROM esim_notifications WHERE iccid=? ORDER BY seq_number ASC",
                (str(iccid),),
            ).fetchall()
        else:
            rows = c.execute("SELECT * FROM esim_notifications ORDER BY created_ts DESC").fetchall()
        return [dict(r) for r in rows]


def record_notification_replay(seq_number: int, iccid: str = "", success: bool = True, error: str = "") -> None:
    """Update notification replay audit status."""
    now = int(time.time())
    status = "replayed" if success else "failed"
    with _lock, _conn() as c:
        if iccid:
            c.execute(
                """
                UPDATE esim_notifications
                SET status=?, last_replayed_ts=?, replay_count=replay_count+1, last_error=?
                WHERE seq_number=? AND iccid=?
                """,
                (status, now, str(error), int(seq_number), str(iccid)),
            )
        else:
            c.execute(
                """
                UPDATE esim_notifications
                SET status=?, last_replayed_ts=?, replay_count=replay_count+1, last_error=?
                WHERE seq_number=?
                """,
                (status, now, str(error), int(seq_number)),
            )
