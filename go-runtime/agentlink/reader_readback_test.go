package agentlink

import (
	"testing"
)

func validReaderReadbackRequest() ReaderReadbackRequest {
	return ReaderReadbackRequest{
		OperationID: "reader-readback-1", ProcessGeneration: "process-1",
		ReaderName: "reader-a", CardID: "8944000000000000001",
		SIMSessionGeneration: "session-1",
	}
}

func validReaderReadbackResponse(request ReaderReadbackRequest) ReaderReadbackResponse {
	return ReaderReadbackResponse{
		OperationID: request.OperationID, ProcessGeneration: request.ProcessGeneration,
		ReaderName: request.ReaderName, CardID: request.CardID,
		SIMSessionGeneration: request.SIMSessionGeneration, State: "applied",
		Reader: &ReaderFact{
			ReaderName: request.ReaderName, CardPresent: true,
			SessionGeneration: request.SIMSessionGeneration, CardID: request.CardID,
			IdentityState: CardIdentified,
		},
	}
}

func TestReaderReadbackResponseRequiresExactIdentity(t *testing.T) {
	request := validReaderReadbackRequest()
	response := validReaderReadbackResponse(request)
	response.CardID = "8944000000000000002"
	if err := response.ValidateFor(request); err == nil {
		t.Fatal("identity mismatch was accepted")
	}
}

func TestReaderReadbackResponseRequiresFailureCode(t *testing.T) {
	request := validReaderReadbackRequest()
	response := validReaderReadbackResponse(request)
	response.State = "unknown"
	response.Reader = nil
	if err := response.ValidateFor(request); err == nil {
		t.Fatal("unknown response without error code was accepted")
	}
}

func TestReaderReadbackEnvelopeRejectsMixedProvision(t *testing.T) {
	request := validReaderReadbackRequest()
	message := envelope{
		Kind: kindReaderReadbackRequest, RequestID: "wire-request-1",
		ReaderReadbackRequest: &request,
		ProvisionRequest:      &ProvisionRequest{ProvisionCommand: ProvisionCommand{}},
	}
	if err := message.validate(); err == nil {
		t.Fatal("mixed reader readback and provision envelope was accepted")
	}
}
