package agentmodem

import (
	"errors"
	"testing"
)

func TestOperationTargetRequiresExactAttachmentEquipmentAndSIM(t *testing.T) {
	facts := []Fact{{
		AttachmentID: "mbn-a", EquipmentID: "862547055201716",
		AT:  ATControlFact{State: ATControlReady, CallSignalling: true},
		SIM: SIMFact{ICCID: "8985200000000000001"},
	}}
	operation := Operation{
		AttachmentID: "mbn-a", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", Action: OperationCallStatus,
	}
	if err := ValidateOperationTarget(facts, operation); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []Operation{
		{AttachmentID: "mbn-b", EquipmentID: operation.EquipmentID, CardID: operation.CardID, Action: operation.Action},
		{AttachmentID: operation.AttachmentID, EquipmentID: "862547055201717", CardID: operation.CardID, Action: operation.Action},
		{AttachmentID: operation.AttachmentID, EquipmentID: operation.EquipmentID, CardID: "8985200000000000002", Action: operation.Action},
	} {
		if err := ValidateOperationTarget(facts, changed); !errors.Is(err, ErrOperationTargetReplaced) {
			t.Fatalf("changed=%+v err=%v", changed, err)
		}
	}
}

func TestOperationTargetRequiresCallReadyATOwner(t *testing.T) {
	operation := Operation{
		AttachmentID: "mbn-a", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", Action: OperationCallHangup,
	}
	facts := []Fact{{
		AttachmentID: operation.AttachmentID, EquipmentID: operation.EquipmentID,
		AT: ATControlFact{State: ATControlBusy}, SIM: SIMFact{ICCID: operation.CardID},
	}}
	if err := ValidateOperationTarget(facts, operation); !errors.Is(err, ErrOperationUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestSMSOperationTargetRejectsReinsertedSameCard(t *testing.T) {
	facts := []Fact{{
		AttachmentID: "mbn-a", EquipmentID: "862547055201716",
		AT:  ATControlFact{State: ATControlReady, SMS: true},
		SIM: SIMFact{State: SIMReady, ICCID: "8985200000000000001", SessionGeneration: "session-new"},
	}}
	operation := Operation{AttachmentID: "mbn-a", EquipmentID: "862547055201716",
		CardID: "8985200000000000001", SIMSessionGeneration: "session-old", Action: OperationSMSSend}
	if err := ValidateOperationTarget(facts, operation); !errors.Is(err, ErrOperationTargetReplaced) {
		t.Fatalf("stale SMS session error=%v", err)
	}
	operation.SIMSessionGeneration = "session-new"
	if err := ValidateOperationTarget(facts, operation); err != nil {
		t.Fatalf("current SMS session error=%v", err)
	}
}
