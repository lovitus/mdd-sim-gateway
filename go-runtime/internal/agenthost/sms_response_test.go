package agenthost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsim"
)

type smsResponseOperator struct{}

func (smsResponseOperator) Run(context.Context) error { return nil }
func (smsResponseOperator) Operate(_ context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	if operation.Action == agentmodem.OperationSMSList {
		return agentmodem.OperationResult{SMS: agentmodem.SMSResult{State: "listed"}}, nil
	}
	return agentmodem.OperationResult{SMS: agentmodem.SMSResult{State: "submitted", References: []int{0, 23}}}, nil
}

func TestActualWorkerSMSRepliesSurviveWireValidation(t *testing.T) {
	worker := &Worker{manager: &agentsim.Manager{}, config: Config{Operations: smsResponseOperator{}}}
	for _, action := range []agentlink.ModemAction{agentlink.ModemSMSSend, agentlink.ModemSMSReceipt, agentlink.ModemSMSList} {
		request := agentlink.ModemRequest{OperationID: "sms-operation", AttachmentID: "attachment", EquipmentID: "862547055201716", CardID: "8985200000000000001", Action: action}
		if action != agentlink.ModemSMSList {
			request.Number = "+15550100123"
			request.Body = "fixture"
		}
		if err := request.Validate(); err != nil {
			t.Fatal(err)
		}
		response := worker.ExecuteModem(context.Background(), request)
		wire, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		var decoded agentlink.ModemResponse
		if err := json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		if err := decoded.ValidateFor(request); err != nil {
			t.Fatalf("action=%s invalid Worker response: %v", action, err)
		}
		if (action == agentlink.ModemSMSList) != (decoded.SMS.Messages != nil) {
			t.Fatalf("wrong null/empty semantics for %s", action)
		}
	}
}
