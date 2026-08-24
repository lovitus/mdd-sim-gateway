from control.app.media_admission import MAX_ENTRIES, MediaAdmissionRegistry


EVIDENCE = {
    "connection_state": "connected",
    "local_track_live": True,
    "remote_track_live": True,
    "playback_started": True,
    "outbound_packets_delta": 2,
    "outbound_bytes_delta": 320,
    "inbound_packets_delta": 2,
    "inbound_bytes_delta": 320,
}


def test_admission_requires_both_proofs_and_consumes_only_one_proof_cycle():
    now = [100.0]
    registry = MediaAdmissionRegistry(clock=lambda: now[0])
    token = registry.issue("7", "engine-a", "route-a")

    assert registry.claim_canary(token, "7", "engine-a", "ws-a", "route-a")
    assert registry.mark_browser(token, "7", "engine-a", EVIDENCE)
    assert not registry.status(token, "7", "engine-a")["ready"]
    assert registry.mark_engine(token, "7", "engine-a")
    assert registry.authorize_invite(token, "7", "engine-a", "ws-a", "call-a", "+441")
    # Digest challenge retry for the same transaction remains authorized.
    assert registry.authorize_invite(token, "7", "engine-a", "ws-a", "call-a", "+441")
    assert not registry.mark_engine(token, "7", "engine-a")
    assert not registry.mark_browser(token, "7", "engine-a", EVIDENCE)
    assert not registry.authorize_invite(token, "7", "engine-a", "ws-a", "call-b", "+441")

    # The consumed proof/token cannot be replayed; a later call must request a new token.
    assert not registry.claim_canary(token, "7", "engine-a", "ws-a", "route-a")
    second = registry.issue("7", "engine-a", "route-a")
    assert registry.claim_canary(second, "7", "engine-a", "ws-a", "route-a")
    assert registry.mark_engine(second, "7", "engine-a")
    assert registry.mark_browser(second, "7", "engine-a", EVIDENCE)
    assert registry.authorize_invite(
        second, "7", "engine-a", "ws-a", "call-b", "+441")


def test_admission_is_bound_to_websocket_generation_and_freshness():
    now = [100.0]
    registry = MediaAdmissionRegistry(clock=lambda: now[0])
    token = registry.issue("1", "engine-a", "route-a")
    assert not registry.claim_canary(token, "1", "engine-a", "ws-a", "route-b")
    assert registry.claim_canary(token, "1", "engine-a", "ws-a", "route-a")
    assert not registry.claim_canary(token, "1", "engine-a", "ws-b", "route-a")
    assert not registry.mark_engine(token, "1", "engine-b")
    assert registry.mark_engine(token, "1", "engine-a")
    assert registry.mark_browser(token, "1", "engine-a", EVIDENCE)
    now[0] += 11
    assert not registry.authorize_invite(
        token, "1", "engine-a", "ws-a", "call-a", "+1")
    assert registry.release_websocket("ws-a") == []
    assert not registry.claim_canary(token, "1", "engine-a", "ws-a", "route-a")


def test_release_returns_only_authorized_calls_for_targeted_cleanup():
    registry = MediaAdmissionRegistry()
    token = registry.issue("1", "engine-a", "route-a")
    assert registry.claim_canary(token, "1", "engine-a", "ws-a", "route-a")
    assert registry.mark_engine(token, "1", "engine-a")
    assert registry.mark_browser(token, "1", "engine-a", EVIDENCE)
    assert registry.authorize_invite(
        token, "1", "engine-a", "ws-a", "transaction", "+44123")
    released = registry.release_websocket("ws-a")
    assert released == [{
        "token": token, "iid": "1", "generation": "engine-a",
        "transaction_id": "transaction", "target": "+44123",
        "source_call_id": "",
    }]


def test_digest_retry_requires_observed_challenge_and_exact_transaction():
    registry = MediaAdmissionRegistry()
    token = registry.issue("1", "engine", "route")
    assert registry.claim_canary(token, "1", "engine", "ws", "route")
    assert registry.mark_engine(token, "1", "engine")
    assert registry.mark_browser(token, "1", "engine", EVIDENCE)
    assert registry.authorize_invite(
        token, "1", "engine", "ws", "dialog", "+44123", 10, "z9hG4bK-a", False)
    assert not registry.authorize_invite(
        token, "1", "engine", "ws", "dialog", "+44123", 11, "z9hG4bK-b", True)
    assert registry.observe_invite_response("ws", "dialog", 10, 401)
    assert registry.authorize_invite(
        token, "1", "engine", "ws", "dialog", "+44123", 11, "z9hG4bK-b", True)
    assert registry.authorize_invite(
        token, "1", "engine", "ws", "dialog", "+44123", 11, "z9hG4bK-b", True)
    assert registry.observe_invite_response("ws", "dialog", 11, 200)
    assert not registry.authorize_invite(
        token, "1", "engine", "ws", "dialog", "+44123", 11, "z9hG4bK-b", True)


def test_bound_call_survives_token_ttl_but_release_and_result_stop_lease():
    clock = [1.0]
    registry = MediaAdmissionRegistry(clock=lambda: clock[0])
    token = registry.issue("1", "engine", "route")
    assert registry.claim_canary(token, "1", "engine", "ws", "route")
    assert registry.mark_engine(token, "1", "engine")
    assert registry.mark_browser(token, "1", "engine", EVIDENCE)
    assert registry.authorize_invite(
        token, "1", "engine", "ws", "dialog", "+44123", 1, "branch", False)
    assert registry.bind_channel(token, "1", "engine", "171.9")
    clock[0] = 301.0
    assert registry.authorization_active(token, "1", "engine", "171.9")
    assert registry.close_call(token, "1", "171.9")
    assert not registry.authorization_active(token, "1", "engine", "171.9")

    second = registry.issue("1", "engine", "route")
    assert registry.claim_canary(second, "1", "engine", "ws", "route")
    assert registry.mark_engine(second, "1", "engine")
    assert registry.mark_browser(second, "1", "engine", EVIDENCE)
    assert registry.authorize_invite(
        second, "1", "engine", "ws", "dialog-2", "+44123", 1, "branch-2", False)
    assert registry.bind_channel(second, "1", "engine", "171.10")
    released = registry.release_websocket("ws")
    assert released[0]["source_call_id"] == "171.10"
    assert not registry.authorization_active(second, "1", "engine", "171.10")


def test_terminal_result_before_start_fences_late_channel_binding():
    now = [100.0]
    registry = MediaAdmissionRegistry(clock=lambda: now[0])
    token = registry.issue("1", "engine", "route")
    assert registry.claim_canary(token, "1", "engine", "ws", "route")
    assert registry.mark_engine(token, "1", "engine")
    assert registry.mark_browser(token, "1", "engine", EVIDENCE)
    assert registry.authorize_invite(
        token, "1", "engine", "ws", "dialog", "+44123", 1, "branch", False)

    # Independent dialplan hooks may deliver the terminal result first.
    assert registry.close_call(token, "1", "171.11")
    assert not registry.bind_channel(token, "1", "engine", "171.11")
    assert not registry.authorization_active(token, "1", "engine", "171.11")
    # A conflicting late event cannot repurpose this one-shot token either.
    assert not registry.bind_channel(token, "1", "engine", "171.12")

    now[0] += 61
    # Tombstones are bounded; once expired the admission is still gone.
    assert not registry.bind_channel(token, "1", "engine", "171.11")


def test_browser_evidence_schema_is_strict():
    for key in EVIDENCE:
        invalid = dict(EVIDENCE)
        invalid[key] = False if key.endswith("delta") else "true"
        assert not MediaAdmissionRegistry.valid_browser_evidence(invalid)


def test_claimed_and_consumed_entries_expire_without_websocket_disconnect():
    now = [100.0]
    registry = MediaAdmissionRegistry(clock=lambda: now[0])
    for index in range(MAX_ENTRIES):
        token = registry.issue("1", "engine", "route-a")
        assert token
        assert registry.claim_canary(
            token, "1", "engine", "one-long-lived-ws", "route-a")
    assert registry.issue("1", "engine", "route-a") == ""
    now[0] += 31
    assert registry.issue("1", "engine", "route-a")
