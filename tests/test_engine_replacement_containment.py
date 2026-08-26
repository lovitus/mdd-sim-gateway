from contextlib import contextmanager
import inspect
import json
import time
from types import SimpleNamespace

import pytest

from host.mdd_engine_replacement import EngineReplacement
from control.app import engine as product_engine


MARKER = {
    "phase": "source_quiescing",
    "source": {"run_id": "run-current", "container_id": "a" * 64},
}


def replacement(*, fenced=True, paid=None, channels=0, status=None, recovery=None):
    events = []
    paid = (paid if paid is not None else {
        "open_call_leases": 0, "pending_messages": 0, "pending_allowance_queries": 0})
    status = status or {
        "state": "AUTH_UNAVAILABLE", "engine_run_id": "run-current",
        "cause_class": "pcsc_service_unavailable", "auth_seq": 7}
    recovery = recovery or {
        "version": 2, "phase": "exhausted", "engine_run_id": "run-current",
        "auth_seq_baseline": 7}

    @contextmanager
    def maintenance(_iid):
        events.append("engine-maint-enter")
        try: yield
        finally: events.append("engine-maint-exit")

    @contextmanager
    def boundary(_iid, *, publish_fence, pending_paid, zero_channels,
                 expected_recovery_identity):
        events.append("boundary-enter")
        assert expected_recovery_identity == {
            "engine_run_id": recovery["engine_run_id"],
            "auth_seq_baseline": recovery.get("auth_seq_baseline", recovery.get("auth_seq")),
            "campaign_epoch": ""}
        if publish_fence() is not True:
            raise RuntimeError("fence changed")
        values = pending_paid(); events.append(("paid", values))
        if set(values) != {"open_call_leases", "pending_messages", "pending_allowance_queries"} \
                or any(type(value) is not int or value != 0 for value in values.values()):
            raise RuntimeError("paid unknown")
        zero = zero_channels(); events.append(("zero", zero))
        if zero is not True:
            raise RuntimeError("channels unknown")
        try: yield
        finally: events.append("boundary-exit")

    engine = SimpleNamespace(
        usim_recovery_fence_pending=lambda _iid: fenced,
        read_engine_maintenance=lambda _iid: MARKER,
        read_run_json=lambda _iid, _name: status,
        read_usim_recovery=lambda _iid: recovery,
        engine_maintenance_locked=maintenance,
        usim_recovery_containment_boundary=boundary,
        active_channel_count=lambda _iid: channels,
    )
    value = EngineReplacement.__new__(EngineReplacement)
    value.engine = engine
    value.guard = SimpleNamespace(pending_paid_work=lambda _database: paid)
    value.database = "/fixture.sqlite"
    return value, events


def test_usim_containment_rechecks_fence_paid_and_channels_before_capture_scope():
    value, events = replacement()
    with value._usim_containment_for_capture("1", MARKER):
        events.append("capture")
    assert events == [
        "engine-maint-enter", "boundary-enter", ("paid", {
            "open_call_leases": 0, "pending_messages": 0,
            "pending_allowance_queries": 0}), ("zero", True), "capture",
        "boundary-exit", "engine-maint-exit"]


@pytest.mark.parametrize("paid,channels", [
    ({"open_call_leases": 1, "pending_messages": 0, "pending_allowance_queries": 0}, 0),
    ({"open_call_leases": 0, "pending_messages": 0}, 0),
    ({"open_call_leases": 0, "pending_messages": 0, "pending_allowance_queries": 0}, None),
])
def test_paid_or_channel_unknown_never_enters_capture_scope(paid, channels):
    value, events = replacement(paid=paid, channels=channels)
    with pytest.raises(RuntimeError):
        with value._usim_containment_for_capture("1", MARKER):
            events.append("capture")
    assert "capture" not in events


def test_changed_marker_run_auth_or_recovery_baseline_refuses():
    cases = [
        {"status": {"state": "AUTH_UNAVAILABLE", "engine_run_id": "other",
                    "cause_class": "pcsc_service_unavailable", "auth_seq": 7}},
        {"status": {"state": "AUTH_OK", "engine_run_id": "run-current",
                    "cause_class": "pcsc_service_unavailable", "auth_seq": 7}},
        {"recovery": {"version": 2, "phase": "exhausted", "engine_run_id": "run-current",
                      "auth_seq_baseline": 8}},
    ]
    for options in cases:
        value, events = replacement(**options)
        with pytest.raises(RuntimeError):
            with value._usim_containment_for_capture("1", MARKER):
                events.append("capture")
        assert "capture" not in events


def test_unfenced_ordinary_path_has_no_added_maintenance_or_paid_checks():
    value, events = replacement(fenced=False)
    with value._usim_containment_for_capture("1", MARKER):
        events.append("capture")
    assert events == ["capture"]


def test_both_destructive_callsites_wrap_capture_with_scoped_and_usim_boundaries():
    normal = inspect.getsource(EngineReplacement._drive_line)
    rollback = inspect.getsource(EngineReplacement._recover_usim_fenced_pending_source)
    for source in (normal, rollback):
        scoped = source.index("with self._scoped_mutation_locked")
        containment = source.index("with self._usim_containment_for_capture", scoped)
        capture = source.index("capture_and_stop_if_idle", containment)
        assert scoped < containment < capture


def test_real_usim_flock_recheck_is_nonrecursive_and_baseline_exact(tmp_path, monkeypatch):
    monkeypatch.setattr(product_engine, "DATA_DIR", str(tmp_path))
    run = tmp_path / "instances/1/run"; run.mkdir(parents=True)
    recovery = {
        "version": 1, "instance": "1", "container_id": "a" * 64,
        "started_at": "2026-08-27T00:00:00Z", "engine_run_id": "run-current",
        "auth_seq": 7, "cause_class": "pcsc_service_unavailable",
        "topology_digest": "b" * 64, "phase": "pending", "attempts": 1,
        "next_attempt_at": 0.0, "updated_at": 1000.0,
        "submitted_at": 0.0, "result_class": "",
    }
    (run / "usim-auth-recovery.json").write_text(json.dumps(recovery), encoding="utf-8")
    (run / "usim-auth-recovery.fence").write_text("fenced", encoding="utf-8")
    monkeypatch.setattr(product_engine, "_acquire_usim_recovery_admission",
                        lambda _iid: object())
    monkeypatch.setattr(product_engine, "release_pcscf_admission", lambda _handle: None)
    started = time.monotonic(); entered = []
    with product_engine.usim_recovery_containment_boundary(
            "1", publish_fence=lambda: True,
            pending_paid=lambda: {"open_call_leases": 0, "pending_messages": 0,
                                  "pending_allowance_queries": 0},
            zero_channels=lambda: True,
            expected_recovery_identity={"engine_run_id": "run-current",
                                        "auth_seq_baseline": 7, "campaign_epoch": ""}):
        entered.append(True)
    assert entered == [True] and time.monotonic() - started < 1
    with pytest.raises(product_engine.UsimRecoveryStateError):
        with product_engine.usim_recovery_containment_boundary(
                "1", publish_fence=lambda: True,
                pending_paid=lambda: {"open_call_leases": 0, "pending_messages": 0,
                                      "pending_allowance_queries": 0},
                zero_channels=lambda: True,
                expected_recovery_identity={"engine_run_id": "run-current",
                                            "auth_seq_baseline": 8,
                                            "campaign_epoch": ""}):
            raise AssertionError("wrong baseline reached capture")
