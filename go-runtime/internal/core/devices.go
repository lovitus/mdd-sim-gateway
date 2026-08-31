package core

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/operations"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
)

const (
	deviceProjectionSchemaVersion = 1
	deviceTopologyTTL             = 30 * time.Second
)

type DeviceSnapshot struct {
	SchemaVersion      int                `json:"schema_version"`
	At                 time.Time          `json:"at"`
	CatalogRevision    uint64             `json:"catalog_revision"`
	RawBindingRevision uint64             `json:"raw_binding_revision"`
	Devices            []DeviceProjection `json:"devices"`
}

// DeviceProjection is a presentation-only physical attachment. It retains
// current operation fences but never creates desired state or lifecycle
// authority. A reader may legitimately contain several independent endpoints.
type DeviceProjection struct {
	ID                string                `json:"id"`
	Kind              string                `json:"kind"`
	Mode              string                `json:"mode"`
	AgentID           string                `json:"agent_id"`
	ProcessGeneration string                `json:"process_generation"`
	Condition         string                `json:"condition"`
	Code              string                `json:"code,omitempty"`
	Reader            *agentlink.ReaderFact `json:"reader,omitempty"`
	Modem             *agentlink.ModemFact  `json:"modem,omitempty"`
	Raw               *RawDeviceProjection  `json:"raw,omitempty"`
	Endpoints         []EndpointProjection  `json:"endpoints"`
}

type RawDeviceProjection struct {
	Binding                   *linecatalog.RawModemBinding  `json:"binding,omitempty"`
	Capture                   *agentlink.RawUSBRecoveryFact `json:"capture,omitempty"`
	SourceSessions            []agentlink.RawUSBSessionFact `json:"source_sessions,omitempty"`
	ImporterSessions          []agentlink.RawUSBSessionFact `json:"importer_sessions,omitempty"`
	ImporterAgentID           string                        `json:"importer_agent_id,omitempty"`
	ImporterProcessGeneration string                        `json:"importer_process_generation,omitempty"`
	ImportedModem             *agentlink.ModemFact          `json:"imported_modem,omitempty"`
	TransportReady            bool                          `json:"transport_ready"`
}

type EndpointProjection struct {
	ID                 string                `json:"id"`
	Kind               string                `json:"kind"`
	SlotID             string                `json:"slot_id,omitempty"`
	SlotLabel          string                `json:"slot_label,omitempty"`
	EID                string                `json:"eid,omitempty"`
	CardIDs            []string              `json:"card_ids"`
	Association        string                `json:"association"`
	Code               string                `json:"code,omitempty"`
	OperationCandidate bool                  `json:"operation_candidate"`
	Line               *DeviceLineProjection `json:"line,omitempty"`

	expectedEquipmentID string
	countIdentity       bool
	rawBindingLineID    string
	rawRouteReady       bool
}

type DeviceLineProjection struct {
	ID         string                           `json:"id"`
	Name       string                           `json:"name,omitempty"`
	Enabled    bool                             `json:"enabled"`
	Operations map[string]OperationAvailability `json:"operations"`
}

type OperationAvailability struct {
	Ready   bool          `json:"ready"`
	Blocked []state.Layer `json:"blocked,omitempty"`
}

func (s *Server) devices(response http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	at := s.now().UTC()
	statuses := []agentlink.ConnectionStatus{}
	if s.agents != nil {
		statuses = s.agents.Statuses()
	}
	catalog := linecatalog.Snapshot{SchemaVersion: linecatalog.SchemaVersion, Lines: []linecatalog.Line{}}
	rawBindings := linecatalog.RawModemSnapshot{
		SchemaVersion: linecatalog.RawModemBindingSchemaVersion, Bindings: []linecatalog.RawModemBinding{},
	}
	if s.catalog != nil {
		var err error
		catalog, err = s.catalog.Snapshot()
		if err != nil {
			writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "catalog_unavailable"})
			return
		}
		rawBindings, err = s.catalog.RawModemBindings()
		if err != nil {
			writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "raw_binding_unavailable"})
			return
		}
	}
	writeJSON(response, http.StatusOK, projectDevices(at, statuses, catalog, s.replay.Projections(at), rawBindings))
}

func projectDevices(at time.Time, statuses []agentlink.ConnectionStatus, catalog linecatalog.Snapshot,
	lines []events.LineProjection, rawBindings linecatalog.RawModemSnapshot) DeviceSnapshot {
	lineByID := make(map[string]linecatalog.Line, len(catalog.Lines))
	linesByCard := make(map[string][]linecatalog.Line)
	for _, line := range catalog.Lines {
		lineByID[line.ID] = line
		linesByCard[line.CardID] = append(linesByCard[line.CardID], line)
	}
	projectionByLine := make(map[string]events.LineProjection, len(lines))
	for _, line := range lines {
		projectionByLine[line.LineID] = line
	}
	bindingsBySourcePair := make(map[string][]linecatalog.RawModemBinding)
	boundImporterPairs := make(map[string]struct{})
	bindingsByImporterPair := make(map[string][]linecatalog.RawModemBinding)
	for _, binding := range rawBindings.Bindings {
		if binding.Enabled {
			key := rawPairKey(binding.SourceAgentID, binding.EquipmentID, binding.CardID)
			bindingsBySourcePair[key] = append(bindingsBySourcePair[key], binding)
			importerKey := rawPairKey(binding.ImporterAgentID, binding.EquipmentID, binding.CardID)
			boundImporterPairs[importerKey] = struct{}{}
			bindingsByImporterPair[importerKey] = append(bindingsByImporterPair[importerKey], binding)
		}
	}
	rawOccurrences := make(map[string]int)
	for _, status := range statuses {
		if status.Topology == nil {
			continue
		}
		for _, capture := range status.Topology.RawUSBRecoveries {
			if capture.State == "capture_reserved" {
				rawOccurrences[pairKey(capture.EquipmentID, capture.CardID)]++
			}
		}
	}

	consumedModems := make(map[string]struct{})
	representedImporterSessions := make(map[string]struct{})
	devices := make([]DeviceProjection, 0)
	for _, status := range statuses {
		if status.Topology == nil {
			continue
		}
		for _, capture := range status.Topology.RawUSBRecoveries {
			if capture.State != "capture_reserved" {
				continue
			}
			captureCopy := capture
			device := DeviceProjection{
				ID: deviceID("raw", status.AgentID, capture.EquipmentID), Kind: "modem", Mode: "raw",
				AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
				Condition: "reserved", Code: "raw_capture_unbound",
				Raw: &RawDeviceProjection{Capture: &captureCopy, SourceSessions: matchingRawSessions(
					*status.Topology, agentlink.RawUSBExporter, capture.EquipmentID, capture.CardID)},
			}
			endpoint := EndpointProjection{
				ID: endpointID(device.ID, "sim", capture.CardID), Kind: "sim", CardIDs: compactIDs([]string{capture.CardID}),
				Association: "unmatched", Code: "raw_capture_unbound", expectedEquipmentID: capture.EquipmentID,
				countIdentity: true,
			}
			bindings := bindingsBySourcePair[rawPairKey(status.AgentID, capture.EquipmentID, capture.CardID)]
			if rawOccurrences[pairKey(capture.EquipmentID, capture.CardID)] != 1 || len(bindings) > 1 {
				device.Condition, device.Code = "ambiguous", "raw_source_ambiguous"
				endpoint.Association, endpoint.Code = "ambiguous", "raw_source_ambiguous"
			} else if len(bindings) == 1 {
				bindingCopy := bindings[0]
				device.Raw.Binding = &bindingCopy
				device.Raw.ImporterAgentID = bindingCopy.ImporterAgentID
				endpoint.rawBindingLineID = bindingCopy.LineID
				ready := rawmodem.BindingRouteReady(bindingCopy, statuses, at, deviceTopologyTTL)
				device.Raw.TransportReady, endpoint.rawRouteReady = ready, ready
				if ready {
					importedStatus, importedModem, found := exactImportedModem(bindingCopy, statuses)
					if found {
						modemCopy := importedModem
						device.Raw.ImportedModem = &modemCopy
						device.Raw.ImporterProcessGeneration = importedStatus.ProcessGeneration
						device.Raw.ImporterSessions = matchingRawSessions(
							*importedStatus.Topology, agentlink.RawUSBImporter, capture.EquipmentID, capture.CardID)
						for _, session := range device.Raw.ImporterSessions {
							representedImporterSessions[rawSessionKey(importedStatus.AgentID, session)] = struct{}{}
						}
						consumedModems[modemAttachmentKey(importedStatus.AgentID, importedModem.AttachmentID)] = struct{}{}
						device.Condition, device.Code = "ready", "raw_transport_ready"
					} else {
						device.Condition, device.Code = "degraded", "raw_imported_modem_missing"
						device.Raw.TransportReady, endpoint.rawRouteReady = false, false
					}
				} else {
					device.Condition, device.Code = "degraded", "raw_transport_not_ready"
				}
			}
			device.Endpoints = []EndpointProjection{endpoint}
			devices = append(devices, device)
		}
	}

	for _, status := range statuses {
		if status.Topology == nil {
			continue
		}
		for _, modem := range status.Topology.Modems {
			attachmentKey := modemAttachmentKey(status.AgentID, modem.AttachmentID)
			if _, consumed := consumedModems[attachmentKey]; consumed {
				continue
			}
			modemCopy := modem
			mode, condition, code, countIdentity := "adapted", modem.Condition, modem.Detail, true
			rawSessions := matchingRawSessions(*status.Topology, agentlink.RawUSBImporter, modem.EquipmentID, modem.SIM.ICCID)
			var raw *RawDeviceProjection
			importerKey := rawPairKey(status.AgentID, modem.EquipmentID, modem.SIM.ICCID)
			bindingCandidates := bindingsByImporterPair[importerKey]
			_, boundImporter := boundImporterPairs[importerKey]
			if len(rawSessions) != 0 || boundImporter {
				mode, condition, code, countIdentity = "raw", "degraded", "raw_import_orphaned", false
				raw = &RawDeviceProjection{
					ImporterAgentID: status.AgentID, ImporterProcessGeneration: status.ProcessGeneration,
					ImporterSessions: rawSessions, ImportedModem: &modemCopy,
				}
			} else if status.Topology.RawUSBImporter && len(status.Topology.Modems) == 1 {
				allImporterSessions := sessionsForRole(*status.Topology, agentlink.RawUSBImporter)
				if len(allImporterSessions) == 1 {
					mode, condition, code, countIdentity = "raw", "degraded", "raw_import_identity_mismatch", false
					rawSessions = allImporterSessions
					bindingCandidates = bindingsByImporterPair[rawPairKey(status.AgentID,
						allImporterSessions[0].EquipmentID, allImporterSessions[0].CardID)]
					raw = &RawDeviceProjection{
						ImporterAgentID: status.AgentID, ImporterProcessGeneration: status.ProcessGeneration,
						ImporterSessions: rawSessions, ImportedModem: &modemCopy,
					}
				}
			}
			if raw != nil && len(bindingCandidates) == 1 {
				bindingCopy := bindingCandidates[0]
				raw.Binding = &bindingCopy
			}
			for _, session := range rawSessions {
				representedImporterSessions[rawSessionKey(status.AgentID, session)] = struct{}{}
			}
			device := DeviceProjection{
				ID: deviceID(mode, status.AgentID, modem.EquipmentID), Kind: "modem", Mode: mode,
				AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
				Condition: condition, Code: code, Modem: &modemCopy, Raw: raw,
			}
			endpoint := EndpointProjection{
				ID: endpointID(device.ID, "sim", modem.SIM.ICCID), Kind: "sim",
				CardIDs: compactIDs([]string{modem.SIM.ICCID}), Association: "unmatched", Code: code,
				expectedEquipmentID: modem.EquipmentID, countIdentity: countIdentity,
			}
			if raw != nil && raw.Binding != nil {
				endpoint.rawBindingLineID = raw.Binding.LineID
			}
			device.Endpoints = []EndpointProjection{endpoint}
			devices = append(devices, device)
		}
		for _, device := range orphanImporterDevices(status, representedImporterSessions, bindingsByImporterPair) {
			devices = append(devices, device)
		}
		for _, reader := range status.Topology.Readers {
			readerCopy := reader
			device := DeviceProjection{
				ID: deviceID("reader", status.AgentID, reader.ReaderName), Kind: "reader", Mode: "remote_card",
				AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
				Condition: string(reader.IdentityState), Code: reader.IdentityDetail, Reader: &readerCopy,
				Endpoints: readerEndpoints(status.AgentID, reader),
			}
			devices = append(devices, device)
		}
	}

	occurrences := make(map[string]int)
	for index := range devices {
		for endpointIndex := range devices[index].Endpoints {
			endpoint := &devices[index].Endpoints[endpointIndex]
			if !endpoint.countIdentity {
				continue
			}
			for _, cardID := range compactIDs(endpoint.CardIDs) {
				occurrences[cardID]++
			}
		}
	}
	for index := range devices {
		for endpointIndex := range devices[index].Endpoints {
			associateEndpoint(&devices[index].Endpoints[endpointIndex], devices[index].Mode,
				occurrences, linesByCard, lineByID, projectionByLine)
		}
		sort.Slice(devices[index].Endpoints, func(left, right int) bool {
			return devices[index].Endpoints[left].ID < devices[index].Endpoints[right].ID
		})
	}
	sort.Slice(devices, func(left, right int) bool {
		if devices[left].Kind != devices[right].Kind {
			return devices[left].Kind < devices[right].Kind
		}
		if devices[left].AgentID != devices[right].AgentID {
			return devices[left].AgentID < devices[right].AgentID
		}
		return devices[left].ID < devices[right].ID
	})
	return DeviceSnapshot{
		SchemaVersion: deviceProjectionSchemaVersion, At: at, CatalogRevision: catalog.Revision,
		RawBindingRevision: rawBindings.Revision, Devices: devices,
	}
}

func orphanImporterDevices(status agentlink.ConnectionStatus, represented map[string]struct{},
	bindings map[string][]linecatalog.RawModemBinding) []DeviceProjection {
	if status.Topology == nil || !status.Topology.RawUSBImporter {
		return nil
	}
	groups := make(map[string][]agentlink.RawUSBSessionFact)
	for _, session := range status.Topology.RawUSBSessions {
		if session.Role != agentlink.RawUSBImporter {
			continue
		}
		if _, exists := represented[rawSessionKey(status.AgentID, session)]; exists {
			continue
		}
		key := pairKey(session.EquipmentID, session.CardID)
		groups[key] = append(groups[key], session)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]DeviceProjection, 0, len(keys))
	for _, key := range keys {
		sessions := groups[key]
		first := sessions[0]
		condition, code := "degraded", "raw_imported_modem_missing"
		if len(sessions) != 1 {
			condition, code = "ambiguous", "raw_import_session_ambiguous"
		}
		raw := &RawDeviceProjection{
			ImporterAgentID: status.AgentID, ImporterProcessGeneration: status.ProcessGeneration,
			ImporterSessions: append([]agentlink.RawUSBSessionFact(nil), sessions...),
		}
		bindingCandidates := bindings[rawPairKey(status.AgentID, first.EquipmentID, first.CardID)]
		if len(bindingCandidates) == 1 {
			bindingCopy := bindingCandidates[0]
			raw.Binding = &bindingCopy
		}
		device := DeviceProjection{
			ID: deviceID("raw-importer", status.AgentID, key), Kind: "modem", Mode: "raw",
			AgentID: status.AgentID, ProcessGeneration: status.ProcessGeneration,
			Condition: condition, Code: code, Raw: raw,
		}
		endpoint := EndpointProjection{
			ID: endpointID(device.ID, "sim", first.CardID), Kind: "sim",
			CardIDs: compactIDs([]string{first.CardID}), Association: "unmatched", Code: code,
			expectedEquipmentID: first.EquipmentID,
		}
		if raw.Binding != nil {
			endpoint.rawBindingLineID = raw.Binding.LineID
		}
		device.Endpoints = []EndpointProjection{endpoint}
		result = append(result, device)
	}
	return result
}

func readerEndpoints(agentID string, reader agentlink.ReaderFact) []EndpointProjection {
	device := deviceID("reader", agentID, reader.ReaderName)
	slots := agentlink.ReaderEUICCs(reader)
	if len(slots) == 0 {
		cards := []string{}
		if reader.CardPresent && reader.IdentityState == agentlink.CardIdentified {
			cards = compactIDs([]string{reader.CardID})
		}
		return []EndpointProjection{{
			ID: endpointID(device, "direct", reader.CardID), Kind: "direct", CardIDs: cards,
			Association: "unmatched", countIdentity: true,
		}}
	}
	result := make([]EndpointProjection, 0, len(slots))
	for index, slot := range slots {
		cards := make([]string, 0)
		for _, profile := range slot.EUICC.Profiles {
			if profile.State == agentlink.EUICCProfileEnabled {
				cards = append(cards, profile.ICCID)
			}
		}
		cards = compactIDs(cards)
		slotKey := slot.SlotID
		if slotKey == "" {
			slotKey = slot.EUICC.EID
		}
		if slotKey == "" {
			slotKey = strconv.Itoa(index)
		}
		result = append(result, EndpointProjection{
			ID: endpointID(device, "euicc", slotKey), Kind: "euicc", SlotID: slot.SlotID,
			SlotLabel: slot.Label, EID: slot.EUICC.EID, CardIDs: cards,
			Association: "unmatched", countIdentity: true,
		})
	}
	return result
}

func associateEndpoint(endpoint *EndpointProjection, mode string, occurrences map[string]int,
	linesByCard map[string][]linecatalog.Line, lineByID map[string]linecatalog.Line,
	projectionByLine map[string]events.LineProjection) {
	endpoint.CardIDs = compactIDs(endpoint.CardIDs)
	if len(endpoint.CardIDs) == 0 {
		endpoint.Association, endpoint.Code = "unmatched", "card_identity_unavailable"
		return
	}
	if len(endpoint.CardIDs) != 1 {
		endpoint.Association, endpoint.Code = "ambiguous", "multiple_enabled_profiles"
		return
	}
	cardID := endpoint.CardIDs[0]
	if endpoint.countIdentity && occurrences[cardID] != 1 {
		endpoint.Association, endpoint.Code = "ambiguous", "card_identity_ambiguous"
		return
	}
	candidates := linesByCard[cardID]
	if len(candidates) == 0 {
		endpoint.Association, endpoint.Code = "unmatched", "line_not_configured"
		return
	}
	if len(candidates) != 1 {
		endpoint.Association, endpoint.Code = "ambiguous", "line_identity_ambiguous"
		return
	}
	line := candidates[0]
	if endpoint.expectedEquipmentID != "" && line.SIM.IMEI != endpoint.expectedEquipmentID {
		endpoint.Association, endpoint.Code = "mismatch", "line_equipment_mismatch"
		return
	}
	if endpoint.rawBindingLineID != "" && endpoint.rawBindingLineID != line.ID {
		endpoint.Association, endpoint.Code = "mismatch", "raw_binding_line_mismatch"
		return
	}
	if stored, exists := lineByID[line.ID]; !exists || stored.CardID != cardID {
		endpoint.Association, endpoint.Code = "mismatch", "line_identity_changed"
		return
	}
	endpoint.Association, endpoint.Code = "exact", "line_exact"
	endpoint.Line = projectDeviceLine(line, projectionByLine)
	if mode != "raw" {
		endpoint.OperationCandidate = true
		return
	}
	if endpoint.rawBindingLineID == "" {
		if endpoint.Code == "line_exact" {
			endpoint.Code = "raw_capture_unbound"
		}
		return
	}
	if !endpoint.rawRouteReady {
		endpoint.Code = "raw_transport_not_ready"
		return
	}
	endpoint.OperationCandidate = true
}

func projectDeviceLine(line linecatalog.Line, projections map[string]events.LineProjection) *DeviceLineProjection {
	availability := unavailableOperations()
	if projection, exists := projections[line.ID]; exists {
		for name, readiness := range projection.Operations {
			availability[name] = OperationAvailability{Ready: readiness.Ready, Blocked: append([]state.Layer(nil), readiness.Blocked...)}
		}
	}
	return &DeviceLineProjection{ID: line.ID, Name: line.Name, Enabled: line.Enabled, Operations: availability}
}

func unavailableOperations() map[string]OperationAvailability {
	result := make(map[string]OperationAvailability)
	for name, readiness := range operations.EvaluateAll(state.LineView{}) {
		result[name] = OperationAvailability{Ready: false, Blocked: append([]state.Layer(nil), readiness.Blocked...)}
	}
	return result
}

func exactImportedModem(binding linecatalog.RawModemBinding,
	statuses []agentlink.ConnectionStatus) (agentlink.ConnectionStatus, agentlink.ModemFact, bool) {
	var selectedStatus agentlink.ConnectionStatus
	var selected agentlink.ModemFact
	matches := 0
	for _, status := range statuses {
		if status.AgentID != binding.ImporterAgentID || status.Topology == nil {
			continue
		}
		for _, modem := range status.Topology.Modems {
			if modem.EquipmentID == binding.EquipmentID && modem.SIM.ICCID == binding.CardID {
				selectedStatus, selected, matches = status, modem, matches+1
			}
		}
	}
	return selectedStatus, selected, matches == 1
}

func matchingRawSessions(topology agentlink.TopologySnapshot, role agentlink.RawUSBRole,
	equipmentID, cardID string) []agentlink.RawUSBSessionFact {
	result := make([]agentlink.RawUSBSessionFact, 0)
	for _, session := range topology.RawUSBSessions {
		if session.Role == role && session.EquipmentID == equipmentID && session.CardID == cardID {
			result = append(result, session)
		}
	}
	return result
}

func sessionsForRole(topology agentlink.TopologySnapshot, role agentlink.RawUSBRole) []agentlink.RawUSBSessionFact {
	result := make([]agentlink.RawUSBSessionFact, 0)
	for _, session := range topology.RawUSBSessions {
		if session.Role == role {
			result = append(result, session)
		}
	}
	return result
}

func compactIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func deviceID(kind, agentID, stable string) string {
	return kind + "-" + shortHash(kind, agentID, stable)
}

func endpointID(device, kind, stable string) string {
	return kind + "-" + shortHash(device, kind, stable)
}

func shortHash(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)[:12])
}

func pairKey(equipmentID, cardID string) string {
	return equipmentID + "\x00" + cardID
}

func rawPairKey(agentID, equipmentID, cardID string) string {
	return agentID + "\x00" + pairKey(equipmentID, cardID)
}

func rawSessionKey(agentID string, session agentlink.RawUSBSessionFact) string {
	return strings.Join([]string{
		agentID, string(session.Role), session.SourceAgentID, session.SourceProcessGeneration,
		session.AttachmentID, session.SessionGeneration, session.EquipmentID, session.CardID,
		session.USBSessionID, session.CaptureGeneration,
	}, "\x00")
}

func modemAttachmentKey(agentID, attachmentID string) string {
	return agentID + "\x00" + attachmentID
}
