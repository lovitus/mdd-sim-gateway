import sqlite3
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from control.app import store


class StoreMigrationTests(unittest.TestCase):
    def test_prepared_call_cancellation_is_a_compare_and_swap_not_an_upsert(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "gateway.sqlite"
            with patch.multiple(store, DATA_DIR=temp, DB_PATH=str(path),
                                PREVIOUS_DB_PATH=str(Path(temp) / "previous.sqlite")):
                store.init()
                self.assertFalse(store.cancel_prepared_cellular_call_lease("missing"))
                for state in ("prepared", "signalling", "active", "cancelled", "terminal_confirmed"):
                    with self.subTest(state=state):
                        call_id = "cas-" + state
                        store.save_cellular_call_lease(call_id, "5", "sim", "in", "prepared")
                        before = store.save_cellular_call_lease(call_id, "5", "sim", "in", state)
                        changed = store.cancel_prepared_cellular_call_lease(call_id)
                        with sqlite3.connect(path) as connection:
                            connection.row_factory = sqlite3.Row
                            after = dict(connection.execute(
                                "SELECT * FROM cellular_call_leases WHERE call_id=?",
                                (call_id,)).fetchone())
                        self.assertEqual(changed, state == "prepared")
                        if changed:
                            self.assertEqual(after["state"], "cancelled")
                            self.assertIsNotNone(after["terminal_ts"])
                        else:
                            self.assertEqual(after, before)

    def test_paid_call_lease_survives_restart_until_terminal_confirmation(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            current = root / "mdd-sim-gateway.sqlite"
            with patch.multiple(store, DATA_DIR=str(root), DB_PATH=str(current),
                                PREVIOUS_DB_PATH=str(root / "vowifi.sqlite")):
                store.init()
                store.save_cellular_call_lease(
                    "call-12345678", "5", "89852312388530152529", "out", "signalling")
                store.init()  # simulated gateway restart
                unresolved = store.open_cellular_call_lease("89852312388530152529")
                self.assertEqual(unresolved["call_id"], "call-12345678")
                store.save_cellular_call_lease(
                    "call-12345678", "5", "89852312388530152529", "out",
                    "terminal_confirmed")
                self.assertIsNone(store.open_cellular_call_lease("89852312388530152529"))

    def test_local_modem_sms_tracking_schema_is_upgraded_in_place(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            current = root / "mdd-sim-gateway.sqlite"
            with sqlite3.connect(current) as connection:
                connection.execute("""
                    CREATE TABLE local_modem_sms (
                        id INTEGER PRIMARY KEY AUTOINCREMENT,
                        instance TEXT NOT NULL, iccid TEXT NOT NULL,
                        modem_path TEXT, sms_path TEXT, content_hash TEXT NOT NULL,
                        created_ts INTEGER NOT NULL, bound_ts INTEGER)
                """)
                connection.execute(
                    "CREATE UNIQUE INDEX idx_local_modem_sms_path "
                    "ON local_modem_sms(iccid,sms_path) WHERE sms_path IS NOT NULL")

            with patch.multiple(store, DATA_DIR=str(root), DB_PATH=str(current),
                                PREVIOUS_DB_PATH=str(root / "vowifi.sqlite")):
                store.init()

            with sqlite3.connect(current) as connection:
                columns = {row[1] for row in connection.execute(
                    "PRAGMA table_info(local_modem_sms)")}
                index_sql = connection.execute(
                    "SELECT sql FROM sqlite_master WHERE name='idx_local_modem_sms_path'"
                ).fetchone()[0]
            self.assertIn("daemon_epoch", columns)
            self.assertIn("message_id", columns)
            self.assertIn("cancelled", columns)
            self.assertIn("daemon_epoch", index_sql)

    def test_previous_database_is_copied_once_and_preserved(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            previous = root / "vowifi.sqlite"
            current = root / "mdd-sim-gateway.sqlite"
            with sqlite3.connect(previous) as connection:
                connection.execute("CREATE TABLE marker (value TEXT)")
                connection.execute("INSERT INTO marker VALUES ('kept')")
            with patch.multiple(store, DATA_DIR=str(root), DB_PATH=str(current),
                                PREVIOUS_DB_PATH=str(previous)):
                store.init()
            self.assertTrue(previous.exists())
            with sqlite3.connect(current) as connection:
                self.assertEqual(connection.execute("SELECT value FROM marker").fetchone()[0],
                                 "kept")

    def test_existing_database_merges_named_legacy_history_once(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            current = root / "mdd-sim-gateway.sqlite"
            previous = root / "vowifi.sqlite"
            with sqlite3.connect(previous) as db:
                db.executescript("""
                    CREATE TABLE calls (id INTEGER PRIMARY KEY, instance TEXT, direction TEXT,
                        peer TEXT, status TEXT, start_ts INTEGER, end_ts INTEGER);
                    CREATE TABLE messages (id INTEGER PRIMARY KEY, instance TEXT, direction TEXT,
                        peer TEXT, body TEXT, status TEXT, ts INTEGER);
                    INSERT INTO calls VALUES(7,'giff','out','service','ended',100,101);
                    INSERT INTO messages VALUES(9,'giff','in','service','hello','ok',102);
                """)
            # Make the current DB exist first: this is the upgrade case the old copy-only
            # migration skipped.
            current.touch()
            with patch.multiple(store, DATA_DIR=str(root), DB_PATH=str(current),
                                PREVIOUS_DB_PATH=str(previous)):
                store.init()
                first = store.migrate_legacy_history({"giff": "3"})
                second = store.migrate_legacy_history({"giff": "3"})

                self.assertEqual(first, {"calls": 1, "messages": 1})
                self.assertEqual(second, {"calls": 0, "messages": 0})
                self.assertEqual(len(store.list_calls("3")), 1)
                self.assertEqual(store.list_threads("3")[0]["n"], 1)

    def test_correlated_call_events_converge_when_result_arrives_before_start(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            current = root / "mdd-sim-gateway.sqlite"
            with patch.multiple(store, DATA_DIR=str(root), DB_PATH=str(current),
                                PREVIOUS_DB_PATH=str(root / "vowifi.sqlite")):
                store.init()
                terminal, created = store.record_call_result(
                    "7", "in", "+44123", "missed", "run-a:linked-1")
                late, late_created = store.record_call_start(
                    "7", "in", "+44123", "ringing", "run-a:linked-1")
                duplicate, duplicate_created = store.record_call_result(
                    "7", "in", "+44123", "missed", "run-a:linked-1")

                self.assertTrue(created)
                self.assertFalse(late_created)
                self.assertFalse(duplicate_created)
                self.assertEqual(terminal["id"], late["id"])
                self.assertEqual(late["id"], duplicate["id"])
                self.assertEqual(late["status"], "missed")
                self.assertIsNotNone(late["end_ts"])
                self.assertEqual(len(store.list_calls("7")), 1)


if __name__ == "__main__":
    unittest.main()
