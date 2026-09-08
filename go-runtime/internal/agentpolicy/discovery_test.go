package agentpolicy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

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
