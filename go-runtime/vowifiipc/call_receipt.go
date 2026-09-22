package vowifiipc

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const CallReceiptCapability = "call-receipt-v1"

func (client *Client) SupportsCallReceipts(ctx context.Context) (bool, error) {
	return client.supportsCapability(ctx, CallReceiptCapability)
}

type CallReceiptRequest struct {
	CallID      string `json:"call_id"`
	OperationID string `json:"operation_id"`
	SessionID   string `json:"media_session_id"`
}

func (r CallReceiptRequest) Validate() error {
	if !validIdentifier(r.CallID) || !validIdentifier(r.OperationID) || !validIdentifier(r.SessionID) {
		return errors.New("invalid call receipt identity")
	}
	return nil
}

type CallReceipt struct {
	CallReceiptRequest
	LineID            string    `json:"line_id"`
	ProviderID        string    `json:"provider_id"`
	ProcessGeneration string    `json:"process_generation"`
	ConfirmedAt       time.Time `json:"confirmed_at"`
	Source            string    `json:"source"`
}

func (r CallReceipt) Validate() error {
	if r.CallReceiptRequest.Validate() != nil || !validIdentifier(r.LineID) || !validIdentifier(r.ProviderID) || !validIdentifier(r.ProcessGeneration) || r.ConfirmedAt.IsZero() || r.ConfirmedAt.After(time.Now().Add(time.Minute)) || (r.Source != "carrier_bye" && r.Source != "confirmed_end") {
		return errors.New("invalid call terminal receipt")
	}
	return nil
}

type CallReceiptBackend interface {
	CallReceipt(context.Context, CallReceiptRequest) (CallReceipt, error)
}

func (api *API) callReceipt(w http.ResponseWriter, r *http.Request) {
	var input CallReceiptRequest
	if !decodeRequest(w, r, &input) || !validateRequest(w, input.Validate()) {
		return
	}
	backend, ok := api.backend.(CallReceiptBackend)
	if !ok {
		writeResult(w, CallReceipt{}, &OperationError{Kind: ErrorNotReady, Code: "call_receipt_unavailable", Layer: "call"})
		return
	}
	ctx, cancel := api.context(r)
	defer cancel()
	result, err := backend.CallReceipt(ctx, input)
	if err == nil {
		err = result.Validate()
		if err == nil && result.CallReceiptRequest != input {
			err = errors.New("call receipt identity mismatch")
		}
	}
	writeResult(w, result, err)
}
func (client *Client) CallReceipt(ctx context.Context, input CallReceiptRequest) (CallReceipt, error) {
	if err := input.Validate(); err != nil {
		return CallReceipt{}, err
	}
	result, err := request[CallReceiptRequest, CallReceipt](ctx, client, http.MethodPost, "/v1/calls/receipt", &input)
	if err == nil {
		err = result.Validate()
		if err == nil && result.CallReceiptRequest != input {
			err = errors.New("call receipt identity mismatch")
		}
	}
	return result, err
}
