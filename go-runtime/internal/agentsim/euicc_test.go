package agentsim

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		{ICCID: "8944000000000000001", State: agentlink.EUICCProfileEnabled, ServiceProviderName: "provider", ProfileName: "profile"},
		{ICCID: "8944000000000000002", State: agentlink.EUICCProfileDisabled, ServiceProviderName: "provider", ProfileName: "profile"},
	}
	if fact.EID != testEID || !fact.ProfilesAvailable || !fact.ProfileManagement || len(fact.Profiles) != len(want) {
		t.Fatalf("eUICC fact=%+v", fact)
	}
	for index := range want {
		if fact.Profiles[index] != want[index] {
			t.Fatalf("profile[%d]=%+v want %+v", index, fact.Profiles[index], want[index])
		}
	}
}

func TestInspectSecureElementsDiscoversTwoESTKTargetsWithoutDefaultFallback(t *testing.T) {
	secondEID := "89049032000000000000000000000002"
	card := estkDualCard(t, testEID, secondEID)
	elements, err := inspectSecureElements(context.Background(), card)
	if err != nil {
		t.Fatal(err)
	}
	if len(elements) != 2 || elements[0].id != "se0" || elements[0].label != "SE1" ||
		elements[0].fact.EID != testEID || !bytes.Equal(elements[0].aid, estkSE0AID) ||
		elements[1].id != "se1" || elements[1].label != "SE2" ||
		elements[1].fact.EID != secondEID || !bytes.Equal(elements[1].aid, estkSE1AID) {
		t.Fatalf("secure elements=%+v", elements)
	}
	card.mu.Lock()
	defer card.mu.Unlock()
	for _, command := range card.commands {
		if len(command) >= 5 && command[1] == 0xA4 && bytes.Equal(command[5:], mustDecodeAID("A0000005591010FFFFFFFF8900000100")) {
			t.Fatalf("eSTK discovery unexpectedly opened default ISD-R: %X", command)
		}
	}
}

func TestInspectSecureElementsAllowsOnePresentESTKTarget(t *testing.T) {
	card := estkDualCard(t, testEID, "")
	elements, err := inspectSecureElements(context.Background(), card)
	if err != nil {
		t.Fatal(err)
	}
	if len(elements) != 1 || elements[0].id != "se0" || elements[0].fact.EID != testEID {
		t.Fatalf("secure elements=%+v", elements)
	}
}

func TestInspectSecureElementsPreservesOptionalSlotTransportFailure(t *testing.T) {
	card := estkDualCard(t, testEID, "")
	original := card.handler
	card.handler = func(command []byte) ([]byte, error) {
		if len(command) >= 5 && command[1] == 0xA4 && bytes.Equal(command[5:], estkSE1AID) {
			return nil, errors.New("reader transport failed")
		}
		return original(command)
	}
	elements, err := inspectSecureElements(context.Background(), card)
	if len(elements) != 1 || err == nil {
		t.Fatalf("secure elements=%+v error=%v", elements, err)
	}
}

func TestInspectSecureElementsAllowsCardWithoutEUICCApplication(t *testing.T) {
	card := &fakeCard{handler: func(command []byte) ([]byte, error) {
		switch {
		case bytes.Equal(command, []byte{0x00, 0x70, 0x00, 0x00, 0x01}):
			return []byte{0x01, 0x90, 0x00}, nil
		case len(command) >= 5 && command[1] == 0xA4:
			return []byte{0x6A, 0x82}, nil
		case bytes.Equal(command, euiccInitialize):
			return []byte{0x6A, 0x82}, nil
		case bytes.Equal(command, []byte{0x00, 0x70, 0x80, 0x01, 0x00}):
			return []byte{0x90, 0x00}, nil
		default:
			return nil, fmt.Errorf("unexpected APDU %X", command)
		}
	}}
	elements, err := inspectSecureElements(context.Background(), card)
	if err != nil || len(elements) != 0 {
		t.Fatalf("secure elements=%+v error=%v", elements, err)
	}
}

func TestSMDSDiscoveryUsesUpstreamES11AndSupportsMissingIMEI(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/gsma/rsp2/es9plus/initiateAuthentication":
			_ = json.NewEncoder(response).Encode(&sgp22.ES9InitiateAuthenticationResponse{
				Header:        &sgp22.Header{ExecutionStatus: &sgp22.ExecutionStatus{Status: "Executed-Success"}},
				TransactionID: []byte{0x01, 0x02},
				Signed1:       bertlv.NewValue(bertlv.ContextSpecific.Primitive(0), []byte{1}),
				Signature1:    bertlv.NewValue(bertlv.ContextSpecific.Primitive(1), []byte{2}),
				UsedIssuer:    bertlv.NewValue(bertlv.ContextSpecific.Primitive(2), []byte{3}),
				Certificate:   bertlv.NewValue(bertlv.ContextSpecific.Primitive(3), []byte{4}),
			})
		case "/gsma/rsp2/es9plus/authenticateClient":
			_ = json.NewEncoder(response).Encode(&sgp22.ES11AuthenticateClientResponse{
				Header:        &sgp22.Header{ExecutionStatus: &sgp22.ExecutionStatus{Status: "Executed-Success"}},
				TransactionID: []byte{0x01, 0x02},
				EventEntries:  []*sgp22.EventEntry{{EventID: "event-1", Address: "rsp.example.com"}},
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	card := &fakeCard{handler: func(command []byte) ([]byte, error) {
		switch {
		case bytes.Equal(command, euiccInitialize):
			return []byte{0x90, 0x00}, nil
		case bytes.Equal(command, []byte{0x00, 0x70, 0x00, 0x00, 0x01}):
			return []byte{0x01, 0x90, 0x00}, nil
		case len(command) >= 5 && command[0] == 0x01 && command[1] == 0xA4:
			return []byte{0x90, 0x00}, nil
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x2E}):
			return append([]byte{0xBF, 0x2E, 0x12, 0x80, 0x10}, append(make([]byte, 16), 0x90, 0x00)...), nil
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x20}):
			return []byte{0xBF, 0x20, 0x00, 0x90, 0x00}, nil
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x38}):
			if !bytes.Contains(command, []byte{0xA1, 0x08, 0x80, 0x04, 0x35, 0x29, 0x06, 0x11, 0xA1, 0x00}) ||
				bytes.Contains(command, []byte{0x82, 0x08}) {
				return nil, fmt.Errorf("missing-IMEI device info is wrong: %X", command)
			}
			return []byte{0xBF, 0x38, 0x00, 0x90, 0x00}, nil
		case bytes.Equal(command, []byte{0x00, 0x70, 0x80, 0x01, 0x00}):
			return []byte{0x90, 0x00}, nil
		default:
			return nil, fmt.Errorf("unexpected discovery APDU: %X", command)
		}
	}}
	request := agentlink.EUICCDiscoveryRequest{
		OperationID: "discovery-upstream-1", SessionGeneration: "insertion-1", EID: testEID,
		SMDS: server.URL,
	}
	effective, entries, err := discoverEUICCProfiles(context.Background(), card, request, nil, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || effective == "" || len(entries) != 1 || entries[0].EventID != "event-1" ||
		entries[0].RSPServerAddress != "rsp.example.com" {
		t.Fatalf("requests=%d effective=%q entries=%+v", requests, effective, entries)
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

func TestEUICCProfileEnableUsesExactLiveIdentityAndRefreshesOnlyCardSession(t *testing.T) {
	const iccid = "8944000000000000001"
	card := euiccCard(t, profileResponse(profileTLV(t, iccid, sgp22.ProfileDisabled)))
	manager, err := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var mutations int
	manager.mutateProfile = func(_ context.Context, gotCard Card, _ []byte, gotICCID string, action agentlink.EUICCProfileAction, nickname string) error {
		mutations++
		if gotCard != card || gotICCID != iccid || action != agentlink.EUICCProfileEnable || nickname != "" {
			t.Fatalf("mutation card=%p iccid=%s action=%s nickname=%q", gotCard, gotICCID, action, nickname)
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{
			Name: "reader", CardPresent: true, SessionGeneration: "insertion-1",
		})
	}()
	waitForSession(t, manager, "insertion-1")

	request := agentlink.EUICCProfileRequest{
		OperationID: "profile-enable-1", SessionGeneration: "insertion-1",
		EID: testEID, ICCID: iccid, Action: agentlink.EUICCProfileEnable,
		ExpectedState: agentlink.EUICCProfileDisabled,
	}
	result := manager.ExecuteEUICCProfile(context.Background(), request)
	if result.Failure != nil || result.Outcome != agentlink.EUICCProfileRefreshPending || !result.Changed || mutations != 1 {
		t.Fatalf("result=%+v mutations=%d", result, mutations)
	}
	select {
	case runErr := <-done:
		if !errors.Is(runErr, errEUICCProfileChanged) {
			t.Fatalf("session error=%v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("changed eUICC session was not reopened by its reader worker")
	}
	if len(manager.Sessions()) != 0 {
		t.Fatalf("stale sessions=%+v", manager.Sessions())
	}
	card.mu.Lock()
	closes := card.closes
	card.mu.Unlock()
	if closes != 1 {
		t.Fatalf("card close count=%d", closes)
	}
}

func TestEUICCProfileOperationIsIdempotentAndFencedBeforeMutation(t *testing.T) {
	const iccid = "8944000000000000001"
	card := euiccCard(t, profileResponse(profileTLV(t, iccid, sgp22.ProfileDisabled)))
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
	manager.mutateProfile = func(context.Context, Card, []byte, string, agentlink.EUICCProfileAction, string) error {
		t.Fatal("already-applied or fenced request reached mutation")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{
			Name: "reader", CardPresent: true, SessionGeneration: "insertion-1",
		})
	}()
	waitForSession(t, manager, "insertion-1")

	disable := agentlink.EUICCProfileRequest{
		OperationID: "profile-disable-1", SessionGeneration: "insertion-1",
		EID: testEID, ICCID: iccid, Action: agentlink.EUICCProfileDisable,
		ExpectedState: agentlink.EUICCProfileEnabled,
	}
	result := manager.ExecuteEUICCProfile(context.Background(), disable)
	if result.Failure != nil || result.Outcome != agentlink.EUICCProfileAlreadyApplied || result.State != agentlink.EUICCProfileDisabled {
		t.Fatalf("already-applied result=%+v", result)
	}
	wrongEID := disable
	wrongEID.OperationID = "profile-disable-wrong-eid"
	wrongEID.EID = strings.Repeat("1", 32)
	if result = manager.ExecuteEUICCProfile(context.Background(), wrongEID); result.Failure == nil || result.Failure.Code != "euicc_identity_mismatch" {
		t.Fatalf("wrong EID result=%+v", result)
	}
	wrongSession := disable
	wrongSession.OperationID = "profile-disable-wrong-session"
	wrongSession.SessionGeneration = "insertion-old"
	if result = manager.ExecuteEUICCProfile(context.Background(), wrongSession); result.Failure == nil || result.Failure.Code != "card_session_replaced" {
		t.Fatalf("wrong session result=%+v", result)
	}
	cancel()
	<-done
}

func TestEUICCProfileNicknameUsesExactLiveIdentityAndCurrentNicknameFence(t *testing.T) {
	const iccid = "8944000000000000001"
	card := euiccCard(t, profileResponse(profileTLV(t, iccid, sgp22.ProfileDisabled, "old")))
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
	mutations := 0
	manager.mutateProfile = func(_ context.Context, gotCard Card, aid []byte, gotICCID string,
		action agentlink.EUICCProfileAction, nickname string) error {
		mutations++
		if gotCard != card || gotICCID != iccid || action != agentlink.EUICCProfileNickname ||
			nickname != "旅行" || aid != nil {
			t.Fatalf("card=%p aid=%X iccid=%s action=%s nickname=%q", gotCard, aid, gotICCID, action, nickname)
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "insertion-1"})
	}()
	waitForSession(t, manager, "insertion-1")

	request := agentlink.EUICCProfileRequest{
		OperationID: "profile-nickname-1", SessionGeneration: "insertion-1",
		EID: testEID, ICCID: iccid, Action: agentlink.EUICCProfileNickname,
		Nickname: "旅行", ExpectedNickname: "old",
	}
	result := manager.ExecuteEUICCProfile(context.Background(), request)
	if result.Failure != nil || result.Outcome != agentlink.EUICCProfileRefreshPending || !result.Changed || mutations != 1 {
		t.Fatalf("result=%+v mutations=%d", result, mutations)
	}
	select {
	case runErr := <-done:
		if !errors.Is(runErr, errEUICCProfileChanged) {
			t.Fatalf("session error=%v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("nickname change did not refresh its card session")
	}
}

func TestEUICCProfileNicknameRoutesSecondSecureElementByEID(t *testing.T) {
	const iccid = "8944000000000000002"
	secondEID := "89049032000000000000000000000002"
	card := estkDualCard(t, testEID, secondEID, map[string][]byte{
		testEID:   emptyProfileResponse(),
		secondEID: profileResponse(profileTLV(t, iccid, sgp22.ProfileDisabled, "old")),
	})
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
	manager.mutateProfile = func(_ context.Context, _ Card, aid []byte, gotICCID string,
		action agentlink.EUICCProfileAction, nickname string) error {
		if !bytes.Equal(aid, estkSE1AID) || gotICCID != iccid || action != agentlink.EUICCProfileNickname || nickname != "new" {
			t.Fatalf("aid=%X iccid=%s action=%s nickname=%q", aid, gotICCID, action, nickname)
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "insertion-1"})
	}()
	waitForSession(t, manager, "insertion-1")
	result := manager.ExecuteEUICCProfile(context.Background(), agentlink.EUICCProfileRequest{
		OperationID: "profile-nickname-se2", SessionGeneration: "insertion-1",
		EID: secondEID, ICCID: iccid, Action: agentlink.EUICCProfileNickname,
		Nickname: "new", ExpectedNickname: "old",
	})
	if result.Failure != nil || result.Outcome != agentlink.EUICCProfileRefreshPending || !result.Changed {
		t.Fatalf("result=%+v", result)
	}
	select {
	case runErr := <-done:
		if !errors.Is(runErr, errEUICCProfileChanged) {
			t.Fatalf("session error=%v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("second secure element nickname did not refresh its card session")
	}
}

func TestEUICCProfileNicknameIdempotentAndStaleRequestsDoNotWrite(t *testing.T) {
	const iccid = "8944000000000000001"
	card := euiccCard(t, profileResponse(profileTLV(t, iccid, sgp22.ProfileDisabled, "current")))
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
	manager.mutateProfile = func(context.Context, Card, []byte, string, agentlink.EUICCProfileAction, string) error {
		t.Fatal("idempotent or stale nickname request reached mutation")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "insertion-1"})
	}()
	waitForSession(t, manager, "insertion-1")
	base := agentlink.EUICCProfileRequest{
		OperationID: "profile-nickname-same", SessionGeneration: "insertion-1",
		EID: testEID, ICCID: iccid, Action: agentlink.EUICCProfileNickname,
		Nickname: "current", ExpectedNickname: "old",
	}
	result := manager.ExecuteEUICCProfile(context.Background(), base)
	if result.Failure != nil || result.Outcome != agentlink.EUICCProfileAlreadyApplied || result.Nickname != "current" {
		t.Fatalf("idempotent result=%+v", result)
	}
	stale := base
	stale.OperationID = "profile-nickname-stale"
	stale.Nickname = "new"
	result = manager.ExecuteEUICCProfile(context.Background(), stale)
	if result.Failure == nil || result.Failure.Code != "euicc_profile_nickname_changed" {
		t.Fatalf("stale result=%+v", result)
	}
	cancel()
	<-done
}

func TestMutateEUICCProfileNicknameUsesUpstreamES10cRequest(t *testing.T) {
	const iccid = "8944000000000000001"
	card := euiccCard(t, emptyProfileResponse())
	if err := mutateEUICCProfile(context.Background(), card, nil, iccid, agentlink.EUICCProfileNickname, "旅行"); err != nil {
		t.Fatal(err)
	}
	card.mu.Lock()
	defer card.mu.Unlock()
	found := false
	for _, command := range card.commands {
		if bytes.Contains(command, []byte{0xBF, 0x29}) && bytes.Contains(command, []byte("旅行")) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("SetNickname APDU was not emitted: %X", card.commands)
	}
}

func TestEUICCProfileDefinitiveRejectionDoesNotRefreshCardSession(t *testing.T) {
	const iccid = "8944000000000000001"
	card := euiccCard(t, profileResponse(profileTLV(t, iccid, sgp22.ProfileDisabled)))
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
	manager.mutateProfile = func(context.Context, Card, []byte, string, agentlink.EUICCProfileAction, string) error {
		return errors.New("disallowed by policy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "insertion-1"})
	}()
	waitForSession(t, manager, "insertion-1")
	result := manager.ExecuteEUICCProfile(context.Background(), agentlink.EUICCProfileRequest{
		OperationID: "profile-enable-rejected", SessionGeneration: "insertion-1",
		EID: testEID, ICCID: iccid, Action: agentlink.EUICCProfileEnable,
		ExpectedState: agentlink.EUICCProfileDisabled,
	})
	if result.Failure == nil || result.Failure.Code != "euicc_profile_policy_rejected" {
		t.Fatalf("rejected result=%+v", result)
	}
	select {
	case runErr := <-done:
		t.Fatalf("definitive rejection stopped card session: %v", runErr)
	case <-time.After(20 * time.Millisecond):
	}
	if len(manager.Sessions()) != 1 {
		t.Fatalf("session disappeared after rejection: %+v", manager.Sessions())
	}
	cancel()
	<-done
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
			if bytes.Equal(command[5:], estkProductAID) || bytes.Equal(command[5:], estkSE0AID) || bytes.Equal(command[5:], estkSE1AID) {
				return []byte{0x6A, 0x82}, nil
			}
			return []byte{0x90, 0x00}, nil
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x3E}):
			return append(eidResponse, 0x90, 0x00), nil
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x2D}):
			return append(append([]byte(nil), profiles...), 0x90, 0x00), nil
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x29}):
			return []byte{0xBF, 0x29, 0x03, 0x80, 0x01, 0x00, 0x90, 0x00}, nil
		case bytes.Equal(command, []byte{0x00, 0x70, 0x80, 0x01, 0x00}):
			return []byte{0x90, 0x00}, nil
		default:
			return nil, errors.New("unexpected eUICC APDU")
		}
	}}
}

func estkDualCard(t *testing.T, firstEID, secondEID string, profileSets ...map[string][]byte) *fakeCard {
	t.Helper()
	eids := map[string]string{hex.EncodeToString(estkSE0AID): firstEID, hex.EncodeToString(estkSE1AID): secondEID}
	selected := ""
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
			selected = hex.EncodeToString(command[5:])
			if bytes.Equal(command[5:], estkProductAID) || eids[selected] != "" {
				return []byte{0x90, 0x00}, nil
			}
			return []byte{0x6A, 0x82}, nil
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x3E}):
			eid, err := hex.DecodeString(eids[selected])
			if err != nil {
				return nil, err
			}
			return append(append([]byte{0xBF, 0x3E, 0x12, 0x5A, 0x10}, eid...), 0x90, 0x00), nil
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x2D}):
			if len(profileSets) > 0 && profileSets[0][eids[selected]] != nil {
				return append(append([]byte(nil), profileSets[0][eids[selected]]...), 0x90, 0x00), nil
			}
			return append(emptyProfileResponse(), 0x90, 0x00), nil
		case bytes.Equal(command, []byte{0x00, 0x70, 0x80, 0x01, 0x00}):
			return []byte{0x90, 0x00}, nil
		default:
			return nil, fmt.Errorf("unexpected eSTK APDU: %X", command)
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

func profileTLV(t *testing.T, iccid string, state sgp22.ProfileState, nickname ...string) *bertlv.TLV {
	t.Helper()
	encoded, err := sgp22.NewICCID(iccid)
	if err != nil {
		t.Fatal(err)
	}
	children := []*bertlv.TLV{
		bertlv.NewValue(sgp22.TagICCID, encoded),
		bertlv.NewValue(sgp22.TagProfileState, []byte{byte(state)}),
		bertlv.NewValue(sgp22.TagServiceProviderName, []byte("provider")),
		bertlv.NewValue(sgp22.TagProfileName, []byte("profile")),
	}
	if len(nickname) > 0 {
		children = append(children, bertlv.NewValue(sgp22.TagNickname, []byte(nickname[0])))
	}
	return bertlv.NewChildren(bertlv.Private.Constructed(3), children...)
}

func containsCommand(commands [][]byte, wanted []byte) bool {
	for _, command := range commands {
		if bytes.Equal(command, wanted) {
			return true
		}
	}
	return false
}
