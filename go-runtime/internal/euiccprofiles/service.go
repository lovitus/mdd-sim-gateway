// Package euiccprofiles exposes current multi-reader eUICC inventory and the
// reversible ES10c profile operations through Core's existing
// authenticated HTTPS listener. Physical ownership remains in the Agent.
package euiccprofiles

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const maximumRequestBytes = 4096

type AgentRuntime interface {
	Statuses() []agentlink.ConnectionStatus
	ExecuteEUICCProfileCommand(context.Context, agentlink.EUICCProfileCommand) (agentlink.EUICCProfileResponse, error)
	ExecuteEUICCDownloadCommand(context.Context, agentlink.EUICCDownloadCommand) (agentlink.EUICCDownloadResponse, error)
	ExecuteEUICCDiscoveryCommand(context.Context, agentlink.EUICCDiscoveryCommand) (agentlink.EUICCDiscoveryResponse, error)
	ExecuteEUICCNotificationCommand(context.Context, agentlink.EUICCNotificationCommand) (agentlink.EUICCNotificationResponse, error)
}

type Service struct {
	agents    AgentRuntime
	catalog   LineCatalog
	providers ProviderStatus
	now       func() time.Time
}

type LineCatalog interface {
	Snapshot() (linecatalog.Snapshot, error)
}

type ProviderStatus interface {
	Status(context.Context, string) (vowifiipc.Snapshot, error)
}

type Option func(*Service) error

func WithDownloadSafety(catalog LineCatalog, providers ProviderStatus) Option {
	return func(service *Service) error {
		if catalog == nil || providers == nil {
			return errors.New("eUICC download safety requires catalog and provider status")
		}
		service.catalog, service.providers = catalog, providers
		return nil
	}
}

type InventoryEntry struct {
	AgentID           string              `json:"agent_id"`
	ProcessGeneration string              `json:"process_generation"`
	LastSeen          time.Time           `json:"last_seen"`
	ReaderName        string              `json:"reader_name"`
	SessionGeneration string              `json:"session_generation"`
	CardID            string              `json:"card_id,omitempty"`
	SlotID            string              `json:"slot_id,omitempty"`
	SlotLabel         string              `json:"slot_label,omitempty"`
	EUICC             agentlink.EUICCFact `json:"euicc"`
}

type mutationRequest struct {
	OperationID      string                      `json:"operation_id"`
	ExpectedState    agentlink.EUICCProfileState `json:"expected_state"`
	Nickname         *string                     `json:"nickname,omitempty"`
	ExpectedNickname *string                     `json:"expected_nickname,omitempty"`
}

type downloadStartRequest struct {
	OperationID      string `json:"operation_id"`
	ActivationCode   string `json:"activation_code"`
	ConfirmationCode string `json:"confirmation_code,omitempty"`
	IMEI             string `json:"imei"`
}

type discoveryRequest struct {
	OperationID string `json:"operation_id"`
	SMDS        string `json:"smds,omitempty"`
	IMEI        string `json:"imei,omitempty"`
}

type notificationDeliveryRequest struct {
	Confirmed bool   `json:"confirmed"`
	Event     string `json:"event"`
	ICCID     string `json:"iccid,omitempty"`
	Address   string `json:"address"`
}

type notificationRemovalRequest struct {
	Confirmed            bool   `json:"confirmed"`
	ReceiverAcknowledged bool   `json:"receiver_acknowledged"`
	Event                string `json:"event"`
	ICCID                string `json:"iccid,omitempty"`
	Address              string `json:"address"`
}

func New(agents AgentRuntime, options ...Option) (*Service, error) {
	if agents == nil {
		return nil, errors.New("eUICC profile service requires an Agent runtime")
	}
	service := &Service{agents: agents, now: time.Now}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("eUICC profile service option is nil")
		}
		if err := option(service); err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (service *Service) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if strings.HasSuffix(request.URL.Path, "/deliver") && strings.Contains(request.URL.Path, "/notifications/") {
		service.deliverNotification(response, request)
		return
	}
	if strings.HasSuffix(request.URL.Path, "/remove") && strings.Contains(request.URL.Path, "/notifications/") {
		service.removeAcknowledgedNotification(response, request)
		return
	}
	if strings.HasSuffix(request.URL.Path, "/notifications") {
		service.notifications(response, request)
		return
	}
	if strings.HasSuffix(request.URL.Path, "/discovery") {
		service.discovery(response, request)
		return
	}
	if strings.Contains(request.URL.Path, "/downloads") {
		service.download(response, request)
		return
	}
	if request.Method == http.MethodGet {
		service.inventory(response)
		return
	}
	if request.Method != http.MethodPost {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	service.mutate(response, request)
}

func (service *Service) removeAcknowledgedNotification(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(response, http.StatusUnsupportedMediaType, map[string]string{"code": "json_required"})
		return
	}
	var input notificationRemovalRequest
	if decodeStrict(request, &input) != nil || !input.Confirmed || !input.ReceiverAcknowledged {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "notification_removal_confirmation_required"})
		return
	}
	sequence, err := strconv.ParseInt(request.PathValue("sequence"), 10, 64)
	if err != nil || sequence < 0 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_notification_removal_request"})
		return
	}
	operationID, err := notificationOperationID()
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "operation_identity_unavailable"})
		return
	}
	command := agentlink.EUICCNotificationCommand{
		OperationID: operationID, EID: strings.TrimSpace(request.PathValue("eid")),
		Action: agentlink.EUICCNotificationRemove,
		Expected: &agentlink.EUICCNotificationEntry{
			SequenceNumber: sequence, Event: strings.TrimSpace(input.Event),
			ICCID: strings.TrimSpace(input.ICCID), Address: strings.TrimSpace(input.Address),
		},
	}
	if command.Validate() != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_notification_removal_request"})
		return
	}
	result, err := service.agents.ExecuteEUICCNotificationCommand(request.Context(), command)
	if err != nil {
		writeEUICCError(response, err, "euicc_notification_removal_failed")
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (service *Service) deliverNotification(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(response, http.StatusUnsupportedMediaType, map[string]string{"code": "json_required"})
		return
	}
	var input notificationDeliveryRequest
	if decodeStrict(request, &input) != nil || !input.Confirmed {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "notification_delivery_confirmation_required"})
		return
	}
	sequence, err := strconv.ParseInt(request.PathValue("sequence"), 10, 64)
	if err != nil || sequence < 0 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_notification_delivery_request"})
		return
	}
	operationID, err := notificationOperationID()
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "operation_identity_unavailable"})
		return
	}
	command := agentlink.EUICCNotificationCommand{
		OperationID: operationID, EID: strings.TrimSpace(request.PathValue("eid")),
		Action: agentlink.EUICCNotificationDeliver,
		Expected: &agentlink.EUICCNotificationEntry{
			SequenceNumber: sequence, Event: strings.TrimSpace(input.Event),
			ICCID: strings.TrimSpace(input.ICCID), Address: strings.TrimSpace(input.Address),
		},
	}
	if command.Validate() != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_notification_delivery_request"})
		return
	}
	result, err := service.agents.ExecuteEUICCNotificationCommand(request.Context(), command)
	if err != nil {
		if result.Acknowledged && !result.Removed {
			writeJSON(response, http.StatusBadGateway, map[string]any{
				"code":         "euicc_notification_acknowledged_not_removed",
				"operation_id": operationID, "acknowledged": true, "removed": false,
			})
			return
		}
		writeEUICCError(response, err, "euicc_notification_delivery_failed")
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (service *Service) notifications(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	operationID, err := notificationOperationID()
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"code": "operation_identity_unavailable"})
		return
	}
	command := agentlink.EUICCNotificationCommand{
		OperationID: operationID, EID: strings.TrimSpace(request.PathValue("eid")),
	}
	if err := command.Validate(); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_notification_request"})
		return
	}
	result, err := service.agents.ExecuteEUICCNotificationCommand(request.Context(), command)
	if err != nil {
		writeEUICCError(response, err, "euicc_notification_operation_failed")
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func notificationOperationID() (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "notification-" + hex.EncodeToString(random[:]), nil
}

func (service *Service) discovery(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(response, http.StatusUnsupportedMediaType, map[string]string{"code": "json_required"})
		return
	}
	var input discoveryRequest
	if err := decodeStrict(request, &input); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_discovery_request"})
		return
	}
	command := agentlink.EUICCDiscoveryCommand{
		OperationID: strings.TrimSpace(input.OperationID), EID: strings.TrimSpace(request.PathValue("eid")),
		SMDS: strings.TrimSpace(input.SMDS), IMEI: strings.TrimSpace(input.IMEI),
	}
	if err := command.Validate(); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_discovery_request"})
		return
	}
	result, err := service.agents.ExecuteEUICCDiscoveryCommand(request.Context(), command)
	if err != nil {
		writeEUICCError(response, err, "euicc_discovery_operation_failed")
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (service *Service) download(response http.ResponseWriter, request *http.Request) {
	eid := strings.TrimSpace(request.PathValue("eid"))
	operationID := strings.TrimSpace(request.PathValue("operation_id"))
	var command agentlink.EUICCDownloadCommand
	switch {
	case request.Method == http.MethodPost && operationID == "":
		mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeJSON(response, http.StatusUnsupportedMediaType, map[string]string{"code": "json_required"})
			return
		}
		var input downloadStartRequest
		if err := decodeStrict(request, &input); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_download_request"})
			return
		}
		command = agentlink.EUICCDownloadCommand{
			OperationID: strings.TrimSpace(input.OperationID), EID: eid, Action: agentlink.EUICCDownloadStart,
			ActivationCode:   strings.TrimSpace(input.ActivationCode),
			ConfirmationCode: strings.TrimSpace(input.ConfirmationCode), IMEI: strings.TrimSpace(input.IMEI),
		}
	case request.Method == http.MethodGet && operationID != "":
		command = agentlink.EUICCDownloadCommand{
			OperationID: operationID, EID: eid, Action: agentlink.EUICCDownloadStatus,
		}
	case request.Method == http.MethodPost && operationID != "" && strings.HasSuffix(request.URL.Path, "/cancel"):
		mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeJSON(response, http.StatusUnsupportedMediaType, map[string]string{"code": "json_required"})
			return
		}
		var empty struct{}
		if err := decodeStrict(request, &empty); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_download_request"})
			return
		}
		command = agentlink.EUICCDownloadCommand{
			OperationID: operationID, EID: eid, Action: agentlink.EUICCDownloadCancel,
		}
	default:
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	if err := command.Validate(); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_download_request"})
		return
	}
	if command.Action == agentlink.EUICCDownloadStart {
		if err := service.downloadSafe(request.Context(), command.EID); err != nil {
			writeDownloadError(response, err)
			return
		}
	}
	result, err := service.agents.ExecuteEUICCDownloadCommand(request.Context(), command)
	if err != nil {
		writeDownloadError(response, err)
		return
	}
	status := http.StatusOK
	if result.Job != nil && (result.Job.State == agentlink.EUICCDownloadQueued ||
		result.Job.State == agentlink.EUICCDownloadRunning || result.Job.State == agentlink.EUICCDownloadCancelling) {
		status = http.StatusAccepted
	}
	writeJSON(response, status, result)
}

func (service *Service) downloadSafe(ctx context.Context, eid string) error {
	if service.catalog == nil || service.providers == nil {
		return nil
	}
	cardIDs := make(map[string]struct{})
	for _, status := range service.agents.Statuses() {
		if status.Topology == nil || status.Topology.ReaderCondition != agentlink.ReaderReady {
			continue
		}
		for _, reader := range status.Topology.Readers {
			for _, slot := range agentlink.ReaderEUICCs(reader) {
				if slot.EUICC.EID != eid {
					continue
				}
				for _, profile := range slot.EUICC.Profiles {
					cardIDs[profile.ICCID] = struct{}{}
				}
			}
		}
	}
	snapshot, err := service.catalog.Snapshot()
	if err != nil {
		return &agentlink.RemoteError{Kind: "not_ready", Code: "euicc_download_line_state_unavailable", Retryable: true}
	}
	for _, line := range snapshot.Lines {
		if !line.Enabled {
			continue
		}
		if _, matches := cardIDs[line.CardID]; !matches {
			continue
		}
		status, err := service.providers.Status(ctx, line.ID)
		if err != nil {
			return &agentlink.RemoteError{Kind: "not_ready", Code: "euicc_download_line_state_unavailable", Retryable: true}
		}
		if status.ActiveCall != nil {
			return &agentlink.RemoteError{Kind: "conflict", Code: "euicc_download_call_active"}
		}
		if status.Runtime.Condition != vowifiipc.RuntimeStopped {
			return &agentlink.RemoteError{Kind: "conflict", Code: "euicc_download_line_active"}
		}
	}
	return nil
}

func (service *Service) inventory(response http.ResponseWriter) {
	entries := make([]InventoryEntry, 0)
	for _, status := range service.agents.Statuses() {
		if status.Topology == nil || status.Topology.ReaderCondition != agentlink.ReaderReady {
			continue
		}
		for _, reader := range status.Topology.Readers {
			if reader.IdentityState != agentlink.CardIdentified {
				continue
			}
			for _, slot := range agentlink.ReaderEUICCs(reader) {
				entries = append(entries, InventoryEntry{
					AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration, LastSeen: status.LastSeen,
					ReaderName: reader.ReaderName, SessionGeneration: reader.SessionGeneration,
					CardID: reader.CardID, SlotID: slot.SlotID, SlotLabel: slot.Label,
					EUICC: *cloneEUICC(&slot.EUICC),
				})
			}
		}
	}
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].EUICC.EID != entries[right].EUICC.EID {
			return entries[left].EUICC.EID < entries[right].EUICC.EID
		}
		if entries[left].AgentID != entries[right].AgentID {
			return entries[left].AgentID < entries[right].AgentID
		}
		return entries[left].ReaderName < entries[right].ReaderName
	})
	writeJSON(response, http.StatusOK, map[string]any{"at": service.now().UTC(), "euiccs": entries})
}

func (service *Service) mutate(response http.ResponseWriter, request *http.Request) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(response, http.StatusUnsupportedMediaType, map[string]string{"code": "json_required"})
		return
	}
	var input mutationRequest
	if err := decodeStrict(request, &input); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_profile_request"})
		return
	}
	action := agentlink.EUICCProfileAction(strings.TrimSpace(request.PathValue("action")))
	if (action == agentlink.EUICCProfileNickname && (input.Nickname == nil || input.ExpectedNickname == nil)) ||
		(action != agentlink.EUICCProfileNickname && (input.Nickname != nil || input.ExpectedNickname != nil)) {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_profile_request"})
		return
	}
	command := agentlink.EUICCProfileCommand{
		OperationID: input.OperationID, EID: strings.TrimSpace(request.PathValue("eid")),
		ICCID:         strings.TrimSpace(request.PathValue("iccid")),
		Action:        action,
		ExpectedState: input.ExpectedState,
	}
	if input.Nickname != nil {
		command.Nickname, command.ExpectedNickname = *input.Nickname, *input.ExpectedNickname
	}
	if err := command.Validate(); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_euicc_profile_request"})
		return
	}
	result, err := service.agents.ExecuteEUICCProfileCommand(request.Context(), command)
	if err != nil {
		writeOperationError(response, err)
		return
	}
	status := http.StatusOK
	if result.Outcome == agentlink.EUICCProfileRefreshPending || result.Outcome == agentlink.EUICCProfileUncertain {
		status = http.StatusAccepted
	}
	writeJSON(response, status, result)
}

func decodeStrict(request *http.Request, target any) error {
	body, err := io.ReadAll(io.LimitReader(request.Body, maximumRequestBytes+1))
	if err != nil || len(body) > maximumRequestBytes {
		return errors.New("request body is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request has trailing JSON")
	}
	return nil
}

func writeOperationError(response http.ResponseWriter, err error) {
	writeEUICCError(response, err, "euicc_profile_operation_failed")
}

func writeDownloadError(response http.ResponseWriter, err error) {
	writeEUICCError(response, err, "euicc_download_operation_failed")
}

func writeEUICCError(response http.ResponseWriter, err error, fallback string) {
	status, code := http.StatusBadGateway, fallback
	switch {
	case errors.Is(err, agentlink.ErrCardOffline), errors.Is(err, agentlink.ErrAgentOffline):
		status, code = http.StatusServiceUnavailable, "euicc_offline"
	case errors.Is(err, agentlink.ErrCardAmbiguous):
		status, code = http.StatusConflict, "euicc_identity_ambiguous"
	case errors.Is(err, agentlink.ErrGenerationMismatch):
		status, code = http.StatusConflict, "euicc_generation_changed"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code = http.StatusGatewayTimeout, "euicc_operation_timeout"
	default:
		var remote *agentlink.RemoteError
		if errors.As(err, &remote) {
			code = remote.Code
			switch remote.Kind {
			case "conflict":
				status = http.StatusConflict
			case "rejected":
				status = http.StatusUnprocessableEntity
			case "not_ready":
				status = http.StatusServiceUnavailable
			case "transport":
				status = http.StatusBadGateway
			default:
				status = http.StatusInternalServerError
			}
		}
	}
	writeJSON(response, status, map[string]string{"code": code})
}

func cloneEUICC(source *agentlink.EUICCFact) *agentlink.EUICCFact {
	if source == nil {
		return nil
	}
	result := *source
	result.Profiles = make([]agentlink.EUICCProfileFact, len(source.Profiles))
	copy(result.Profiles, source.Profiles)
	return &result
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
