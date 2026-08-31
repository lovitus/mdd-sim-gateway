package agentlink

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fixedModemAdmission struct{ required string }

func (admission fixedModemAdmission) RequiredModemAgent(string, string,
	[]ConnectionStatus) (string, bool, error) {
	return admission.required, true, nil
}

func (admission fixedModemAdmission) RequiredCardAgent(string,
	[]ConnectionStatus) (string, bool, error) {
	return admission.required, true, nil
}

func TestBoundModemNeverFallsBackToSourceOrThirdAgent(t *testing.T) {
	server, err := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) {
		return testToken, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetModemRouteAdmission(fixedModemAdmission{required: "importer-agent"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, agentID := range []string{"source-agent", "third-agent"} {
		topology := modemAdmissionTopology()
		server.agents[agentID] = &serverConnection{
			hello:       Hello{SchemaVersion: SchemaVersion, AgentID: agentID, ProcessGeneration: agentID + "-process"},
			connectedAt: now, lastReport: now, topology: &topology,
		}
	}
	commands := []func() error{
		func() error {
			_, err := server.ExecuteModemCommand(context.Background(), ModemCommand{
				OperationID: "bound-call", EquipmentID: "862547055201716", CardID: "8985200000000000001",
				Action: ModemCallDial, LeaseID: "bound-call-lease", Number: "+15550100123",
			})
			return err
		},
		func() error {
			_, err := server.ExecuteModemMediaCommand(context.Background(), ModemMediaCommand{
				OperationID: "bound-media", EquipmentID: "862547055201716", CardID: "8985200000000000001",
				Action: ModemMediaPrepare, SessionID: "bound-media-session", MediaToken: testToken,
			})
			return err
		},
		func() error {
			_, err := server.ExecuteModemDataCommand(context.Background(), ModemDataCommand{
				OperationID: "bound-data", EquipmentID: "862547055201716", CardID: "8985200000000000001",
				Action: ModemDataPrepare, SessionID: "bound-data-session", ExpiresAt: now.Add(time.Minute), MaxBytes: 1024,
			})
			return err
		},
	}
	for index, execute := range commands {
		if err := execute(); !errors.Is(err, ErrModemOffline) {
			t.Fatalf("command %d error=%v", index, err)
		}
	}
}

func TestBoundModemAKANeverFallsBackToSourceOrReader(t *testing.T) {
	server, err := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) {
		return testToken, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetModemRouteAdmission(fixedModemAdmission{required: "importer-agent"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	modem := modemAdmissionTopology()
	modem.Modems[0].AT.SIMAPDU = true
	reader := TopologySnapshot{
		ReaderCondition: ReaderReady,
		Readers: []ReaderFact{{
			ReaderName: "reader-a", CardPresent: true, SessionGeneration: "reader-generation",
			CardID: "8985200000000000001", IdentityState: CardIdentified,
		}},
	}
	server.agents["source-agent"] = &serverConnection{
		hello:       Hello{SchemaVersion: SchemaVersion, AgentID: "source-agent", ProcessGeneration: "source-process"},
		connectedAt: now, lastReport: now, topology: &modem,
	}
	server.agents["reader-agent"] = &serverConnection{
		hello:       Hello{SchemaVersion: SchemaVersion, AgentID: "reader-agent", ProcessGeneration: "reader-process"},
		connectedAt: now, lastReport: now, topology: &reader,
	}
	_, err = server.AuthenticateCardAKA(context.Background(), AKAChallenge{
		OperationID: "bound-aka", CardID: "8985200000000000001",
		Application: AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
	})
	if !errors.Is(err, ErrCardOffline) {
		t.Fatalf("AKA error=%v", err)
	}
}

func modemAdmissionTopology() TopologySnapshot {
	return TopologySnapshot{
		ModemCondition: ModemReady,
		Modems: []ModemFact{{
			AttachmentID: "attachment", EquipmentID: "862547055201716", Condition: "ready",
			Capabilities: ModemCapabilities{CellularData: true},
			AT:           ModemATControlFact{State: "ready", CallSignalling: true, SMS: true},
			SIM:          ModemSIMFact{State: "ready", ICCID: "8985200000000000001"},
			Network:      ModemNetworkFact{Data: "disconnected", DataGuard: "protected"},
		}},
	}
}
