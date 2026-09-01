// Package cellularmessages exposes typed cellular SMS operations through the
// existing authenticated Core HTTPS listener. Hardware access remains owned
// by the exact live Agent attachment; Core only resolves line identity,
// records durable business events, and preserves submission idempotency.
package cellularmessages

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

const maximumRequestBytes = 20 << 10

type Catalog interface {
	Get(string) (linecatalog.Line, error)
}

type AgentRuntime interface {
	ResolveModemTargetForCardAction(string, agentlink.ModemAction) (agentlink.ModemTarget, error)
	ExecuteModem(context.Context, string, string, agentlink.ModemRequest) (agentlink.ModemResponse, error)
}

type Service struct {
	catalog    Catalog
	agents     AgentRuntime
	store      *providermessages.Store
	operations *OperationStore
	now        func() time.Time
	allowance  AllowanceDispatchAuthorizer
}

type AllowanceDispatchAuthorizer interface {
	AuthorizeDispatch(queryID, transport, lineID, expectedCardID, operationID, messageID, recipient, body string) error
}

type SendRequest struct {
	OperationID      string `json:"operation_id"`
	MessageID        string `json:"message_id"`
	Recipient        string `json:"recipient"`
	Body             string `json:"body"`
	ExpectedCardID   string `json:"expected_card_id"`
	AllowanceQueryID string `json:"allowance_query_id,omitempty"`
}

func New(catalog Catalog, agents AgentRuntime, store *providermessages.Store, operations *OperationStore) (*Service, error) {
	if catalog == nil || agents == nil || store == nil || operations == nil {
		return nil, errors.New("invalid cellular message configuration")
	}
	return &Service{catalog: catalog, agents: agents, store: store, operations: operations, now: time.Now}, nil
}

func (service *Service) BindAllowanceDispatchAuthorizer(authorizer AllowanceDispatchAuthorizer) error {
	if service == nil || authorizer == nil {
		return errors.New("allowance dispatch authorizer is required")
	}
	if service.allowance != nil {
		return errors.New("allowance dispatch authorizer is already bound")
	}
	service.allowance = authorizer
	return nil
}

// VerifyMessageRoute resolves the same exact modem/SIM action target used by
// send without performing a modem operation.
func (service *Service) VerifyMessageRoute(lineID, expectedCardID string) error {
	line, err := service.catalog.Get(strings.TrimSpace(lineID))
	if err != nil || !line.Enabled || line.CardID != strings.TrimSpace(expectedCardID) {
		return agentlink.ErrModemOffline
	}
	_, err = service.agents.ResolveModemTargetForCardAction(line.CardID, agentlink.ModemSMSSend)
	return err
}

func (service *Service) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.RawQuery != "" {
		writeFailure(response, http.StatusBadRequest, "invalid_cellular_message_request")
		return
	}
	lineID := strings.TrimSpace(request.PathValue("lineID"))
	line, err := service.catalog.Get(lineID)
	if err != nil {
		writeFailure(response, http.StatusNotFound, "cellular_line_not_found")
		return
	}
	cardID := strings.TrimSpace(line.CardID)
	if cardID == "" {
		writeFailure(response, http.StatusPreconditionFailed, "cellular_sms_target_unconfigured")
		return
	}
	switch request.Method {
	case http.MethodGet:
		service.list(response, request, lineID, cardID)
	case http.MethodPost:
		service.send(response, request, lineID, cardID, line.Enabled)
	default:
		writeFailure(response, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (service *Service) list(response http.ResponseWriter, request *http.Request, lineID, cardID string) {
	target, err := service.agents.ResolveModemTargetForCardAction(cardID, agentlink.ModemSMSList)
	if err != nil {
		writeAgentFailure(response, err)
		return
	}
	operationID, err := randomID("sms-list-")
	if err != nil {
		writeFailure(response, http.StatusInternalServerError, "cellular_sms_identity_failed")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 35*time.Second)
	result, err := service.agents.ExecuteModem(ctx, target.AgentID, target.ProcessGeneration, agentlink.ModemRequest{
		OperationID: operationID, AttachmentID: target.AttachmentID, EquipmentID: target.EquipmentID,
		CardID: cardID, Action: agentlink.ModemSMSList,
	})
	cancel()
	if err != nil {
		writeAgentFailure(response, err)
		return
	}
	for _, message := range result.SMS.Messages {
		if err := service.acceptFact(lineID, target, message); err != nil {
			writeFailure(response, http.StatusInternalServerError, "cellular_sms_persist_failed")
			return
		}
	}
	records, err := service.store.List(lineID, 100)
	if err != nil {
		writeFailure(response, http.StatusInternalServerError, "cellular_sms_read_failed")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"code": "cellular_sms_listed", "messages": records})
}

func (service *Service) send(response http.ResponseWriter, request *http.Request, lineID, cardID string, lineEnabled bool) {
	var input SendRequest
	if decodeRequest(request, &input) != nil || !validID(input.OperationID) || !validID(input.MessageID) ||
		!validTelephone(input.Recipient) || strings.TrimSpace(input.Body) == "" || len(input.Body) > 16<<10 {
		writeFailure(response, http.StatusBadRequest, "invalid_cellular_sms_submit")
		return
	}
	if strings.TrimSpace(input.ExpectedCardID) != cardID {
		writeFailure(response, http.StatusConflict, "cellular_sms_card_mismatch")
		return
	}
	if !lineEnabled {
		writeFailure(response, http.StatusPreconditionFailed, "cellular_sms_line_disabled")
		return
	}
	if input.AllowanceQueryID != "" {
		if service.allowance == nil || service.allowance.AuthorizeDispatch(strings.TrimSpace(input.AllowanceQueryID), "cellular",
			lineID, cardID, input.OperationID, input.MessageID, input.Recipient, input.Body) != nil {
			writeFailure(response, http.StatusConflict, "allowance_query_changed")
			return
		}
	}
	digest := sha256.Sum256([]byte(input.Body))
	requestIdentity := OperationRecord{
		OperationID: input.OperationID, MessageID: input.MessageID, LineID: lineID,
		CardID: cardID, Recipient: input.Recipient,
		BodySHA256: hex.EncodeToString(digest[:]),
	}
	prior, found, err := service.operations.Get(input.OperationID)
	if err != nil {
		writeFailure(response, http.StatusInternalServerError, "cellular_sms_operation_read_failed")
		return
	}
	if found {
		if !sameBrowserOperation(prior, requestIdentity) {
			writeFailure(response, http.StatusConflict, "cellular_sms_operation_conflict")
			return
		}
		service.replayOperation(response, prior)
		return
	}
	target, err := service.agents.ResolveModemTargetForCardAction(cardID, agentlink.ModemSMSSend)
	if err != nil {
		writeAgentFailure(response, err)
		return
	}
	requestIdentity.AgentID = target.AgentID
	requestIdentity.ProcessGeneration = target.ProcessGeneration
	requestIdentity.AttachmentID = target.AttachmentID
	requestIdentity.EquipmentID = target.EquipmentID
	requestIdentity.CreatedAt = service.now().UTC()
	operation, created, err := service.operations.Begin(requestIdentity)
	if err != nil {
		if errors.Is(err, ErrOperationConflict) {
			writeFailure(response, http.StatusConflict, "cellular_sms_operation_conflict")
		} else {
			writeFailure(response, http.StatusInternalServerError, "cellular_sms_operation_persist_failed")
		}
		return
	}
	if !created {
		service.replayOperation(response, operation)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 135*time.Second)
	result, err := service.agents.ExecuteModem(ctx, target.AgentID, target.ProcessGeneration, agentlink.ModemRequest{
		OperationID: input.OperationID, AttachmentID: target.AttachmentID, EquipmentID: target.EquipmentID,
		CardID: cardID, Action: agentlink.ModemSMSSend, Number: input.Recipient, Body: input.Body,
	})
	cancel()
	if err != nil {
		if definitelyUnsent(err) {
			_ = service.operations.Delete(input.OperationID)
			writeAgentFailure(response, err)
		} else {
			_, _ = service.operations.Mark(input.OperationID, "uncertain", nil)
			writeFailure(response, http.StatusConflict, "modem_sms_submit_uncertain")
		}
		return
	}
	operation, err = service.operations.Mark(input.OperationID, "submitted", result.SMS.References)
	if err != nil {
		writeFailure(response, http.StatusInternalServerError, "cellular_sms_operation_persist_failed")
		return
	}
	if err := service.persistSubmission(operation); err != nil {
		writeFailure(response, http.StatusInternalServerError, "cellular_sms_persist_failed")
		return
	}
	service.writeSubmitted(response, operation)
}

func (service *Service) replayOperation(response http.ResponseWriter, operation OperationRecord) {
	if operation.State != "submitted" {
		writeFailure(response, http.StatusConflict, "modem_sms_submit_uncertain")
		return
	}
	if err := service.persistSubmission(operation); err != nil {
		writeFailure(response, http.StatusInternalServerError, "cellular_sms_persist_failed")
		return
	}
	service.writeSubmitted(response, operation)
}

func (service *Service) persistSubmission(operation OperationRecord) error {
	for index, reference := range operation.References {
		event := providermessages.Event{
			SchemaVersion: providermessages.SchemaVersion,
			EventID:       "cellular-submit-" + operation.OperationID + "-" + stringID(index+1),
			LineID:        operation.LineID, ProviderID: "cellular", ProcessGeneration: operation.ProcessGeneration,
			Kind: providermessages.KindSubmitted, ObservedAt: operation.CreatedAt, MessageID: operation.MessageID,
			Part: index + 1, Recipient: operation.Recipient, CallID: messageReferenceID(reference), RPMR: reference,
			State: "submitted",
		}
		if err := service.acceptSubmitted(event); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) writeSubmitted(response http.ResponseWriter, operation OperationRecord) {
	writeJSON(response, http.StatusOK, map[string]any{
		"code": "cellular_sms_submitted", "message_id": operation.MessageID, "references": operation.References,
	})
}

func sameBrowserOperation(stored, request OperationRecord) bool {
	return stored.OperationID == request.OperationID && stored.MessageID == request.MessageID &&
		stored.LineID == request.LineID && stored.CardID == request.CardID &&
		stored.Recipient == request.Recipient && stored.BodySHA256 == request.BodySHA256
}

func definitelyUnsent(err error) bool {
	var remote *agentlink.RemoteError
	if !errors.As(err, &remote) {
		return false
	}
	switch remote.Code {
	case "modem_at_unavailable", "modem_target_replaced", "modem_sms_submit_failed", "modem_paid_call_active", "agent_operation_limit":
		return true
	default:
		return false
	}
}

func (service *Service) acceptSubmitted(event providermessages.Event) error {
	if matched, err := service.submittedExists(event); err != nil || matched {
		return err
	}
	_, _, err := service.store.Accept(event, service.now().UTC())
	if !errors.Is(err, providermessages.ErrConflict) {
		return err
	}
	matched, readErr := service.submittedExists(event)
	if readErr != nil || !matched {
		return errors.Join(err, readErr)
	}
	return nil
}

func (service *Service) submittedExists(event providermessages.Event) (bool, error) {
	record, found, err := service.store.Find(event.LineID, event.ProviderID, event.EventID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if record.Kind != event.Kind || record.MessageID != event.MessageID || record.Part != event.Part ||
		record.Recipient != event.Recipient || record.CallID != event.CallID || record.RPMR != event.RPMR ||
		record.State != event.State {
		return false, providermessages.ErrConflict
	}
	return true, nil
}

func (service *Service) acceptFact(lineID string, target agentlink.ModemTarget, message agentlink.ModemSMSMessage) error {
	eventID, err := providermessages.CellularEventID(target.CardID, message.Fingerprint)
	if err != nil {
		return err
	}
	event := providermessages.Event{
		SchemaVersion: providermessages.SchemaVersion, EventID: eventID,
		LineID: lineID, ProviderID: "cellular", ProcessGeneration: target.ProcessGeneration,
		ObservedAt: message.ObservedAt,
	}
	switch message.State {
	case "received":
		event.Kind, event.MessageID = providermessages.KindReceived, message.Fingerprint
		event.Sender, event.Body = message.Peer, message.Body
	case "delivery":
		event.Kind, event.State = providermessages.KindDelivery, message.Delivery
		event.CallID, event.RPMR = messageReferenceID(message.Reference), message.Reference
	default:
		return nil
	}
	if legacy, found, readErr := service.store.FindEvent(lineID, "cellular-"+message.Fingerprint); readErr != nil {
		return readErr
	} else if found {
		candidate := event
		candidate.EventID = legacy.EventID
		candidate.ProcessGeneration = legacy.ProcessGeneration
		candidate.ProviderID = legacy.ProviderID
		if legacy.Event == candidate {
			return nil
		}
		return providermessages.ErrConflict
	}
	_, _, err = service.store.Accept(event, service.now().UTC())
	return err
}

func decodeRequest(request *http.Request, target any) error {
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return errors.New("invalid content type")
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumRequestBytes {
		return errors.New("invalid request size")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request has trailing JSON")
	}
	return nil
}

func randomID(prefix string) (string, error) {
	wire := make([]byte, 16)
	if _, err := rand.Read(wire); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(wire), nil
}

func validID(value string) bool {
	if len(value) < 1 || len(value) > 200 {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validTelephone(value string) bool {
	if len(value) < 1 || len(value) > 32 {
		return false
	}
	digits := 0
	for index, character := range value {
		if character >= '0' && character <= '9' {
			digits++
		} else if character != '+' || index != 0 {
			return false
		}
	}
	return digits > 0
}

func messageReferenceID(reference int) string { return "cellular-mr-" + stringID(reference) }

func stringID(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	buffer := [20]byte{}
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = digits[value%10]
		value /= 10
	}
	return string(buffer[position:])
}

func writeAgentFailure(response http.ResponseWriter, err error) {
	var remote *agentlink.RemoteError
	switch {
	case errors.As(err, &remote) && remote.Code == "modem_sms_submit_uncertain":
		writeFailure(response, http.StatusConflict, remote.Code)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeFailure(response, http.StatusGatewayTimeout, "cellular_sms_timeout")
	case errors.Is(err, agentlink.ErrModemAmbiguous):
		writeFailure(response, http.StatusConflict, "cellular_sms_target_ambiguous")
	case errors.Is(err, agentlink.ErrModemOffline), errors.Is(err, agentlink.ErrAgentOffline),
		errors.Is(err, agentlink.ErrGenerationMismatch):
		writeFailure(response, http.StatusPreconditionFailed, "cellular_sms_target_unavailable")
	case errors.As(err, &remote):
		writeFailure(response, http.StatusBadGateway, remote.Code)
	default:
		writeFailure(response, http.StatusBadGateway, "cellular_sms_transport_failed")
	}
}

func writeFailure(response http.ResponseWriter, status int, code string) {
	writeJSON(response, status, map[string]string{"code": code})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
