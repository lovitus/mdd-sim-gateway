from control.app import main


def test_blank_euicc_placeholder_identity_is_not_a_real_sim():
    assert main._is_placeholder_sim_identity({
        "iccid": "89111111111111111111",
        "imsi": "111111111111111",
    })
    assert not main._is_placeholder_sim_identity({
        "iccid": "8944110069499811522",
        "imsi": "234104046996669",
    })


def test_empty_profile_cache_matches_by_eid_and_endpoint(tmp_path, monkeypatch):
    monkeypatch.setattr(main, "_ESIM_CACHE_PATH", str(tmp_path / "esim-cache.json"))
    eid = "89086030202200000025000132416289"
    ses = [{"id": "default", "eid": eid, "profiles": [], "notifications": [],
            "error": None, "chip": {"eid": eid}}]
    main._esim_cache_store(ses, "", {"endpoint_key": "phone/omapi"})

    by_eid = main._esim_cache_for_card({"eid": eid})
    by_endpoint = main._esim_cache_for_card({"endpoint_key": "phone/omapi"})
    assert by_eid["ses"][0]["profiles"] == []
    assert by_endpoint["ses"][0]["eid"] == eid
