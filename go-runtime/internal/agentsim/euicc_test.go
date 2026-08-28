package agentsim

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/damonto/euicc-go/bertlv"
	sgp22 "github.com/damonto/euicc-go/v2"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
)

const testEID = "89049032000000000000000000000001"

func TestInspectEUICCReadsEIDAndEmptyProfileListOnOwnedCard(t *testing.T) {
	card := euiccCard(t, emptyProfileResponse())
	fact, err := inspectEUICC(context.Background(), card)
	if err != nil {
		t.Fatal(err)
	}
	if fact.EID != testEID || !fact.ProfilesAvailable || len(fact.Profiles) != 0 {
		t.Fatalf("eUICC fact=%+v", fact)
	}
	card.mu.Lock()
	defer card.mu.Unlock()
	if card.begins != 0 || card.ends != 0 || card.closes != 0 ||
		!containsCommand(card.commands, []byte{0x00, 0x70, 0x80, 0x01, 0x00}) {
		t.Fatalf("card ownership changed begins=%d ends=%d closes=%d commands=%X", card.begins, card.ends, card.closes, card.commands)
	}
}

func TestInspectEUICCReadsSortedProfileIdentityAndState(t *testing.T) {
	card := euiccCard(t, profileResponse(
		profileTLV(t, "8944000000000000002", sgp22.ProfileDisabled),
		profileTLV(t, "8944000000000000001", sgp22.ProfileEnabled),
	))
	fact, err := inspectEUICC(context.Background(), card)
	if err != nil {
		t.Fatal(err)
	}
	want := []agentlink.EUICCProfileFact{
		{ICCID: "8944000000000000001", State: agentlink.EUICCProfileEnabled},
		{ICCID: "8944000000000000002", State: agentlink.EUICCProfileDisabled},
	}
	if fact.EID != testEID || !fact.ProfilesAvailable || len(fact.Profiles) != len(want) {
		t.Fatalf("eUICC fact=%+v", fact)
	}
	for index := range want {
		if fact.Profiles[index] != want[index] {
			t.Fatalf("profile[%d]=%+v want %+v", index, fact.Profiles[index], want[index])
		}
	}
}

func TestManagerIdentifiesBlankEUICCWithoutOfferingAKA(t *testing.T) {
	card := euiccCard(t, emptyProfileResponse())
	manager, err := NewManager(fakeConnector{cards: map[string]*fakeCard{"blank-euicc": card}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{
			Name: "blank-euicc", CardPresent: true, SessionGeneration: "blank-euicc-session",
		})
	}()
	waitForSession(t, manager, "blank-euicc-session")
	views := manager.Sessions()
	if len(views) != 1 || views[0].CardID != "" || views[0].EUICC == nil ||
		views[0].EUICC.EID != testEID || !views[0].EUICC.ProfilesAvailable ||
		views[0].EUICC.Profiles == nil {
		t.Fatalf("blank eUICC view=%+v", views)
	}
	response := manager.AuthenticateAKA(context.Background(), agentlink.AKARequest{
		OperationID: "blank-euicc-op", SessionGeneration: "blank-euicc-session", CardID: "1",
		Application: agentlink.AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
	})
	if response.Failure == nil || response.Failure.Code != "card_identity_unavailable" {
		t.Fatalf("blank eUICC AKA response=%+v", response)
	}
	cancel()
	<-done
}

func TestMalformedProfileResponseCannotCrashAgentOrEraseEID(t *testing.T) {
	malformed := profileResponse(bertlv.NewChildren(bertlv.Private.Constructed(3)))
	fact, err := inspectEUICC(context.Background(), euiccCard(t, malformed))
	if err == nil || fact == nil || fact.EID != testEID || fact.ProfilesAvailable {
		t.Fatalf("malformed profile fact=%+v error=%v", fact, err)
	}
}

func euiccCard(t *testing.T, profiles []byte) *fakeCard {
	t.Helper()
	eid, err := hex.DecodeString(testEID)
	if err != nil {
		t.Fatal(err)
	}
	eidResponse := append([]byte{0xBF, 0x3E, 0x12, 0x5A, 0x10}, eid...)
	return &fakeCard{handler: func(command []byte) ([]byte, error) {
		switch {
		case bytes.Equal(command, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x3F, 0x00, 0x00}):
			return []byte{0x90, 0x00}, nil
		case bytes.Equal(command, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x2F, 0xE2, 0x00}):
			return []byte{0x6A, 0x82}, nil
		case bytes.Equal(command, euiccInitialize):
			return []byte{0x90, 0x00}, nil
		case bytes.Equal(command, []byte{0x00, 0x70, 0x00, 0x00, 0x01}):
			return []byte{0x01, 0x90, 0x00}, nil
		case len(command) >= 5 && command[0] == 0x01 && command[1] == 0xA4:
			return []byte{0x90, 0x00}, nil
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x3E}):
			return append(eidResponse, 0x90, 0x00), nil
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x2D}):
			return append(append([]byte(nil), profiles...), 0x90, 0x00), nil
		case bytes.Equal(command, []byte{0x00, 0x70, 0x80, 0x01, 0x00}):
			return []byte{0x90, 0x00}, nil
		default:
			return nil, errors.New("unexpected eUICC APDU")
		}
	}}
}

func emptyProfileResponse() []byte { return profileResponse() }

func profileResponse(profiles ...*bertlv.TLV) []byte {
	return bertlv.NewChildren(
		bertlv.ContextSpecific.Constructed(45),
		bertlv.NewChildren(bertlv.ContextSpecific.Constructed(0), profiles...),
	).Bytes()
}

func profileTLV(t *testing.T, iccid string, state sgp22.ProfileState) *bertlv.TLV {
	t.Helper()
	encoded, err := sgp22.NewICCID(iccid)
	if err != nil {
		t.Fatal(err)
	}
	return bertlv.NewChildren(
		bertlv.Private.Constructed(3),
		bertlv.NewValue(sgp22.TagICCID, encoded),
		bertlv.NewValue(sgp22.TagProfileState, []byte{byte(state)}),
		bertlv.NewValue(sgp22.TagServiceProviderName, []byte("provider")),
		bertlv.NewValue(sgp22.TagProfileName, []byte("profile")),
	)
}

func containsCommand(commands [][]byte, wanted []byte) bool {
	for _, command := range commands {
		if bytes.Equal(command, wanted) {
			return true
		}
	}
	return false
}
