package linebootstrap

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type mutableFacts struct{ statuses []agentlink.ConnectionStatus }

func (facts *mutableFacts) Statuses() []agentlink.ConnectionStatus {
	return append([]agentlink.ConnectionStatus(nil), facts.statuses...)
}

func testCatalog(t *testing.T) *linecatalog.Store {
	t.Helper()
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func modemStatus(now time.Time, agentID, process, attachment, equipment, card, session string) agentlink.ConnectionStatus {
	return agentlink.ConnectionStatus{
		AgentID: agentID, ProcessGeneration: process, LastReport: now,
		Topology: &agentlink.TopologySnapshot{
			ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{},
			ModemCondition: agentlink.ModemReady, Modems: []agentlink.ModemFact{{
				AttachmentID: attachment, EquipmentID: equipment, Condition: "ready",
				AT: agentlink.ModemATControlFact{State: "ready", Port: "COM3", CallSignalling: true},
				SIM: agentlink.ModemSIMFact{
					State: "ready", SessionGeneration: session, ICCID: card, IMSI: "454120123456789",
					MSISDNs: []string{"+15550100124", "+15550100124"}, SMSC: "+15550100125",
				},
				Network: agentlink.ModemNetworkFact{
					Registration: "unknown", SoftwareRadio: "unknown", HardwareRadio: "unknown", Data: "unknown",
				},
			}},
		},
	}
}

func requireValidTopologies(t *testing.T, statuses []agentlink.ConnectionStatus) {
	t.Helper()
	for _, status := range statuses {
		if status.Topology == nil {
			t.Fatalf("Agent %s has no topology", status.AgentID)
		}
		if err := status.Topology.Validate(); err != nil {
			t.Fatalf("Agent %s topology does not satisfy the wire contract: %v", status.AgentID, err)
		}
	}
}

func readerStatus(now time.Time, agentID, process, reader, card, session string) agentlink.ConnectionStatus {
	return agentlink.ConnectionStatus{
		AgentID: agentID, ProcessGeneration: process, LastReport: now,
		Topology: &agentlink.TopologySnapshot{
			ReaderCondition: agentlink.ReaderReady,
			Readers: []agentlink.ReaderFact{{ReaderName: reader, CardPresent: true,
				IdentityState: agentlink.CardIdentified, SessionGeneration: session, CardID: card}},
			ModemCondition: agentlink.ModemDisabled, Modems: []agentlink.ModemFact{},
		},
	}
}

func TestProjectUsesOnlyFreshExactCurrentAttachments(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := testCatalog(t)
	existing := linecatalog.Line{SchemaVersion: 1, ID: "existing", Name: "existing", Enabled: false,
		CardID: "89010000000000000001"}
	if _, err := store.Put(existing); err != nil {
		t.Fatal(err)
	}
	facts := &mutableFacts{statuses: []agentlink.ConnectionStatus{
		readerStatus(now, "reader-agent", "process-r", "reader-a", existing.CardID, "session-r"),
		modemStatus(now, "modem-agent", "process-m", "attachment-m", "862547055201716", "89010000000000000002", "session-m"),
		readerStatus(now.Add(-31*time.Second), "stale-agent", "process-s", "reader-s", "89010000000000000003", "session-s"),
	}}
	service, err := New(store, facts, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Project()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CatalogRevision != 2 || len(snapshot.Candidates) != 2 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	var configured, modem *Candidate
	for index := range snapshot.Candidates {
		candidate := &snapshot.Candidates[index]
		if candidate.CardID == existing.CardID {
			configured = candidate
		} else {
			modem = candidate
		}
	}
	if configured == nil || configured.Condition != "configured" || configured.CanClaim || configured.ConfiguredLineID != existing.ID {
		t.Fatalf("configured=%+v", configured)
	}
	if modem == nil || modem.Mode != "adapted" || modem.Condition != "identity_incomplete" || !modem.CanClaim ||
		modem.Observed.IMSI != "454120123456789" || modem.Observed.MCC != "454" || modem.Observed.MNC != "" ||
		modem.Observed.IMEI != "862547055201716" || modem.Observed.MSISDN != "+15550100124" ||
		modem.Raw == nil || modem.Raw.Available || modem.Raw.Code != "raw_isolation_unproven" {
		t.Fatalf("modem=%+v", modem)
	}
}

func TestClaimCreatesOnlyDisabledDraftWithoutRuntimeIntent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := testCatalog(t)
	facts := &mutableFacts{statuses: []agentlink.ConnectionStatus{
		modemStatus(now, "agent-a", "process-a", "attachment-a", "862547055201716", "89010000000000000001", "session-a"),
	}}
	service, _ := New(store, facts, func() time.Time { return now })
	service.random = bytes.NewReader(bytes.Repeat([]byte{0x2a}, 12))
	snapshot, err := service.Project()
	if err != nil || len(snapshot.Candidates) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	result, err := service.Claim(snapshot.Candidates[0].CandidateID, "香港测试卡", snapshot.CatalogRevision)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 2 || result.Line.ID != "line-"+strings.Repeat("2a", 12) || result.Line.Enabled ||
		result.Line.CardID != "89010000000000000001" || result.Line.SIM.IMSI != "454120123456789" ||
		result.Line.SIM.MCC != "454" || result.Line.SIM.MNC != "" || result.Line.SIM.IMEI != "862547055201716" {
		t.Fatalf("result=%+v", result)
	}
	if enabled, found, _, err := store.RuntimeIntent(result.Line.ID); err != nil || found || enabled {
		t.Fatalf("runtime intent enabled=%t found=%t err=%v", enabled, found, err)
	}
	stored, err := store.Snapshot()
	if err != nil || len(stored.Lines) != 1 || stored.Lines[0].Enabled {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestClaimRejectsChangedOrAmbiguousCurrentIdentityWithoutWrite(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := testCatalog(t)
	facts := &mutableFacts{statuses: []agentlink.ConnectionStatus{
		modemStatus(now, "agent-a", "process-a", "attachment-a", "862547055201716", "89010000000000000001", "session-a"),
	}}
	service, _ := New(store, facts, func() time.Time { return now })
	snapshot, _ := service.Project()
	original := snapshot.Candidates[0].CandidateID
	facts.statuses[0].ProcessGeneration = "process-replaced"
	if result, err := service.Claim(original, "stale", 1); !errors.Is(err, ErrCandidateStale) || result.Revision != 1 {
		t.Fatalf("stale result=%+v err=%v", result, err)
	}

	facts.statuses = []agentlink.ConnectionStatus{
		modemStatus(now, "agent-a", "process-a", "attachment-a", "862547055201716", "89010000000000000001", "session-a"),
		readerStatus(now, "agent-b", "process-b", "reader-b", "89010000000000000001", "session-b"),
	}
	snapshot, _ = service.Project()
	if len(snapshot.Candidates) != 2 {
		t.Fatalf("candidates=%+v", snapshot.Candidates)
	}
	for _, candidate := range snapshot.Candidates {
		if candidate.Condition != "ambiguous_card" || candidate.CanClaim {
			t.Fatalf("candidate=%+v", candidate)
		}
		if candidate.ProvisionState != "blocked" || len(candidate.ProvisionBlockers) != 1 || candidate.ProvisionBlockers[0] != "identity_ambiguous" {
			t.Fatalf("ambiguous provision state=%+v", candidate)
		}
		if _, err := service.Claim(candidate.CandidateID, "ambiguous", 1); !errors.Is(err, ErrCandidateBlocked) {
			t.Fatalf("claim err=%v", err)
		}
	}
	stored, err := store.Snapshot()
	if err != nil || stored.Revision != 1 || len(stored.Lines) != 0 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestIncompleteDuplicateModemStillMakesICCIDGloballyAmbiguous(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := testCatalog(t)
	complete := modemStatus(now, "agent-a", "process-a", "attachment-a", "862547055201716", "89010000000000000001", "session-a")
	incomplete := modemStatus(now, "agent-b", "process-b", "attachment-b", "", "89010000000000000001", "")
	incomplete.Topology.Modems[0].AT = agentlink.ModemATControlFact{State: "unknown"}
	incomplete.Topology.Modems[0].Capabilities = agentlink.ModemCapabilities{}
	facts := &mutableFacts{statuses: []agentlink.ConnectionStatus{complete, incomplete}}
	requireValidTopologies(t, facts.statuses)
	service, _ := New(store, facts, func() time.Time { return now })

	snapshot, err := service.Project()
	if err != nil || len(snapshot.Candidates) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	candidate := snapshot.Candidates[0]
	if candidate.Condition != "ambiguous_card" || candidate.CanClaim {
		t.Fatalf("candidate=%+v", candidate)
	}
	if _, err := service.Claim(candidate.CandidateID, "must not be created", snapshot.CatalogRevision); !errors.Is(err, ErrCandidateBlocked) {
		t.Fatalf("claim err=%v", err)
	}
	stored, err := store.Snapshot()
	if err != nil || stored.Revision != 1 || len(stored.Lines) != 0 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestCandidateIDFencesEveryCurrentAttachmentIdentity(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	modemMutations := map[string]func(*agentlink.ConnectionStatus){
		"Agent":      func(status *agentlink.ConnectionStatus) { status.AgentID = "agent-b" },
		"process":    func(status *agentlink.ConnectionStatus) { status.ProcessGeneration = "process-b" },
		"attachment": func(status *agentlink.ConnectionStatus) { status.Topology.Modems[0].AttachmentID = "attachment-b" },
		"equipment":  func(status *agentlink.ConnectionStatus) { status.Topology.Modems[0].EquipmentID = "862547055201717" },
		"card":       func(status *agentlink.ConnectionStatus) { status.Topology.Modems[0].SIM.ICCID = "89010000000000000002" },
		"SIM session": func(status *agentlink.ConnectionStatus) {
			status.Topology.Modems[0].SIM.SessionGeneration = "session-b"
		},
	}
	for name, mutate := range modemMutations {
		t.Run("modem "+name, func(t *testing.T) {
			store := testCatalog(t)
			status := modemStatus(now, "agent-a", "process-a", "attachment-a", "862547055201716", "89010000000000000001", "session-a")
			facts := &mutableFacts{statuses: []agentlink.ConnectionStatus{status}}
			service, _ := New(store, facts, func() time.Time { return now })
			snapshot, _ := service.Project()
			mutate(&facts.statuses[0])
			if _, err := service.Claim(snapshot.Candidates[0].CandidateID, "stale", 1); !errors.Is(err, ErrCandidateStale) {
				t.Fatalf("claim err=%v", err)
			}
		})
	}
	readerMutations := map[string]func(*agentlink.ConnectionStatus){
		"Agent":        func(status *agentlink.ConnectionStatus) { status.AgentID = "agent-b" },
		"process":      func(status *agentlink.ConnectionStatus) { status.ProcessGeneration = "process-b" },
		"reader":       func(status *agentlink.ConnectionStatus) { status.Topology.Readers[0].ReaderName = "reader-b" },
		"card":         func(status *agentlink.ConnectionStatus) { status.Topology.Readers[0].CardID = "89010000000000000002" },
		"card session": func(status *agentlink.ConnectionStatus) { status.Topology.Readers[0].SessionGeneration = "session-b" },
	}
	for name, mutate := range readerMutations {
		t.Run("reader "+name, func(t *testing.T) {
			store := testCatalog(t)
			status := readerStatus(now, "agent-a", "process-a", "reader-a", "89010000000000000001", "session-a")
			facts := &mutableFacts{statuses: []agentlink.ConnectionStatus{status}}
			service, _ := New(store, facts, func() time.Time { return now })
			snapshot, _ := service.Project()
			mutate(&facts.statuses[0])
			if _, err := service.Claim(snapshot.Candidates[0].CandidateID, "stale", 1); !errors.Is(err, ErrCandidateStale) {
				t.Fatalf("claim err=%v", err)
			}
		})
	}
}
