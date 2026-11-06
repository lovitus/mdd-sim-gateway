package agenthost

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcall"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentevents"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsim"
)

type incomingBorrowHardware struct {
	call           agentmodem.CallResult
	answers, stops int
}

func (h *incomingBorrowHardware) Operate(_ context.Context, op agentmodem.Operation) (agentmodem.OperationResult, error) {
	switch op.Action {
	case agentmodem.OperationCallAnswer:
		h.answers++
		h.call.State = "active"
	case agentmodem.OperationCallHangup:
		h.call.State = "idle"
		h.call.TerminalConfirmed = true
	case agentmodem.OperationCallStatus:
	default:
		return agentmodem.OperationResult{}, errors.New("unexpected operation")
	}
	return agentmodem.OperationResult{Call: h.call}, nil
}
func (*incomingBorrowHardware) PrepareData(context.Context, agentdata.Target, agentdata.Profile) (string, error) {
	return "carrier", nil
}
func (*incomingBorrowHardware) DialData(context.Context, agentdata.Target, string, string) (net.Conn, error) {
	return nil, errors.New("no data test")
}
func (h *incomingBorrowHardware) StopData(context.Context, agentdata.Target) error {
	h.stops++
	return nil
}

type borrowAdmission struct{}

func (borrowAdmission) ResolveDataProfile(context.Context, agentdata.Target, string, string, string) (agentdata.Profile, error) {
	return agentdata.Profile{Name: "carrier"}, nil
}
func (borrowAdmission) ValidateDataTarget(context.Context, agentdata.Target) error { return nil }
func (borrowAdmission) ValidateDataLease(context.Context, agentdata.Target) error  { return nil }
func (borrowAdmission) DataPrepared(agentdata.Target, string, string)              {}
func (borrowAdmission) DataCleanup(agentdata.Target, string, string)               {}
func (borrowAdmission) DataReleased(agentdata.Target, string)                      {}

func TestCoexistenceWorkerAnswersWithDurableIncomingFenceAndBorrower(t *testing.T) {
	now := time.Now()
	h := &incomingBorrowHardware{call: agentmodem.CallResult{State: "ringing_in", Direction: "in", NativeIndex: 1, VoiceCalls: 1, IncomingCalls: 1, Authoritative: true, ObservedAt: now}}
	calls, err := agentcall.Open(filepath.Join(t.TempDir(), "calls.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer calls.Close()
	cm, err := agentcall.NewManager(calls, h)
	if err != nil {
		t.Fatal(err)
	}
	events, err := agentevents.Open(filepath.Join(t.TempDir(), "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	events.MarkReady()
	fence := agentevents.Fence{AttachmentID: "attachment", EquipmentID: "862547055201716", CardID: "8985200000000000001", SIMSessionGeneration: "generation"}
	if err := events.ObserveCall(fence, h.call, now); err != nil {
		t.Fatal(err)
	}
	pending, err := events.PendingModemEvents(now, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("incoming event=%+v %v", pending, err)
	}
	if err := cm.BindIncomingCallVerifier(events); err != nil {
		t.Fatal(err)
	}
	sim, err := agentsim.NewManager(unusedConnector{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{config: Config{Operations: cm}, manager: sim}
	data, err := agentdata.NewManager(agentdata.Config{Context: t.Context(), ServerURL: "http://127.0.0.1:1", ServerToken: hostTestToken,
		AgentID: "agent", ProcessGeneration: "process", HTTPClient: http.DefaultClient, Backend: h, Admission: borrowAdmission{}, Coordinator: cm})
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	prep := agentlink.ModemDataRequest{OperationID: "prepare", Action: agentlink.ModemDataPrepare, AttachmentID: fence.AttachmentID, EquipmentID: fence.EquipmentID,
		CardID: fence.CardID, SIMSessionGeneration: fence.SIMSessionGeneration, SessionID: "borrower", ExpiresAt: now.Add(time.Minute), MaxBytes: 4096}
	if out := data.ExecuteModemData(t.Context(), prep); out.Failure != nil {
		t.Fatal(out)
	}
	adapter := dataCoordinatedModems{worker: w, data: data}
	request := agentlink.ModemRequest{OperationID: "answer", Action: agentlink.ModemCallAnswer, AttachmentID: fence.AttachmentID, EquipmentID: fence.EquipmentID,
		CardID: fence.CardID, SIMSessionGeneration: fence.SIMSessionGeneration, LeaseID: "paid-lease", IncomingEventID: pending[0].Call.IncomingEventID,
		NativeCallIndex: 1, CallOccurrence: pending[0].Call.Occurrence}
	defer func() { request.Action = agentlink.ModemCallHangup; adapter.ExecuteModem(t.Context(), request) }()
	if out := adapter.ExecuteModem(t.Context(), request); out.Failure != nil || h.answers != 1 {
		t.Fatalf("worker borrower blocked answer: %+v", out)
	}
	if records, err := calls.Records(); err != nil || len(records) != 1 || records[0].Direction != "in" {
		t.Fatal("answer lost durable lease")
	}
	prep.Action = agentlink.ModemDataStop
	prep.OperationID = "stop"
	if out := data.ExecuteModemData(t.Context(), prep); out.Failure == nil || h.stops != 0 {
		t.Fatal("cleanup disrupted active answer")
	}
	request.Action = agentlink.ModemCallHangup
	if out := adapter.ExecuteModem(t.Context(), request); out.Failure != nil || h.call.State != "idle" {
		t.Fatalf("independent hangup blocked: %+v", out)
	}
	if out := data.ExecuteModemData(t.Context(), prep); out.Failure != nil {
		t.Fatal(out)
	}
	if records, _ := calls.Records(); len(records) != 0 {
		t.Fatal("hangup left paid lease")
	}
}
