package cellularmedia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmedia"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
)

type receiptAgent struct {
	fakeAgentRuntime
	calls        int
	confirmed    bool
	endOperation string
	hangups      int
}

func (a *receiptAgent) Status(id string) (agentlink.ConnectionStatus, bool) {
	status, found := a.fakeAgentRuntime.Status(id)
	status.Capabilities = append(status.Capabilities, agentlink.ModemCallReceiptFeature)
	return status, found
}

func (a *receiptAgent) ExecuteModem(_ context.Context, agent, generation string, r agentlink.ModemRequest) (agentlink.ModemResponse, error) {
	a.calls++
	if a.endOperation != "" && r.Action == agentlink.ModemCallHangup && r.OperationID == a.endOperation && r.LeaseID == "original-session" && agent == "agent-1" && generation == "generation-1" && r.AttachmentID == "attachment-1" && r.EquipmentID == "862547055201716" && r.CardID == "8985200000000000001" {
		a.hangups++
		a.confirmed = true
		return agentlink.ModemResponse{OperationID: r.OperationID, AttachmentID: r.AttachmentID, EquipmentID: r.EquipmentID, CardID: r.CardID,
			Call: &agentlink.ModemCallResult{State: "idle", ObservedAt: time.Now().UTC(), Authoritative: true, TerminalConfirmed: true, Strategy: "chup"}}, nil
	}
	if agent != "agent-1" || generation != "generation-1" || r.Action != agentlink.ModemCallReceipt || r.OperationID != "original-start" || r.LeaseID != "original-session" {
		return agentlink.ModemResponse{}, errors.New("unexpected receipt request")
	}
	if !a.confirmed {
		return agentlink.ModemResponse{}, &agentlink.RemoteError{Kind: "not_ready", Code: "modem_call_terminal_unconfirmed"}
	}
	return agentlink.ModemResponse{OperationID: r.OperationID, AttachmentID: r.AttachmentID, EquipmentID: r.EquipmentID, CardID: r.CardID,
		Call: &agentlink.ModemCallResult{State: "idle", ObservedAt: time.Now().UTC(), Authoritative: true, TerminalConfirmed: true, Strategy: "stored_terminal"}}, nil
}

func TestNewAuthenticatedSubjectCanEndOnlyOriginalBoundCellularCall(t *testing.T) {
	store, err := callhistory.Open(filepath.Join(t.TempDir(), "history.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := strings.Repeat("a", 64)
	target := agentlink.ModemTarget{AgentID: "agent-1", ProcessGeneration: "generation-1", AttachmentID: "attachment-1", EquipmentID: "862547055201716", CardID: "8985200000000000001"}
	record := callhistory.RecoveryRecord{LineID: "line-1", Transport: "cellular", CardID: target.CardID, CallID: "original-call", OperationID: "original-start", SessionID: "original-session", Subject: "original-login", Target: target, CreatedAt: time.Now().Add(-time.Minute)}
	if err = store.BindRecovery(record, key); err != nil {
		t.Fatal(err)
	}
	record, err = store.ReadRecovery(record.LineID, record.Transport, record.CallID, record.OperationID, key)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := agentmedia.NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return serviceTestToken, nil }), nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	agents := &receiptAgent{endOperation: "original-end"}
	current := &session{id: record.SessionID, lineID: record.LineID, callID: record.CallID, subject: record.Subject, target: target, phase: "hangup_unconfirmed", recovery: &record}
	service := &Service{config: Config{Auth: fakeBrowserAuth{}, Recovery: store, Agents: agents, Broker: broker, Now: time.Now}, sessions: map[string]*session{record.SessionID: current}}
	invoke := func(capability string, authorized bool) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(map[string]string{"action": "end", "call_id": record.CallID, "operation_id": record.OperationID, "recovery_key": capability, "end_operation_id": "original-end"})
		r := httptest.NewRequest("POST", "/v1/lines/line-1/cellular/calls/recovery", bytes.NewReader(payload))
		r.Header.Set("X-MDD-CSRF-Token", "csrf-1")
		if authorized {
			r.Header.Set("Cookie", "test-session=browser-token")
		}
		w := httptest.NewRecorder()
		service.recoverCall(w, r, record.LineID)
		return w
	}
	if w := invoke(key, false); w.Code != 403 || agents.calls != 0 {
		t.Fatal("unauthenticated end reached Agent", w.Code)
	}
	if w := invoke(strings.Repeat("b", 64), true); w.Code != 404 || agents.calls != 0 {
		t.Fatal("new login without grant took control", w.Code)
	}
	current.target.CardID = "another-card"
	if w := invoke(key, true); w.Code != 409 || agents.hangups != 0 {
		t.Fatal("changed current card was controlled", w.Code)
	}
	current.target = target
	w := invoke(key, true)
	var result map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result["terminal_confirmed"] != true || result["session_id"] != record.SessionID || agents.hangups != 1 {
		t.Fatalf("end did not confirm original call: %d %s", w.Code, w.Body.String())
	}
	if _, exists := service.sessions[record.SessionID]; exists {
		t.Fatal("confirmed original session was not retired")
	}
	requests := agents.calls
	if w = invoke(key, true); w.Code != 200 || agents.calls != requests || agents.hangups != 1 {
		t.Fatal("receipt retry executed another hangup", w.Code)
	}
	saved, err := store.ReadRecovery(record.LineID, record.Transport, record.CallID, record.OperationID, key)
	if err != nil || saved.Subject != record.Subject || saved.TerminalAt.IsZero() {
		t.Fatal("recovery rebound the call subject or lost terminal evidence", err)
	}
}

func TestRecoveryAfterCoreSessionLossUsesExactAgentReceiptAndCapability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := callhistory.Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	target := agentlink.ModemTarget{AgentID: "agent-1", ProcessGeneration: "old-process", AttachmentID: "attachment-1", EquipmentID: "862547055201716", CardID: "8985200000000000001"}
	record := callhistory.RecoveryRecord{LineID: "line-1", Transport: "cellular", CardID: target.CardID, CallID: "original-call", OperationID: "original-start", SessionID: "original-session", Subject: "original-login", Target: target, CreatedAt: time.Now().Add(-time.Minute)}
	if err = store.BindRecovery(record, key); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = callhistory.Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	agents := &receiptAgent{confirmed: true}
	service := &Service{config: Config{Auth: fakeBrowserAuth{}, Recovery: store, Agents: agents}, sessions: map[string]*session{}}
	invoke := func(k string, authorized bool) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(map[string]string{"action": "status", "call_id": record.CallID, "operation_id": record.OperationID, "recovery_key": k})
		r := httptest.NewRequest("POST", "/v1/lines/line-1/cellular/calls/recovery", bytes.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-MDD-CSRF-Token", "csrf-1")
		if authorized {
			r.Header.Set("Cookie", "test-session=browser-token")
		}
		w := httptest.NewRecorder()
		service.recoverCall(w, r, "line-1")
		return w
	}
	if response := invoke(key, false); response.Code != 403 || agents.calls != 0 {
		t.Fatal("unauthenticated recovery reached agent", response.Code)
	}
	if response := invoke(strings.Repeat("b", 64), true); response.Code != 404 || agents.calls != 0 {
		t.Fatal("unrelated capability reached agent", response.Code)
	}
	response := invoke(key, true)
	var result map[string]any
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result["terminal_confirmed"] != true || result["session_id"] != record.SessionID || agents.calls != 1 {
		t.Fatalf("%d %s calls=%d", response.Code, response.Body.String(), agents.calls)
	}
	// The new authenticated subject differs from the original login; capability,
	// durable association and exact hardware evidence are all required.
	if response = invoke(key, true); response.Code != 200 || agents.calls != 1 {
		t.Fatal("durable terminal queried hardware again")
	}
}
