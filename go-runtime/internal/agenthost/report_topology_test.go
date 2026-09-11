package agenthost

import (
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func TestBadReaderPayloadDoesNotDisconnectHealthyReader(t *testing.T) {
	bad := agentlink.ReaderFact{ReaderName: "a", CardPresent: true, SessionGeneration: "session-a",
		CardID: "invalid", IdentityState: agentlink.CardIdentified}
	good := agentlink.ReaderFact{ReaderName: "b", CardPresent: true, SessionGeneration: "session-b",
		CardID: "89440002", IdentityState: agentlink.CardIdentified}
	source := agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{bad, good}}
	result := reportTopology(source)
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(result.Readers) != 2 || result.Readers[0].IdentityState != agentlink.CardIdentityUnavailable ||
		result.Readers[0].CardID != "" || result.Readers[0].IdentityDetail == "" || result.Readers[1].CardID != good.CardID {
		t.Fatalf("wrong isolation: %+v", result)
	}
	if source.Readers[0].CardID != "invalid" {
		t.Fatal("diagnostic evidence overwritten")
	}
	source.Readers[0].CardID = "89440001"
	recovered := reportTopology(source)
	if recovered.Readers[0].IdentityState != agentlink.CardIdentified || recovered.Readers[0].CardID != "89440001" {
		t.Fatal("fresh valid observation did not recover automatically")
	}
}

func TestInvalidReaderFenceIsRecoveringNotEmptyReady(t *testing.T) {
	source := agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{{
		ReaderName: "reader", CardPresent: true, IdentityState: agentlink.CardIdentified,
	}}}
	result := reportTopology(source)
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if result.ReaderCondition != agentlink.ReaderRecovering || result.ReaderDetail == "" || len(result.Readers) != 0 {
		t.Fatalf("invalid fence misreported: %+v", result)
	}
}
