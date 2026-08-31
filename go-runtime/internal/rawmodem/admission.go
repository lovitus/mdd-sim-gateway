package rawmodem

import (
	"errors"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type AdmissionStore interface {
	Get(string) (linecatalog.Line, error)
	RawModemBindings() (linecatalog.RawModemSnapshot, error)
}

// Admission makes the durable raw binding the only ordinary modem route while
// it is enabled. It discovers no fallback: both transport endpoint facts and
// one fully fail-closed imported ordinary modem must describe the same exact
// source session before any call, SMS, media or data resolver may select the
// importer Agent.
type Admission struct {
	store AdmissionStore
	now   func() time.Time
	ttl   time.Duration
}

func NewAdmission(store AdmissionStore, now func() time.Time) (*Admission, error) {
	if store == nil {
		return nil, errors.New("raw modem admission store is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Admission{store: store, now: now, ttl: defaultTopologyTTL}, nil
}

func (admission *Admission) RequiredModemAgent(equipmentID, cardID string,
	statuses []agentlink.ConnectionStatus) (string, bool, error) {
	return admission.requiredAgent(equipmentID, cardID, statuses)
}

func (admission *Admission) RequiredCardAgent(cardID string,
	statuses []agentlink.ConnectionStatus) (string, bool, error) {
	return admission.requiredAgent("", cardID, statuses)
}

func (admission *Admission) requiredAgent(equipmentID, cardID string,
	statuses []agentlink.ConnectionStatus) (string, bool, error) {
	snapshot, err := admission.store.RawModemBindings()
	if err != nil {
		return "", true, err
	}
	var selected *linecatalog.RawModemBinding
	for index := range snapshot.Bindings {
		binding := &snapshot.Bindings[index]
		if !binding.Enabled || binding.CardID != cardID || equipmentID != "" && binding.EquipmentID != equipmentID {
			continue
		}
		line, lineErr := admission.store.Get(binding.LineID)
		if errors.Is(lineErr, linecatalog.ErrNotFound) || lineErr == nil &&
			(!line.Enabled || line.CardID != binding.CardID || line.SIM.IMEI != binding.EquipmentID) {
			continue
		}
		if lineErr != nil {
			return "", true, lineErr
		}
		if selected != nil {
			return "", true, agentlink.ErrModemAmbiguous
		}
		selected = binding
	}
	if selected == nil {
		return "", false, nil
	}
	if !bindingRouteReady(*selected, statuses, admission.now().UTC(), admission.ttl) {
		return "", true, agentlink.ErrModemOffline
	}
	return selected.ImporterAgentID, true, nil
}

func bindingRouteReady(binding linecatalog.RawModemBinding, statuses []agentlink.ConnectionStatus,
	now time.Time, ttl time.Duration) bool {
	var source, importer *agentlink.ConnectionStatus
	for index := range statuses {
		status := &statuses[index]
		if !fresh(*status, now, ttl) || status.Topology == nil {
			continue
		}
		switch status.AgentID {
		case binding.SourceAgentID:
			if source != nil || !status.Topology.RawUSBSource {
				return false
			}
			source = status
		case binding.ImporterAgentID:
			if importer != nil || !status.Topology.RawUSBImporter || status.Topology.ModemCondition != agentlink.ModemReady {
				return false
			}
			importer = status
		}
	}
	if source == nil || importer == nil {
		return false
	}

	matchingSessions := 0
	for _, exported := range source.Topology.RawUSBSessions {
		if exported.Role != agentlink.RawUSBExporter || exported.SourceAgentID != binding.SourceAgentID ||
			exported.SourceProcessGeneration != source.ProcessGeneration || exported.EquipmentID != binding.EquipmentID ||
			exported.CardID != binding.CardID || exported.State != "transport_active" {
			continue
		}
		for _, imported := range importer.Topology.RawUSBSessions {
			if imported.Role == agentlink.RawUSBImporter && sameTransportSession(exported, imported) {
				matchingSessions++
			}
		}
	}
	if matchingSessions != 1 {
		return false
	}
	matchingModems := 0
	for _, modem := range importer.Topology.Modems {
		if modem.EquipmentID == binding.EquipmentID && modem.SIM.ICCID == binding.CardID &&
			modem.Condition == "ready" && modem.SIM.State == "ready" && modem.SIM.SessionGeneration != "" &&
			modem.AT.State == "ready" && modem.Network.DataGuard == "protected" && modem.Network.Data == "disconnected" {
			matchingModems++
		}
	}
	return matchingModems == 1
}

func sameTransportSession(left, right agentlink.RawUSBSessionFact) bool {
	return left.SourceAgentID == right.SourceAgentID &&
		left.SourceProcessGeneration == right.SourceProcessGeneration &&
		left.AttachmentID == right.AttachmentID && left.SessionGeneration == right.SessionGeneration &&
		left.EquipmentID == right.EquipmentID && left.CardID == right.CardID &&
		left.USBSessionID == right.USBSessionID && right.State == "transport_active"
}

var _ agentlink.ModemRouteAdmission = (*Admission)(nil)
