import sqlite3
import asyncio
from contextlib import nullcontext
import threading
from unittest.mock import AsyncMock, patch

import pytest

from control.app import store
from control.app import main


@pytest.fixture
def browser_store(tmp_path):
    database = tmp_path / "mdd-sim-gateway.sqlite"
    with patch.multiple(
            store, DATA_DIR=str(tmp_path), DB_PATH=str(database),
            PREVIOUS_DB_PATH=str(tmp_path / "old.sqlite")):
        store.init()
        yield database


def start_call():
    return store.record_call_start(
        "7", "in", "+447700900123", "ringing",
        "run-7:171.7", engine_run_id="run-7")[0]


def test_legacy_open_incoming_migrates_to_unknown_in_one_schema_transaction(tmp_path):
    database = tmp_path / "mdd-sim-gateway.sqlite"
    with sqlite3.connect(database) as connection:
        connection.execute(
            "CREATE TABLE calls (id INTEGER PRIMARY KEY AUTOINCREMENT,instance TEXT NOT NULL,"
            "direction TEXT NOT NULL,peer TEXT NOT NULL,status TEXT,start_ts INTEGER NOT NULL,"
            "end_ts INTEGER,transport TEXT DEFAULT 'vowifi',source_call_id TEXT NOT NULL "
            "DEFAULT '',engine_run_id TEXT NOT NULL DEFAULT '')")
        connection.execute(
            "INSERT INTO calls(instance,direction,peer,status,start_ts,transport,"
            "source_call_id,engine_run_id) VALUES('7','in','peer','ringing',1,'vowifi',"
            "'run-7:171.7','run-7')")
        connection.execute(
            "INSERT INTO calls(instance,direction,peer,status,start_ts,end_ts,transport) "
            "VALUES('7','in','old','missed',1,2,'vowifi')")
    with patch.multiple(
            store, DATA_DIR=str(tmp_path), DB_PATH=str(database),
            PREVIOUS_DB_PATH=str(tmp_path / "old.sqlite")):
        store.init()
    with sqlite3.connect(database) as connection:
        rows = connection.execute(
            "SELECT browser_state,browser_revision,end_ts FROM calls ORDER BY id").fetchall()
    assert rows == [("unknown", 1, None), (None, None, 2)]


def test_browser_schema_alter_failure_rolls_back_all_new_columns(tmp_path):
    database = tmp_path / "mdd-sim-gateway.sqlite"
    with sqlite3.connect(database) as connection:
        connection.execute(
            "CREATE TABLE calls (id INTEGER PRIMARY KEY AUTOINCREMENT,instance TEXT NOT NULL,"
            "direction TEXT NOT NULL,peer TEXT NOT NULL,status TEXT,start_ts INTEGER NOT NULL,"
            "end_ts INTEGER,transport TEXT DEFAULT 'vowifi',source_call_id TEXT NOT NULL "
            "DEFAULT '',engine_run_id TEXT NOT NULL DEFAULT '')")
        connection.execute(
            "INSERT INTO calls(instance,direction,peer,status,start_ts,transport) "
            "VALUES('7','in','peer','ringing',1,'vowifi')")

    class FailingConnection:
        def __init__(self):
            self.connection = sqlite3.connect(database)
            self.alters = 0

        def __enter__(self):
            return self

        def __exit__(self, kind, value, traceback):
            if kind:
                self.connection.rollback()
            else:
                self.connection.commit()
            self.connection.close()

        def executescript(self, value):
            return self.connection.executescript(value)

        def execute(self, value, parameters=()):
            if value.startswith("ALTER TABLE calls ADD COLUMN browser_"):
                self.alters += 1
                if self.alters == 3:
                    raise sqlite3.OperationalError("injected browser schema failure")
            return self.connection.execute(value, parameters)

    with patch.multiple(
            store, DATA_DIR=str(tmp_path), DB_PATH=str(database),
            PREVIOUS_DB_PATH=str(tmp_path / "old.sqlite")), \
            patch.object(store, "_conn", side_effect=lambda: FailingConnection()), \
            pytest.raises(sqlite3.OperationalError, match="injected"):
        store.init()
    with sqlite3.connect(database) as connection:
        columns = {row[1] for row in connection.execute("PRAGMA table_info(calls)")}
        row = connection.execute("SELECT peer,end_ts FROM calls").fetchone()
    assert not {"browser_state", "browser_revision"} & columns
    assert row == ("peer", None)


@pytest.mark.parametrize("state", sorted(store.BROWSER_CALL_STATES))
def test_duplicate_call_in_never_resets_browser_state_or_owner(browser_store, state):
    row = start_call()
    owner_state = state in {
        "claiming", "attach_submitted_unknown", "answer_submitted_unknown", "active",
    }
    owner = "owner_session_1234" if owner_state else ""
    operation = "a" * 32 if owner_state else ""
    epoch = "B" * 24 if owner_state else ""
    end_ts = 99 if state == "terminal" else None
    with sqlite3.connect(browser_store) as connection:
        connection.execute(
            "UPDATE calls SET browser_state=?,browser_revision=7,browser_owner_session=?,"
            "browser_operation=?,browser_epoch=?,end_ts=? WHERE id=?",
            (state, owner, operation, epoch, end_ts, row["id"]))
    duplicate, created = store.record_call_start(
        "7", "in", "+447700900123", "ringing", "run-7:171.7",
        engine_run_id="run-7")
    assert created is False
    assert duplicate["browser_state"] == state
    assert duplicate["browser_revision"] == 7
    assert duplicate["browser_owner_session"] == owner
    assert duplicate["end_ts"] == end_ts


def test_exact_cas_has_one_winner_and_never_releases_unknown(browser_store):
    row = start_call()
    won = store.transition_browser_call(
        "7", row["id"], row["source_call_id"], row["engine_run_id"],
        expected_state="ringing", expected_revision=0, new_state="claiming",
        owner_session="owner_session_1234", operation="a" * 32, epoch="B" * 24)
    assert won["browser_state"] == "claiming" and won["browser_revision"] == 1
    assert store.transition_browser_call(
        "7", row["id"], row["source_call_id"], row["engine_run_id"],
        expected_state="ringing", expected_revision=0, new_state="claiming",
        owner_session="other_session_123", operation="b" * 32,
        epoch="C" * 24) is None
    attached = store.transition_browser_call(
        "7", row["id"], row["source_call_id"], row["engine_run_id"],
        expected_state="claiming", expected_revision=1,
        expected_owner="owner_session_1234", new_state="attach_submitted_unknown")
    assert attached["browser_revision"] == 2
    ending = store.transition_browser_call(
        "7", row["id"], row["source_call_id"], row["engine_run_id"],
        expected_state="attach_submitted_unknown", expected_revision=2,
        expected_owner="owner_session_1234", new_state="ending")
    assert ending["browser_state"] == "ending"
    with pytest.raises(ValueError):
        store.transition_browser_call(
            "7", row["id"], row["source_call_id"], row["engine_run_id"],
            expected_state="ending", expected_revision=3, new_state="ringing")


def test_call_result_is_terminal_once_and_wins_over_delayed_owner(browser_store):
    row = start_call()
    claimed = store.transition_browser_call(
        "7", row["id"], row["source_call_id"], row["engine_run_id"],
        expected_state="ringing", expected_revision=0, new_state="claiming",
        owner_session="owner_session_1234", operation="a" * 32, epoch="B" * 24)
    terminal, _ = store.record_call_result(
        "7", "in", row["peer"], "answered", row["source_call_id"],
        engine_run_id=row["engine_run_id"])
    assert terminal["browser_state"] == "terminal"
    assert terminal["browser_revision"] == claimed["browser_revision"] + 1
    duplicate, _ = store.record_call_result(
        "7", "in", row["peer"], "answered", row["source_call_id"],
        engine_run_id=row["engine_run_id"])
    assert duplicate["browser_revision"] == terminal["browser_revision"]
    assert store.transition_browser_call(
        "7", row["id"], row["source_call_id"], row["engine_run_id"],
        expected_state="claiming", expected_revision=1,
        expected_owner="owner_session_1234",
        new_state="attach_submitted_unknown") is None
    assert store.terminal_browser_call_exact(
        "7", row["id"], row["source_call_id"], row["engine_run_id"],
        expected_state="claiming", expected_revision=1,
        expected_owner="owner_session_1234") is None


def test_absence_terminal_cas_requires_exact_revision_and_owner(browser_store):
    row = start_call()
    claimed = store.transition_browser_call(
        "7", row["id"], row["source_call_id"], row["engine_run_id"],
        expected_state="ringing", expected_revision=0, new_state="claiming",
        owner_session="owner_session_1234", operation="a" * 32, epoch="B" * 24)
    assert store.terminal_browser_call_exact(
        "7", row["id"], row["source_call_id"], row["engine_run_id"],
        expected_state="claiming", expected_revision=claimed["browser_revision"],
        expected_owner="wrong_owner_1234") is None
    terminal = store.terminal_browser_call_exact(
        "7", row["id"], row["source_call_id"], row["engine_run_id"],
        expected_state="claiming", expected_revision=claimed["browser_revision"],
        expected_owner="owner_session_1234", status="ended")
    assert terminal["browser_state"] == "terminal" and terminal["end_ts"] is not None


def test_existing_exact_finalize_path_is_browser_aware_and_idempotent(browser_store):
    row = start_call()
    terminal = store.finalize_exact_call(
        "7", row["id"], row["source_call_id"], row["engine_run_id"], "ended")
    assert terminal["browser_state"] == "terminal"
    assert terminal["browser_revision"] == 1
    assert store.browser_call_paid_work_count() == 0
    duplicate = store.finalize_exact_call(
        "7", row["id"], row["source_call_id"], row["engine_run_id"], "ended")
    assert duplicate["browser_revision"] == 1
    assert store.finalize_exact_call(
        "7", row["id"], row["source_call_id"], "other-run", "ended") is None


def test_delete_and_instance_fence_are_transactionally_fail_closed(browser_store):
    row = start_call()
    with pytest.raises(store.BrowserCallConflict):
        store.delete_calls("7", [row["id"]])
    with pytest.raises(store.BrowserCallConflict):
        store.clear_calls("7")
    with pytest.raises(store.BrowserCallConflict):
        store.soft_delete_instance("7")
    with pytest.raises(store.BrowserCallConflict):
        store.begin_instance_call_fence("7")

    store.record_call_result(
        "7", "in", row["peer"], "missed", row["source_call_id"],
        engine_run_id=row["engine_run_id"])
    store.begin_instance_call_fence("7")
    with pytest.raises(store.BrowserCallConflict):
        store.record_call_start(
            "7", "in", "peer", "ringing", "run-7:other",
            engine_run_id="run-7")
    store.end_instance_call_fence("7")
    assert store.clear_calls("7") == 1
    store.soft_delete_instance("7")
    with pytest.raises(store.BrowserCallConflict):
        store.record_call_start(
            "7", "in", "peer", "ringing", "run-7:late",
            engine_run_id="run-7")


def test_corrupt_browser_state_never_counts_as_safe(browser_store):
    row = start_call()
    with sqlite3.connect(browser_store) as connection:
        connection.execute(
            "UPDATE calls SET browser_state='active',browser_revision=1 WHERE id=?",
            (row["id"],))
    with pytest.raises(store.BrowserCallConflict, match="owner is missing"):
        store.browser_call_paid_work_count()


@pytest.mark.asyncio
async def test_api_delete_paths_map_store_guard_to_409_before_side_effects():
    conflict = store.BrowserCallConflict("incoming call is not terminal")
    with patch.object(main.store, "clear_calls", side_effect=conflict), \
            pytest.raises(main.HTTPException) as calls:
        await main.api_calls_delete("7", {"all": True})
    assert calls.value.status_code == 409

    stop = AsyncMock()
    with patch.object(main.cfg, "get_instance", return_value={"id": "7"}), \
            patch.object(main.store, "begin_instance_call_fence", side_effect=conflict), \
            patch.object(main.engine, "stop", stop), \
            pytest.raises(main.HTTPException) as soft:
        await main.api_instance_soft_delete("7")
    assert soft.value.status_code == 409
    stop.assert_not_awaited()

    with patch.object(main.cfg, "get_instance", return_value={"id": "7", "iccid": ""}), \
            patch.object(main.cfg, "list_instances", return_value=[]), \
            patch.object(main.hub, "cards_list", return_value=[]), \
            patch.object(main, "_normal_delete_permit_or_http",
                         return_value=nullcontext(object())), \
            patch.object(main.store, "begin_instance_call_fence", side_effect=conflict), \
            patch.object(main.engine, "stop") as hard_stop, \
            pytest.raises(main.HTTPException) as hard:
        await main.api_instance_delete("7", confirm_id="7")
    assert hard.value.status_code == 409
    hard_stop.assert_not_called()


@pytest.mark.asyncio
async def test_cancelled_request_cannot_orphan_instance_call_fence():
    began = threading.Event()
    release_begin = threading.Event()
    operation_ran = asyncio.Event()
    ended = []

    def begin(_iid):
        began.set()
        assert release_begin.wait(2)

    def end(iid):
        ended.append(iid)

    async def operation():
        operation_ran.set()
        return "done"

    with patch.object(main.store, "begin_instance_call_fence", side_effect=begin), \
            patch.object(main.store, "end_instance_call_fence", side_effect=end):
        request = asyncio.create_task(
            main._shielded_instance_call_fenced("7", operation))
        assert await asyncio.to_thread(began.wait, 1)
        request.cancel()
        release_begin.set()
        with pytest.raises(asyncio.CancelledError):
            await request
        await asyncio.wait_for(operation_ran.wait(), 1)
        deadline = asyncio.get_running_loop().time() + 1
        while main._instance_delete_tasks and asyncio.get_running_loop().time() < deadline:
            await asyncio.sleep(0.01)
    assert ended == ["7"]
    assert not main._instance_delete_tasks


@pytest.mark.asyncio
async def test_shutdown_waits_for_executor_delete_before_clearing_fence():
    worker_started = threading.Event()
    worker_release = threading.Event()
    ended = []

    def blocking_worker():
        worker_started.set()
        assert worker_release.wait(2)

    async def operation():
        await asyncio.to_thread(blocking_worker)

    with patch.object(main.store, "begin_instance_call_fence"), \
            patch.object(main.store, "end_instance_call_fence",
                         side_effect=lambda iid: ended.append(iid)):
        request = asyncio.create_task(
            main._shielded_instance_call_fenced("7", operation))
        assert await asyncio.to_thread(worker_started.wait, 1)
        request.cancel()
        with pytest.raises(asyncio.CancelledError):
            await request
        shutdown = asyncio.create_task(main._shutdown_instance_delete_tasks())
        await asyncio.sleep(0.05)
        assert not shutdown.done()
        assert ended == []
        worker_release.set()
        await asyncio.wait_for(shutdown, 1)
    assert ended == ["7"]
    assert not main._instance_delete_tasks
