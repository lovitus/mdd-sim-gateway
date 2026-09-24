package cellularmedia

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
)

var errReceiptUpgrade = errors.New("Agent call receipt capability is required")

func (service *Service) requireCallReceiptTarget(target agentlink.ModemTarget) error {
	status, online := service.config.Agents.Status(target.AgentID)
	if !online {
		return agentlink.ErrAgentOffline
	}
	if status.ProcessGeneration != target.ProcessGeneration {
		return agentlink.ErrGenerationMismatch
	}
	if !slices.Contains(status.Capabilities, agentlink.ModemCallReceiptFeature) {
		return errReceiptUpgrade
	}
	return nil
}

func (service *Service) bindRecovery(current *session, operation, key string) error {
	if key == "" {
		return nil
	}
	if err := service.requireCallReceiptTarget(current.target); err != nil {
		return err
	}
	record := callhistory.RecoveryRecord{LineID: current.lineID, Transport: "cellular", CardID: current.target.CardID, CallID: current.callID, OperationID: operation, SessionID: current.id, Subject: current.subject, Target: current.target, CreatedAt: current.createdAt}
	if err := service.config.Recovery.BindRecovery(record, key); err != nil {
		return err
	}
	stored, err := service.config.Recovery.ReadRecovery(record.LineID, record.Transport, record.CallID, operation, key)
	if err != nil {
		return err
	}
	current.mu.Lock()
	current.recovery = &stored
	current.startOperation = operation
	current.mu.Unlock()
	return nil
}

func (service *Service) recoverCall(w http.ResponseWriter, r *http.Request, line string) {
	if _, err := service.config.Auth.AuthorizeBrowserMutation(r); err != nil {
		writeJSON(w, 403, map[string]string{"code": "browser_authorization_failed"})
		return
	}
	var input struct {
		CallID         string `json:"call_id"`
		OperationID    string `json:"operation_id"`
		RecoveryKey    string `json:"recovery_key"`
		Action         string `json:"action"`
		EndOperationID string `json:"end_operation_id,omitempty"`
	}
	if service.config.Recovery == nil || decodeRequest(r.Body, &input) != nil || !validID(input.CallID) || !validID(input.OperationID) || (input.Action != "status" && input.Action != "end") || input.Action == "end" && !validID(input.EndOperationID) {
		writeJSON(w, 400, map[string]string{"code": "invalid_call_recovery"})
		return
	}
	record, err := service.config.Recovery.ReadRecovery(line, "cellular", input.CallID, input.OperationID, input.RecoveryKey)
	if err != nil {
		writeJSON(w, 404, map[string]string{"code": "call_recovery_unavailable"})
		return
	}
	terminal := func() {
		writeJSON(w, 200, map[string]any{"state": "terminal", "call_id": record.CallID, "operation_id": record.OperationID, "session_id": record.SessionID, "terminal_confirmed": true, "terminal_at": record.TerminalAt})
	}
	if !record.TerminalAt.IsZero() {
		terminal()
		return
	}
	status, online := service.config.Agents.Status(record.Target.AgentID)
	if !online {
		writeJSON(w, 200, map[string]any{"state": "unknown", "reason": "agent_offline", "terminal_confirmed": false})
		return
	}
	if !slices.Contains(status.Capabilities, agentlink.ModemCallReceiptFeature) {
		writeJSON(w, 200, map[string]any{"state": "unknown", "reason": "agent_recovery_upgrade_required", "terminal_confirmed": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	proof, proofErr := service.config.Agents.ExecuteModem(ctx, record.Target.AgentID, status.ProcessGeneration, agentlink.ModemRequest{OperationID: record.OperationID, AttachmentID: record.Target.AttachmentID, EquipmentID: record.Target.EquipmentID, CardID: record.CardID, Action: agentlink.ModemCallReceipt, LeaseID: record.SessionID})
	cancel()
	if proofErr == nil && proof.Call != nil && proof.Call.ValidateFor(agentlink.ModemCallReceipt) == nil {
		if err := service.config.Recovery.ConfirmRecovery(record, proof.Call.ObservedAt, "agent_terminal_receipt"); err != nil {
			writeJSON(w, 503, map[string]string{"code": "call_terminal_persist_failed"})
			return
		}
		if current := service.lookup(record.SessionID); current != nil && current.callID == record.CallID && current.subject == record.Subject && current.lineID == record.LineID {
			current.mu.Lock()
			current.terminal = true
			current.phase = "ended"
			current.mu.Unlock()
			service.remove(current)
			service.stopMedia(current)
			if service.config.Calls != nil {
				_ = service.config.Calls.Finish(record.LineID, "cellular", record.CallID, "ended", proof.Call.ObservedAt)
			}
		}
		record.TerminalAt = proof.Call.ObservedAt
		terminal()
		return
	}
	current := service.lookup(record.SessionID)
	if current == nil {
		writeJSON(w, 200, map[string]any{"state": "unknown", "reason": "original_session_unavailable", "terminal_confirmed": false})
		return
	}
	if current.subject != record.Subject || current.callID != record.CallID || current.lineID != record.LineID || current.target.AgentID != record.Target.AgentID || current.target.AttachmentID != record.Target.AttachmentID || current.target.EquipmentID != record.Target.EquipmentID || current.target.CardID != record.CardID || current.target.ProcessGeneration != record.Target.ProcessGeneration {
		writeJSON(w, 409, map[string]string{"code": "call_recovery_identity_conflict"})
		return
	}
	if input.Action == "end" {
		confirmed, err := service.hangup(current, input.EndOperationID, "user_recovery_hangup")
		if err != nil || !confirmed {
			writeJSON(w, 200, map[string]any{"state": "ending_unconfirmed", "terminal_confirmed": false})
			return
		}
		// hangup persisted the exact physical receipt before declaring the record terminal.
		updated, err := service.config.Recovery.ReadRecovery(line, "cellular", input.CallID, input.OperationID, input.RecoveryKey)
		if err != nil || updated.TerminalAt.IsZero() {
			writeJSON(w, 503, map[string]string{"code": "call_terminal_persist_failed"})
			return
		}
		record = updated
		terminal()
		return
	}
	current.mu.Lock()
	phase := current.phase
	current.mu.Unlock()
	writeJSON(w, 200, map[string]any{"state": phase, "call_id": record.CallID, "session_id": record.SessionID, "can_end": true, "terminal_confirmed": false})
}
