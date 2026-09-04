package agentlink

import "testing"

func boolPointer(value bool) *bool { return &value }

func TestSIMPINContractsFenceReaderAndModemInsertions(t *testing.T) {
	reader := SIMPINRequest{OperationID: "pin-operation-1", ProcessGeneration: "process-1",
		CardID: "89010000000000000001", ReaderName: "reader-a", SIMSessionGeneration: "sim-session-1",
		Action: SIMPINVerify, PIN: "1234"}
	if err := reader.Validate(); err != nil {
		t.Fatal(err)
	}
	modem := SIMPINRequest{OperationID: "pin-operation-2", ProcessGeneration: "process-1",
		CardID: "89010000000000000001", AttachmentID: "attachment-1", EquipmentID: "862547055201716",
		SIMSessionGeneration: "sim-session-1", Action: SIMPINChange, PIN: "1234", NewPIN: "5678"}
	if err := modem.Validate(); err != nil {
		t.Fatal(err)
	}
	setEnabled := SIMPINCommand{OperationID: "pin-operation-3", CardID: reader.CardID,
		ReaderName: reader.ReaderName, Action: SIMPINSetEnabled, PIN: "1234", Enabled: boolPointer(false)}
	if err := setEnabled.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSIMPINContractsRejectAmbiguousTargetsAndUnsafeFields(t *testing.T) {
	base := SIMPINRequest{OperationID: "pin-operation-1", ProcessGeneration: "process-1",
		CardID: "89010000000000000001", ReaderName: "reader-a", SIMSessionGeneration: "sim-session-1",
		Action: SIMPINVerify, PIN: "1234"}
	tests := map[string]func(*SIMPINRequest){
		"missing target":               func(value *SIMPINRequest) { value.ReaderName = "" },
		"two targets":                  func(value *SIMPINRequest) { value.EquipmentID = "862547055201716" },
		"missing generation":           func(value *SIMPINRequest) { value.SIMSessionGeneration = "" },
		"non numeric PIN":              func(value *SIMPINRequest) { value.PIN = "12ab" },
		"verify with new PIN":          func(value *SIMPINRequest) { value.NewPIN = "5678" },
		"change without new PIN":       func(value *SIMPINRequest) { value.Action = SIMPINChange },
		"enable without desired state": func(value *SIMPINRequest) { value.Action = SIMPINSetEnabled },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatalf("unsafe request accepted: %+v", value)
			}
		})
	}
}

func TestSIMPINEnvelopeRoundTripRejectsCredentialCrossingOtherKinds(t *testing.T) {
	request := SIMPINRequest{OperationID: "pin-operation-1", ProcessGeneration: "process-1",
		CardID: "89010000000000000001", ReaderName: "reader-a", SIMSessionGeneration: "sim-session-1",
		Action: SIMPINVerify, PIN: "1234"}
	payload := `{"kind":"sim_pin_request","request_id":"request-1","sim_pin_request":{"operation_id":"pin-operation-1","process_generation":"process-1","card_id":"89010000000000000001","reader_name":"reader-a","sim_session_generation":"sim-session-1","action":"verify","pin":"1234"}}`
	message, err := decodeEnvelope([]byte(payload))
	if err != nil || message.validate() != nil || message.SIMPINRequest == nil {
		t.Fatalf("message=%+v err=%v", message, err)
	}
	if request.Validate() != nil {
		t.Fatal("request contract rejected valid fixture")
	}
	bad := `{"kind":"modem_request","request_id":"request-1","modem_request":{},"sim_pin_request":{"operation_id":"pin-operation-1"}}`
	if message, err := decodeEnvelope([]byte(bad)); err == nil && message.validate() == nil {
		t.Fatal("PIN fields crossed into modem envelope")
	}
}
