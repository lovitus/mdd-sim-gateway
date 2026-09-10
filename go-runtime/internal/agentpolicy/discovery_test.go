package agentpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestInitializedDefaultsRemainEligibleAcrossObservationAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	const equipment, card = "867530900000002", "8944100000000000001"
	now := time.Unix(1000, 0)
	defaults := InitialDefaults{Authority: "fixture", Revision: 7, VoWiFiEnabled: true}
	if _, err := store.ObserveEquipment(nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.observeEquipment([]string{equipment}, nil, now, &defaults); err != nil {
		t.Fatal(err)
	}
	policy, created, err := store.initializeDiscoveredPolicy(equipment, card, defaults)
	if err != nil || !created {
		t.Fatal("initialization failed", err)
	}
	check := func(protected bool) {
		t.Helper()
		records, err := store.observeEquipment([]string{equipment}, nil, now.Add(time.Hour), &defaults)
		if err != nil || len(records) != 1 || records[0].Baseline != protected || records[0].InitializedCard != card || !records[0].FirstSeen.Equal(now) {
			t.Fatal("enrollment provenance changed", records, err)
		}
		actual, found, err := store.Get(equipment, card)
		if err != nil || !found || actual != policy {
			t.Fatal("observation rewrote policy", err)
		}
	}
	check(false)
	// Reproduce the legacy bug: an auto-created revision-one policy was marked baseline.
	if err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(discoveryBucket)
		var record Discovery
		if err := json.Unmarshal(bucket.Get([]byte(equipment)), &record); err != nil {
			return err
		}
		record.Baseline = true
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(equipment), payload)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := store.RepairDefaultEnrollment(equipment, card, now.Add(time.Second)); err == nil {
		t.Fatal("wrong first-seen accepted")
	}
	if err := store.RepairDefaultEnrollment(equipment, "8944100000000000002", now); err == nil {
		t.Fatal("wrong card accepted")
	}
	if err := store.RepairDefaultEnrollment(equipment, card, now); err != nil {
		t.Fatal(err)
	}
	check(false)
	// An explicit no-op save is still a user choice and must block auto enrollment.
	policy, err = store.PutExpected(policy, policy.Revision)
	if err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := store.RepairDefaultEnrollment(equipment, card, now); err == nil {
		t.Fatal("explicit user policy repair accepted")
	}
}

func TestExplicitModeProtectionSurvivesRemovalAfterInitialization(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "policy.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const equipment, card = "867530900000002", "8944100000000000001"
	now := time.Unix(1000, 0)
	defaults := InitialDefaults{Authority: "fixture", Revision: 1, VoWiFiEnabled: true}
	if _, err := store.ObserveEquipment(nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.observeEquipment([]string{equipment}, nil, now, &defaults); err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.initializeDiscoveredPolicy(equipment, card, defaults); err != nil || !created {
		t.Fatal(err)
	}
	if _, err := store.observeEquipment([]string{equipment}, []string{equipment}, now, &defaults); err != nil {
		t.Fatal(err)
	}
	records, err := store.observeEquipment([]string{equipment}, nil, now, &defaults)
	if err != nil || len(records) != 1 || !records[0].Baseline || !records[0].ExplicitlyProtected {
		t.Fatal("explicit mode protection lost", records, err)
	}
}

func TestInitializedPolicyRequiresUnchangedOriginalCardAndIntent(t *testing.T) {
	d := Discovery{EquipmentID: "867530900000002", InitializedCard: "8944100000000000001",
		Defaults: &InitialDefaults{Authority: "fixture", Revision: 1, VoWiFiEnabled: true}}
	p := Default(d.EquipmentID, d.InitializedCard)
	p.Revision = 1
	if !initializedPolicy(d, p) {
		t.Fatal("initial policy not recognized")
	}
	for _, change := range []func(*Policy){
		func(p *Policy) { p.CardID = "8944100000000000002" },
		func(p *Policy) { p.EquipmentID = "867530900000003" },
		func(p *Policy) { p.Revision++ },
		func(p *Policy) { p.Desired.CellularEnabled = true },
		func(p *Policy) { p.Desired.ConnectionEnabled = true },
		func(p *Policy) { p.Desired.SelectedProfile = "user" },
	} {
		changed := p
		change(&changed)
		if initializedPolicy(d, changed) {
			t.Fatal("changed user policy treated as initialization")
		}
	}
}

func TestDiscoveryBaselineReconnectAndPolicyProtection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const existing, newcomer, disconnected = "867530900000001", "867530900000002", "867530900000003"
	now := time.Unix(1000, 0)
	first, err := store.ObserveEquipment([]string{existing}, []string{disconnected}, now)
	if err != nil || len(first) != 1 || !first[0].Baseline {
		t.Fatal("existing fleet was treated as new", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ObserveEquipment([]string{newcomer, existing, disconnected}, nil, now.Add(time.Hour))
	if err != nil || len(second) != 3 || !second[0].Baseline || second[1].Baseline || !second[2].Baseline || !second[0].FirstSeen.Equal(now) {
		t.Fatal("reopen or disconnected explicit choice lost baseline", err)
	}
	policy := Default(newcomer, "8944100000000000001")
	if _, err := store.PutExpected(policy, 0); err != nil {
		t.Fatal(err)
	}
	third, err := store.ObserveEquipment([]string{newcomer}, nil, now.Add(2*time.Hour))
	if err != nil || len(third) != 1 || !third[0].Baseline || !third[0].FirstSeen.Equal(now.Add(time.Hour)) {
		t.Fatal("explicit policy failed to protect discovered hardware", err)
	}
}

func TestInventoryRecordsDiscoveryWithoutCreatingDevicePolicy(t *testing.T) {
	manager, runtime, _ := testManager(t)
	facts, err := runtime.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager.ReconcilePolicies(context.Background(), facts)
	record, found, err := manager.DiscoveryFor(facts[0].EquipmentID)
	if err != nil || !found || !record.Baseline {
		t.Fatal("successful inventory did not establish baseline", err)
	}
	if _, found, err := manager.config.Store.Get(facts[0].EquipmentID, facts[0].SIM.ICCID); err != nil || found || runtime.radioCalls != 0 {
		t.Fatal("discovery created or applied a policy", err)
	}
	manager.config.ProtectedEquipment = func() ([]string, error) { return nil, errors.New("mode store unavailable") }
	manager.ReconcilePolicies(context.Background(), facts)
	if _, found, err := manager.DiscoveryFor(facts[0].EquipmentID); err == nil || found {
		t.Fatal("stale discovery remained authoritative")
	}
}

func TestDiscoveryFailureDoesNotBlockExistingRadioIntent(t *testing.T) {
	manager, runtime, _ := testManager(t)
	facts, err := runtime.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	policy := Default(facts[0].EquipmentID, facts[0].SIM.ICCID)
	policy.Desired.FlightMode = true
	if _, err := manager.config.Store.PutExpected(policy, 0); err != nil {
		t.Fatal(err)
	}
	manager.config.ProtectedEquipment = func() ([]string, error) { return nil, errors.New("fixture discovery failure") }
	manager.ReconcilePolicies(context.Background(), facts)
	if runtime.radioCalls != 1 {
		t.Fatal("discovery failure prevented established policy reconciliation")
	}
	if _, found, err := manager.DiscoveryFor(facts[0].EquipmentID); err == nil || found {
		t.Fatal("failed discovery authorized enrollment")
	}
}

func TestInvalidInventoryDoesNotConsumeDiscoveryBaseline(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "policy.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.ObserveEquipment([]string{"invalid"}, nil, time.Now()); err == nil {
		t.Fatal("invalid identity accepted")
	}
	records, err := store.ObserveEquipment([]string{"867530900000001"}, nil, time.Now())
	if err != nil || len(records) != 1 || !records[0].Baseline {
		t.Fatal("invalid inventory consumed baseline", err)
	}
}

func TestDefaultsFreezeAtFirstDiscoveryWithoutRetroactiveApplication(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "policy.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1000, 0)
	const baseline, without, with = "867530900000001", "867530900000002", "867530900000003"
	if _, err := store.ObserveEquipment([]string{baseline}, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ObserveEquipment([]string{without}, nil, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	defaults := InitialDefaults{Authority: "core-fixture", Revision: 2, VoWiFiEnabled: true}
	first, err := store.observeEquipment([]string{baseline, without, with}, nil, now.Add(2*time.Minute), &defaults)
	if err != nil || len(first) != 3 || first[0].Defaults != nil || first[1].Defaults != nil || first[2].Defaults == nil || *first[2].Defaults != defaults {
		t.Fatal("defaults were retrospectively applied or lost", err)
	}
	defaults.Revision = 3
	defaults.ConnectionEnabled = true
	second, err := store.observeEquipment([]string{with}, nil, now.Add(3*time.Minute), &defaults)
	if err != nil || second[0].Defaults.Revision != 2 || second[0].Defaults.ConnectionEnabled {
		t.Fatal("new settings overwrote frozen intent", err)
	}
	second[0].Defaults.Revision = 99
	third, err := store.observeEquipment([]string{with}, nil, now.Add(4*time.Minute), nil)
	if err != nil || third[0].Defaults.Revision != 2 {
		t.Fatal("returned intent aliased durable state", err)
	}
}

func TestDefaultBindingRejectsSameRevisionConflictAndClonesInput(t *testing.T) {
	manager, _, _ := testManager(t)
	defaults := InitialDefaults{Authority: "core-fixture", Revision: 2, VoWiFiEnabled: true}
	if err := manager.BindInitialDefaults(&defaults); err != nil {
		t.Fatal(err)
	}
	defaults.ConnectionEnabled = true
	if err := manager.BindInitialDefaults(&defaults); err == nil {
		t.Fatal("same revision changed intent")
	}
	defaults.Revision = 1
	if err := manager.BindInitialDefaults(&defaults); err == nil {
		t.Fatal("stale template accepted")
	}
	if manager.initialDefaults.ConnectionEnabled {
		t.Fatal("caller mutation changed bound template")
	}
	if err := manager.BindInitialDefaults(nil); err != nil {
		t.Fatal(err)
	}
	if manager.initialDefaults != nil {
		t.Fatal("session template remained after unbind")
	}
}

func TestFrozenDefaultsInitializeActualPolicyOnceWithoutBorrowingOrSIMReplacement(t *testing.T) {
	manager, runtime, _ := testManager(t)
	manager.ReconcilePolicies(context.Background(), nil)
	if err := manager.BindInitialDefaults(&InitialDefaults{Authority: "fixture", Revision: 1, FlightMode: true}); err != nil {
		t.Fatal(err)
	}
	facts, err := runtime.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager.ReconcilePolicies(context.Background(), facts)
	policy, found, err := manager.config.Store.Get(facts[0].EquipmentID, facts[0].SIM.ICCID)
	if err != nil || !found || policy.Revision != 1 || !policy.Desired.FlightMode || policy.Desired.CellularEnabled || runtime.radioCalls != 1 {
		t.Fatal("initial default did not use normal radio policy or enabled borrowing", err)
	}
	facts, err = runtime.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager.ReconcilePolicies(context.Background(), facts)
	if runtime.radioCalls != 1 {
		t.Fatal("initialized policy was repeated")
	}
	runtime.mu.Lock()
	runtime.facts[0].SIM.ICCID = "8985200000000000002"
	runtime.facts[0].SIM.SessionGeneration = "replacement"
	runtime.mu.Unlock()
	facts, err = runtime.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager.ReconcilePolicies(context.Background(), facts)
	if _, found, err := manager.config.Store.Get(facts[0].EquipmentID, facts[0].SIM.ICCID); err != nil || found {
		t.Fatal("SIM replacement inherited new-device policy", err)
	}
}

func TestLocalModeProtectionWinsBeforeDefaultInitialization(t *testing.T) {
	manager, runtime, _ := testManager(t)
	manager.ReconcilePolicies(context.Background(), nil)
	if err := manager.BindInitialDefaults(&InitialDefaults{Authority: "fixture", Revision: 1, FlightMode: true}); err != nil {
		t.Fatal(err)
	}
	facts, err := runtime.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	manager.config.ProtectedEquipment = func() ([]string, error) {
		calls++
		if calls > 1 {
			return []string{facts[0].EquipmentID}, nil
		}
		return nil, nil
	}
	manager.ReconcilePolicies(context.Background(), facts)
	if _, found, err := manager.config.Store.Get(facts[0].EquipmentID, facts[0].SIM.ICCID); err != nil || found || runtime.radioCalls != 0 {
		t.Fatal("new local mode selection was overwritten by enrollment", err)
	}
}

func TestNewInstallationWaitsForTemplateAndInitializesFirstInventory(t *testing.T) {
	manager, runtime, _ := testManager(t)
	manager.config.InitialInventoryDefaults = true
	now := time.Unix(2000, 0)
	manager.config.Now = func() time.Time { return now }
	facts, err := runtime.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager.ReconcilePolicies(context.Background(), facts)
	if established, err := manager.config.Store.discoveryBaselineEstablished(); err != nil || established {
		t.Fatal("pre-auth scan consumed new installation baseline", err)
	}
	if runtime.radioCalls != 0 {
		t.Fatal("new install operated before Core intent")
	}
	if err := manager.BindInitialDefaults(&InitialDefaults{Authority: "fixture", Revision: 1, FlightMode: true}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	manager.ReconcilePolicies(context.Background(), facts)
	if _, found, err := manager.config.Store.Get(facts[0].EquipmentID, facts[0].SIM.ICCID); err != nil || !found || runtime.radioCalls != 1 {
		t.Fatal("new installation first inventory ignored defaults", err)
	}
	manager.ClearInitialDefaults()
	if manager.defaultsReceived || manager.initialDefaults != nil {
		t.Fatal("disconnection retained session state")
	}
}

func TestNewInstallationWithoutConfiguredDefaultsDoesNotWaitForever(t *testing.T) {
	manager, runtime, _ := testManager(t)
	manager.config.InitialInventoryDefaults = true
	if err := manager.BindInitialDefaults(nil); err != nil {
		t.Fatal(err)
	}
	facts, err := runtime.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager.ReconcilePolicies(context.Background(), facts)
	if established, err := manager.config.Store.discoveryBaselineEstablished(); err != nil || !established {
		t.Fatal("unconfigured Core did not establish inventory", err)
	}
	if _, found, err := manager.config.Store.Get(facts[0].EquipmentID, facts[0].SIM.ICCID); err != nil || found || runtime.radioCalls != 0 {
		t.Fatal("missing defaults created policy", err)
	}
}
