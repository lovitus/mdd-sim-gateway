package agentsim

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/damonto/euicc-go/bertlv"
	"github.com/damonto/euicc-go/bertlv/primitive"
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
