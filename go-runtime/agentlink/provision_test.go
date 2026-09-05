package agentlink

import "testing"

func validProvisionCommand() ProvisionCommand {
	return ProvisionCommand{
		OperationID: "provision-1", EquipmentID: "862547055201716",
		CardID: "89010000000000000001", AttachmentID: "attach-1",
		SIMSessionGeneration: "sim-1", IMSI: "460001234567890",
		MCC: "460", MNC: "01", IMEI: "356789012345678",
		SMSC: "+8613800138000",
	}
}

func TestProvisionCommandValidate(t *testing.T) {
	if err := validProvisionCommand().Validate(); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}
	tests := []struct {
		name string
		mut  func(*ProvisionCommand)
	}{
		{"missing smsc", func(command *ProvisionCommand) { command.SMSC = "" }},
		{"bad imei", func(command *ProvisionCommand) { command.IMEI = "123" }},
		{"bad imsi", func(command *ProvisionCommand) { command.IMSI = "46001x" }},
		{"missing generation", func(command *ProvisionCommand) { command.SIMSessionGeneration = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := validProvisionCommand()
			test.mut(&command)
			if err := command.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestProvisionResponseValidate(t *testing.T) {
	response := ProvisionResponse{
		OperationID: "provision-1", State: ProvisionApplied,
		EquipmentID: "862547055201716", CardID: "89010000000000000001",
		SIMSessionGeneration: "sim-1",
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	response.State = ProvisionFailed
	if err := response.Validate(); err == nil {
		t.Fatal("failed response without error code accepted")
	}
	response.ErrorCode = "pin_required"
	if err := response.Validate(); err != nil {
		t.Fatalf("failed response rejected: %v", err)
	}
}
