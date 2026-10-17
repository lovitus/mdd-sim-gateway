package agentlink

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// The Android instrumentation suite compares its real TLS/WebSocket response
// with this shared contract, including the Service's fresh ReaderHub result.
func TestAndroidSIMReadbackWireContract(t *testing.T) {
	data, err := os.ReadFile("../../android-agent/contract/sim-identity-readback.json")
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Kind      string                 `json:"kind"`
		RequestID string                 `json:"request_id"`
		Response  ReaderReadbackResponse `json:"reader_readback_response"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frame); err != nil {
		t.Fatal(err)
	}
	if frame.Kind != "reader_readback_response" || frame.RequestID != "readback-wire-1" {
		t.Fatalf("wrong envelope: %+v", frame)
	}
	request := ReaderReadbackRequest{Scope: ReaderReadbackSIMIdentity, OperationID: "readback-operation-1", ProcessGeneration: "android-test-process", ReaderName: "Synthetic USIM reader", CardID: "89012345678901234567", SIMSessionGeneration: "test-insertion-1"}
	if err := frame.Response.ValidateFor(request); err != nil {
		t.Fatal(err)
	}
}
