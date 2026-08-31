package rawmodem

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

const maximumBindingRequestBytes = 16 << 10

type BindingStore interface {
	Get(string) (linecatalog.Line, error)
	RawModemBindings() (linecatalog.RawModemSnapshot, error)
	PutRawModemBindingExpected(linecatalog.RawModemBinding, uint64) (linecatalog.RawModemBinding, uint64, bool, error)
}

type BindingAgents interface {
	Statuses() []agentlink.ConnectionStatus
}

type Handler struct {
	store  BindingStore
	agents BindingAgents
	wake   func()
	now    func() time.Time
	ttl    time.Duration
}

type bindingRequest struct {
	ExpectedRevision    uint64 `json:"expected_revision"`
	ExpectedEquipmentID string `json:"expected_equipment_id"`
	ExpectedCardID      string `json:"expected_card_id"`
	Enabled             bool   `json:"enabled"`
	SourceAgentID       string `json:"source_agent_id,omitempty"`
	ImporterAgentID     string `json:"importer_agent_id,omitempty"`
}

type bindingCandidate struct {
	SourceAgentID string `json:"source_agent_id"`
	EquipmentID   string `json:"equipment_id"`
	CardID        string `json:"card_id"`
	Manufacturer  string `json:"manufacturer,omitempty"`
	Model         string `json:"model,omitempty"`
	AttachmentID  string `json:"attachment_id"`
}

type bindingView struct {
	SchemaVersion int                          `json:"schema_version"`
	Revision      uint64                       `json:"revision"`
	EquipmentID   string                       `json:"equipment_id"`
	CardID        string                       `json:"card_id"`
	Binding       *linecatalog.RawModemBinding `json:"binding,omitempty"`
	Candidates    []bindingCandidate           `json:"candidates"`
	Importers     []string                     `json:"importers"`
}

func NewHandler(store BindingStore, agents BindingAgents, wake func(), now func() time.Time) (*Handler, error) {
	if store == nil || agents == nil {
		return nil, errors.New("invalid raw modem binding handler dependencies")
	}
	if wake == nil {
		wake = func() {}
	}
	if now == nil {
		now = time.Now
	}
	return &Handler{store: store, agents: agents, wake: wake, now: now, ttl: defaultTopologyTTL}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	lineID := strings.TrimSpace(request.PathValue("lineID"))
	if lineID == "" || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	switch request.Method {
	case http.MethodGet:
		handler.read(response, lineID)
	case http.MethodPut:
		handler.put(response, request, lineID)
	default:
		response.Header().Set("Allow", "GET, PUT")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (handler *Handler) read(response http.ResponseWriter, lineID string) {
	line, err := handler.store.Get(lineID)
	if err != nil {
		writeBindingError(response, err)
		return
	}
	snapshot, err := handler.store.RawModemBindings()
	if err != nil {
		writeBindingError(response, err)
		return
	}
	view := handler.view(line, snapshot)
	writeBindingJSON(response, http.StatusOK, view)
}

func (handler *Handler) put(response http.ResponseWriter, request *http.Request, lineID string) {
	request.Body = http.MaxBytesReader(response, request.Body, maximumBindingRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input bindingRequest
	if err := decoder.Decode(&input); err != nil {
		http.Error(response, "invalid raw modem binding request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(response, "invalid raw modem binding request", http.StatusBadRequest)
		return
	}
	line, err := handler.store.Get(lineID)
	if err != nil {
		writeBindingError(response, err)
		return
	}
	snapshot, err := handler.store.RawModemBindings()
	if err != nil {
		writeBindingError(response, err)
		return
	}
	if snapshot.Revision != input.ExpectedRevision {
		writeBindingError(response, linecatalog.ErrRawModemRevision)
		return
	}
	var binding linecatalog.RawModemBinding
	if input.Enabled {
		if input.ExpectedEquipmentID != line.SIM.IMEI || input.ExpectedCardID != line.CardID {
			http.Error(response, "raw modem line identity changed", http.StatusConflict)
			return
		}
		if current, exists := bindingForLine(snapshot, lineID); exists && current.Enabled &&
			current.SourceAgentID == strings.TrimSpace(input.SourceAgentID) &&
			current.ImporterAgentID == strings.TrimSpace(input.ImporterAgentID) &&
			current.EquipmentID == line.SIM.IMEI && current.CardID == line.CardID {
			writeBindingJSON(response, http.StatusOK, handler.view(line, snapshot))
			return
		}
		binding, err = handler.resolveLiveBinding(line, input.SourceAgentID, input.ImporterAgentID)
		if err != nil {
			http.Error(response, err.Error(), http.StatusConflict)
			return
		}
	} else {
		current, exists := bindingForLine(snapshot, lineID)
		if !exists {
			writeBindingJSON(response, http.StatusOK, handler.view(line, snapshot))
			return
		}
		if input.ExpectedEquipmentID != current.EquipmentID || input.ExpectedCardID != current.CardID {
			http.Error(response, "raw modem binding identity changed", http.StatusConflict)
			return
		}
		binding = current
		binding.Enabled = false
	}
	_, _, changed, err := handler.store.PutRawModemBindingExpected(binding, input.ExpectedRevision)
	if err != nil {
		writeBindingError(response, err)
		return
	}
	updated, err := handler.store.RawModemBindings()
	if err != nil {
		writeBindingError(response, err)
		return
	}
	if changed {
		handler.wake()
	}
	writeBindingJSON(response, http.StatusOK, handler.view(line, updated))
}

func (handler *Handler) resolveLiveBinding(line linecatalog.Line, sourceID, importerID string) (linecatalog.RawModemBinding, error) {
	sourceID, importerID = strings.TrimSpace(sourceID), strings.TrimSpace(importerID)
	if sourceID == "" || importerID == "" || sourceID == importerID || !line.Enabled ||
		line.SIM.IMEI == "" || line.CardID == "" {
		return linecatalog.RawModemBinding{}, errors.New("raw modem source, importer, or line identity is invalid")
	}
	now := handler.now().UTC()
	statuses := handler.agents.Statuses()
	importerReady := false
	for _, status := range statuses {
		if status.AgentID == importerID && fresh(status, now, handler.ttl) && status.Topology != nil && status.Topology.RawUSBImporter {
			if importerReady {
				return linecatalog.RawModemBinding{}, errors.New("raw modem importer is ambiguous")
			}
			importerReady = true
		}
	}
	if !importerReady {
		return linecatalog.RawModemBinding{}, errors.New("raw modem importer is not currently ready")
	}
	matches := handler.candidates(line, statuses)
	selected := 0
	for _, candidate := range matches {
		if candidate.SourceAgentID == sourceID {
			selected++
		}
	}
	if selected != 1 {
		return linecatalog.RawModemBinding{}, errors.New("selected Agent does not expose one exact ready modem/card candidate")
	}
	return linecatalog.RawModemBinding{
		LineID: line.ID, SourceAgentID: sourceID, EquipmentID: line.SIM.IMEI,
		CardID: line.CardID, ImporterAgentID: importerID, Enabled: true,
	}, nil
}

func (handler *Handler) view(line linecatalog.Line, snapshot linecatalog.RawModemSnapshot) bindingView {
	statuses := handler.agents.Statuses()
	view := bindingView{
		SchemaVersion: linecatalog.RawModemBindingSchemaVersion, Revision: snapshot.Revision,
		EquipmentID: line.SIM.IMEI, CardID: line.CardID,
		Candidates: handler.candidates(line, statuses), Importers: []string{},
	}
	if binding, exists := bindingForLine(snapshot, line.ID); exists {
		copy := binding
		view.Binding = &copy
	}
	now := handler.now().UTC()
	for _, status := range statuses {
		if fresh(status, now, handler.ttl) && status.Topology != nil && status.Topology.RawUSBImporter {
			view.Importers = append(view.Importers, status.AgentID)
		}
	}
	sort.Strings(view.Importers)
	view.Importers = compactStrings(view.Importers)
	return view
}

func (handler *Handler) candidates(line linecatalog.Line, statuses []agentlink.ConnectionStatus) []bindingCandidate {
	now := handler.now().UTC()
	result := []bindingCandidate{}
	for _, status := range statuses {
		if !fresh(status, now, handler.ttl) || status.Topology == nil || !status.Topology.RawUSBSource ||
			sourceSessionConflict(*status.Topology,
				linecatalog.RawModemBinding{EquipmentID: line.SIM.IMEI, CardID: line.CardID}) {
			continue
		}
		for _, capture := range status.Topology.RawUSBRecoveries {
			if capture.EquipmentID == line.SIM.IMEI && capture.CardID == line.CardID &&
				capture.CaptureGeneration != "" && capture.State == "capture_reserved" {
				result = append(result, bindingCandidate{
					SourceAgentID: status.AgentID, EquipmentID: capture.EquipmentID, CardID: capture.CardID,
					AttachmentID: capture.AttachmentID,
				})
			}
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].SourceAgentID == result[right].SourceAgentID {
			return result[left].AttachmentID < result[right].AttachmentID
		}
		return result[left].SourceAgentID < result[right].SourceAgentID
	})
	return result
}

func bindingForLine(snapshot linecatalog.RawModemSnapshot, lineID string) (linecatalog.RawModemBinding, bool) {
	for _, binding := range snapshot.Bindings {
		if binding.LineID == lineID {
			return binding, true
		}
	}
	return linecatalog.RawModemBinding{}, false
}

func compactStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func writeBindingError(response http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, linecatalog.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, linecatalog.ErrRawModemRevision):
		status = http.StatusConflict
	case errors.Is(err, linecatalog.ErrRawModemBindingInUse):
		status = http.StatusConflict
	}
	http.Error(response, err.Error(), status)
}

func writeBindingJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
