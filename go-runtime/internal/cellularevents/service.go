// Package cellularevents accepts authenticated Agent-originated cellular SMS
// and call observations. It owns no hardware polling and performs no paid
// action; every non-terminal event is re-fenced against the live Agent
// topology before business persistence.
package cellularevents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

type Catalog interface {
	Snapshot() (linecatalog.Snapshot, error)
}

type Agents interface {
	Status(string) (agentlink.ConnectionStatus, bool)
	ResolveModemTargetForAction(string, string, agentlink.ModemAction) (agentlink.ModemTarget, error)
}

type Service struct {
	catalog  Catalog
	agents   Agents
	messages *providermessages.Store
	calls    *callhistory.Store
	now      func() time.Time
}

func New(catalog Catalog, agents Agents, messages *providermessages.Store, calls *callhistory.Store) (*Service, error) {
	if catalog == nil || agents == nil || messages == nil || calls == nil {
		return nil, errors.New("invalid cellular event service configuration")
	}
	return &Service{catalog: catalog, agents: agents, messages: messages, calls: calls, now: time.Now}, nil
}

func (service *Service) AcceptModemEvent(_ context.Context, source agentlink.AgentEventContext,
	event agentlink.ModemEvent,
) agentlink.ModemEventDisposition {
	if event.Validate() != nil || strings.TrimSpace(source.AgentID) == "" || strings.TrimSpace(source.ProcessGeneration) == "" {
		return rejected("invalid_modem_event")
	}
	terminalCall := event.Kind == agentlink.ModemEventKindCall && event.Call != nil &&
		(event.Call.State == "idle" || event.Call.State == "unavailable")
	lineID := ""
	if event.Kind == agentlink.ModemEventKindCall && event.Call != nil {
		prior, found, err := service.calls.CellularCall(event.Call.IncomingEventID)
		if err != nil {
			return retry("cellular_call_source_read_failed")
		}
		if found {
			lineID = prior.LineID
		}
	}
	if lineID == "" {
		var err error
		lineID, err = service.lineForCard(event.CardID)
		if err != nil {
			if errors.Is(err, errLineUnavailable) {
				return retry("cellular_event_line_unavailable")
			}
			return rejected("cellular_event_line_ambiguous")
		}
	}
	if !terminalCall {
		action := agentlink.ModemCallStatus
		if event.Kind == agentlink.ModemEventKindSMS {
			action = agentlink.ModemSMSList
		}
		status, found := service.agents.Status(source.AgentID)
		if !found || status.ProcessGeneration != source.ProcessGeneration {
			return rejected("stale_modem_event_process")
		}
		if !topologyFence(status.Topology, event) {
			return retry("modem_event_topology_not_ready")
		}
		target, resolveErr := service.agents.ResolveModemTargetForAction(event.EquipmentID, event.CardID, action)
		if resolveErr != nil {
			return retry("modem_event_route_not_ready")
		}
		if target.AgentID != source.AgentID || target.ProcessGeneration != source.ProcessGeneration ||
			target.AttachmentID != event.AttachmentID || target.EquipmentID != event.EquipmentID {
			return rejected("stale_modem_event_source")
		}
	}
	if event.Kind == agentlink.ModemEventKindSMS {
		return service.acceptSMS(lineID, source, event)
	}
	return service.acceptCall(lineID, source, event, terminalCall)
}

func (service *Service) acceptSMS(lineID string, source agentlink.AgentEventContext,
	event agentlink.ModemEvent,
) agentlink.ModemEventDisposition {
	sms := event.SMS
	eventID, identityErr := providermessages.CellularEventID(event.CardID, sms.Fingerprint)
	if identityErr != nil {
		return rejected("invalid_cellular_message_identity")
	}
	business := providermessages.Event{
		SchemaVersion: providermessages.SchemaVersion, EventID: eventID,
		LineID: lineID, ProviderID: "cellular", ProcessGeneration: source.ProcessGeneration,
		ObservedAt: event.ObservedAt,
	}
	if sms.State == "received" {
		business.Kind, business.MessageID = providermessages.KindReceived, sms.Fingerprint
		business.Sender, business.Body = sms.Peer, sms.Body
	} else {
		business.Kind, business.State = providermessages.KindDelivery, sms.Delivery
		business.CallID, business.RPMR = messageReferenceID(sms.Reference), sms.Reference
	}
	if legacy, found, err := service.messages.FindEvent(lineID, "cellular-"+sms.Fingerprint); err != nil {
		return retry("cellular_message_read_failed")
	} else if found {
		candidate := business
		candidate.EventID = legacy.EventID
		if !sameMessage(legacy.Event, candidate) {
			return rejected("cellular_message_event_conflict")
		}
		return accepted()
	}
	if existing, found, err := service.messages.FindEvent("", business.EventID); err != nil {
		if errors.Is(err, providermessages.ErrConflict) {
			return rejected("cellular_message_event_conflict")
		}
		return retry("cellular_message_read_failed")
	} else if found {
		if !sameMessage(existing.Event, business) {
			return rejected("cellular_message_event_conflict")
		}
		if existing.ProviderID != "cellular" || existing.LineID != lineID {
			return accepted()
		}
	}
	_, _, err := service.messages.AcceptWithNotificationTransport(business, event.CardID, "cellular", service.now().UTC())
	if err == nil {
		return accepted()
	}
	if errors.Is(err, providermessages.ErrConflict) {
		return rejected("cellular_message_event_conflict")
	}
	return retry("cellular_message_persist_failed")
}

func (service *Service) acceptCall(lineID string, source agentlink.AgentEventContext,
	event agentlink.ModemEvent, terminal bool,
) agentlink.ModemEventDisposition {
	call := event.Call
	if terminal {
		prior, found, err := service.calls.CellularCall(call.IncomingEventID)
		if err != nil {
			return retry("cellular_call_source_read_failed")
		}
		if found && (prior.AgentID != source.AgentID || prior.EquipmentID != event.EquipmentID || prior.CardID != event.CardID ||
			prior.Occurrence != call.Occurrence || prior.NativeCallIndex != call.NativeIndex) {
			return rejected("cellular_call_source_conflict")
		}
	}
	stored, err := service.calls.AcceptCellularEvent(callhistory.CellularCallSource{
		IncomingEventID: call.IncomingEventID, LastEventID: event.EventID, Revision: call.Revision,
		LineID: lineID, AgentID: source.AgentID, ProcessGeneration: source.ProcessGeneration,
		AttachmentID: event.AttachmentID, EquipmentID: event.EquipmentID, CardID: event.CardID,
		SIMSessionGeneration: event.SIMSessionGeneration, Occurrence: call.Occurrence,
		NativeCallIndex: call.NativeIndex, State: call.State, Direction: call.Direction, Number: call.Number,
		FirstObservedAt: call.FirstObservedAt, ObservedAt: event.ObservedAt,
		ReceivedAt: service.now().UTC(), Notify: call.Notify,
	})
	_ = stored
	if err == nil {
		return accepted()
	}
	if strings.Contains(err.Error(), "conflict") {
		return rejected("cellular_call_event_conflict")
	}
	return retry("cellular_call_persist_failed")
}

var errLineUnavailable = errors.New("cellular event line unavailable")

func (service *Service) lineForCard(cardID string) (string, error) {
	snapshot, err := service.catalog.Snapshot()
	if err != nil {
		return "", errLineUnavailable
	}
	matches := []string{}
	for _, line := range snapshot.Lines {
		if strings.TrimSpace(line.CardID) == strings.TrimSpace(cardID) {
			matches = append(matches, line.ID)
		}
	}
	if len(matches) == 0 {
		return "", errLineUnavailable
	}
	if len(matches) != 1 {
		return "", errors.New("cellular event line identity is ambiguous")
	}
	return matches[0], nil
}

func topologyFence(topology *agentlink.TopologySnapshot, event agentlink.ModemEvent) bool {
	if topology == nil || topology.ModemCondition != agentlink.ModemReady {
		return false
	}
	matches := 0
	for _, modem := range topology.Modems {
		if modem.AttachmentID == event.AttachmentID && modem.EquipmentID == event.EquipmentID &&
			modem.SIM.ICCID == event.CardID && modem.SIM.SessionGeneration == event.SIMSessionGeneration &&
			modem.Condition == "ready" && modem.SIM.State == "ready" && modem.AT.State == "ready" {
			matches++
		}
	}
	return matches == 1
}

func sameMessage(existing, expected providermessages.Event) bool {
	existing.ProcessGeneration, expected.ProcessGeneration = "", ""
	existing.ProviderID, expected.ProviderID = "", ""
	existing.LineID, expected.LineID = "", ""
	return existing == expected
}

func messageReferenceID(reference int) string { return fmt.Sprintf("cellular-mr-%d", reference) }

func accepted() agentlink.ModemEventDisposition {
	return agentlink.ModemEventDisposition{Accepted: true}
}

func retry(code string) agentlink.ModemEventDisposition {
	return agentlink.ModemEventDisposition{Retryable: true, Code: code}
}

func rejected(code string) agentlink.ModemEventDisposition {
	return agentlink.ModemEventDisposition{Code: code}
}
