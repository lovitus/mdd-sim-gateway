package providercontrol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

// The public Core middleware authorizes the current administrator. The additional
// high-entropy capability proves association with the originally authorized lease.
func (handler *Handler) recoverCall(w http.ResponseWriter, r *http.Request, line string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	write := func(status int, value any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(value) }
	var input struct {
		CallID         string `json:"call_id"`
		OperationID    string `json:"operation_id"`
		RecoveryKey    string `json:"recovery_key"`
		Action         string `json:"action"`
		EndOperationID string `json:"end_operation_id,omitempty"`
	}
	store, ok := handler.calls.(*callhistory.Store)
	if !ok || r.Method != http.MethodPost || r.URL.RawQuery != "" || decodeRequest(r, &input) != nil || (input.Action != "status" && input.Action != "end") {
		write(400, map[string]string{"code": "invalid_call_recovery"})
		return
	}
	record, err := store.ReadRecovery(line, "vowifi", input.CallID, input.OperationID, input.RecoveryKey)
	if err != nil {
		write(404, map[string]string{"code": "call_recovery_unavailable"})
		return
	}
	terminal := func() {
		write(200, map[string]any{"state": "terminal", "call_id": record.CallID, "operation_id": record.OperationID, "session_id": record.SessionID, "terminal_confirmed": true, "terminal_at": record.TerminalAt})
	}
	if !record.TerminalAt.IsZero() {
		terminal()
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err = handler.providers.UseCurrent(ctx, line, func(provider mediaauth.Provider) error {
		if provider.ProviderID != record.ProviderID || provider.CardID != record.CardID {
			return mediaauth.ErrProviderFenceConflict
		}
		client, err := handler.client(provider)
		if err != nil {
			return err
		}
		proof, proofErr := client.CallReceipt(ctx, vowifiipc.CallReceiptRequest{CallID: record.CallID, OperationID: record.OperationID, SessionID: record.SessionID})
		if proofErr == nil {
			if proof.LineID != record.LineID || proof.ProviderID != record.ProviderID || proof.ProcessGeneration != record.ProviderGeneration {
				return mediaauth.ErrProviderFenceConflict
			}
			if err := store.ConfirmRecovery(record, proof.ConfirmedAt, "provider_terminal_receipt"); err != nil {
				return err
			}
			record.TerminalAt = proof.ConfirmedAt
			return nil
		}
		// A replacement process can serve retained receipts, but cannot acquire control
		// of an unproved old call merely because it has the same catalog line.
		if provider.Generation != record.ProviderGeneration {
			return mediaauth.ErrProviderFenceConflict
		}
		// Across HTTP the typed failure is carried by ResponseError, not returned
		// as a direct backend OperationError. Keep transport and business checks.
		var active *vowifiipc.ResponseError
		if !errors.As(proofErr, &active) || active.Status != http.StatusPreconditionFailed || active.Failure.Kind != vowifiipc.ErrorNotReady || active.Failure.Code != "call_active" || active.Failure.Layer != "call" {
			return callhistory.ErrRecoveryIdentity
		}
		if input.Action == "end" {
			result, err := client.EndCall(ctx, vowifiipc.EndCallRequest{OperationID: input.EndOperationID, CallID: record.CallID, ReasonCode: "user_recovery_hangup", ExpectedStartOperationID: record.OperationID})
			if err != nil {
				return err
			}
			if err = validateIdentity(result, record.LineID, provider.ProviderID, provider.Generation); err != nil {
				return err
			}
			if !result.Accepted || result.Code != "ended" {
				return callhistory.ErrRecoveryIdentity
			}
			proof, err = client.CallReceipt(ctx, vowifiipc.CallReceiptRequest{CallID: record.CallID, OperationID: record.OperationID, SessionID: record.SessionID})
			if err != nil {
				return err
			}
			if proof.LineID != record.LineID || proof.ProviderID != record.ProviderID || proof.ProcessGeneration != record.ProviderGeneration {
				return mediaauth.ErrProviderFenceConflict
			}
			if err := store.ConfirmRecovery(record, proof.ConfirmedAt, "provider_terminal_receipt"); err != nil {
				return err
			}
			record.TerminalAt = proof.ConfirmedAt
		}
		return nil
	})
	if !record.TerminalAt.IsZero() {
		terminal()
		return
	}
	if err != nil {
		write(200, map[string]any{"state": "unknown", "reason": "original_call_evidence_unavailable", "terminal_confirmed": false})
		return
	}
	write(200, map[string]any{"state": "unresolved", "call_id": record.CallID, "session_id": record.SessionID, "can_end": true, "terminal_confirmed": false})
}
