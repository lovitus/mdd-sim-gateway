package agenthost

import (
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsim"
)

func TestTopologySeparatesAttachmentSessionAndDurableCardIdentity(t *testing.T) {
	state := &topologyState{}
	state.observe(agentreader.Observation{Condition: agentreader.MonitorReady, Readers: []agentreader.Reader{
		{Name: "reader-c", CardPresent: true, SessionGeneration: "generation-c", ATR: []byte{3}},
		{Name: "reader-a"},
		{Name: "reader-b", CardPresent: true, SessionGeneration: "generation-b", ATR: []byte{2}},
	}})
	topology := state.snapshot([]agentsim.SessionView{
		{ReaderName: "reader-b", SessionGeneration: "generation-b", CardID: "89440001"},
		{ReaderName: "reader-c", SessionGeneration: "generation-c"},
	}, time.Minute)
	if err := topology.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(topology.Readers) != 3 || topology.Readers[0].ReaderName != "reader-a" {
		t.Fatalf("topology order=%+v", topology.Readers)
	}
	if topology.Readers[0].IdentityState != agentlink.CardAbsent ||
		topology.Readers[1].IdentityState != agentlink.CardIdentified ||
		topology.Readers[1].CardID != "89440001" ||
		topology.Readers[2].IdentityState != agentlink.CardIdentityUnavailable {
		t.Fatalf("topology facts=%+v", topology.Readers)
	}
	if topology.Readers[1].ATRSHA256 == "" || topology.Readers[2].ATRSHA256 == "" {
		t.Fatal("present attachments omitted ATR fingerprints")
	}
}

func TestTopologyReportsDiscoveryWithoutGuessingIdentity(t *testing.T) {
	state := &topologyState{}
	state.observe(agentreader.Observation{Condition: agentreader.MonitorReady, Readers: []agentreader.Reader{{
		Name: "reader", CardPresent: true, SessionGeneration: "generation", ATR: []byte{1},
	}}})
	topology := state.snapshot(nil, time.Minute)
	if err := topology.Validate(); err != nil {
		t.Fatal(err)
	}
	if topology.Readers[0].IdentityState != agentlink.CardIdentityDiscovering || topology.Readers[0].CardID != "" {
		t.Fatalf("discovering fact=%+v", topology.Readers[0])
	}
}

func TestTopologyIdentifiesBlankEUICCByEIDWithoutInventingActiveICCID(t *testing.T) {
	state := &topologyState{}
	state.observe(agentreader.Observation{Condition: agentreader.MonitorReady, Readers: []agentreader.Reader{{
		Name: "reader", CardPresent: true, SessionGeneration: "generation", ATR: []byte{1},
	}}})
	topology := state.snapshot([]agentsim.SessionView{{
		ReaderName: "reader", SessionGeneration: "generation",
		EUICC: &agentlink.EUICCFact{
			EID: "89049032000000000000000000000001", ProfilesAvailable: true,
			Profiles: []agentlink.EUICCProfileFact{},
		},
	}}, time.Minute)
	if err := topology.Validate(); err != nil {
		t.Fatal(err)
	}
	reader := topology.Readers[0]
	if reader.IdentityState != agentlink.CardIdentified || reader.CardID != "" ||
		reader.EUICC == nil || reader.EUICC.EID != "89049032000000000000000000000001" ||
		!reader.EUICC.ProfilesAvailable {
		t.Fatalf("blank eUICC topology=%+v", reader)
	}
}

func TestTopologyClearsAttachmentsWhileReaderMonitorRecovers(t *testing.T) {
	state := &topologyState{}
	state.observe(agentreader.Observation{Condition: agentreader.MonitorReady, Readers: []agentreader.Reader{{
		Name: "reader", CardPresent: true, SessionGeneration: "generation",
	}}})
	state.observe(agentreader.Observation{Condition: agentreader.MonitorRecovering, Detail: "PC/SC unavailable"})
	topology := state.snapshot(nil, time.Minute)
	if err := topology.Validate(); err != nil {
		t.Fatal(err)
	}
	if topology.ReaderCondition != agentlink.ReaderRecovering || topology.ReaderDetail != "PC/SC unavailable" || len(topology.Readers) != 0 {
		t.Fatalf("recovering topology=%+v", topology)
	}
}

func TestTopologyMarksAStuckReaderObservationRecovering(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	state := &topologyState{now: func() time.Time { return now }}
	state.observe(agentreader.Observation{Condition: agentreader.MonitorReady, Readers: []agentreader.Reader{{
		Name: "reader", CardPresent: true, SessionGeneration: "generation",
	}}})
	now = now.Add(4 * time.Second)
	topology := state.snapshot(nil, 3*time.Second)
	if topology.ReaderCondition != agentlink.ReaderRecovering || topology.ReaderDetail != "PC/SC observation is stale" || len(topology.Readers) != 0 {
		t.Fatalf("stale topology=%+v", topology)
	}
}
