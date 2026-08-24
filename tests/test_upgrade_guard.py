import importlib.util
import sqlite3
import sys
from pathlib import Path

import pytest


ROOT = Path(__file__).parents[1]
SPEC = importlib.util.spec_from_file_location(
    "mdd_upgrade_guard", ROOT / "host" / "mdd_upgrade_guard.py")
guard = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = guard
SPEC.loader.exec_module(guard)


class FakeInotify:
    def __init__(self, drains=None):
        self.watches = []
        self.drains = list(drains or [])
        self.closed = False

    def watch(self, path):
        self.watches.append(Path(path))

    def drain(self):
        return self.drains.pop(0) if self.drains else []

    def wait(self, _timeout):
        return False

    def close(self):
        self.closed = True


def factory(backend):
    return lambda: backend


def test_watcher_registers_parent_and_files_before_baseline(tmp_path):
    messages = tmp_path / "messages.txt"
    events = tmp_path / "events.jsonl"
    messages.write_text("old", encoding="utf-8")
    backend = FakeInotify()
    watcher = guard.MessageFileGuard(
        [messages, events], backend_factory=factory(backend))
    watcher.arm()
    assert backend.watches == [tmp_path, messages]
    assert watcher.baseline[messages].exists
    assert not watcher.baseline[events].exists
    watcher.close()
    assert backend.closed


def test_registration_period_event_aborts_before_arming(tmp_path):
    messages = tmp_path / "messages.txt"
    messages.write_text("old", encoding="utf-8")
    backend = FakeInotify(drains=[[], [(1, 2, "messages.txt")]])
    watcher = guard.MessageFileGuard([messages], backend_factory=factory(backend))
    with pytest.raises(guard.UpgradeGuardError, match="while watcher armed"):
        watcher.arm()
    assert backend.closed


@pytest.mark.parametrize("mutation", ["append", "truncate", "rotate", "create"])
def test_poll_secondary_catches_every_evidence_mutation(tmp_path, mutation):
    path = tmp_path / "messages.txt"
    if mutation != "create":
        path.write_text("before", encoding="utf-8")
    backend = FakeInotify()
    watcher = guard.MessageFileGuard([path], backend_factory=factory(backend))
    watcher.arm()
    if mutation == "append":
        with path.open("a", encoding="utf-8") as handle:
            handle.write("after")
    elif mutation == "truncate":
        path.write_text("x", encoding="utf-8")
    elif mutation == "rotate":
        path.rename(tmp_path / "messages.old")
        path.write_text("replacement", encoding="utf-8")
    else:
        path.write_text("new", encoding="utf-8")
    with pytest.raises(guard.UpgradeGuardError, match="during maintenance"):
        watcher.check()
    watcher.close()


def test_backend_event_aborts_even_when_stat_returns_to_baseline(tmp_path):
    path = tmp_path / "messages.txt"
    path.write_text("same", encoding="utf-8")
    backend = FakeInotify(drains=[[], [], [(1, 2, "messages.txt")]])
    watcher = guard.MessageFileGuard([path], backend_factory=factory(backend))
    watcher.arm()
    with pytest.raises(guard.UpgradeGuardError):
        watcher.check()


def _database(path):
    connection = sqlite3.connect(path)
    connection.executescript("""
        CREATE TABLE cellular_call_leases(state TEXT NOT NULL);
        CREATE TABLE messages(direction TEXT NOT NULL,status TEXT NOT NULL);
        CREATE TABLE sms_submission_guards(state TEXT NOT NULL);
        CREATE TABLE allowance_queries(status TEXT NOT NULL);
    """)
    connection.commit()
    connection.close()


def test_filesystem_and_sqlite_probes_commit_readback_and_clean(tmp_path):
    logs = tmp_path / "logs"
    logs.mkdir()
    guard.filesystem_durability_probe(logs, "deploy-20260823-0001")
    assert list(logs.iterdir()) == []

    database = tmp_path / "mdd-sim-gateway.sqlite"
    _database(database)
    guard.sqlite_durability_probe(database, "deploy-20260823-0001")
    connection = sqlite3.connect(database)
    assert connection.execute(
        "SELECT COUNT(*) FROM maintenance_durability_probes").fetchone() == (0,)
    connection.close()


def test_paid_work_gate_counts_open_leases_and_pending_submissions(tmp_path):
    database = tmp_path / "mdd-sim-gateway.sqlite"
    _database(database)
    connection = sqlite3.connect(database)
    connection.executemany("INSERT INTO cellular_call_leases(state) VALUES(?)", [
        ("active",), ("terminal_confirmed",), ("cancelled",)])
    connection.executemany("INSERT INTO messages(direction,status) VALUES(?,?)", [
        ("out", "pending"), ("in", "pending"), ("out", "sent")])
    connection.execute("INSERT INTO sms_submission_guards(state) VALUES('orphaned')")
    connection.execute("INSERT INTO sms_submission_guards(state) VALUES('completed')")
    connection.execute("INSERT INTO sms_submission_guards(state) VALUES('active')")
    connection.executemany("INSERT INTO allowance_queries(status) VALUES(?)", [
        ("pending",), ("sent",)])
    connection.commit()
    connection.close()
    assert guard.pending_paid_work(database) == {
        "open_call_leases": 1, "pending_messages": 2,
        "pending_allowance_queries": 1,
    }


def test_paid_work_gate_rejects_unknown_sms_submission_state(tmp_path):
    database = tmp_path / "mdd-sim-gateway.sqlite"
    _database(database)
    connection = sqlite3.connect(database)
    connection.execute("INSERT INTO sms_submission_guards(state) VALUES('surprise')")
    connection.commit()
    connection.close()
    with pytest.raises(guard.UpgradeGuardError, match="invalid SMS submission state"):
        guard.pending_paid_work(database)


def test_missing_or_wrong_database_fails_closed(tmp_path):
    with pytest.raises(guard.UpgradeGuardError, match="unavailable"):
        guard.sqlite_durability_probe(tmp_path / "missing.sqlite", "txid-0001")
    with pytest.raises(guard.UpgradeGuardError, match="unknown"):
        guard.pending_paid_work(tmp_path / "missing.sqlite")
