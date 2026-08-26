"""D2 RED: failover samples cannot reset or cross a durable recovery campaign."""
import json

from control.app import failover


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def test_campaign_ledger_has_stable_campaign_and_exact_sample_generation():
    ledger = failover.blank_ledger()
    assert ledger["campaign_epoch"] == "" and ledger["sample_generation"] is None
    ledger = failover.begin_campaign(
        ledger, campaign_epoch="campaign-a", sample_generation="sample-a",
        stable_card_key="eid:eid-a", line_config_epoch="config-a")
    assert ledger["campaign_epoch"] == "campaign-a"
    assert ledger["sample_generation"] == "sample-a"
    assert ledger["line_config_epoch"] == "config-a"
    assert ledger["stable_card_key"] == "eid:eid-a"


def test_late_sample_is_zero_write():
    ledger = {
        **failover.blank_ledger(), "campaign_epoch": "campaign-new",
        "sample_generation": "sample-new", "stable_card_key": "eid:eid-1",
        "line_config_epoch": "config-1", "strikes": 2, "failures": 4,
        "next_probe": 500.0, "cooldown": 60.0,
    }
    before = canonical(ledger)
    action, after = failover.record(
        ledger, failover.BLAMES_EXIT, "node-a", False, ["node-a", "node-b"],
        campaign_epoch="campaign-new", sample_generation="sample-old",
        expected_sample_generation="sample-new")
    assert action == failover.HOLD
    assert canonical(after) == before


def test_begin_campaign_resets_only_at_a_real_boundary():
    existing = {
        **failover.blank_ledger(), "campaign_epoch": "campaign-a",
        "sample_generation": "engine-a", "stable_card_key": "iccid:card-a",
        "line_config_epoch": "config-a", "strikes": 2, "failures": 2,
    }
    late = failover.begin_campaign(
        existing, campaign_epoch="campaign-a", sample_generation="late-engine",
        stable_card_key="iccid:card-a", line_config_epoch="config-a")
    assert canonical(late) == canonical(existing)
    rebuilt = failover.begin_campaign(
        existing, campaign_epoch="campaign-a", sample_generation="engine-b",
        stable_card_key="iccid:card-a", line_config_epoch="config-a",
        controlled_rebuild=True)
    assert rebuilt["sample_generation"] == "engine-b"
    assert rebuilt["strikes"] == 2 and rebuilt["failures"] == 2
    changed = failover.begin_campaign(
        existing, campaign_epoch="campaign-b", sample_generation="engine-c",
        stable_card_key="iccid:card-b", line_config_epoch="config-b")
    assert changed["campaign_epoch"] == "campaign-b"
    assert changed["strikes"] == changed["failures"] == 0


def test_controlled_rebuild_changes_sample_but_retains_campaign_budget():
    ledger = {
        **failover.blank_ledger(), "campaign_epoch": "campaign-a",
        "sample_generation": "engine-a", "stable_card_key": "iccid:card-a",
        "line_config_epoch": "config-a", "node": "node-a", "strikes": 2,
        "failures": 2,
    }
    action, after = failover.record(
        ledger, failover.BLAMES_EXIT, "node-a", False, ["node-a", "node-b"],
        campaign_epoch="campaign-a", sample_generation="engine-b",
        expected_sample_generation="engine-a", controlled_rebuild=True)
    assert action == failover.SWITCH
    assert after["campaign_epoch"] == "campaign-a"
    assert after["sample_generation"] == "engine-b"
    assert after["failures"] == 3


def test_unclear_is_byte_for_byte_zero_write():
    ledger = {
        **failover.blank_ledger(), "campaign_epoch": "campaign-a",
        "sample_generation": "engine-a", "stable_card_key": "eid:eid-a",
        "line_config_epoch": "config-a", "strikes": 2, "failures": 9,
        "next_probe": 900.0, "cooldown": 3600.0, "reported": True,
    }
    before = canonical(ledger)
    action, after = failover.record(
        ledger, failover.UNCLEAR, "node-a", False, ["node-a"],
        campaign_epoch="campaign-a", sample_generation="engine-a",
        expected_sample_generation="engine-a")
    assert action == failover.HOLD
    assert canonical(after) == before
