import json
from unittest.mock import Mock, patch

from control.app import failover, main


INST = {"id": "9", "enabled": True, "iccid": "8944110000000000001",
        "imsi": "234100000000001", "mcc": "234", "mnc": "10", "mnc_len": 2,
        "reader_index": 1, "reader_port": "", "apn": "ims", "idr_mode": "apn",
        "cp_mode": "v6", "sip": {"user_eq_phone": True}}
RUNTIME = {"running": True, "container_id": "a" * 64,
           "started_at": "2026-08-27T01:00:00Z", "engine_run_id": "run-a"}
STATUS = {"state": "TUNNEL_DOWN", "reason_code": "tunnel_network", "reason": "x",
          "detail": {"registration_event_key": "registration-event-a",
                     "registration_event_at": 100.0}}
CARD = {"present": True, "iccid": INST["iccid"], "eid": "89049032000000000000000000000001",
        "remote": True, "connection_online": True, "identity_current": True,
        "session_generation": "route-a", "identity_session_generation": "route-a"}


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def planning(iid="9", *, runtime=None, status=None, cards=None, inst=None):
    with patch.object(main.hub, "cards_list", return_value=list(CARD for _ in [0])
                      if cards is None else cards), \
            patch.object(main.egress, "line_country", return_value="gb"), \
            patch.object(main.egress, "status", return_value={"exits": {"gb": {
                "node": "node-a", "candidates": ["node-a", "node-b"],
                "selection": "auto"}}}), \
            patch.object(main, "_peer_line_registered", return_value=False), \
            patch.object(main.engine, "read_run_json", return_value={"state": "DOWN"}), \
            patch.object(main.engine, "ike_evidence", return_value={"retransmits": 0}):
        return main._plan_exit_failure(
            iid, dict(inst or INST), 30, runtime=runtime or dict(RUNTIME),
            status=status or dict(STATUS), enforce_campaign=True)


def setup_function():
    main.hub.exit_ledgers.pop("9", None)


def teardown_function():
    main.hub.exit_ledgers.pop("9", None)


def test_first_campaign_is_durable_and_controlled_rebuild_keeps_budget():
    first = planning()
    assert first["persist"] is True and first["ledger"]["outage_counter"] == 1
    assert first["ledger"]["stable_card_key"].startswith("eid:")
    with patch.object(main, "_save_exit_ledgers"):
        main._commit_exit_failure_plan("9", INST, STATUS, 30, first, engine_retained=False)
    saved = dict(main.hub.exit_ledgers["9"])
    assert saved["controlled_rebuild_pending"] is True

    next_runtime = {**RUNTIME, "container_id": "b" * 64,
                    "started_at": "2026-08-27T01:01:00Z", "engine_run_id": "run-b"}
    second = planning(runtime=next_runtime, status={
        **STATUS, "detail": {"registration_event_key": "registration-event-b",
                             "registration_event_at": 200.0}})
    assert second["persist"] is True
    assert second["ledger"]["campaign_epoch"] == saved["campaign_epoch"]
    assert second["ledger"]["outage_counter"] == 1
    assert second["ledger"]["failures"] == saved["failures"] + 1


def test_newer_event_in_same_engine_advances_but_older_event_is_zero_write():
    first = planning()
    main.hub.exit_ledgers["9"] = dict(first["ledger"])
    newer = planning(status={
        **STATUS, "detail": {"registration_event_key": "registration-event-b",
                             "registration_event_at": 101.0}})
    assert newer["persist"] is True
    assert newer["ledger"]["failures"] == first["ledger"]["failures"] + 1
    main.hub.exit_ledgers["9"] = dict(newer["ledger"])
    before = canonical(main.hub.exit_ledgers["9"])
    old = planning(status=STATUS)
    assert old["persist"] is False and canonical(old["ledger"]) == before


def test_late_or_incomplete_sample_is_byte_zero_and_not_saved():
    first = planning()
    main.hub.exit_ledgers["9"] = dict(first["ledger"])
    before = canonical(main.hub.exit_ledgers["9"])
    duplicate = planning()
    assert duplicate["persist"] is False
    assert canonical(duplicate["ledger"]) == before
    late = planning(runtime={**RUNTIME, "engine_run_id": "unexpected-run"})
    assert late["persist"] is False and late["verdict"] == failover.UNCLEAR
    with patch.object(main, "_save_exit_ledgers") as save:
        assert main._commit_exit_failure_plan("9", INST, STATUS, 30, late) == failover.HOLD
    save.assert_not_called()
    assert canonical(main.hub.exit_ledgers["9"]) == before

    missing = planning(status={**STATUS, "detail": {}}, cards=[])
    assert missing["persist"] is False and missing["ledger"] == main.hub.exit_ledgers["9"]


def test_card_or_config_change_starts_new_counter_and_resets_budget():
    first = planning()
    main.hub.exit_ledgers["9"] = {**first["ledger"], "strikes": 2, "failures": 4}
    changed_card = {**CARD, "eid": "89049032000000000000000000000002"}
    second = planning(cards=[changed_card])
    assert second["ledger"]["outage_counter"] == 2
    assert second["ledger"]["failures"] == 1
    assert second["ledger"]["campaign_epoch"] != first["ledger"]["campaign_epoch"]
    main.hub.exit_ledgers["9"] = dict(first["ledger"])
    changed_config = planning(inst={**INST, "apn": "carrier-ims"})
    assert changed_config["ledger"]["outage_counter"] == 2
    assert changed_config["ledger"]["failures"] == 1
    main.hub.exit_ledgers["9"] = dict(first["ledger"])
    route_only = planning(inst={**INST, "reader_index": 10, "reader_port": "usb-new"})
    assert route_only["ledger"]["campaign_epoch"] == first["ledger"]["campaign_epoch"]
    assert route_only["ledger"]["outage_counter"] == 1


def test_ok_and_manual_reset_keep_closed_counter_tombstone():
    first = planning()
    main.hub.exit_ledgers["9"] = dict(first["ledger"])
    with patch.object(main, "_save_exit_ledgers") as save:
        result = main.apply_health("9", INST, {"state": "OK"}, RUNTIME["container_id"])
    assert result["state"] == "OK" and save.call_count == 1
    closed = main.hub.exit_ledgers["9"]
    assert closed["closed"] is True and closed["outage_counter"] == 1
    assert closed["failures"] == closed["strikes"] == 0
    with patch.object(main, "_save_exit_ledgers") as save:
        main.apply_health("9", INST, {"state": "OK"}, RUNTIME["container_id"])
    save.assert_not_called()
    with patch.object(main, "_save_exit_ledgers"):
        main._clear_manual_recovery_history("9")
    reset = main.hub.exit_ledgers["9"]
    assert reset["closed"] is True and reset["outage_counter"] == 2
    assert reset["next_campaign_reserved"] is True


def test_commit_cas_rejects_a_concurrent_ledger_change():
    plan = planning()
    main.hub.exit_ledgers["9"] = {"foreign": True}
    with patch.object(main, "_save_exit_ledgers") as save:
        assert main._commit_exit_failure_plan("9", INST, STATUS, 30, plan) == failover.HOLD
    save.assert_not_called()
    assert main.hub.exit_ledgers["9"] == {"foreign": True}
