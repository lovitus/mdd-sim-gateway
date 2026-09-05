package agenthost

import (
	"context"
	"net/http"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcall"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type fakeProvisionHardware struct {
	calls       int
	err         error
	badReadback bool
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
