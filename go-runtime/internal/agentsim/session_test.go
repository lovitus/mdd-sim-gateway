package agentsim

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
)

type fakeCard struct {
	mu         sync.Mutex
	handler    func([]byte) ([]byte, error)
	commands   [][]byte
	begins     int
	ends       int
	closes     int
	beginErrAt int
	endErrAt   int
}

func (card *fakeCard) BeginTransaction() error {
	card.mu.Lock()
	defer card.mu.Unlock()
	card.begins++
	if card.beginErrAt == card.begins {
		return errors.New("begin transaction failed")
	}
	return nil
}

func (card *fakeCard) EndTransaction() error {
	card.mu.Lock()
	defer card.mu.Unlock()
	card.ends++
	if card.endErrAt == card.ends {
		return errors.New("end transaction failed")
	}
	return nil
}

func (card *fakeCard) Transmit(command []byte) ([]byte, error) {
	card.mu.Lock()
	defer card.mu.Unlock()
	copy := append([]byte(nil), command...)
	card.commands = append(card.commands, copy)
	return card.handler(copy)
}

func (card *fakeCard) Close() error {
	card.mu.Lock()
	defer card.mu.Unlock()
	card.closes++
	return nil
}

type fakeConnector struct {
	cards map[string]*fakeCard
}

const (
	pinAlreadyVerified = iota
	pinRejected
	pinAcceptedPerTransaction
)

func (connector fakeConnector) Connect(readerName string) (Card, error) {
	card := connector.cards[readerName]
	if card == nil {
		return nil, errors.New("reader unavailable")
	}
	return card, nil
}

func TestManagerAuthenticatesExactLiveCardSession(t *testing.T) {
	const cardID = "8944000000000000001"
	card := scriptedCard(cardID, pinAlreadyVerified)
	manager, err := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader-a": card}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{
			Name: "reader-a", CardPresent: true, SessionGeneration: "session-a",
		})
	}()
	waitForSession(t, manager, "session-a")
	request := agentlink.AKARequest{
		OperationID: "aka-1", SessionGeneration: "session-a", CardID: cardID,
		Application: agentlink.AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
	}
	response := manager.AuthenticateAKA(context.Background(), request)
	if response.Failure != nil || response.SW1 != 0x90 || response.SW2 != 0 ||
		!bytes.Equal(response.Body, []byte{0xDB, 0x04, 1, 2, 3, 4}) {
		t.Fatalf("AuthenticateAKA() = %+v", response)
	}
	wrong := request
	wrong.OperationID = "aka-2"
	wrong.CardID = "1"
	if response := manager.AuthenticateAKA(context.Background(), wrong); response.Failure == nil || response.Failure.Code != "card_identity_mismatch" {
		t.Fatalf("identity mismatch = %+v", response)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SIM session did not stop")
	}
	if response := manager.AuthenticateAKA(context.Background(), request); response.Failure == nil || response.Failure.Code != "card_session_replaced" {
		t.Fatalf("removed session response = %+v", response)
	}
	card.mu.Lock()
	defer card.mu.Unlock()
	if card.begins != 2 || card.ends != 2 || card.closes != 1 {
		t.Fatalf("card lifecycle begins=%d ends=%d closes=%d", card.begins, card.ends, card.closes)
	}
}

func TestManagerVerifiesPINOnlyForExactReaderSession(t *testing.T) {
	const cardID = "8944000000000000001"
	card := scriptedCard(cardID, pinAcceptedPerTransaction)
	manager, err := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader-a": card}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "reader-a", CardPresent: true, SessionGeneration: "session-pin"})
	}()
	waitForSession(t, manager, "session-pin")
	statusRequest := agentlink.SIMPINRequest{OperationID: "pin-status-1", ProcessGeneration: "process-1",
		CardID: cardID, ReaderName: "reader-a", SIMSessionGeneration: "session-pin", Action: agentlink.SIMPINStatus}
	status := manager.ExecuteSIMPIN(context.Background(), statusRequest)
	if status.Failure != nil || status.State != "retry_counter" || status.AttemptsRemaining == nil || *status.AttemptsRemaining != 3 {
		t.Fatalf("status=%+v", status)
	}
	request := agentlink.SIMPINRequest{OperationID: "pin-operation-1", ProcessGeneration: "process-1", CardID: cardID, ReaderName: "reader-a", SIMSessionGeneration: "session-pin", Action: agentlink.SIMPINVerify, PIN: "1234"}
	response := manager.ExecuteSIMPIN(context.Background(), request)
	if response.Failure != nil || response.State != "verified" {
		t.Fatalf("response=%+v", response)
	}
	statusRequest.OperationID = "pin-status-2"
	status = manager.ExecuteSIMPIN(context.Background(), statusRequest)
	if status.Failure != nil || status.State != "retry_counter" || status.AttemptsRemaining == nil || *status.AttemptsRemaining != 3 {
		t.Fatalf("post-verify status=%+v", status)
	}
	wrong := request
	wrong.CardID = "8944000000000000002"
	if response := manager.ExecuteSIMPIN(context.Background(), wrong); response.Failure == nil || response.Failure.Code != "sim_pin_session_replaced" {
		t.Fatalf("wrong identity=%+v", response)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SIM session did not stop")
	}
}

func TestReaderReadbackFailureCodes(t *testing.T) {
	const cardID = "8944000000000000001"
	request := agentlink.ReaderReadbackRequest{
		OperationID: "reader-readback-test", ProcessGeneration: "process-1",
		ReaderName: "reader-a", CardID: cardID, SIMSessionGeneration: "session-a",
	}
	stale, err := NewManager(fakeConnector{cards: map[string]*fakeCard{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response := stale.ReadReader(context.Background(), request); response.ErrorCode != "reader_readback_identity_stale" {
		t.Fatalf("stale response=%+v", response)
	}
	if code := readerReadbackErrorCode(context.Canceled); code != "reader_readback_interrupted" {
		t.Fatalf("canceled code=%q", code)
	}

	for name, testCase := range map[string]struct {
		mutate   func(*fakeCard)
		wantCode string
	}{
		"begin transaction": {
			mutate:   func(card *fakeCard) { card.beginErrAt = card.begins + 1 },
			wantCode: "reader_readback_transaction_failed",
		},
		"ICCID read": {
			mutate: func(card *fakeCard) {
				original := card.handler
				card.handler = func(command []byte) ([]byte, error) {
					if len(command) == 5 && command[1] == 0xB0 {
						return nil, errors.New("reader transport failed")
					}
					return original(command)
				}
			},
			wantCode: "reader_readback_iccid_failed",
		},
		"ICCID changed": {
			mutate: func(card *fakeCard) {
				original := card.handler
				card.handler = func(command []byte) ([]byte, error) {
					if len(command) == 5 && command[1] == 0xB0 {
						return append(encodeICCID("8944000000000000002"), 0x90, 0x00), nil
					}
					return original(command)
				}
			},
			wantCode: "reader_readback_identity_changed",
		},
		"secure element": {
			mutate: func(card *fakeCard) {
				original := card.handler
				card.handler = func(command []byte) ([]byte, error) {
					if bytes.Equal(command, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x2F, 0x00, 0x00}) {
						return nil, errors.New("secure element transport failed")
					}
					return original(command)
				}
			},
			wantCode: "reader_readback_secure_element_failed",
		},
		"end transaction": {
			mutate: func(card *fakeCard) {
				original := card.handler
				card.handler = func(command []byte) ([]byte, error) {
					if len(command) > 8 && command[1] == 0xA4 && command[2] == 0x04 {
						return []byte{0x6A, 0x82}, nil
					}
					return original(command)
				}
				card.endErrAt = card.ends + 1
			},
			wantCode: "reader_readback_transaction_release_failed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			card := scriptedCard(cardID, pinAlreadyVerified)
			manager, err := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader-a": card}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- manager.Run(ctx, agentreader.Reader{Name: "reader-a", CardPresent: true, SessionGeneration: "session-a"})
			}()
			waitForSession(t, manager, "session-a")
			card.mu.Lock()
			testCase.mutate(card)
			card.mu.Unlock()
			response := manager.ReadReader(context.Background(), request)
			if response.State != "unknown" || response.ErrorCode != testCase.wantCode {
				t.Fatalf("response=%+v, want code %q", response, testCase.wantCode)
			}
			cancel()
			<-done
		})
	}
}

func TestAgentWSSRoutesToExactPCSCSession(t *testing.T) {
	const (
		cardID = "8944000000000000001"
		token  = "abcdef0123456789abcdef0123456789"
	)
	card := scriptedCard(cardID, pinAlreadyVerified)
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader-wss": card}}, nil)
	sessionContext, stopSession := context.WithCancel(context.Background())
	sessionDone := make(chan error, 1)
	go func() {
		sessionDone <- manager.Run(sessionContext, agentreader.Reader{
			Name: "reader-wss", CardPresent: true, SessionGeneration: "card-wss-1",
		})
	}()
	waitForSession(t, manager, "card-wss-1")
	defer func() { stopSession(); <-sessionDone }()

	server, err := agentlink.NewServer(agentlink.TokenResolverFunc(
		func(context.Context, string) (string, error) { return token, nil },
	))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	linkContext, stopLink := context.WithCancel(context.Background())
	linkDone := make(chan error, 1)
	go func() {
		linkDone <- (agentlink.Client{
			URL:   strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/v1/agent/connect",
			Token: token,
			Hello: agentlink.Hello{
				SchemaVersion: agentlink.SchemaVersion, AgentID: "agent-wss", ProcessGeneration: "process-wss-1",
			},
			Authenticator: manager, OperationTimeout: time.Second,
		}).Run(linkContext)
	}()
	defer func() { stopLink(); <-linkDone }()

	request := agentlink.AKARequest{
		OperationID: "wss-aka-1", SessionGeneration: "card-wss-1", CardID: cardID,
		Application: agentlink.AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, requestErr := server.AuthenticateAKA(
			context.Background(), "agent-wss", "process-wss-1", request,
		)
		if errors.Is(requestErr, agentlink.ErrAgentOffline) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
			continue
		}
		if requestErr != nil || response.SW1 != 0x90 || !bytes.Equal(response.Body, []byte{0xDB, 0x04, 1, 2, 3, 4}) {
			t.Fatalf("WSS AKA response=%+v error=%v", response, requestErr)
		}
		break
	}
}

func TestManagerKeepsBlankCardVisibleWithoutOfferingAKA(t *testing.T) {
	card := &fakeCard{handler: func(command []byte) ([]byte, error) {
		if bytes.Equal(command, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x2F, 0xE2, 0x00}) {
			return []byte{0x6A, 0x82}, nil
		}
		return []byte{0x90, 0x00}, nil
	}}
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"blank": card}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "blank", CardPresent: true, SessionGeneration: "blank-session"})
	}()
	waitForSession(t, manager, "blank-session")
	views := manager.Sessions()
	if len(views) != 1 || views[0].CardID != "" {
		t.Fatalf("blank card view = %+v", views)
	}
	response := manager.AuthenticateAKA(context.Background(), agentlink.AKARequest{
		OperationID: "blank-op", SessionGeneration: "blank-session", CardID: "1",
		Application: agentlink.AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
	})
	if response.Failure == nil || response.Failure.Code != "card_identity_unavailable" {
		t.Fatalf("blank card AKA response = %+v", response)
	}
	cancel()
	<-done
}

func TestManagerDoesNotRepeatRejectedPINInOneProcess(t *testing.T) {
	const cardID = "8944000000000000001"
	card := scriptedCard(cardID, pinRejected)
	manager, _ := NewManager(
		fakeConnector{cards: map[string]*fakeCard{"pin-reader": card}},
		PINResolverFunc(func(context.Context, string) (string, error) { return "1234", nil }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "pin-reader", CardPresent: true, SessionGeneration: "pin-session"})
	}()
	waitForSession(t, manager, "pin-session")
	request := agentlink.AKARequest{
		OperationID: "pin-1", SessionGeneration: "pin-session", CardID: cardID,
		Application: agentlink.AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
	}
	first := manager.AuthenticateAKA(context.Background(), request)
	request.OperationID = "pin-2"
	second := manager.AuthenticateAKA(context.Background(), request)
	if first.Failure == nil || second.Failure == nil || first.Failure.Code != "pin_verification_failed" || second.Failure.Code != "pin_verification_failed" {
		t.Fatalf("PIN failures first=%+v second=%+v", first, second)
	}
	card.mu.Lock()
	verifyCount := 0
	for _, command := range card.commands {
		if len(command) == 13 && command[1] == 0x20 {
			verifyCount++
		}
	}
	card.mu.Unlock()
	if verifyCount != 1 {
		t.Fatalf("VERIFY PIN count = %d, want 1", verifyCount)
	}
	cancel()
	<-done
}

func TestManagerRepeatsAcceptedPINAfterTransactionRelock(t *testing.T) {
	const cardID = "8944000000000000001"
	card := scriptedCard(cardID, pinAcceptedPerTransaction)
	manager, _ := NewManager(
		fakeConnector{cards: map[string]*fakeCard{"pin-reader": card}},
		PINResolverFunc(func(context.Context, string) (string, error) { return "1234", nil }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "pin-reader", CardPresent: true, SessionGeneration: "pin-session"})
	}()
	waitForSession(t, manager, "pin-session")
	request := agentlink.AKARequest{
		OperationID: "pin-success-1", SessionGeneration: "pin-session", CardID: cardID,
		Application: agentlink.AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
	}
	first := manager.AuthenticateAKA(context.Background(), request)
	request.OperationID = "pin-success-2"
	second := manager.AuthenticateAKA(context.Background(), request)
	if first.Failure != nil || second.Failure != nil {
		t.Fatalf("PIN results first=%+v second=%+v", first, second)
	}
	card.mu.Lock()
	verifyCount := 0
	for _, command := range card.commands {
		if len(command) == 13 && command[1] == 0x20 {
			verifyCount++
		}
	}
	card.mu.Unlock()
	if verifyCount != 2 {
		t.Fatalf("VERIFY PIN count = %d, want one per relocked transaction", verifyCount)
	}
	cancel()
	<-done
}

func TestVerifyPINPreservesFinalTwoAttempts(t *testing.T) {
	for _, remaining := range []byte{1, 2} {
		var commands [][]byte
		card := &fakeCard{handler: func(command []byte) ([]byte, error) {
			commands = append(commands, append([]byte(nil), command...))
			return []byte{0x63, 0xC0 | remaining}, nil
		}}
		attempted, err := verifyPIN(context.Background(), card, "1234", true)
		if attempted || err == nil || !strings.Contains(err.Error(), "retry counter is too low") {
			t.Fatalf("remaining=%d attempted=%t err=%v", remaining, attempted, err)
		}
		if len(commands) != 1 || len(commands[0]) != 5 {
			t.Fatalf("remaining=%d commands=%X, PIN must not be submitted", remaining, commands)
		}
	}
}

func TestIdentityTransportFailureRetriesThroughSessionWorker(t *testing.T) {
	card := &fakeCard{handler: func(command []byte) ([]byte, error) {
		if len(command) > 1 && command[1] == 0xB0 {
			return nil, errors.New("reader removed")
		}
		return []byte{0x90, 0x00}, nil
	}}
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"broken": card}}, nil)
	err := manager.Run(context.Background(), agentreader.Reader{
		Name: "broken", CardPresent: true, SessionGeneration: "broken-session",
	})
	if err == nil || len(manager.Sessions()) != 0 {
		t.Fatalf("Run() error=%v sessions=%+v", err, manager.Sessions())
	}
}

func TestIdentityUnexpectedStatusRetriesInsteadOfBecomingBlank(t *testing.T) {
	card := &fakeCard{handler: func([]byte) ([]byte, error) { return []byte{0x6F, 0x00}, nil }}
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"status": card}}, nil)
	err := manager.Run(context.Background(), agentreader.Reader{
		Name: "status", CardPresent: true, SessionGeneration: "status-session",
	})
	if err == nil || len(manager.Sessions()) != 0 {
		t.Fatalf("Run() error=%v sessions=%+v", err, manager.Sessions())
	}
}

func TestApplicationTransportAndTransactionReleaseRemainFailures(t *testing.T) {
	const cardID = "8944000000000000001"
	for name, testCase := range map[string]struct {
		mutate   func(*fakeCard)
		wantCode string
	}{
		"select transport": {
			mutate: func(card *fakeCard) {
				original := card.handler
				card.handler = func(command []byte) ([]byte, error) {
					if bytes.Equal(command, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x2F, 0x00, 0x00}) {
						return nil, errors.New("reader transport failed")
					}
					return original(command)
				}
			},
			wantCode: "sim_select_transport_failed",
		},
		"release transaction": {
			mutate:   func(card *fakeCard) { card.endErrAt = 2 },
			wantCode: "pcsc_transaction_release_failed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			card := scriptedCard(cardID, pinAlreadyVerified)
			testCase.mutate(card)
			manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "session"})
			}()
			waitForSession(t, manager, "session")
			response := manager.AuthenticateAKA(context.Background(), agentlink.AKARequest{
				OperationID: "op", SessionGeneration: "session", CardID: cardID,
				Application: agentlink.AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
			})
			if response.Failure == nil || response.Failure.Code != testCase.wantCode || response.Body != nil {
				t.Fatalf("AuthenticateAKA() = %+v, want code %s", response, testCase.wantCode)
			}
			cancel()
			<-done
		})
	}
}

func TestAuthenticateCorrectsMissingLeWithoutCorruptingChallenge(t *testing.T) {
	var commands [][]byte
	card := &fakeCard{handler: func(command []byte) ([]byte, error) {
		commands = append(commands, append([]byte(nil), command...))
		if len(commands) == 1 {
			return []byte{0x6C, 0x00}, nil
		}
		return []byte{0xDB, 0x00, 0x90, 0x00}, nil
	}}
	response, err := authenticate(context.Background(), card, make([]byte, 16), make([]byte, 16))
	if err != nil || !response.success() || len(commands) != 2 {
		t.Fatalf("authenticate response=%+v commands=%d err=%v", response, len(commands), err)
	}
	if len(commands[1]) != len(commands[0])+1 || !bytes.Equal(commands[1][:len(commands[0])], commands[0]) {
		t.Fatalf("corrected AUTH command first=%X second=%X", commands[0], commands[1])
	}
}

func scriptedCard(cardID string, pinBehavior int) *fakeCard {
	aid, _ := hex.DecodeString(usimAIDPrefix + "FFFFFFFF")
	iccid := encodeICCID(cardID)
	return &fakeCard{handler: func(command []byte) ([]byte, error) {
		switch {
		case bytes.Equal(command, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x3F, 0x00, 0x00}):
			return []byte{0x90, 0x00}, nil
		case bytes.Equal(command, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x2F, 0xE2, 0x00}):
			return []byte{0x90, 0x00}, nil
		case len(command) == 5 && command[1] == 0xB0:
			return append(iccid, 0x90, 0x00), nil
		case bytes.Equal(command, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x2F, 0x00, 0x00}):
			return []byte{0x90, 0x00}, nil
		case len(command) == 5 && command[1] == 0xB2 && command[2] == 1 && command[4] == 0:
			return []byte{0x6C, byte(len(aid) + 4)}, nil
		case len(command) == 5 && command[1] == 0xB2 && command[2] == 1:
			record := append([]byte{0x61, byte(len(aid) + 2), 0x4F, byte(len(aid))}, aid...)
			return append(record, 0x90, 0x00), nil
		case len(command) == 5 && command[1] == 0xB2 && command[2] == 2:
			return []byte{0x6A, 0x83}, nil
		case len(command) >= 5 && command[1] == 0xA4 && command[2] == 0x04:
			return []byte{0x90, 0x00}, nil
		case len(command) == 5 && command[1] == 0x20:
			if pinBehavior != pinAlreadyVerified {
				return []byte{0x63, 0xC3}, nil
			}
			return []byte{0x90, 0x00}, nil
		case len(command) == 13 && command[1] == 0x20:
			if pinBehavior == pinRejected {
				return []byte{0x63, 0xC2}, nil
			}
			return []byte{0x90, 0x00}, nil
		case len(command) >= 5 && command[1] == 0x88:
			return []byte{0x61, 0x06}, nil
		case bytes.Equal(command, []byte{0x00, 0xC0, 0x00, 0x00, 0x06}):
			return []byte{0xDB, 0x04, 1, 2, 3, 4, 0x90, 0x00}, nil
		default:
			return nil, fmt.Errorf("unexpected APDU %X", command)
		}
	}}
}

func encodeICCID(cardID string) []byte {
	if len(cardID)%2 != 0 {
		cardID += "F"
	}
	encoded := make([]byte, len(cardID)/2)
	for index := range encoded {
		low := fromHex(cardID[index*2])
		high := fromHex(cardID[index*2+1])
		encoded[index] = low | high<<4
	}
	return encoded
}

func fromHex(value byte) byte {
	if value >= '0' && value <= '9' {
		return value - '0'
	}
	return 0x0F
}

func waitForSession(t *testing.T, manager *Manager, generation string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, view := range manager.Sessions() {
			if view.SessionGeneration == generation {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session %s did not appear", generation)
}
