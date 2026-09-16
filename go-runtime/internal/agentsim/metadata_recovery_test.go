package agentsim

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func metadataRecoveryFixture(t *testing.T) (*Manager, *session, *fakeCard) {
	t.Helper()
	manager, err := NewManager(fakeConnector{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	card := scriptedCard("8944000000000000001", pinAlreadyVerified)
	original := card.handler
	card.handler = func(command []byte) ([]byte, error) {
		if len(command) > 1 && command[0] == 0x80 && command[1] == 0xAA {
			return []byte{0x6A, 0x82}, nil
		}
		if len(command) == 5 && command[1] == 0x70 {
			if command[2] == 0 {
				return []byte{1, 0x90, 0}, nil
			}
			return []byte{0x90, 0}, nil
		}
		if len(command) > 5 && command[0] == 1 && command[1] == 0xA4 {
			return []byte{0x6A, 0x82}, nil
		}
		return original(command)
	}
	current := &session{readerName: "reader", generation: "generation", cardID: "8944000000000000001", card: card, ctx: context.Background(),
		simIdentity: &agentlink.ReaderSIMFact{IdentityState: "ready", IMSI: "bad-metadata"}}
	current.active.Store(true)
	manager.sessions[current.generation] = current
	return manager, current, card
}

func TestInvalidMetadataIsRepairedWithoutResetPINOrProfileMutation(t *testing.T) {
	manager, current, card := metadataRecoveryFixture(t)
	result := manager.RepairInvalidMetadata(context.Background(), time.Now())
	if len(result) != 1 || !result[0].Recovered || result[0].ValidationRule == "" {
		t.Fatalf("result %+v", result)
	}
	if current.simIdentity.IMSI == "bad-metadata" || !current.active.Load() {
		t.Fatal("metadata not replaced in the original live session")
	}
	if len(manager.RepairInvalidMetadata(context.Background(), time.Now().Add(time.Hour))) != 0 {
		t.Fatal("healthy session was repeatedly probed")
	}
	if card.closes != 0 || card.unpowers != 0 {
		t.Fatal("recovery reset card hardware")
	}
	for _, command := range card.commands {
		if len(command) > 1 && (command[1] == 0x24 || command[1] == 0x26 || command[1] == 0x28 || (command[1] == 0x20 && len(command) > 5)) {
			t.Fatalf("recovery sent PIN mutation: %X", command)
		}
	}
}

func TestMetadataRecoveryBackoffAndBudgetDoNotResetByPolling(t *testing.T) {
	manager, current, card := metadataRecoveryFixture(t)
	card.handler = func([]byte) ([]byte, error) { return nil, errors.New("injected transport failure") }
	now := time.Unix(100, 0)
	for _, offset := range []time.Duration{0, 60 * time.Second, 180 * time.Second} {
		result := manager.RepairInvalidMetadata(context.Background(), now.Add(offset))
		if len(result) != 1 || result[0].Recovered {
			t.Fatalf("result %+v", result)
		}
		if len(manager.RepairInvalidMetadata(context.Background(), now.Add(offset+time.Second))) != 0 {
			t.Fatal("retry ignored backoff")
		}
	}
	if len(manager.RepairInvalidMetadata(context.Background(), now.Add(24*time.Hour))) != 0 || current.metadataRecoveryAttempts != 3 {
		t.Fatal("recovery exceeded per-session budget")
	}
}

func TestMetadataRecoverySkipsBusyAndUncertainDownload(t *testing.T) {
	manager, current, card := metadataRecoveryFixture(t)
	current.operation.Lock()
	if len(manager.RepairInvalidMetadata(context.Background(), time.Now())) != 0 {
		t.Fatal("busy reader recovery attempted")
	}
	current.operation.Unlock()
	manager.downloads = map[string]*downloadJob{"job": {sessionGeneration: current.generation, status: agentlink.EUICCDownloadJob{State: agentlink.EUICCDownloadUncertain}}}
	if len(manager.RepairInvalidMetadata(context.Background(), time.Now())) != 0 || len(card.commands) != 0 {
		t.Fatal("uncertain download disturbed")
	}
}

func TestMetadataReadbackRetainsLegacySingleEUICCShape(t *testing.T) {
	manager, current, _ := metadataRecoveryFixture(t)
	card := euiccCard(t, emptyProfileResponse())
	original := card.handler
	card.handler = func(command []byte) ([]byte, error) {
		if len(command) >= 7 && command[1] == 0xA4 && command[5] == 0x2F && command[6] == 0xE2 {
			return []byte{0x90, 0}, nil
		}
		if len(command) == 5 && command[1] == 0xB0 {
			return append(encodeICCID(current.cardID), 0x90, 0), nil
		}
		return original(command)
	}
	current.card = card
	readback, err := manager.ReadCardIdentity(context.Background(), current.readerName, current.generation, current.cardID)
	if err != nil {
		t.Fatal(err)
	}
	if readback.EUICC == nil || readback.EUICC.EID != testEID || len(readback.SecureElements) != 0 {
		t.Fatal("legacy single eUICC became invalid empty slot identity")
	}
	view := manager.Sessions()[0]
	if view.EUICC == nil || view.EUICC.EID != testEID || len(view.SecureElements) != 0 {
		t.Fatal("repaired metadata was not persisted to session")
	}
}
