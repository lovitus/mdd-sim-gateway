package cellularmedia

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func (service *Service) serveCall(response http.ResponseWriter, request *http.Request) {
	lineID := strings.TrimSpace(request.PathValue("lineID"))
	operation := strings.TrimSpace(request.PathValue("operation"))
	if !validID(lineID) || request.URL.RawQuery != "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_cellular_call_route"})
		return
	}
	if operation == "status" && request.Method == http.MethodGet {
		if _, err := service.config.Auth.VerifyBrowserSession(request.Context(), request); err != nil {
			writeJSON(response, http.StatusUnauthorized, map[string]string{"code": "login_required"})
			return
		}
		service.writeCallStatus(response, lineID)
		return
	}
	if request.Method != http.MethodPost || operation != "start" && operation != "hangup" {
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "cellular_call_operation_not_found"})
		return
	}
	subject, err := service.config.Auth.AuthorizeBrowserMutation(request)
	if err != nil {
		writeJSON(response, http.StatusForbidden, map[string]string{"code": "browser_authorization_failed"})
		return
	}
	if operation == "start" {
		service.startCall(response, request, lineID, subject)
		return
	}
	service.hangupCall(response, request, lineID, subject)
}

func (service *Service) startCall(response http.ResponseWriter, request *http.Request, lineID, subject string) {
	var input struct {
		OperationID    string `json:"operation_id"`
		SessionID      string `json:"session_id"`
		Callee         string `json:"callee"`
		ExpectedCardID string `json:"expected_card_id"`
	}
	if decodeRequest(request.Body, &input) != nil || !validID(input.OperationID) || !validID(input.SessionID) ||
		!validTelephone(input.Callee) || !validCardID(input.ExpectedCardID) {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_cellular_call_start"})
		return
	}
	current := service.lookup(strings.TrimSpace(input.SessionID))
	if current == nil || current.subject != subject || current.lineID != lineID {
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "cellular_media_not_found"})
		return
	}
	expectedCardID := strings.TrimSpace(input.ExpectedCardID)
	line, catalogErr := service.config.Catalog.Get(lineID)
	if catalogErr != nil || line.CardID != expectedCardID || current.target.CardID != expectedCardID {
		writeJSON(response, http.StatusConflict, map[string]string{
			"code": "paid_action_card_mismatch", "detail": errPaidActionCardMismatch.Error(),
		})
		return
	}
	now := service.config.Now().UTC()
	current.mu.Lock()
	if current.phase != "ready" || !current.canaryReady || current.connection == nil || now.Sub(current.lastHeartbeat) >= heartbeatTimeout {
		current.mu.Unlock()
		writeJSON(response, http.StatusPreconditionFailed, map[string]string{"code": "cellular_media_not_ready"})
		return
	}
	current.phase = "starting"
	current.lastFailure = ""
	current.mu.Unlock()
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	result, err := service.config.Agents.ExecuteModem(ctx, current.target.AgentID, current.target.ProcessGeneration,
		agentlink.ModemRequest{
			OperationID: strings.TrimSpace(input.OperationID), AttachmentID: current.target.AttachmentID,
			EquipmentID: current.target.EquipmentID, CardID: current.target.CardID,
			Action: agentlink.ModemCallDial, LeaseID: current.id, Number: strings.TrimSpace(input.Callee),
		})
	cancel()
	current.mu.Lock()
	if err == nil {
		current.phase = "active"
		current.nextRenew = service.config.Now().UTC().Add(renewEvery)
		current.mu.Unlock()
		writeJSON(response, http.StatusOK, map[string]any{
			"code": "cellular_call_started", "session_id": current.id, "call_id": current.callID,
			"state": result.Call.State, "lease_expires_at": result.Lease.ExpiresAt,
		})
		return
	}
	if callStartUncertain(err) {
		current.phase = "uncertain"
		current.nextRenew = service.config.Now().UTC()
		current.lastFailure = "cellular_call_start_uncertain"
		current.mu.Unlock()
		writeJSON(response, http.StatusBadGateway, map[string]string{
			"code": "cellular_call_start_uncertain", "detail": "the Agent may have started the call and retained its paid-call safety lease",
		})
		return
	}
	current.phase = "ready"
	current.lastFailure = err.Error()
	current.mu.Unlock()
	writeServiceError(response, err)
}

func callStartUncertain(err error) bool {
	var remote *agentlink.RemoteError
	return !errors.As(err, &remote) || remote.Kind == "transport" ||
		remote.Code == "modem_operation_timeout" || remote.Code == "modem_call_start_uncertain"
}

func (service *Service) hangupCall(response http.ResponseWriter, request *http.Request, lineID, subject string) {
	var input struct {
		OperationID string `json:"operation_id"`
		SessionID   string `json:"session_id"`
	}
	if decodeRequest(request.Body, &input) != nil || !validID(input.OperationID) || !validID(input.SessionID) {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_cellular_call_hangup"})
		return
	}
	current := service.lookup(strings.TrimSpace(input.SessionID))
	if current == nil || current.subject != subject || current.lineID != lineID {
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "cellular_call_not_found"})
		return
	}
	confirmed, err := service.hangup(current, strings.TrimSpace(input.OperationID), "user_hangup")
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"code": "cellular_call_ended", "session_id": current.id, "terminal_confirmed": confirmed,
	})
}

func (service *Service) renew(current *session) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	_, err := service.config.Agents.ExecuteModem(ctx, current.target.AgentID, current.target.ProcessGeneration,
		agentlink.ModemRequest{
			OperationID: "renew-" + current.id, AttachmentID: current.target.AttachmentID,
			EquipmentID: current.target.EquipmentID, CardID: current.target.CardID,
			Action: agentlink.ModemCallRenew, LeaseID: current.id,
		})
	cancel()
	current.mu.Lock()
	current.renewing = false
	if err == nil && (current.phase == "active" || current.phase == "uncertain") {
		current.nextRenew = service.config.Now().UTC().Add(renewEvery)
		current.mu.Unlock()
		return
	}
	if err != nil {
		current.lastFailure = err.Error()
	}
	if (current.phase == "active" || current.phase == "uncertain") && !current.hangupStarted {
		current.hangupStarted = true
		current.phase = "ending"
		current.mu.Unlock()
		service.terminate(current, "lease_renew_failed")
		return
	}
	current.mu.Unlock()
}

func (service *Service) terminate(current *session, reason string) {
	_, _ = service.hangup(current, "guard-hangup-"+current.id, reason)
}

func (service *Service) hangup(current *session, operationID, reason string) (bool, error) {
	current.mu.Lock()
	if current.terminal {
		current.mu.Unlock()
		return true, nil
	}
	current.phase = "ending"
	current.hangupStarted = true
	current.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	result, err := service.config.Agents.ExecuteModem(ctx, current.target.AgentID, current.target.ProcessGeneration,
		agentlink.ModemRequest{
			OperationID: operationID, AttachmentID: current.target.AttachmentID,
			EquipmentID: current.target.EquipmentID, CardID: current.target.CardID,
			Action: agentlink.ModemCallHangup,
		})
	cancel()
	current.mu.Lock()
	if err == nil && result.Call != nil && result.Call.TerminalConfirmed {
		current.phase = "ended"
		current.terminal = true
		current.lastFailure = ""
		current.mu.Unlock()
		service.remove(current)
		service.stopMedia(current)
		return true, nil
	}
	current.phase = "hangup_unconfirmed"
	if err != nil {
		current.lastFailure = err.Error()
	} else {
		current.lastFailure = "terminal confirmation missing"
	}
	current.mu.Unlock()
	// Stop browser/hardware media and, critically, never renew again. Agent's
	// already-durable local lease performs the bounded physical fallback.
	service.stopMedia(current)
	if err == nil {
		err = errors.New("cellular hangup was not terminally confirmed")
	}
	_ = reason
	return false, err
}

func (service *Service) writeCallStatus(response http.ResponseWriter, lineID string) {
	type view struct {
		SessionID     string    `json:"session_id"`
		CallID        string    `json:"call_id"`
		Phase         string    `json:"phase"`
		LastHeartbeat time.Time `json:"last_heartbeat"`
		Failure       string    `json:"failure,omitempty"`
	}
	views := []view{}
	service.mu.Lock()
	for _, current := range service.sessions {
		if current.lineID != lineID {
			continue
		}
		current.mu.Lock()
		views = append(views, view{
			SessionID: current.id, CallID: current.callID, Phase: current.phase,
			LastHeartbeat: current.lastHeartbeat, Failure: current.lastFailure,
		})
		current.mu.Unlock()
	}
	service.mu.Unlock()
	writeJSON(response, http.StatusOK, map[string]any{"line_id": lineID, "sessions": views})
}

func validTelephone(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 32 {
		return false
	}
	digits := 0
	for index, character := range value {
		if character == '+' && index == 0 {
			continue
		}
		if character < '0' || character > '9' {
			return false
		}
		digits++
	}
	return digits > 0
}
