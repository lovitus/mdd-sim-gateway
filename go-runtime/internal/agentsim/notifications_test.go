package agentsim

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/damonto/euicc-go/bertlv"
	"github.com/damonto/euicc-go/bertlv/primitive"
	euichttp "github.com/damonto/euicc-go/http"
	"github.com/damonto/euicc-go/lpa"
	sgp22 "github.com/damonto/euicc-go/v2"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
)

func TestListNotificationsUsesUnfilteredBF28AndMapsExtendedEvent(t *testing.T) {
	card := euiccCard(t, emptyProfileResponse())
	base := card.handler
	card.handler = func(command []byte) ([]byte, error) {
		if len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x28}) {
			if !bytes.Contains(command, []byte{0xBF, 0x28, 0x00}) {
				t.Fatalf("ListNotification contains a search filter: %X", command)
			}
			response := notificationListResponse(t,
				notificationMetadata(t, 8, 7, "", "notify.example.com"),
				notificationMetadata(t, 9, 4, "8944000000000000001", "legacy.example.com"),
			)
			return append(response, 0x90, 0x00), nil
		}
		return base(command)
	}
	entries, err := listEUICCNotifications(context.Background(), card, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []agentlink.EUICCNotificationEntry{
		{SequenceNumber: 8, Event: "rpm", Address: "notify.example.com"},
		{SequenceNumber: 9, Event: "enable", ICCID: "8944000000000000001", Address: "legacy.example.com"},
	}
	if len(entries) != len(want) || entries[0] != want[0] || entries[1] != want[1] {
		t.Fatalf("entries=%+v want=%+v", entries, want)
	}
}

func TestListNotificationsContainsMalformedUpstreamResponsePanic(t *testing.T) {
	card := euiccCard(t, emptyProfileResponse())
	base := card.handler
	card.handler = func(command []byte) ([]byte, error) {
		if len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x28}) {
			return []byte{0xBF, 0x28, 0x00, 0x90, 0x00}, nil
		}
		return base(command)
	}
	if _, err := listEUICCNotifications(context.Background(), card, nil); err == nil {
		t.Fatal("malformed notification response was accepted")
	}
}

func TestSendPendingNotificationRequiresExactHTTP204AndDoesNotRetry(t *testing.T) {
	requests := 0
	status := http.StatusNoContent
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/gsma/rsp2/es9plus/handleNotification" ||
			request.Header.Get("X-Admin-Protocol") != "gsma/rsp/v2.5.0" {
			t.Errorf("path=%s headers=%v", request.URL.Path, request.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["pendingNotification"] == nil {
			t.Errorf("body=%v err=%v", body, err)
		}
		if status == http.StatusTemporaryRedirect {
			response.Header().Set("Location", "/gsma/rsp2/es9plus/handleNotification")
		}
		response.WriteHeader(status)
	}))
	defer server.Close()
	receiver, _ := url.Parse(server.URL)
	client := &lpa.Client{HTTP: &euichttp.Client{
		Client: server.Client(), AdminProtocolVersion: "2.5.0",
	}}
	pending := &sgp22.PendingNotification{PendingNotification: bertlv.NewChildren(bertlv.ContextSpecific.Constructed(55))}
	if err := sendPendingNotification(context.Background(), client, receiver, pending); err != nil || requests != 1 {
		t.Fatalf("204 err=%v requests=%d", err, requests)
	}
	status = http.StatusOK
	if err := sendPendingNotification(context.Background(), client, receiver, pending); !errors.Is(err, errNotificationReceiverRejected) || requests != 2 {
		t.Fatalf("200 err=%v requests=%d", err, requests)
	}
	status = http.StatusTemporaryRedirect
	if err := sendPendingNotification(context.Background(), client, receiver, pending); !errors.Is(err, errNotificationReceiverRejected) || requests != 3 {
		t.Fatalf("redirect err=%v requests=%d", err, requests)
	}
}

func TestNotificationReceiverAcceptsOnlyHTTPSHostMetadata(t *testing.T) {
	valid, err := notificationReceiverURL("notify.example.com:8443")
	if err != nil || valid.String() != "https://notify.example.com:8443" {
		t.Fatalf("valid=%v err=%v", valid, err)
	}
	for _, address := range []string{"https://notify.example.com", "notify.example.com/path", "localhost", "127.0.0.1"} {
		if _, err := notificationReceiverURL(address); !errors.Is(err, errNotificationAddress) {
			t.Fatalf("unsafe address %q err=%v", address, err)
		}
	}
}

func TestDeliverNotificationSendsOnceThenRemovesAfter204(t *testing.T) {
	httpRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		httpRequests++
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	card := euiccCard(t, emptyProfileResponse())
	base := card.handler
	var cardOperations []string
	card.handler = func(command []byte) ([]byte, error) {
		switch {
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x2B}):
			cardOperations = append(cardOperations, "retrieve")
			metadata := notificationMetadata(t, 17, 3, "8944000000000000001", "notify.example.com")
			pending := bertlv.NewChildren(bertlv.ContextSpecific.Constructed(55),
				bertlv.NewChildren(bertlv.ContextSpecific.Constructed(39), metadata))
			payload := bertlv.NewChildren(bertlv.ContextSpecific.Constructed(43),
				bertlv.NewChildren(bertlv.ContextSpecific.Constructed(0), pending)).Bytes()
			return append(payload, 0x90, 0x00), nil
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x30}):
			cardOperations = append(cardOperations, "remove")
			payload := bertlv.NewChildren(bertlv.ContextSpecific.Constructed(48),
				bertlv.NewValue(bertlv.ContextSpecific.Primitive(0), []byte{0})).Bytes()
			return append(payload, 0x90, 0x00), nil
		default:
			return base(command)
		}
	}
	client, err := lpa.New(&lpa.Options{
		Channel: &euiccCardChannel{ctx: context.Background(), card: card},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.HTTP.Client = &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // test server only
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}}
	expected := agentlink.EUICCNotificationEntry{
		SequenceNumber: 17, Event: "delete", ICCID: "8944000000000000001", Address: "notify.example.com",
	}
	acknowledged, removed, err := deliverEUICCNotificationWithClient(context.Background(), client, expected)
	if err != nil || !acknowledged || !removed || httpRequests != 1 ||
		len(cardOperations) != 2 || cardOperations[0] != "retrieve" || cardOperations[1] != "remove" {
		t.Fatalf("ack=%t removed=%t http=%d card=%v err=%v", acknowledged, removed, httpRequests, cardOperations, err)
	}
}

func TestRemoveAcknowledgedNotificationRetrievesExactEntryThenRemovesWithoutHTTP(t *testing.T) {
	card := euiccCard(t, emptyProfileResponse())
	base := card.handler
	var cardOperations []string
	card.handler = func(command []byte) ([]byte, error) {
		switch {
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x2B}):
			cardOperations = append(cardOperations, "retrieve")
			metadata := notificationMetadata(t, 18, 4, "8944000000000000001", "notify.example.com")
			pending := bertlv.NewChildren(bertlv.ContextSpecific.Constructed(55),
				bertlv.NewChildren(bertlv.ContextSpecific.Constructed(39), metadata))
			payload := bertlv.NewChildren(bertlv.ContextSpecific.Constructed(43),
				bertlv.NewChildren(bertlv.ContextSpecific.Constructed(0), pending)).Bytes()
			return append(payload, 0x90, 0x00), nil
		case len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x30}):
			cardOperations = append(cardOperations, "remove")
			payload := bertlv.NewChildren(bertlv.ContextSpecific.Constructed(48),
				bertlv.NewValue(bertlv.ContextSpecific.Primitive(0), []byte{0})).Bytes()
			return append(payload, 0x90, 0x00), nil
		default:
			return base(command)
		}
	}
	removed, err := removeEUICCNotification(context.Background(), card, nil, agentlink.EUICCNotificationEntry{
		SequenceNumber: 18, Event: "enable", ICCID: "8944000000000000001", Address: "notify.example.com",
	})
	if err != nil || !removed || len(cardOperations) != 2 ||
		cardOperations[0] != "retrieve" || cardOperations[1] != "remove" {
		t.Fatalf("removed=%t operations=%v err=%v", removed, cardOperations, err)
	}
	cardOperations = nil
	removed, err = removeEUICCNotification(context.Background(), card, nil, agentlink.EUICCNotificationEntry{
		SequenceNumber: 18, Event: "enable", ICCID: "8944000000000000001", Address: "changed.example.com",
	})
	if removed || !errors.Is(err, errNotificationChanged) || len(cardOperations) != 1 || cardOperations[0] != "retrieve" {
		t.Fatalf("stale removed=%t operations=%v err=%v", removed, cardOperations, err)
	}
}

func TestNotificationInventoryRoutesExactSecondSecureElementWithoutRefreshingSession(t *testing.T) {
	secondEID := "89049032000000000000000000000002"
	card := estkDualCard(t, testEID, secondEID)
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
	calls := 0
	manager.listNotifications = func(_ context.Context, gotCard Card, aid []byte) ([]agentlink.EUICCNotificationEntry, error) {
		calls++
		if gotCard != card || !bytes.Equal(aid, estkSE1AID) {
			t.Fatalf("card=%p aid=%X", gotCard, aid)
		}
		return []agentlink.EUICCNotificationEntry{{SequenceNumber: 3, Event: "install", Address: "notify.example.com"}}, nil
	}
	deliveryCalls := 0
	manager.deliverNotification = func(_ context.Context, gotCard Card, aid []byte,
		expected agentlink.EUICCNotificationEntry) (bool, bool, error) {
		deliveryCalls++
		if gotCard != card || !bytes.Equal(aid, estkSE1AID) || expected.SequenceNumber != 3 ||
			expected.Event != "install" || expected.Address != "notify.example.com" {
			t.Fatalf("card=%p aid=%X expected=%+v", gotCard, aid, expected)
		}
		return true, true, nil
	}
	removalCalls := 0
	manager.removeNotification = func(_ context.Context, gotCard Card, aid []byte,
		expected agentlink.EUICCNotificationEntry) (bool, error) {
		removalCalls++
		if gotCard != card || !bytes.Equal(aid, estkSE1AID) || expected.SequenceNumber != 3 ||
			expected.Event != "install" || expected.Address != "notify.example.com" {
			t.Fatalf("card=%p aid=%X expected=%+v", gotCard, aid, expected)
		}
		return true, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "insertion-1"})
	}()
	waitForSession(t, manager, "insertion-1")
	result := manager.ExecuteEUICCNotification(context.Background(), agentlink.EUICCNotificationRequest{
		OperationID: "notification-se2", SessionGeneration: "insertion-1", EID: secondEID,
	})
	if result.Failure != nil || len(result.Entries) != 1 || calls != 1 || len(manager.Sessions()) != 1 {
		t.Fatalf("result=%+v calls=%d sessions=%+v", result, calls, manager.Sessions())
	}
	wrong := manager.ExecuteEUICCNotification(context.Background(), agentlink.EUICCNotificationRequest{
		OperationID: "notification-wrong-eid", SessionGeneration: "insertion-1",
		EID: "89049032000000000000000000000003",
	})
	if wrong.Failure == nil || wrong.Failure.Code != "euicc_identity_mismatch" || calls != 1 {
		t.Fatalf("wrong=%+v calls=%d", wrong, calls)
	}
	delivery := manager.ExecuteEUICCNotification(context.Background(), agentlink.EUICCNotificationRequest{
		OperationID: "notification-delivery-se2", SessionGeneration: "insertion-1", EID: secondEID,
		Action: agentlink.EUICCNotificationDeliver,
		Expected: &agentlink.EUICCNotificationEntry{
			SequenceNumber: 3, Event: "install", Address: "notify.example.com",
		},
	})
	if delivery.Failure != nil || !delivery.Acknowledged || !delivery.Removed || deliveryCalls != 1 || calls != 1 {
		t.Fatalf("delivery=%+v deliveryCalls=%d inventoryCalls=%d", delivery, deliveryCalls, calls)
	}
	removal := manager.ExecuteEUICCNotification(context.Background(), agentlink.EUICCNotificationRequest{
		OperationID: "notification-removal-se2", SessionGeneration: "insertion-1", EID: secondEID,
		Action: agentlink.EUICCNotificationRemove,
		Expected: &agentlink.EUICCNotificationEntry{
			SequenceNumber: 3, Event: "install", Address: "notify.example.com",
		},
	})
	if removal.Failure != nil || removal.Acknowledged || !removal.Removed || removalCalls != 1 ||
		deliveryCalls != 1 || calls != 1 {
		t.Fatalf("removal=%+v removalCalls=%d deliveryCalls=%d inventoryCalls=%d", removal, removalCalls, deliveryCalls, calls)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func notificationListResponse(t *testing.T, notifications ...*bertlv.TLV) []byte {
	t.Helper()
	return bertlv.NewChildren(
		bertlv.ContextSpecific.Constructed(40),
		bertlv.NewChildren(bertlv.ContextSpecific.Constructed(0), notifications...),
	).Bytes()
}

func notificationMetadata(t *testing.T, sequence int64, event int, iccid, address string) *bertlv.TLV {
	t.Helper()
	sequenceBytes, err := primitive.MarshalInt(sgp22.SequenceNumber(sequence)).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	bits := make([]bool, event+1)
	bits[event] = true
	eventBytes, err := primitive.MarshalBitString(bits).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if len(bits)%8 == 0 {
		eventBytes = eventBytes[:len(eventBytes)-1]
		eventBytes[0] = 0
	}
	children := []*bertlv.TLV{
		bertlv.NewValue(bertlv.ContextSpecific.Primitive(0), sequenceBytes),
		bertlv.NewValue(bertlv.ContextSpecific.Primitive(1), eventBytes),
		bertlv.NewValue(bertlv.Universal.Primitive(12), []byte(address)),
	}
	if iccid != "" {
		encoded, err := sgp22.NewICCID(iccid)
		if err != nil {
			t.Fatal(err)
		}
		children = append(children, bertlv.NewValue(bertlv.Application.Primitive(26), encoded))
	}
	return bertlv.NewChildren(bertlv.ContextSpecific.Constructed(47), children...)
}
