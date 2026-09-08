package linebootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func TestDefaultClaimUsesExistingStoppedDraftLedger(t *testing.T) {
	now := time.Now().UTC()
	key := []byte("01234567890123456789012345678901")
	template, err := (agentlink.DeviceDefaults{Authority: "core-a", Revision: 1, VoWiFiEnabled: true}).Authorize(key)
	if err != nil {
		t.Fatal(err)
	}
	status := modemStatus(now, "agent-a", "process-a", "attachment-a", "862547055201716", "8944100000000000001", "session-a")
	status.Topology.Modems[0].Policy = &agentlink.ModemPolicyFact{Enrollment: &agentlink.DeviceEnrollment{FirstSeen: now, Initialized: true, Template: &template}}
	facts := &mutableFacts{statuses: []agentlink.ConnectionStatus{status}}
	catalog := testCatalog(t)
	service, err := New(catalog, facts, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Project()
	if err != nil || len(snapshot.Candidates) != 1 {
		t.Fatal("candidate missing", err)
	}
	result, err := service.ClaimDefaultDraft("default-claim-1", snapshot.Candidates[0].CandidateID, snapshot.CatalogRevision, "core-a", key)
	if err != nil {
		t.Fatal(err)
	}
	if result.Line.Enabled || result.Line.HardwareProvisionState != "draft" || result.Line.CardID != status.Topology.Modems[0].SIM.ICCID {
		t.Fatal("default claim did not preserve stopped exact-card draft")
	}
	current, err := catalog.Snapshot()
	if err != nil || len(current.Lines) != 1 {
		t.Fatal("draft was not persisted", err)
	}
	if _, err := service.ClaimDefaultDraft("default-claim-2", snapshot.Candidates[0].CandidateID, current.Revision, "core-a", key); !errors.Is(err, ErrCandidateBlocked) {
		t.Fatal("configured SIM was claimed again", err)
	}
	current, _ = catalog.Snapshot()
	if len(current.Lines) != 1 || current.Lines[0].Enabled {
		t.Fatal("repeat changed existing line")
	}
	if err := service.ReconcileDefaultDrafts(context.Background(), "core-a", key); err != nil {
		t.Fatal(err)
	}
	after, _ := catalog.Snapshot()
	if after.Revision != current.Revision {
		t.Fatal("reconciliation modified an existing draft")
	}
	secondCatalog := testCatalog(t)
	secondService, err := New(secondCatalog, facts, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := secondService.ReconcileDefaultDrafts(context.Background(), "core-a", key); err != nil {
		t.Fatal(err)
	}
	created, err := secondCatalog.Snapshot()
	if err != nil || len(created.Lines) != 1 || created.Lines[0].Enabled || created.Lines[0].HardwareProvisionState != "draft" {
		t.Fatal("coordinator failed to create only a stopped draft", err)
	}
}

func TestDefaultDraftRequiresCoreIntentAndNewInitializedHardware(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	template, err := (agentlink.DeviceDefaults{Authority: "core-a", Revision: 1, VoWiFiEnabled: true}).Authorize(key)
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{Kind: "modem", Enrollment: &agentlink.DeviceEnrollment{FirstSeen: time.Now(), Initialized: true, Template: &template}}
	if !defaultClaimAuthorized(candidate, "core-a", key) {
		t.Fatal("authorized new device rejected")
	}
	for _, mutate := range []func(*Candidate){
		func(c *Candidate) { c.Kind = "reader" },
		func(c *Candidate) { c.Enrollment.Protected = true },
		func(c *Candidate) { c.Enrollment.Initialized = false },
		func(c *Candidate) { c.Enrollment.Template.VoWiFiEnabled = false },
		func(c *Candidate) { c.Enrollment.Template.Authority = "other" },
		func(c *Candidate) { c.Enrollment.Template.Authorization = "" },
	} {
		copy := candidate
		copy.Enrollment = candidate.Enrollment.Clone()
		mutate(&copy)
		if defaultClaimAuthorized(copy, "core-a", key) {
			t.Fatal("unapproved default claimed a draft")
		}
	}
}

func TestModemIdentityUsesOnlyReportedMNCLength(t *testing.T) {
	for _, tc := range []struct {
		length int
		mnc    string
	}{{0, ""}, {2, "10"}, {3, "100"}, {4, ""}} {
		fact := agentlink.ModemFact{SIM: agentlink.ModemSIMFact{IMSI: "234100000000001", MNCLength: tc.length}}
		identity := modemIdentity(fact)
		if identity.MCC != "234" || identity.MNC != tc.mnc {
			t.Fatal("MNC was guessed or truncated", tc.length, identity.MNC)
		}
	}
}

func TestDefaultClaimIdentitySurvivesAgentReconnect(t *testing.T) {
	candidate := Candidate{AgentID: "agent", EquipmentID: "equipment", CardID: "card", CandidateID: "old", ProcessGeneration: "old", SessionGeneration: "old",
		Enrollment: &agentlink.DeviceEnrollment{FirstSeen: time.Now().UTC()}}
	want := defaultClaimID(candidate, "authority")
	candidate.CandidateID = "new"
	candidate.ProcessGeneration = "new"
	candidate.SessionGeneration = "new"
	if defaultClaimID(candidate, "authority") != want {
		t.Fatal("reconnect lost automatic draft ownership")
	}
	candidate.CardID = "replacement"
	if defaultClaimID(candidate, "authority") == want {
		t.Fatal("different SIM reused automatic draft ownership")
	}
}
