package agenthost

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcall"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type fakeProvisionHardware struct {
	calls       int
	err         error
	badReadback bool
	entered     chan struct{}
	release     chan struct{}
}

type rejectingProvisionAuxiliary struct{}

func (rejectingProvisionAuxiliary) DoAuxiliary(context.Context, string, func(context.Context) error) error {
	return agentcall.ErrAuxiliaryDuringCall
}

func (hardware *fakeProvisionHardware) ApplyProvision(_ context.Context, request agentlink.ProvisionRequest) (string, ProvisionReadback, error) {
	hardware.calls++
	if hardware.err != nil {
		return "apply", ProvisionReadback{}, hardware.err
	}
	command := request.ProvisionCommand
	readback := ProvisionReadback{
		EquipmentID: command.EquipmentID, CardID: command.CardID, SIMSessionGeneration: command.SIMSessionGeneration,
		IMSI: command.IMSI, MCC: command.MCC, MNC: command.MNC, IMEI: command.IMEI, IMEISV: command.IMEISV,
		MSISDN: command.MSISDN, SMSC: command.SMSC, ReaderPort: command.ReaderPort, APN: command.APN,
	}
	if hardware.badReadback {
		readback.SMSC = "wrong"
	}
	return "apply", readback, nil
}

func (hardware *fakeProvisionHardware) ReadProvision(_ context.Context, request agentlink.ProvisionRequest) (string, ProvisionReadback, error) {
	hardware.calls++
	if hardware.entered != nil {
		close(hardware.entered)
		<-hardware.release
	}
	if hardware.err != nil {
		return "readback", ProvisionReadback{}, hardware.err
	}
	command := request.ProvisionCommand
	readback := ProvisionReadback{
		EquipmentID: command.EquipmentID, CardID: command.CardID, SIMSessionGeneration: command.SIMSessionGeneration,
		IMSI: command.IMSI, MCC: command.MCC, MNC: command.MNC, IMEI: command.IMEI, IMEISV: command.IMEISV,
		MSISDN: command.MSISDN, SMSC: command.SMSC, ReaderPort: command.ReaderPort, APN: command.APN,
	}
	if hardware.badReadback {
		readback.SMSC = "wrong"
	}
	return "readback", readback, nil
}

func provisionTestRequest(generation string) agentlink.ProvisionRequest {
	return agentlink.ProvisionRequest{ProvisionCommand: agentlink.ProvisionCommand{
		OperationID: "provision-1", LineID: "line-1", Enabled: true,
		EquipmentID: "862547055201716", CardID: "8985200000000000001", AttachmentID: "mbn-a",
		SIMSessionGeneration: generation, IMSI: "234100000000001", MCC: "234", MNC: "10",
		IMEI: "862547055201716", SMSC: "+441234567890",
	}}
}

func provisionTestWorker(t *testing.T, hardware ProvisionHardware) (*Worker, agentlink.ProvisionRequest) {
	t.Helper()
	config := testHostConfig("ws://127.0.0.1:1/v1/agent/ws", nil)
	config.HTTPClient = contextHTTPClient()
	config.ProvisionHardware = hardware
	config.ModemAuxiliary = passAuxiliary{}
	worker, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	fact := agentmodem.Fact{AttachmentID: "mbn-a", EquipmentID: "862547055201716", Condition: agentmodem.DeviceReady,
		AT:  agentmodem.ATControlFact{State: agentmodem.ATControlReady},
		SIM: agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: "8985200000000000001"}}
	worker.modems.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady, Modems: []agentmodem.Fact{fact}})
	request := provisionTestRequest(worker.Topology().Modems[0].SIM.SessionGeneration)
	return worker, request
}

func contextHTTPClient() *http.Client { return http.DefaultClient }

func TestExecuteProvisionRequiresExactReadyTargetAndApplies(t *testing.T) {
	hardware := &fakeProvisionHardware{}
	worker, request := provisionTestWorker(t, hardware)
	response := worker.ExecuteProvision(context.Background(), request)
	if response.State != agentlink.ProvisionApplied || response.Step != "apply" || hardware.calls != 1 {
		t.Fatalf("response=%+v calls=%d", response, hardware.calls)
	}
}

func TestReconcileProvisionReadsExactTargetWithoutApplying(t *testing.T) {
	hardware := &fakeProvisionHardware{}
	worker, request := provisionTestWorker(t, hardware)
	response := worker.ReconcileProvision(context.Background(), request)
	if response.State != agentlink.ProvisionApplied || response.Step != "reconcile_readback" || hardware.calls != 1 {
		t.Fatalf("response=%+v calls=%d", response, hardware.calls)
	}
}

func TestReconcileProvisionRebindsStaleSessionWithoutWriting(t *testing.T) {
	hardware := &fakeProvisionHardware{}
	worker, request := provisionTestWorker(t, hardware)
	wanted := request.SIMSessionGeneration
	request.SIMSessionGeneration = "stale-session"
	response := worker.ReconcileProvision(context.Background(), request)
	if response.State != agentlink.ProvisionApplied || response.SIMSessionGeneration != wanted ||
		response.Step != "reconcile_readback" || hardware.calls != 1 {
		t.Fatalf("response=%+v calls=%d", response, hardware.calls)
	}
}

func TestProvisionSerializesTopologyPublishThroughPostcondition(t *testing.T) {
	hardware := &fakeProvisionHardware{entered: make(chan struct{}), release: make(chan struct{})}
	worker, request := provisionTestWorker(t, hardware)
	result := make(chan agentlink.ProvisionResponse, 1)
	go func() { result <- worker.ReconcileProvision(context.Background(), request) }()
	select {
	case <-hardware.entered:
	case <-time.After(time.Second):
		t.Fatal("provision readback did not enter hardware transaction")
	}
	scanDone := make(chan struct{})
	go func() {
		_ = (modemCycleCoordinator{mu: &worker.modemCycleMu}).DoBackgroundScan(context.Background(), func(context.Context) error {
			worker.modems.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady})
			return nil
		})
		close(scanDone)
	}()
	select {
	case <-scanDone:
		t.Fatal("topology publish interleaved with provision readback")
	case <-time.After(20 * time.Millisecond):
	}
	close(hardware.release)
	if response := <-result; response.State != agentlink.ProvisionApplied {
		t.Fatalf("response=%+v", response)
	}
	select {
	case <-scanDone:
	case <-time.After(time.Second):
		t.Fatal("topology publish did not resume after provision")
	}
}

func TestReconcileProvisionFailsClosedOnReadbackMismatch(t *testing.T) {
	hardware := &fakeProvisionHardware{badReadback: true}
	worker, request := provisionTestWorker(t, hardware)
	response := worker.ReconcileProvision(context.Background(), request)
	if response.State != agentlink.ProvisionUnknown || response.ErrorCode != "provision_smsc_readback_mismatch" {
		t.Fatalf("response=%+v", response)
	}
}

func TestExecuteProvisionMapsInterruptedHardwareToUnknown(t *testing.T) {
	hardware := &fakeProvisionHardware{err: context.Canceled}
	worker, request := provisionTestWorker(t, hardware)
	response := worker.ExecuteProvision(context.Background(), request)
	if response.State != agentlink.ProvisionUnknown || response.ErrorCode != "provision_hardware_interrupted" {
		t.Fatalf("response=%+v", response)
	}
}

func TestExecuteProvisionDoesNotRunWhenTargetWasReplaced(t *testing.T) {
	hardware := &fakeProvisionHardware{}
	worker, request := provisionTestWorker(t, hardware)
	worker.modems.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady})
	response := worker.ExecuteProvision(context.Background(), request)
	if response.State != agentlink.ProvisionUnknown || response.ErrorCode != "provision_target_replaced" || hardware.calls != 0 {
		t.Fatalf("response=%+v calls=%d", response, hardware.calls)
	}
}

func TestExecuteProvisionDoesNotRebindStaleSession(t *testing.T) {
	hardware := &fakeProvisionHardware{}
	worker, request := provisionTestWorker(t, hardware)
	request.SIMSessionGeneration = "stale-session"
	response := worker.ExecuteProvision(context.Background(), request)
	if response.State != agentlink.ProvisionUnknown || response.ErrorCode != "provision_target_replaced" || hardware.calls != 0 {
		t.Fatalf("response=%+v calls=%d", response, hardware.calls)
	}
}

func TestExecuteProvisionDoesNotRunDuringPaidCall(t *testing.T) {
	hardware := &fakeProvisionHardware{}
	worker, request := provisionTestWorker(t, hardware)
	worker.config.ModemAuxiliary = rejectingProvisionAuxiliary{}
	response := worker.ExecuteProvision(context.Background(), request)
	if response.State != agentlink.ProvisionUnknown || response.ErrorCode != "provision_active_call" || hardware.calls != 0 {
		t.Fatalf("response=%+v calls=%d", response, hardware.calls)
	}
}

func TestExecuteProvisionRejectsMismatchedHardwareReadback(t *testing.T) {
	hardware := &fakeProvisionHardware{badReadback: true}
	worker, request := provisionTestWorker(t, hardware)
	response := worker.ExecuteProvision(context.Background(), request)
	if response.State != agentlink.ProvisionUnknown || response.ErrorCode != "provision_readback_mismatch" || hardware.calls != 1 {
		t.Fatalf("response=%+v calls=%d", response, hardware.calls)
	}
}
