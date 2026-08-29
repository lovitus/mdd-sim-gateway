// Package euiccprofiles exposes current multi-reader eUICC inventory and the
// two reversible ES10c profile state operations through Core's existing
// authenticated HTTPS listener. Physical ownership remains in the Agent.
package euiccprofiles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

const maximumRequestBytes = 4096

type AgentRuntime interface {
	Statuses() []agentlink.ConnectionStatus
	ExecuteEUICCProfileCommand(context.Context, agentlink.EUICCProfileCommand) (agentlink.EUICCProfileResponse, error)
}

type Service struct {
	agents AgentRuntime
	now    func() time.Time
}

type InventoryEntry struct {
	AgentID           string              `json:"agent_id"`
	ProcessGeneration string              `json:"process_generation"`
	LastSeen          time.Time           `json:"last_seen"`
	ReaderName        string              `json:"reader_name"`
	SessionGeneration string              `json:"session_generation"`
	CardID            string              `json:"card_id,omitempty"`
	EUICC             agentlink.EUICCFact `json:"euicc"`
}

type mutationRequest struct {
	OperationID   string                      `json:"operation_id"`
	ExpectedState agentlink.EUICCProfileState `json:"expected_state"`
}

func New(agents AgentRuntime) (*Service, error) {
	if agents == nil {
		return nil, errors.New("eUICC profile service requires an Agent runtime")
	}
	return &Service{agents: agents, now: time.Now}, nil
}

func (service *Service) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
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

func (service *Service) inventory(response http.ResponseWriter) {
	entries := make([]InventoryEntry, 0)
	for _, status := range service.agents.Statuses() {
		if status.Topology == nil || status.Topology.ReaderCondition != agentlink.ReaderReady {
			continue
		}
		for _, reader := range status.Topology.Readers {
			if reader.EUICC == nil || reader.IdentityState != agentlink.CardIdentified {
				continue
			}
			entries = append(entries, InventoryEntry{
				AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration, LastSeen: status.LastSeen,
				ReaderName: reader.ReaderName, SessionGeneration: reader.SessionGeneration,
				CardID: reader.CardID, EUICC: *cloneEUICC(reader.EUICC),
			})
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
	command := agentlink.EUICCProfileCommand{
		OperationID: input.OperationID, EID: strings.TrimSpace(request.PathValue("eid")),
		ICCID:         strings.TrimSpace(request.PathValue("iccid")),
		Action:        agentlink.EUICCProfileAction(strings.TrimSpace(request.PathValue("action"))),
		ExpectedState: input.ExpectedState,
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
	status, code := http.StatusBadGateway, "euicc_profile_operation_failed"
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
	copy := *source
	copy.Profiles = append([]agentlink.EUICCProfileFact(nil), source.Profiles...)
	return &copy
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
