import json

from control.app import engine


def write_bare_fence(run, *, engine_run_id="run-old", auth_seq=2,
                     cause_class="pcsc_card_reset", created_at=999.0):
    run.mkdir(parents=True, exist_ok=True)
    (run / "usim-auth-recovery.fence").write_text(json.dumps({
        "version": 1, "engine_run_id": engine_run_id, "auth_seq": auth_seq,
        "cause_class": cause_class, "created_at": created_at,
    }), encoding="utf-8")


def write_current_generation(run, *, run_id="run-new"):
    run.mkdir(parents=True, exist_ok=True)
    (run / "engine-run-id").write_text(run_id, encoding="utf-8")


TXID = "usim-reconcile-1787810000-abcdef012345"


def test_no_fence_is_a_noop(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    result = engine.reconcile_orphaned_usim_recovery_fence("1", txid=TXID)
    assert result == {"status": "no_fence"}


def test_same_generation_is_left_to_the_normal_reconciler(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_bare_fence(run, engine_run_id="run-current")
    write_current_generation(run, run_id="run-current")

    result = engine.reconcile_orphaned_usim_recovery_fence("1", txid=TXID)
    assert result == {"status": "same_generation"}
    assert (run / "usim-auth-recovery.fence").exists()


def test_claimed_campaign_record_is_left_alone(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_bare_fence(run, engine_run_id="run-old")
    write_current_generation(run, run_id="run-new")
    (run / "usim-auth-recovery.json").write_text(json.dumps({
        "version": 1, "instance": "1", "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00.000000000Z", "engine_run_id": "run-old",
        "auth_seq": 2, "cause_class": "pcsc_card_reset", "topology_digest": "b" * 64,
        "phase": "pending", "attempts": 0, "next_attempt_at": 1.0,
        "updated_at": 1.0, "submitted_at": 0.0, "result_class": "",
    }), encoding="utf-8")

    result = engine.reconcile_orphaned_usim_recovery_fence("1", txid=TXID)
    assert result == {"status": "campaign_owns_fence"}
    assert (run / "usim-auth-recovery.fence").exists()


def test_permit_debris_is_left_alone_even_without_a_record(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_bare_fence(run, engine_run_id="run-old")
    write_current_generation(run, run_id="run-new")
    (run / "usim-registration-permit.json").write_text("{}", encoding="utf-8")

    result = engine.reconcile_orphaned_usim_recovery_fence("1", txid=TXID)
    assert result == {"status": "campaign_owns_fence"}
    assert (run / "usim-auth-recovery.fence").exists()


def test_current_generation_unknown_leaves_the_fence_in_place(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_bare_fence(run, engine_run_id="run-old")

    result = engine.reconcile_orphaned_usim_recovery_fence("1", txid=TXID)
    assert result == {"status": "current_generation_unknown"}
    assert (run / "usim-auth-recovery.fence").exists()


def test_unhealthy_current_generation_leaves_the_fence_in_place(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_bare_fence(run, engine_run_id="run-old")
    write_current_generation(run, run_id="run-new")

    result = engine.reconcile_orphaned_usim_recovery_fence(
        "1", txid=TXID, transport_ready_fn=lambda iid, run_id: False)
    assert result == {"status": "unhealthy", "reason": "transport_not_ready"}
    assert (run / "usim-auth-recovery.fence").exists()


def test_archives_and_removes_an_orphaned_fence_behind_a_ready_new_generation(
        tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_bare_fence(run, engine_run_id="run-old", auth_seq=2)
    write_current_generation(run, run_id="run-new")

    seen_run_id = []
    result = engine.reconcile_orphaned_usim_recovery_fence(
        "1", txid=TXID,
        transport_ready_fn=lambda iid, run_id: seen_run_id.append(run_id) or True)
    assert seen_run_id == ["run-new"]
    assert result["status"] == "archived" and result["terminal"] is True
    assert result["stale_engine_run_id"] == "run-old"
    assert result["current_engine_run_id"] == "run-new"
    assert not (run / "usim-auth-recovery.fence").exists()

    manifest_path = (tmp_path / "orchestrator" / "usim-recovery-orphaned-fence-archive"
                     / f"1-{TXID}.json")
    manifest = json.loads(manifest_path.read_text())
    assert manifest["fence"]["engine_run_id"] == "run-old"
    assert manifest["artifacts"] == result["artifacts"]


def test_archive_is_idempotent_for_the_same_txid(tmp_path, monkeypatch):
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_bare_fence(run, engine_run_id="run-old", auth_seq=2, created_at=999.0)
    write_current_generation(run, run_id="run-new")
    stale_fence = json.loads((run / "usim-auth-recovery.fence").read_text())

    first = engine.reconcile_orphaned_usim_recovery_fence(
        "1", txid=TXID, transport_ready_fn=lambda iid, run_id: True)
    # The fence file is already gone -- a real replay must not need to re-read it.
    second_digests = engine._archive_orphaned_usim_recovery_fence_unlocked(
        "1", fence=stale_fence, current_engine_run_id="run-new", txid=TXID)
    assert second_digests == first["artifacts"]


def test_a_fresh_fence_written_for_the_current_generation_blocks_deletion(
        tmp_path, monkeypatch):
    """Guards the cross-process race: the Engine (not this Control process) may overwrite
    the bare fence file directly, without this module's own lock, at any time. If that
    happens between the initial read and the final commit, the now-current fence for a
    genuinely new outage must never be silently destroyed."""
    monkeypatch.setattr(engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"
    write_bare_fence(run, engine_run_id="run-old", auth_seq=2, created_at=999.0)
    write_current_generation(run, run_id="run-new")
    stale_fence = json.loads((run / "usim-auth-recovery.fence").read_text())

    def rewrite_and_approve(iid, run_id):
        write_bare_fence(run, engine_run_id="run-new", auth_seq=9, created_at=5000.0)
        return True

    result = engine.reconcile_orphaned_usim_recovery_fence(
        "1", txid=TXID, transport_ready_fn=rewrite_and_approve)
    assert result["status"] == "fence_changed"
    current = json.loads((run / "usim-auth-recovery.fence").read_text())
    assert current["engine_run_id"] == "run-new"
    assert current != stale_fence
