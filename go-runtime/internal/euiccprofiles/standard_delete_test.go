package euiccprofiles

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

type deletionFixture struct {
	*fakeAgents
	store              *events.BoltStore
	present            bool
	deletes            int
	loseReply          bool
	archiveUnavailable bool
}

func (f *deletionFixture) ExecuteEUICCProfileCommand(_ context.Context, c agentlink.EUICCProfileCommand) (agentlink.EUICCProfileResponse, error) {
	if c.Action == agentlink.EUICCProfileRefresh {
		var profiles []agentlink.EUICCProfileFact
		if f.present {
			profiles = []agentlink.EUICCProfileFact{{ICCID: testICCID, State: agentlink.EUICCProfileDisabled}}
		}
		return agentlink.EUICCProfileResponse{OperationID: c.OperationID, SessionGeneration: "fixture", EID: c.EID, Action: c.Action, Outcome: agentlink.EUICCProfileRefreshed, Inventory: &agentlink.EUICCFact{EID: testEID, InventoryRefresh: true, ProfileManagement: true, SoftDelete: true, NotificationInventory: true, ProfilesAvailable: true, ProfileDeletion: true, Profiles: profiles}}, nil
	}
	if c.Action != agentlink.EUICCProfileDelete {
		return agentlink.EUICCProfileResponse{}, errors.New("unexpected card mutation")
	}
	if record, found, err := f.store.EUICCDeletion(c.EID, c.OperationID); err != nil || !found || record.State != "pending" {
		return agentlink.EUICCProfileResponse{}, errors.New("intent not durable before card deletion")
	}
	f.deletes++
	f.present = false
	if f.loseReply {
		return agentlink.EUICCProfileResponse{}, errors.New("lost Agent reply after card deletion")
	}
	return agentlink.EUICCProfileResponse{OperationID: c.OperationID, SessionGeneration: "fixture", EID: c.EID, ICCID: c.ICCID, Action: c.Action, Outcome: agentlink.EUICCProfileRefreshPending, Changed: true}, nil
}
func (f *deletionFixture) ExecuteEUICCNotificationCommand(_ context.Context, c agentlink.EUICCNotificationCommand) (agentlink.EUICCNotificationResponse, error) {
	entry := agentlink.EUICCNotificationEntry{Event: "delete", ICCID: testICCID, SequenceNumber: 0, Address: "notify.example.com"}
	if !f.present && f.archiveUnavailable {
		return agentlink.EUICCNotificationResponse{}, errors.New("Agent disconnected before archive")
	}
	if c.Action == "" {
		entries := []agentlink.EUICCNotificationEntry{entry}
		if !f.present {
			entry.SequenceNumber = 1
			entries = append(entries, entry)
		}
		return agentlink.EUICCNotificationResponse{OperationID: c.OperationID, SessionGeneration: "fixture", EID: c.EID, Entries: entries}, nil
	}
	if c.Action == agentlink.EUICCNotificationArchive {
		return agentlink.EUICCNotificationResponse{OperationID: c.OperationID, SessionGeneration: "fixture", EID: c.EID, Payload: []byte{0x30, byte(c.Expected.SequenceNumber)}}, nil
	}
	return agentlink.EUICCNotificationResponse{}, errors.New("unexpected network send or remove")
}

func TestStandardDeletionRecoversAfterLostReplyAndArchiveGap(t *testing.T) {
	for _, loseReply := range []bool{false, true} {
		name := "archive_gap"
		if loseReply {
			name = "reply_lost"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.db")
			store, err := events.OpenBoltStore(path, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			prior, err := store.SaveEUICCNotification(testEID, agentlink.EUICCNotificationEntry{Event: "delete", ICCID: testICCID, SequenceNumber: 0, Address: "notify.example.com"}, []byte{0x30, 0})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = store.BeginEUICCReplay(testEID, 0, "old-attempt", prior.SHA256); err != nil {
				t.Fatal(err)
			}
			if err = store.FinishEUICCReplay(testEID, 0, "old-attempt", "acknowledged"); err != nil {
				t.Fatal(err)
			}
			fixture := &deletionFixture{fakeAgents: &fakeAgents{}, store: store, present: true, loseReply: loseReply, archiveUnavailable: true}
			service, err := New(fixture, WithDeletionStore(store))
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mux.Handle("POST /v1/euiccs/{eid}/profiles/{iccid}/{action}", service)
			mux.Handle("POST /v1/euiccs/{eid}/deletions/{operation_id}/recover", service)
			route := "/v1/euiccs/" + testEID + "/profiles/" + testICCID + "/delete"
			body := map[string]any{"operation_id": "standard-delete", "confirm_iccid": testICCID, "confirm_permanent_delete": true, "confirm_delivery_may_be_pending": true}
			for _, field := range []string{"confirm_iccid", "confirm_permanent_delete", "confirm_delivery_may_be_pending"} {
				copy := map[string]any{}
				for k, v := range body {
					copy[k] = v
				}
				delete(copy, field)
				if result := post(t, mux, route, copy); result.Code != 400 {
					t.Fatal("missing confirmation accepted")
				}
			}
			if fixture.deletes != 0 {
				t.Fatal("unconfirmed card delete")
			}
			result := post(t, mux, route, body)
			if result.Code != 202 || fixture.deletes != 1 {
				t.Fatal(result.Code, result.Body.String())
			}
			var decoded struct {
				Operation events.EUICCDeletion `json:"operation"`
				Delivery  string               `json:"delivery"`
			}
			if err := json.Unmarshal(result.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Delivery == "receiver_acknowledged" {
				t.Fatal("old ACK satisfied new deletion")
			}
			if result = post(t, mux, route, body); result.Code != 200 || fixture.deletes != 1 {
				t.Fatal("duplicate card deletion")
			}
			store.Close()
			store, err = events.OpenBoltStore(path, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			service.deletions = store
			fixture.store = store
			fixture.archiveUnavailable = false
			result = post(t, mux, "/v1/euiccs/"+testEID+"/deletions/standard-delete/recover", map[string]any{})
			if result.Code != 200 {
				t.Fatal(result.Body.String())
			}
			if err := json.Unmarshal(result.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Delivery != "pending_delivery" || len(decoded.Operation.Notifications) != 1 || decoded.Operation.Notifications[0] != 1 || fixture.deletes != 1 {
				t.Fatal("recovery did not preserve split state", result.Body.String())
			}
			archive, err := store.EUICCNotificationArchive(testEID, 1)
			if err != nil || len(archive.Payload) == 0 {
				t.Fatal("new notification not durable")
			}
			if _, _, err = store.BeginEUICCReplay(testEID, 1, "new-attempt", archive.SHA256); err != nil {
				t.Fatal(err)
			}
			if err = store.FinishEUICCReplay(testEID, 1, "new-attempt", "acknowledged"); err != nil {
				t.Fatal(err)
			}
			if service.deletionDelivery(decoded.Operation) != "receiver_acknowledged" {
				t.Fatal("new ACK not reflected")
			}
			if result = post(t, mux, route, body); result.Code != 200 || fixture.deletes != 1 {
				t.Fatal("replayed physical deletion after recovery")
			}
		})
	}
}
