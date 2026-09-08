package agentlink

import (
	"context"
	"testing"
)

func TestEUICCRefreshDoesNotRequireInstalledProfile(t *testing.T) {
	request := EUICCProfileRequest{OperationID: "refresh-1", SessionGeneration: "session-1", EID: "89049032000000000000000000000001", Action: EUICCProfileRefresh}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	response := EUICCProfileResponse{OperationID: request.OperationID, SessionGeneration: request.SessionGeneration, EID: request.EID, Action: request.Action,
		Outcome: EUICCProfileRefreshed, Inventory: &EUICCFact{EID: request.EID, ProfilesAvailable: true, InventoryRefresh: true, Profiles: []EUICCProfileFact{}}}
	if err := response.ValidateFor(request); err != nil {
		t.Fatal(err)
	}
	response.Changed = true
	if response.ValidateFor(request) == nil {
		t.Fatal("read-only refresh claimed a mutation")
	}
	response.Changed = false
	response.Inventory.EID = "89049032000000000000000000000002"
	if response.ValidateFor(request) == nil {
		t.Fatal("different chip accepted")
	}
	request.ICCID = "8944000000000000001"
	if request.Validate() == nil {
		t.Fatal("refresh accepted profile mutation identity")
	}
}

func TestRefreshResolverAcceptsBlankChipOnlyWithRefreshCapability(t *testing.T) {
	server, err := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	if err != nil {
		t.Fatal(err)
	}
	fact := &EUICCFact{EID: "89049032000000000000000000000001", InventoryRefresh: true, Profiles: []EUICCProfileFact{}}
	server.agents["agent"] = &serverConnection{hello: Hello{SchemaVersion: SchemaVersion, AgentID: "agent", ProcessGeneration: "process"},
		topology: &TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{ReaderName: "blank", CardPresent: true, SessionGeneration: "session", IdentityState: CardIdentified, EUICC: fact}}}}
	target, err := server.ResolveEUICCProfileTarget(fact.EID, "")
	if err != nil || target.AgentID != "agent" || target.SessionGeneration != "session" {
		t.Fatal(target, err)
	}
	fact.InventoryRefresh = false
	if _, err := server.ResolveEUICCProfileTarget(fact.EID, ""); err == nil {
		t.Fatal("legacy Agent received unsupported refresh")
	}
}
