import os
import pytest
from fastapi.testclient import TestClient
from unittest.mock import patch

from control.app import config as cfg
from control.app import store
from control.app.main import app


@pytest.fixture(autouse=True)
def isolated_env(tmp_path, monkeypatch):
    data_dir = str(tmp_path / "data")
    os.makedirs(data_dir, exist_ok=True)
    monkeypatch.setattr(cfg, "DATA_DIR", data_dir)
    monkeypatch.setattr(cfg, "CONFIG_PATH", os.path.join(data_dir, "config.yaml"))
    monkeypatch.setattr(store, "DATA_DIR", data_dir)
    monkeypatch.setattr(store, "DB_PATH", os.path.join(data_dir, "mdd-sim-gateway.sqlite"))
    store.init()
    cfg.save({
        "internal": {},
        "settings": cfg.DEFAULTS["settings"],
        "instances": {},
    })


def test_soft_delete_and_restore_config_and_store():
    # 1. Create instance
    inst = cfg.upsert_instance({
        "id": "line-1",
        "imsi": "234101234567890",
        "iccid": "89441012345678901234",
        "name": "Test-SIM-1",
        "mcc": "234",
        "mnc": "10",
    })
    assert inst["id"] == "line-1"

    # Add messages & call history to store
    store.add_message("line-1", "in", "+447000000000", "Hello before delete")
    historical_call = store.add_call("line-1", "in", "+447000000000", "answered")
    store.update_call(historical_call["id"], "answered", ended=True)


    # Initial active listing
    active = cfg.list_instances(include_deleted=False)
    assert len(active) == 1
    assert active[0]["id"] == "line-1"

    # 2. Soft-delete instance
    assert cfg.soft_delete_instance("line-1") is True
    store.soft_delete_instance("line-1", iccid=inst["iccid"], imsi=inst["imsi"], name=inst["name"])

    # Active listing must be 0
    active_after = cfg.list_instances(include_deleted=False)
    assert len(active_after) == 0

    # Soft-deleted listing must have line-1
    trash = cfg.list_soft_deleted_instances()
    assert len(trash) == 1
    assert trash[0]["id"] == "line-1"
    assert trash[0]["soft_deleted"] is True

    trash_store = store.list_soft_deleted_instances()
    assert len(trash_store) == 1
    assert trash_store[0]["instance_id"] == "line-1"

    # Messages and call history must remain completely intact!
    threads = store.list_threads("line-1")
    assert len(threads) == 1
    assert threads[0]["last_body"] == "Hello before delete"

    calls = store.list_calls("line-1")
    assert len(calls) == 1
    assert calls[0]["peer"] == "+447000000000"



    # 3. Restore instance
    assert cfg.restore_instance("line-1") is True
    assert store.restore_instance("line-1") is True

    # Active listing is restored
    active_restored = cfg.list_instances(include_deleted=False)
    assert len(active_restored) == 1
    assert active_restored[0]["id"] == "line-1"
    assert "soft_deleted" not in active_restored[0]

    # Trash is now empty
    assert len(cfg.list_soft_deleted_instances()) == 0
    assert len(store.list_soft_deleted_instances()) == 0


def test_api_soft_delete_and_restore_endpoints():
    cfg.upsert_instance({
        "id": "line-2",
        "imsi": "234201234567890",
        "iccid": "89442012345678901234",
        "name": "Test-SIM-2",
    })

    with patch("control.app.auth.session", return_value={"user": "admin", "csrf": "test-csrf"}), \
         patch("control.app.engine.stop", return_value=None), \
         patch("control.app.engine.is_running", return_value=False):
        client = TestClient(app, cookies={"mdd_session": "test-session"})

        # Get active instances
        resp = client.get("/api/instances")
        assert resp.status_code == 200
        assert len(resp.json()["instances"]) == 1

        # Soft delete line-2
        resp = client.post(
            "/api/instances/line-2/soft-delete",
            headers={"x-mdd-csrf-token": "test-csrf"}
        )
        assert resp.status_code == 200
        assert resp.json()["soft_deleted"] is True

        # Active list is now empty
        resp = client.get("/api/instances")
        assert len(resp.json()["instances"]) == 0

        # Trash list contains line-2
        resp = client.get("/api/instances/soft-deleted")
        assert resp.status_code == 200
        assert len(resp.json()["instances"]) == 1
        assert resp.json()["instances"][0]["id"] == "line-2"

        # Restore line-2
        resp = client.post(
            "/api/instances/line-2/restore",
            headers={"x-mdd-csrf-token": "test-csrf"}
        )
        assert resp.status_code == 200
        assert resp.json()["restored"] is True

        # Active list has line-2 again
        resp = client.get("/api/instances")
        assert len(resp.json()["instances"]) == 1
        assert resp.json()["instances"][0]["id"] == "line-2"
