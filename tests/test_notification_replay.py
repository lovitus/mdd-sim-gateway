import os
import pytest
from fastapi.testclient import TestClient
from unittest.mock import patch, AsyncMock

from control.app import store
from control.app.main import app


@pytest.fixture(autouse=True)
def isolated_env(tmp_path, monkeypatch):
    data_dir = str(tmp_path / "data")
    os.makedirs(data_dir, exist_ok=True)
    monkeypatch.setattr(store, "DATA_DIR", data_dir)
    monkeypatch.setattr(store, "DB_PATH", os.path.join(data_dir, "mdd-sim-gateway.sqlite"))
    store.init()


def test_esim_notifications_store():
    # 1. Save notifications
    notes = [
        {"seqNumber": 1, "profileManagementOperation": "install", "iccid": "89000000000000000001"},
        {"seqNumber": 2, "profileManagementOperation": "delete", "iccid": "89000000000000000002"},
    ]
    saved = store.save_esim_notifications(notes, reader_name="reader0")
    assert saved == 2

    # 2. List notifications
    all_notes = store.list_esim_notifications()
    assert len(all_notes) == 2

    # 3. Record replay
    store.record_notification_replay(2, iccid="89000000000000000002", success=True)
    note2 = [n for n in store.list_esim_notifications() if n["seq_number"] == 2][0]
    assert note2["status"] == "replayed"
    assert note2["replay_count"] == 1


def test_api_notification_replay_requires_confirmation():
    with patch("control.app.auth.session", return_value={"user": "admin", "csrf": "test-csrf"}):
        client = TestClient(app, cookies={"mdd_session": "test-session"})

        # Without confirmation -> must fail with 400
        resp = client.post(
            "/api/esim/notifications/replay",
            json={"seq": 1, "confirmed": False},
            headers={"x-mdd-csrf-token": "test-csrf"}
        )
        assert resp.status_code == 400
        detail = resp.json().get("detail", {})
        assert detail.get("code") == "confirmation_required"

        # With confirmation -> calls notification_replay
        with patch("control.app.lpa.notification_replay", new_callable=AsyncMock) as mock_replay, \
             patch("control.app.main._esim_resolve_reader", return_value=("reader0", 0)):
            mock_replay.return_value = {"code": 0, "message": "success"}

            resp = client.post(
                "/api/esim/notifications/replay",
                json={
                    "seq": 2,
                    "iccid": "89000000000000000002",
                    "confirmed": True,
                },
                headers={"x-mdd-csrf-token": "test-csrf"}
            )
            assert resp.status_code == 200
            assert resp.json()["ok"] is True
            assert resp.json()["replayed"] is True
            assert resp.json()["seq"] == 2
