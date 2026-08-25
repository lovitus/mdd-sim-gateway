import asyncio
from contextlib import contextmanager, nullcontext
import os
from pathlib import Path
import shutil
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock, patch

import pytest
import yaml

from control.app import engine
from control.app import engine_start_quarantine_contract as contract
from control.app import main
from control.app.vpcd_slots import VpcdSlotRegistry
from host import mdd_admission_authority
from host import mdd_line_start_quarantine as host_quarantine


TXID = "deploy-20260825-quarantine-0001"


def _record(iid="9", reason="Preserve the intentionally absent Engine during deployment"):
    return {
        "version": 1,
        "instance": iid,
        "owner": {"type": "deployment", "txid": TXID},
        "reason": reason,
        "created_at": 1787620000,
    }


def _config(data: Path, iid="9"):
    data.mkdir(parents=True, exist_ok=True)
    (data / "config.yaml").write_text(yaml.safe_dump({
        "version": 1,
        "instances": {iid: {"id": iid, "enabled": True}},
        "settings": {},
    }), encoding="utf-8")
    (data / "instances" / iid / "run").mkdir(parents=True, exist_ok=True)


def test_host_acquire_release_is_owner_digest_cas_and_keeps_audit_tombstone(tmp_path):
    _config(tmp_path)
    result = host_quarantine.acquire(
        tmp_path, "9", TXID, _record()["reason"],
        docker_object_exists=lambda _iid: False, now=lambda: 1787620000)
    assert result["acquisition_digest"] == contract.record_digest(_record())
    assert contract.read_active(tmp_path, "9") == (
        _record(), result["acquisition_digest"])

    with pytest.raises(contract.QuarantineContractError, match="ownership mismatch"):
        host_quarantine.release(tmp_path, "9", TXID, "0" * 64)
    assert contract.is_pending(tmp_path, "9") is True

    released = host_quarantine.release(
        tmp_path, "9", TXID, result["acquisition_digest"])
    assert contract.is_pending(tmp_path, "9") is False
    tombstone = tmp_path / released["tombstone"]
    assert tombstone.is_file()
    assert tombstone.parent.parent.name == contract.RELEASE_ROOT_NAME


@pytest.mark.parametrize("kind", ["corrupt", "symlink", "directory", "fifo"])
def test_malicious_marker_corpus_is_fail_closed_for_all_consumers(tmp_path, kind):
    run = tmp_path / "instances" / "9" / "run"
    run.mkdir(parents=True)
    marker = run / contract.QUARANTINE_NAME
    if kind == "corrupt":
        marker.write_text("{bad", encoding="utf-8")
        marker.chmod(0o600)
    elif kind == "symlink":
        target = tmp_path / "target.json"
        target.write_text("{}", encoding="utf-8")
        marker.symlink_to(target)
    elif kind == "directory":
        marker.mkdir()
    else:
        os.mkfifo(marker, 0o600)

    assert contract.is_pending(tmp_path, "9") is True
    with pytest.raises(contract.QuarantineContractError):
        contract.read_active(tmp_path, "9")
    with patch.object(engine, "DATA_DIR", str(tmp_path)):
        assert engine.engine_start_quarantine_status("9")["valid"] is False
    writer = mdd_admission_authority.NormalAuthorityWriter(
        tmp_path, inspector=Mock(), boot_id="a" * 32)
    assert writer._line_deny_reason("9") == "line_engine_start_quarantine"


def test_stable_lock_inode_survives_instance_rmtree_and_recreation(tmp_path):
    _config(tmp_path)
    with contract.locked_lines(tmp_path, ["9"], exclusive=True) as handles:
        held = os.fstat(handles[0].fileno())
        shutil.rmtree(tmp_path / "instances" / "9")
        (tmp_path / "instances" / "9" / "run").mkdir(parents=True)
        current = contract.stable_lock_path(tmp_path, "9").stat()
        assert (held.st_dev, held.st_ino) == (current.st_dev, current.st_ino)


def test_normal_start_uses_private_permit_and_blocks_active_or_expired_state(tmp_path):
    with patch.object(engine, "DATA_DIR", str(tmp_path)), \
            patch.object(engine, "global_maintenance_pending", return_value=False), \
            patch.object(engine, "engine_default_promotion_pending", return_value=False), \
            patch.object(engine, "engine_maintenance_pending", return_value=False), \
            patch.object(engine, "_start_container", return_value="new") as create:
        assert engine.start_if_absent({"id": "9"}, {}) == "new"
        permit = create.call_args.kwargs["permit"]
        assert permit.active is False
        with pytest.raises(engine.EngineLifecycleFenced, match="expired"):
            engine._require_start_permit(permit, "9")
        with pytest.raises(engine.EngineLifecycleFenced, match="invalid"):
            engine._require_start_permit(object(), "9")

        contract.write_active(tmp_path, _record())
        create.reset_mock()
        with pytest.raises(engine.EngineStartQuarantined):
            engine.start_if_absent({"id": "9"}, {})
        create.assert_not_called()


def test_active_start_permit_cannot_be_reused_for_another_instance(tmp_path):
    with patch.object(engine, "DATA_DIR", str(tmp_path)):
        with engine.normal_start_permit("9") as permit:
            assert permit.active is True
            with pytest.raises(engine.EngineLifecycleFenced, match="invalid"):
                engine._require_start_permit(permit, "7")


def test_clear_runtime_and_soft_data_paths_never_remove_quarantine(tmp_path):
    base = tmp_path / "instances" / "9"
    (base / "run").mkdir(parents=True)
    contract.write_active(tmp_path, _record())
    (base / "run" / "swu_status.json").write_text("{}", encoding="utf-8")
    engine._clear_runtime_state(str(base))
    assert contract.is_pending(tmp_path, "9") is True
    with patch.object(engine, "DATA_DIR", str(tmp_path)):
        with pytest.raises(engine.EngineStartQuarantined):
            engine.delete_instance_data("9")
    assert base.is_dir()


def test_maintenance_start_takes_only_line_lock_after_external_global_owner(tmp_path):
    marker = {
        "txid": TXID,
        "phase": "target_starting",
        "target_image_digest": "sha256:" + "c" * 64,
        "source": {"image_id": "sha256:" + "b" * 64},
    }
    with patch.object(engine, "DATA_DIR", str(tmp_path)), \
            patch.object(engine, "engine_maintenance_locked", return_value=nullcontext()), \
            patch.object(engine, "read_engine_maintenance", return_value=marker), \
            patch.object(engine, "replacement_lifecycle_shared_locked",
                         side_effect=AssertionError("must not reacquire global SH")), \
            patch.object(engine, "_start_container", return_value="target") as create:
        result = engine.start_absent(
            {"id": "9"}, {}, "sha256:" + "c" * 64, TXID)
    assert result == "target"
    assert create.call_args.kwargs["maintenance"] is True
    assert create.call_args.kwargs["permit"].active is False


@pytest.mark.parametrize("intent", ["target", "rollback"])
def test_maintenance_snapshot_start_keeps_private_permit_for_target_and_rollback(
        tmp_path, intent):
    target_digest = "sha256:" + "c" * 64
    source_digest = "sha256:" + "b" * 64
    image_digest = target_digest if intent == "target" else source_digest
    create_spec = {"instance": "9", "frozen": True}
    marker = {
        "txid": TXID,
        "phase": "target_starting" if intent == "target" else "rollback_starting",
        "target_image_digest": target_digest,
        "source": {"image_id": source_digest},
        "source_create_spec": create_spec,
        "source_create_spec_digest": engine._canonical_digest(create_spec),
        "rollback_image_ref": "mdd-engine:rollback-" + "a" * 12,
    }
    retained = SimpleNamespace(id=source_digest)
    client = SimpleNamespace(images=SimpleNamespace(get=Mock(return_value=retained)))
    with patch.object(engine, "DATA_DIR", str(tmp_path)), \
            patch.object(engine, "engine_maintenance_locked", return_value=nullcontext()), \
            patch.object(engine, "read_engine_maintenance", return_value=marker), \
            patch.object(engine, "replacement_lifecycle_shared_locked",
                         side_effect=AssertionError("must not reacquire global SH")), \
            patch.object(engine, "_client", return_value=client), \
            patch.object(engine, "_start_container_from_create_spec",
                         return_value=intent) as create:
        result = engine.start_absent_from_snapshot(
            "9", image_digest, TXID, intent=intent)
    assert result == intent
    assert create.call_args.args == (create_spec, image_digest)
    permit = create.call_args.kwargs["permit"]
    assert permit.mode == "maintenance"
    assert permit.active is False


@contextmanager
def _blocked_start_context(*_args):
    raise engine.EngineStartQuarantined("blocked", blocked_iids=["9"])
    yield  # pragma: no cover


@contextmanager
def _allowed_probe_context(*_args):
    yield SimpleNamespace(bind_actual=Mock())


@pytest.mark.parametrize("endpoint", ["start", "reprovision"])
def test_explicit_start_paths_reject_before_pin_apdu(endpoint):
    reader = Mock()
    common = (
        patch.object(main.cfg, "get_instance", return_value={"id": "9", "enabled": True}),
        patch.object(main.engine, "normal_start_permit", side_effect=_blocked_start_context),
        patch.object(main.sim, "read_card", reader),
    )
    with common[0], common[1], common[2]:
        with pytest.raises(main.HTTPException) as caught:
            if endpoint == "start":
                asyncio.run(main.api_instance_start("9", {"pin": "1234"}))
            else:
                asyncio.run(main.api_reprovision("9", {"name": "must-not-write"}))
    assert caught.value.status_code == 409
    assert reader.call_count == 0


def test_card_probe_lock_failure_keeps_identity_unknown_and_never_reads_apdu():
    name = "Virtual PCD 00 0C"
    main.hub.cards.pop(name, None)


def test_release_converges_on_next_normal_card_cycle_without_release_side_effects():
    name = "Virtual PCD 00 0C"
    main.hub.cards.pop(name, None)
    pending = Mock(return_value=True)
    reader = Mock(return_value=SimpleNamespace(
        iccid=None, imsi=None, mcc=None, mnc=None, mnc_len=None,
        pin_enabled=False, pin_tries=None, smsc=None, spn=None,
        carrier_identity={}))
    common = (
        patch.object(main, "_reader_quarantine_candidates", return_value=["9"]),
        patch.object(main, "_find_running_by_reader", return_value=None),
        patch.object(main.usbreader, "port_for_index", return_value=""),
        patch.object(main.sim, "read_card", reader),
        patch.object(main.vpcd_registry, "begin_observation", return_value=None),
        patch.object(main.vpcd_registry, "observe_card", return_value=True),
        patch.object(main, "_ensure_card_draft", return_value=None),
        patch.object(main.engine, "engine_start_quarantine_pending", pending),
    )
    with (common[0], common[1], common[2], common[3], common[4], common[5], common[6],
          common[7]):
        with patch.object(main.engine, "card_probe_permits",
                          side_effect=_blocked_start_context):
            asyncio.run(main._on_card_insert(name, 12))
        assert reader.call_count == 0
        assert main.hub.cards[name]["quarantined"] is True

        # Host release has no callback into Control. A later ordinary monitor pass simply
        # observes that the permit is available and performs the one normal identity probe.
        pending.return_value = False
        with patch.object(main.engine, "card_probe_permits",
                          side_effect=_allowed_probe_context):
            asyncio.run(main._on_card_insert(name, 12))
    assert reader.call_count == 1
    assert main.hub.cards[name].get("quarantined") is not True
    main.hub.cards.pop(name, None)
    reader = Mock()
    with patch.object(main, "_reader_quarantine_candidates", return_value=["9"]), \
            patch.object(main, "_find_running_by_reader", return_value=None), \
            patch.object(main.usbreader, "port_for_index", return_value=""), \
            patch.object(main.engine, "card_probe_permits",
                         side_effect=_blocked_start_context), \
            patch.object(main.sim, "read_card", reader), \
            patch.object(main.vpcd_registry, "begin_observation", return_value="generation"):
        asyncio.run(main._on_card_insert(name, 12))
    observed = main.hub.cards[name]
    assert reader.call_count == 0
    assert observed["identity_current"] is False
    assert observed["matched"] is None
    assert observed["iccid"] is None
    main.hub.cards.pop(name, None)


def test_vpcd_history_does_not_restore_current_identity_to_quarantined_unknown(tmp_path):
    registry = VpcdSlotRegistry(str(tmp_path / "slots.json"))
    claim = registry.claim(agent_id="agent", reader_id="reader", requested_slot=12,
                           card_id="historic-card")
    assert registry.observe_card(
        "Virtual PCD 00 0C", {"iccid": "historic-card", "matched": "9"},
        expected_generation=claim.session_generation)
    rows = registry.enrich_cards([{
        "name": "Virtual PCD 00 0C", "present": True, "quarantined": True,
        "identity_current": False, "matched": None, "iccid": None,
    }])
    assert rows[0]["matched"] is None
    assert rows[0]["iccid"] is None
    assert rows[0]["identity_current"] is False


def test_acquire_rejects_any_canonical_docker_object(tmp_path):
    _config(tmp_path)
    with pytest.raises(host_quarantine.LineStartQuarantineError,
                       match="completely absent"):
        host_quarantine.acquire(
            tmp_path, "9", TXID, _record()["reason"],
            docker_object_exists=lambda _iid: True, now=lambda: 1787620000)
    assert contract.is_pending(tmp_path, "9") is False


def test_start_probe_and_delete_permits_linearize_before_host_acquire(tmp_path):
    _config(tmp_path)
    reason = _record()["reason"]
    with patch.object(engine, "DATA_DIR", str(tmp_path)):
        for manager in (
                engine.normal_start_permit("9"),
                engine.card_probe_permits([]),
                engine.normal_delete_permit("9")):
            with manager:
                with pytest.raises(contract.QuarantineContractError,
                                   match="global Engine lifecycle lock is busy"):
                    host_quarantine.acquire(
                        tmp_path, "9", TXID, reason,
                        docker_object_exists=lambda _iid: False,
                        now=lambda: 1787620000, lock_timeout_seconds=0)
            assert contract.is_pending(tmp_path, "9") is False

        acquired = host_quarantine.acquire(
            tmp_path, "9", TXID, reason,
            docker_object_exists=lambda _iid: False, now=lambda: 1787620000)
        with pytest.raises(engine.EngineStartQuarantined):
            with engine.normal_start_permit("9"):
                pass
        host_quarantine.release(
            tmp_path, "9", TXID, acquired["acquisition_digest"])


def test_multiple_historical_quarantine_candidates_stay_ambiguous_and_unmatched():
    name = "Virtual PCD 00 0C"
    info = {"index": 12, "name": name, "present": True}
    with patch.object(main.engine, "engine_start_quarantine_pending", return_value=True), \
            patch.object(main.vpcd_registry, "begin_observation"), \
            patch.object(main.vpcd_registry, "snapshot", return_value=[]):
        main._publish_quarantined_card_unknown(name, info, ["7", "9"])
    observed = main.hub.cards[name]
    assert observed["quarantine_ambiguous"] is True
    assert observed["quarantine_expected_instance"] is None
    assert observed["quarantine_expected_instances"] == ["7", "9"]
    assert observed["matched"] is None
    assert observed["iccid"] is None
    main.hub.cards.pop(name, None)


@pytest.mark.parametrize("history", [[], ["7"]])
def test_existing_global_marker_blocks_empty_or_wrong_history_before_apdu(tmp_path, history):
    _config(tmp_path)
    contract.write_active(tmp_path, _record())
    name = "Virtual PCD 00 0C"
    reader = Mock()
    main.hub.cards.pop(name, None)
    main.hub.probe_quarantine_blockers.clear()
    with patch.object(engine, "DATA_DIR", str(tmp_path)), \
            patch.object(main, "_reader_quarantine_candidates", return_value=history), \
            patch.object(main.usbreader, "port_for_index", return_value=""), \
            patch.object(main.sim, "read_card", reader), \
            patch.object(main.vpcd_registry, "begin_observation"), \
            patch.object(main.vpcd_registry, "snapshot", return_value=[]):
        asyncio.run(main._on_card_insert(name, 12))
    observed = main.hub.cards[name]
    assert reader.call_count == 0
    assert observed["identity_current"] is False
    assert observed["matched"] is None
    assert observed["iccid"] is None
    assert observed["probe_blocked"] is True
    assert observed["probe_blocked_by_quarantines"] == ["9"]
    assert observed["quarantine_expected_instance"] is None
    main.hub.cards.pop(name, None)
    main.hub.probe_quarantine_blockers.clear()


def test_untrusted_global_marker_blocks_apdu_with_generic_manual_state(tmp_path):
    _config(tmp_path)
    marker = contract.active_path(tmp_path, "9")
    marker.write_text("{bad", encoding="utf-8")
    marker.chmod(0o600)
    name = "Virtual PCD 00 0C"
    reader = Mock()
    main.hub.cards.pop(name, None)
    main.hub.probe_quarantine_blockers.clear()
    main.hub.probe_quarantine_state_unknown = False
    with patch.object(engine, "DATA_DIR", str(tmp_path)), \
            patch.object(main, "_reader_quarantine_candidates", return_value=[]), \
            patch.object(main.usbreader, "port_for_index", return_value=""), \
            patch.object(main.sim, "read_card", reader), \
            patch.object(main.vpcd_registry, "begin_observation"), \
            patch.object(main.vpcd_registry, "snapshot", return_value=[]):
        asyncio.run(main._on_card_insert(name, 12))
    observed = main.hub.cards[name]
    assert reader.call_count == 0
    assert observed["probe_blocked"] is True
    assert observed["probe_blocked_state_unknown"] is True
    assert observed["probe_blocked_by_quarantines"] == []
    assert observed["probe_resume_armed"] is False
    main.hub.cards.pop(name, None)
    main.hub.probe_quarantine_state_unknown = False


def test_actual_probe_permit_covers_registry_and_hub_publication(tmp_path):
    _config(tmp_path)
    name = "Virtual PCD 00 0C"
    current = {
        "id": "9", "iccid": "8901000000000000009", "imsi": "234100000000009",
        "mcc": "234", "mnc": "10", "mnc_len": 2, "smsc": "+1",
        "reader_index": 12, "reader_port": "", "carrier_identity": {},
    }
    card_info = SimpleNamespace(
        iccid=current["iccid"], imsi=current["imsi"], mcc="234", mnc="10",
        mnc_len=2, pin_enabled=False, pin_tries=None, smsc="+1", spn=None,
        carrier_identity={})
    attempts = []

    def observe(*_args, **_kwargs):
        with pytest.raises(contract.QuarantineContractError,
                           match="global Engine lifecycle lock is busy"):
            host_quarantine.acquire(
                tmp_path, "9", TXID, _record()["reason"],
                docker_object_exists=lambda _iid: False,
                now=lambda: 1787620000, lock_timeout_seconds=0)
        attempts.append("blocked")
        return True

    main.hub.cards.pop(name, None)
    with patch.object(engine, "DATA_DIR", str(tmp_path)), \
            patch.object(main, "_reader_quarantine_candidates", return_value=[]), \
            patch.object(main, "_find_running_by_reader", return_value=None), \
            patch.object(main, "_modem_identity_for_reader", return_value={}), \
            patch.object(main.usbreader, "port_for_index", return_value=""), \
            patch.object(main.sim, "read_card", return_value=card_info), \
            patch.object(main.cfg, "instances_by_iccid", return_value=[current]), \
            patch.object(main.vpcd_registry, "begin_observation", return_value="generation"), \
            patch.object(main.vpcd_registry, "observe_card", side_effect=observe):
        asyncio.run(main._on_card_insert(name, 12))
    assert attempts == ["blocked", "blocked"]
    assert main.hub.cards[name]["identity_current"] is True
    assert main.hub.cards[name]["matched"] == "9"
    acquired = host_quarantine.acquire(
        tmp_path, "9", TXID, _record()["reason"],
        docker_object_exists=lambda _iid: False, now=lambda: 1787620000)
    host_quarantine.release(tmp_path, "9", TXID, acquired["acquisition_digest"])
    main.hub.cards.pop(name, None)


def test_current_identity_is_sanitized_without_apdu_after_acquire(tmp_path):
    _config(tmp_path)
    contract.write_active(tmp_path, _record())
    name = "Virtual PCD 00 0C"
    entry = {"name": name, "index": 12, "present": True,
             "identity_current": True, "matched": "9",
             "iccid": "8901000000000000009"}
    reader = Mock()
    main.hub.cards[name] = entry
    main.hub.probe_quarantine_blockers.clear()
    with patch.object(engine, "DATA_DIR", str(tmp_path)), \
            patch.object(main, "_reader_quarantine_candidates", return_value=["9"]), \
            patch.object(main.sim, "read_card", reader), \
            patch.object(main.vpcd_registry, "begin_observation"), \
            patch.object(main.vpcd_registry, "snapshot", return_value=[]):
        assert main._sanitize_card_for_probe_quarantine(
            name, {"name": name, "index": 12, "present": True}, entry, ["9"], False)
    assert reader.call_count == 0
    assert main.hub.cards[name]["identity_current"] is False
    assert main.hub.cards[name]["matched"] is None
    assert main.hub.cards[name]["probe_resume_armed"] is True
    main.hub.cards.pop(name, None)
    main.hub.probe_quarantine_blockers.clear()


def test_new_draft_actual_bind_is_exclusive_between_two_probe_transactions(tmp_path):
    _config(tmp_path)
    with patch.object(engine, "DATA_DIR", str(tmp_path)):
        with engine.card_probe_permits() as first:
            first.bind_actual(["10"], exclusive=True)
            with engine.card_probe_permits() as second:
                with pytest.raises(engine.EngineLifecycleFenced,
                                   match="quarantine lock is busy"):
                    second.bind_actual(["10"], exclusive=True)
                with pytest.raises(engine.EngineLifecycleFenced,
                                   match="already-bound"):
                    second.bind_actual(["11"], exclusive=True)


def test_atomic_unique_identity_helper_never_overwrites_draft(tmp_path):
    data = tmp_path / "config-data"
    config_path = data / "config.yaml"
    first = {"id": "10", "iccid": "8901000000000000010", "name": "first",
             "ports": main.cfg._alloc_ports(10), "sip": {}, "debug": {}}
    with patch.object(main.cfg, "DATA_DIR", str(data)), \
            patch.object(main.cfg, "CONFIG_PATH", str(config_path)):
        created = main.cfg.upsert_instance_unique_iccid(
            first, require_iid_absent=True)
        with pytest.raises(main.cfg.InstanceIdentityConflict):
            main.cfg.upsert_instance_unique_iccid(
                {**first, "id": "11", "name": "wrong"},
                require_iid_absent=True)
        with pytest.raises(main.cfg.InstanceIdConflict):
            main.cfg.upsert_instance_unique_iccid(
                {**first, "iccid": "8901000000000000099", "name": "overwrite"},
                require_iid_absent=True)
        current = main.cfg.get_instance("10")
    assert created["name"] == "first"
    assert current["name"] == "first"
    assert current["iccid"] == first["iccid"]


def test_cross_iid_provision_rejects_before_upsert_or_create():
    card_info = SimpleNamespace(
        iccid="8901000000000000009", imsi="234100000000009", mcc="234", mnc="10",
        error=None, pin_tries=None)
    probe = SimpleNamespace(bind_actual=Mock())

    @contextmanager
    def probe_context():
        yield probe

    with patch.object(main, "_card_probe_permit_or_http", side_effect=probe_context), \
            patch.object(main, "_normal_start_permit_or_http") as normal, \
            patch.object(main, "_resolve_reader_index", return_value=0), \
            patch.object(main.sim, "list_readers", return_value=["reader"]), \
            patch.object(main.sim, "read_card", return_value=card_info), \
            patch.object(main.cfg, "instances_by_iccid", return_value=[{"id": "9"}]), \
            patch.object(main.cfg, "upsert_instance_unique_iccid") as upsert, \
            patch.object(main, "_start_engine_checked") as start:
        with pytest.raises(main.HTTPException) as caught:
            asyncio.run(main.api_provision({"id": "10"}))
    assert caught.value.status_code == 409
    assert caught.value.detail["code"] == "sim_identity_conflict"
    probe.bind_actual.assert_not_called()
    normal.assert_not_called()
    upsert.assert_not_called()
    start.assert_not_called()


def test_host_acquire_between_provision_stages_prevents_every_side_effect(tmp_path):
    _config(tmp_path)
    card_info = SimpleNamespace(
        iccid="8901000000000000009", imsi="234100000000009", mcc="234", mnc="10",
        error=None, pin_tries=None)
    probe = SimpleNamespace(bind_actual=Mock())

    @contextmanager
    def probe_then_host_wins():
        yield probe
        contract.write_active(tmp_path, _record())

    with patch.object(engine, "DATA_DIR", str(tmp_path)), \
            patch.object(main, "_card_probe_permit_or_http",
                         side_effect=probe_then_host_wins), \
            patch.object(main, "_resolve_reader_index", return_value=0), \
            patch.object(main.sim, "list_readers", return_value=["reader"]), \
            patch.object(main.sim, "read_card", return_value=card_info), \
            patch.object(main.cfg, "instances_by_iccid", return_value=[]), \
            patch.object(main.cfg, "upsert_instance_unique_iccid") as upsert, \
            patch.object(main, "_start_engine_checked") as start:
        with pytest.raises(main.HTTPException) as caught:
            asyncio.run(main.api_provision({"id": "9"}))
    assert caught.value.status_code == 409
    assert caught.value.detail["code"] == "engine_start_quarantined"
    upsert.assert_not_called()
    start.assert_not_called()


def test_probe_resume_is_consumed_once_after_release():
    entry = {
        "probe_deferred": True, "probe_resume_armed": True,
        "probe_resume_attempted": False,
        "probe_blocked_by_quarantines": ["9"],
    }
    with patch.object(main.engine, "engine_start_quarantine_pending", return_value=False):
        assert main._consume_probe_resume(entry, state_unknown=False) is True
        assert main._consume_probe_resume(entry, state_unknown=False) is False
    assert entry["probe_resume_armed"] is False
    assert entry["probe_resume_attempted"] is True
