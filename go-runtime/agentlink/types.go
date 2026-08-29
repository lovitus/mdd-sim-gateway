// Package agentlink defines the narrow, authenticated WSS control channel
// between one MDD Agent and Core. It exposes fixed high-level AKA and modem
// operations instead of a general APDU or AT-command tunnel.
package agentlink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const SchemaVersion = 1

type AKAApplication string

const (
	AKAApplicationUSIM AKAApplication = "usim"
	AKAApplicationISIM AKAApplication = "isim"
)

type Hello struct {
	SchemaVersion     int    `json:"schema_version"`
	AgentID           string `json:"agent_id"`
	ProcessGeneration string `json:"process_generation"`
}

type CardIdentityState string

type ReaderCondition string

type ModemCondition string

type EUICCProfileState string

const (
	CardAbsent              CardIdentityState = "absent"
	CardIdentityDiscovering CardIdentityState = "discovering"
	CardIdentified          CardIdentityState = "identified"
	CardIdentityUnavailable CardIdentityState = "identity_unavailable"
)

const (
	EUICCProfileEnabled  EUICCProfileState = "enabled"
	EUICCProfileDisabled EUICCProfileState = "disabled"
)

// EUICCProfileFact is the durable identity and current state of one profile.
// Profile operations remain outside the Agent topology protocol.
type EUICCProfileFact struct {
	ICCID string            `json:"iccid"`
	State EUICCProfileState `json:"state"`
}

// EUICCFact identifies the inserted eUICC independently from its reader and
// active profile. ProfilesAvailable distinguishes a blank eUICC from a failed
// profile query.
type EUICCFact struct {
	EID               string             `json:"eid"`
	ProfilesAvailable bool               `json:"profiles_available"`
	Profiles          []EUICCProfileFact `json:"profiles"`
}

const (
	ReaderStarting   ReaderCondition = "starting"
	ReaderReady      ReaderCondition = "ready"
	ReaderRecovering ReaderCondition = "recovering"
)

const (
	ModemDisabled   ModemCondition = "disabled"
	ModemStarting   ModemCondition = "starting"
	ModemReady      ModemCondition = "ready"
	ModemRecovering ModemCondition = "recovering"
)

type ModemCapabilities struct {
	CellularData  bool   `json:"cellular_data"`
	SMSReceive    bool   `json:"sms_receive"`
	SMSSend       bool   `json:"sms_send"`
	MBNVoiceClass string `json:"mbn_voice_class,omitempty"`
}

type ModemSIMFact struct {
	State      string   `json:"state"`
	ICCID      string   `json:"iccid,omitempty"`
	IMSI       string   `json:"imsi,omitempty"`
	MSISDNs    []string `json:"msisdns,omitempty"`
	Configured bool     `json:"sms_configured"`
	SMSC       string   `json:"smsc,omitempty"`
	SMSError   string   `json:"sms_error,omitempty"`
}

type ModemNetworkFact struct {
	Registration  string  `json:"registration"`
	OperatorID    string  `json:"operator_id,omitempty"`
	OperatorName  string  `json:"operator_name,omitempty"`
	SignalPercent *uint32 `json:"signal_percent,omitempty"`
	SoftwareRadio string  `json:"software_radio"`
	HardwareRadio string  `json:"hardware_radio"`
	Data          string  `json:"data"`
	Profile       string  `json:"profile,omitempty"`
}

type ModemATControlFact struct {
	State          string `json:"state"`
	Port           string `json:"port,omitempty"`
	Detail         string `json:"detail,omitempty"`
	CallSignalling bool   `json:"call_signalling"`
	SMS            bool   `json:"sms"`
	SIMAPDU        bool   `json:"sim_apdu"`
}

// ModemFact reports one local modem attachment. AttachmentID identifies the
// current Windows MBN attachment; SIM.ICCID identifies the inserted card.
type ModemFact struct {
	AttachmentID string             `json:"attachment_id"`
	EquipmentID  string             `json:"equipment_id,omitempty"`
	Manufacturer string             `json:"manufacturer,omitempty"`
	Model        string             `json:"model,omitempty"`
	Firmware     string             `json:"firmware,omitempty"`
	Condition    string             `json:"condition"`
	Detail       string             `json:"detail,omitempty"`
	Capabilities ModemCapabilities  `json:"capabilities"`
	AT           ModemATControlFact `json:"at_control"`
	SIM          ModemSIMFact       `json:"sim"`
	Network      ModemNetworkFact   `json:"network"`
}

// ReaderFact describes one current PC/SC attachment. ReaderName is only a
// local attachment label. SessionGeneration fences one insertion, while
// CardID is the durable ICCID when the card exposes one.
type ReaderFact struct {
	ReaderName        string            `json:"reader_name"`
	CardPresent       bool              `json:"card_present"`
	SessionGeneration string            `json:"session_generation,omitempty"`
	CardID            string            `json:"card_id,omitempty"`
	EUICC             *EUICCFact        `json:"euicc,omitempty"`
	IdentityState     CardIdentityState `json:"identity_state"`
	IdentityDetail    string            `json:"identity_detail,omitempty"`
	ATRSHA256         string            `json:"atr_sha256,omitempty"`
}

type TopologySnapshot struct {
	ReaderCondition ReaderCondition `json:"reader_condition"`
	ReaderDetail    string          `json:"reader_detail,omitempty"`
	Readers         []ReaderFact    `json:"readers"`
	// Empty ModemCondition is the legacy schema-1 representation and is valid
	// only with no modem facts. This keeps already deployed PC/SC Agents wire
	// compatible while new Agents explicitly report disabled/starting/ready.
	ModemCondition ModemCondition `json:"modem_condition,omitempty"`
	ModemDetail    string         `json:"modem_detail,omitempty"`
	Modems         []ModemFact    `json:"modems,omitempty"`
}

// HealthReport is sent every ten seconds in production. Topology is present
// on the first report and whenever TopologyRevision changes; an unchanged
// report renews only the application heartbeat.
type HealthReport struct {
	SchemaVersion    int               `json:"schema_version"`
	Sequence         uint64            `json:"sequence"`
	TopologyRevision string            `json:"topology_revision"`
	Topology         *TopologySnapshot `json:"topology,omitempty"`
}

// AKARequest targets one exact live card attachment. SessionGeneration is
// replaced on removal/reinsertion even when reader name and ATR are unchanged.
// CardID is the active ICCID selected by Core and must match the session's
// discovered identity. An eUICC EID alone is never sufficient for AKA.
type AKARequest struct {
	OperationID       string         `json:"operation_id"`
	SessionGeneration string         `json:"session_generation"`
	CardID            string         `json:"card_id"`
	Application       AKAApplication `json:"application"`
	RAND              []byte         `json:"rand"`
	AUTN              []byte         `json:"autn"`
}

// AKAChallenge is the stable provider-to-Core request. The provider selects
// only the UICC ICCID; Core resolves the current Agent process and insertion
// generation from live topology immediately before forwarding the operation.
// This keeps hotplug and moving a card between Agent hosts out of persistent
// provider configuration without weakening the exact Agent-side fence.
type AKAChallenge struct {
	OperationID string         `json:"operation_id"`
	CardID      string         `json:"card_id"`
	Application AKAApplication `json:"application"`
	RAND        []byte         `json:"rand"`
	AUTN        []byte         `json:"autn"`
}

// AKAResponse contains only the response body and status from the one
// AUTHENTICATE operation. Parsing RES/CK/IK/AUTS remains in the isolated
// VoWiFi provider; Core must not persist or log Body.
type AKAResponse struct {
	OperationID       string       `json:"operation_id"`
	SessionGeneration string       `json:"session_generation"`
	Body              []byte       `json:"body,omitempty"`
	SW1               byte         `json:"sw1,omitempty"`
	SW2               byte         `json:"sw2,omitempty"`
	Failure           *RemoteError `json:"failure,omitempty"`
}

type RemoteError struct {
	Kind       string `json:"kind"`
	Code       string `json:"code"`
	Retryable  bool   `json:"retryable"`
	RetryAfter int64  `json:"retry_after_ms,omitempty"`
}

type ModemAction string

const (
	ModemCallStatus ModemAction = "call_status"
	ModemCallHangup ModemAction = "call_hangup"
	ModemCallDial   ModemAction = "call_dial"
	ModemCallAnswer ModemAction = "call_answer"
	ModemCallRenew  ModemAction = "call_renew"
)

type ModemMediaAction string

const (
	ModemMediaPrepare ModemMediaAction = "media_prepare"
	ModemMediaStop    ModemMediaAction = "media_stop"
)

// ModemCommand is the stable Core-side target. Core resolves it to one exact
// Agent process and current MBN attachment immediately before forwarding it.
type ModemCommand struct {
	OperationID string      `json:"operation_id"`
	EquipmentID string      `json:"equipment_id"`
	CardID      string      `json:"card_id"`
	Action      ModemAction `json:"action"`
	LeaseID     string      `json:"lease_id,omitempty"`
	Number      string      `json:"number,omitempty"`
}

// ModemRequest adds the attachment fence selected from the Agent's current
// topology. The Agent rechecks equipment and SIM identity before touching AT.
type ModemRequest struct {
	OperationID  string      `json:"operation_id"`
	AttachmentID string      `json:"attachment_id"`
	EquipmentID  string      `json:"equipment_id"`
	CardID       string      `json:"card_id"`
	Action       ModemAction `json:"action"`
	LeaseID      string      `json:"lease_id,omitempty"`
	Number       string      `json:"number,omitempty"`
}

// ModemMediaCommand identifies one browser media session without exposing a
// generic serial or AT tunnel. MediaToken is an opaque, single-session bearer
// consumed by Core's Agent-media WebSocket endpoint.
type ModemMediaCommand struct {
	OperationID string           `json:"operation_id"`
	EquipmentID string           `json:"equipment_id"`
	CardID      string           `json:"card_id"`
	Action      ModemMediaAction `json:"action"`
	SessionID   string           `json:"session_id"`
	MediaToken  string           `json:"media_token,omitempty"`
}

// ModemMediaRequest adds the live attachment fence resolved by Core.
type ModemMediaRequest struct {
	OperationID  string           `json:"operation_id"`
	AttachmentID string           `json:"attachment_id"`
	EquipmentID  string           `json:"equipment_id"`
	CardID       string           `json:"card_id"`
	Action       ModemMediaAction `json:"action"`
	SessionID    string           `json:"session_id"`
	MediaToken   string           `json:"media_token,omitempty"`
}

type ModemMediaResponse struct {
	OperationID  string       `json:"operation_id"`
	AttachmentID string       `json:"attachment_id"`
	EquipmentID  string       `json:"equipment_id"`
	CardID       string       `json:"card_id"`
	SessionID    string       `json:"session_id"`
	State        string       `json:"state,omitempty"`
	Failure      *RemoteError `json:"failure,omitempty"`
}

type ModemCallResult struct {
	State             string    `json:"state"`
	Direction         string    `json:"direction,omitempty"`
	Number            string    `json:"number,omitempty"`
	ObservedAt        time.Time `json:"observed_at"`
	Authoritative     bool      `json:"authoritative"`
	TerminalConfirmed bool      `json:"terminal_confirmed"`
	Strategy          string    `json:"strategy,omitempty"`
}

type ModemLeaseResult struct {
	LeaseID   string    `json:"lease_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type ModemResponse struct {
	OperationID  string            `json:"operation_id"`
	AttachmentID string            `json:"attachment_id"`
	EquipmentID  string            `json:"equipment_id"`
	CardID       string            `json:"card_id"`
	Call         *ModemCallResult  `json:"call,omitempty"`
	Lease        *ModemLeaseResult `json:"lease,omitempty"`
	Failure      *RemoteError      `json:"failure,omitempty"`
}

func (failure *RemoteError) Error() string {
	if failure == nil {
		return "Agent operation failed"
	}
	return fmt.Sprintf("Agent operation failed (%s/%s)", failure.Kind, failure.Code)
}

type Authenticator interface {
	AuthenticateAKA(context.Context, AKARequest) AKAResponse
}

type ModemExecutor interface {
	ExecuteModem(context.Context, ModemRequest) ModemResponse
}

type ModemMediaExecutor interface {
	ExecuteModemMedia(context.Context, ModemMediaRequest) ModemMediaResponse
}

func (command ModemMediaCommand) Validate() error {
	if !validIdentifier(command.OperationID) || !validEquipmentID(command.EquipmentID) ||
		!validCardID(command.CardID) || !validIdentifier(command.SessionID) ||
		!validModemMediaAction(command.Action) {
		return errors.New("invalid modem media command")
	}
	if command.Action == ModemMediaPrepare && (len(command.MediaToken) < minimumTokenBytes || len(command.MediaToken) > 512) ||
		command.Action == ModemMediaStop && command.MediaToken != "" {
		return errors.New("invalid modem media action fields")
	}
	return nil
}

func (command ModemMediaCommand) requestFor(attachmentID string) ModemMediaRequest {
	return ModemMediaRequest{
		OperationID: command.OperationID, AttachmentID: attachmentID,
		EquipmentID: command.EquipmentID, CardID: command.CardID, Action: command.Action,
		SessionID: command.SessionID, MediaToken: command.MediaToken,
	}
}

func (request ModemMediaRequest) Validate() error {
	command := ModemMediaCommand{
		OperationID: request.OperationID, EquipmentID: request.EquipmentID,
		CardID: request.CardID, Action: request.Action, SessionID: request.SessionID,
		MediaToken: request.MediaToken,
	}
	if !validIdentifier(request.AttachmentID) || command.Validate() != nil {
		return errors.New("invalid modem media request")
	}
	return nil
}

func (response ModemMediaResponse) ValidateFor(request ModemMediaRequest) error {
	if response.OperationID != request.OperationID || response.AttachmentID != request.AttachmentID ||
		response.EquipmentID != request.EquipmentID || response.CardID != request.CardID ||
		response.SessionID != request.SessionID {
		return errors.New("modem media response identity does not match request")
	}
	if response.Failure != nil {
		if response.Failure.Validate() != nil || response.State != "" {
			return errors.New("invalid failed modem media response")
		}
		return nil
	}
	want := "ready"
	if request.Action == ModemMediaStop {
		want = "stopped"
	}
	if response.State != want {
		return errors.New("invalid successful modem media response state")
	}
	return nil
}

func (command ModemCommand) Validate() error {
	if !validIdentifier(command.OperationID) || !validEquipmentID(command.EquipmentID) ||
		!validCardID(command.CardID) || !validModemAction(command.Action) {
		return errors.New("invalid modem command identity, target, or action")
	}
	if err := validateModemActionFields(command.Action, command.LeaseID, command.Number); err != nil {
		return err
	}
	return nil
}

func (command ModemCommand) requestFor(attachmentID string) ModemRequest {
	return ModemRequest{
		OperationID: command.OperationID, AttachmentID: attachmentID,
		EquipmentID: command.EquipmentID, CardID: command.CardID, Action: command.Action,
		LeaseID: command.LeaseID, Number: command.Number,
	}
}

func (request ModemRequest) Validate() error {
	if !validIdentifier(request.OperationID) || !validIdentifier(request.AttachmentID) ||
		!validEquipmentID(request.EquipmentID) || !validCardID(request.CardID) ||
		!validModemAction(request.Action) {
		return errors.New("invalid modem request identity, attachment, target, or action")
	}
	if err := validateModemActionFields(request.Action, request.LeaseID, request.Number); err != nil {
		return err
	}
	return nil
}

func (response ModemResponse) ValidateFor(request ModemRequest) error {
	if response.OperationID != request.OperationID || response.AttachmentID != request.AttachmentID ||
		response.EquipmentID != request.EquipmentID || response.CardID != request.CardID {
		return errors.New("modem response identity does not match request")
	}
	if response.Failure != nil {
		if response.Failure.Validate() != nil || response.Call != nil || response.Lease != nil {
			return errors.New("invalid failed modem response")
		}
		return nil
	}
	if request.Action == ModemCallRenew {
		if response.Call != nil || response.Lease == nil || response.Lease.ValidateFor(request.LeaseID) != nil {
			return errors.New("invalid successful modem lease renewal")
		}
		return nil
	}
	if response.Call == nil || response.Call.ValidateFor(request.Action) != nil {
		return errors.New("invalid successful modem response")
	}
	needsLease := request.Action == ModemCallDial || request.Action == ModemCallAnswer
	if needsLease != (response.Lease != nil) || response.Lease != nil && response.Lease.ValidateFor(request.LeaseID) != nil {
		return errors.New("invalid modem call lease result")
	}
	return nil
}

func (result ModemLeaseResult) ValidateFor(leaseID string) error {
	if result.LeaseID != leaseID || !validIdentifier(result.LeaseID) || result.ExpiresAt.IsZero() {
		return errors.New("invalid modem call lease")
	}
	return nil
}

func (result ModemCallResult) ValidateFor(action ModemAction) error {
	if !oneOf(result.State, "idle", "active", "held", "dialing", "ringing_out", "ringing_in", "waiting") ||
		!oneOf(result.Direction, "", "in", "out") || len(result.Number) > 64 ||
		result.ObservedAt.IsZero() || result.ObservedAt.After(time.Now().Add(time.Minute)) || !result.Authoritative {
		return errors.New("invalid modem call result")
	}
	if action == ModemCallHangup {
		if !result.TerminalConfirmed || result.State != "idle" ||
			!oneOf(result.Strategy, "already_idle", "chup", "chup_ath") {
			return errors.New("modem hangup lacks terminal confirmation")
		}
	} else if result.TerminalConfirmed || result.Strategy != "" {
		return errors.New("modem status contains hangup state")
	}
	if result.State == "idle" && (result.Direction != "" || result.Number != "") ||
		action == ModemCallDial && result.State != "idle" && result.Direction != "out" ||
		action == ModemCallAnswer && result.State != "idle" && result.Direction != "in" {
		return errors.New("modem call result direction is inconsistent")
	}
	return nil
}

func validModemAction(value ModemAction) bool {
	return value == ModemCallStatus || value == ModemCallHangup || value == ModemCallDial ||
		value == ModemCallAnswer || value == ModemCallRenew
}

func validModemMediaAction(value ModemMediaAction) bool {
	return value == ModemMediaPrepare || value == ModemMediaStop
}

func validateModemActionFields(action ModemAction, leaseID, number string) error {
	switch action {
	case ModemCallStatus, ModemCallHangup:
		if leaseID != "" || number != "" {
			return errors.New("status and hangup do not accept lease or number fields")
		}
	case ModemCallDial:
		if !validIdentifier(leaseID) || !validTelephone(number) {
			return errors.New("dial requires a valid lease and telephone number")
		}
	case ModemCallAnswer, ModemCallRenew:
		if !validIdentifier(leaseID) || number != "" {
			return errors.New("answer and renewal require only a valid lease")
		}
	}
	return nil
}

func validTelephone(value string) bool {
	if len(value) < 1 || len(value) > 32 {
		return false
	}
	digits := 0
	for index, character := range value {
		if character >= '0' && character <= '9' {
			digits++
			continue
		}
		if character == '+' && index == 0 {
			continue
		}
		return false
	}
	return digits > 0
}

func (challenge AKAChallenge) Validate() error {
	return (AKARequest{
		OperationID: challenge.OperationID, SessionGeneration: "validation",
		CardID: challenge.CardID, Application: challenge.Application,
		RAND: challenge.RAND, AUTN: challenge.AUTN,
	}).Validate()
}

func (challenge AKAChallenge) requestFor(sessionGeneration string) AKARequest {
	return AKARequest{
		OperationID: challenge.OperationID, SessionGeneration: sessionGeneration,
		CardID: challenge.CardID, Application: challenge.Application,
		RAND: append([]byte(nil), challenge.RAND...), AUTN: append([]byte(nil), challenge.AUTN...),
	}
}

func (hello Hello) Validate() error {
	if hello.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported Agent link schema version %d", hello.SchemaVersion)
	}
	if !validIdentifier(hello.AgentID) || !validIdentifier(hello.ProcessGeneration) {
		return errors.New("invalid Agent link identity or process generation")
	}
	return nil
}

func (topology TopologySnapshot) Validate() error {
	if topology.ReaderCondition != ReaderStarting && topology.ReaderCondition != ReaderReady &&
		topology.ReaderCondition != ReaderRecovering {
		return errors.New("Agent topology has an invalid reader condition")
	}
	if len(topology.ReaderDetail) > 1024 || topology.ReaderCondition != ReaderRecovering && topology.ReaderDetail != "" ||
		topology.ReaderCondition != ReaderReady && len(topology.Readers) != 0 {
		return errors.New("Agent topology reader condition has inconsistent detail or attachments")
	}
	if len(topology.Readers) > 64 {
		return errors.New("Agent topology has too many reader attachments")
	}
	previous := ""
	for index, reader := range topology.Readers {
		if !validReaderName(reader.ReaderName) || index > 0 && reader.ReaderName <= previous {
			return errors.New("Agent topology reader names must be valid, unique, and sorted")
		}
		previous = reader.ReaderName
		if reader.SessionGeneration != "" && !validIdentifier(reader.SessionGeneration) ||
			reader.CardID != "" && !validCardID(reader.CardID) ||
			reader.ATRSHA256 != "" && !validSHA256(reader.ATRSHA256) || len(reader.IdentityDetail) > 1024 {
			return errors.New("Agent topology contains an invalid card fact")
		}
		if err := validateEUICC(reader.EUICC); err != nil {
			return err
		}
		switch reader.IdentityState {
		case CardAbsent:
			if reader.CardPresent || reader.SessionGeneration != "" || reader.CardID != "" || reader.EUICC != nil || reader.ATRSHA256 != "" {
				return errors.New("absent topology attachment contains card state")
			}
		case CardIdentityDiscovering, CardIdentityUnavailable:
			if !reader.CardPresent || reader.SessionGeneration == "" || reader.CardID != "" || reader.EUICC != nil {
				return errors.New("unidentified topology card has inconsistent state")
			}
		case CardIdentified:
			if !reader.CardPresent || reader.SessionGeneration == "" || reader.CardID == "" && reader.EUICC == nil {
				return errors.New("identified topology card has inconsistent state")
			}
		default:
			return errors.New("Agent topology has an unknown identity state")
		}
		if reader.IdentityDetail != "" && reader.IdentityState != CardIdentityDiscovering &&
			reader.IdentityState != CardIdentityUnavailable {
			return errors.New("Agent topology contains identity detail for a completed identity")
		}
	}
	if err := topology.validateModems(); err != nil {
		return err
	}
	return nil
}

func (topology TopologySnapshot) validateModems() error {
	switch topology.ModemCondition {
	case "":
		if topology.ModemDetail != "" || len(topology.Modems) != 0 {
			return errors.New("legacy Agent topology contains modem state")
		}
	case ModemDisabled, ModemStarting:
		if topology.ModemDetail != "" || len(topology.Modems) != 0 {
			return errors.New("inactive modem topology contains attachments")
		}
	case ModemRecovering:
		if topology.ModemDetail == "" || len(topology.ModemDetail) > 1024 || len(topology.Modems) != 0 {
			return errors.New("recovering modem topology has inconsistent detail or attachments")
		}
	case ModemReady:
		if topology.ModemDetail != "" || len(topology.Modems) > 32 {
			return errors.New("ready modem topology has inconsistent detail or too many attachments")
		}
	default:
		return errors.New("Agent topology has an invalid modem condition")
	}
	previous := ""
	for index, modem := range topology.Modems {
		if !validIdentifier(modem.AttachmentID) || index > 0 && modem.AttachmentID <= previous ||
			len(modem.EquipmentID) > 128 || len(modem.Manufacturer) > 256 || len(modem.Model) > 256 ||
			len(modem.Firmware) > 256 || len(modem.Detail) > 1024 || len(modem.Capabilities.MBNVoiceClass) > 64 ||
			len(modem.SIM.SMSError) > 128 || len(modem.SIM.SMSC) > 64 || len(modem.Network.OperatorID) > 64 ||
			len(modem.Network.OperatorName) > 256 || len(modem.Network.Profile) > 256 || len(modem.SIM.MSISDNs) > 16 {
			return errors.New("Agent topology contains an invalid modem fact")
		}
		previous = modem.AttachmentID
		if modem.Condition != "ready" && modem.Condition != "degraded" ||
			modem.Condition == "ready" && modem.Detail != "" || modem.Condition == "degraded" && modem.Detail == "" {
			return errors.New("Agent topology contains an inconsistent modem condition")
		}
		if modem.SIM.ICCID != "" && !validCardID(modem.SIM.ICCID) || modem.SIM.IMSI != "" && !validCardID(modem.SIM.IMSI) {
			return errors.New("Agent topology contains an invalid modem SIM identity")
		}
		for _, number := range modem.SIM.MSISDNs {
			if len(number) > 64 {
				return errors.New("Agent topology contains an invalid modem telephone number")
			}
		}
		if modem.Network.SignalPercent != nil && *modem.Network.SignalPercent > 100 {
			return errors.New("Agent topology contains an invalid modem signal")
		}
		if !oneOf(modem.SIM.State, "unknown", "ready", "absent", "locked", "failed") ||
			!oneOf(modem.Network.Registration, "unknown", "unregistered", "searching", "home", "roaming", "denied") ||
			!oneOf(modem.Network.SoftwareRadio, "unknown", "off", "on") ||
			!oneOf(modem.Network.HardwareRadio, "unknown", "off", "on") ||
			!oneOf(modem.Network.Data, "unknown", "disconnected", "connecting", "connected", "disconnecting") {
			return errors.New("Agent topology contains an invalid modem machine state")
		}
		if err := validateModemAT(modem.AT); err != nil {
			return err
		}
	}
	return nil
}

func validateModemAT(value ModemATControlFact) error {
	if len(value.Port) > 64 || len(value.Detail) > 1024 ||
		!oneOf(value.State, "", "unknown", "ready", "busy", "unavailable", "degraded") {
		return errors.New("Agent topology contains an invalid modem AT control fact")
	}
	capable := value.CallSignalling || value.SMS || value.SIMAPDU
	switch value.State {
	case "", "unknown":
		if value.Port != "" || value.Detail != "" || capable {
			return errors.New("unknown modem AT control contains observed state")
		}
	case "ready":
		if !validPortLabel(value.Port) || value.Detail != "" {
			return errors.New("ready modem AT control is missing its owned port")
		}
	default:
		if value.Detail == "" || capable {
			return errors.New("inactive modem AT control has inconsistent detail or capabilities")
		}
	}
	return nil
}

func validPortLabel(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-_.:", character) {
			continue
		}
		return false
	}
	return true
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validateEUICC(euicc *EUICCFact) error {
	if euicc == nil {
		return nil
	}
	if len(euicc.EID) != 32 || !validCardID(euicc.EID) || len(euicc.Profiles) > 128 ||
		!euicc.ProfilesAvailable && len(euicc.Profiles) != 0 {
		return errors.New("Agent topology contains an invalid eUICC fact")
	}
	previous := ""
	for index, profile := range euicc.Profiles {
		if !validCardID(profile.ICCID) || index > 0 && profile.ICCID <= previous ||
			profile.State != EUICCProfileEnabled && profile.State != EUICCProfileDisabled {
			return errors.New("Agent topology contains invalid or unsorted eUICC profiles")
		}
		previous = profile.ICCID
	}
	return nil
}

func (topology TopologySnapshot) Revision() (string, error) {
	if err := topology.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(topology)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func (report HealthReport) Validate() error {
	if report.SchemaVersion != SchemaVersion || report.Sequence == 0 || !validSHA256(report.TopologyRevision) {
		return errors.New("invalid Agent health identity, sequence, or topology revision")
	}
	if report.Topology != nil {
		revision, err := report.Topology.Revision()
		if err != nil || revision != report.TopologyRevision {
			return errors.New("Agent health topology does not match its revision")
		}
	}
	return nil
}

func NormalizeTopology(topology TopologySnapshot) TopologySnapshot {
	result := TopologySnapshot{
		ReaderCondition: topology.ReaderCondition, ReaderDetail: topology.ReaderDetail,
		Readers:        make([]ReaderFact, len(topology.Readers)),
		ModemCondition: topology.ModemCondition, ModemDetail: topology.ModemDetail,
		Modems: make([]ModemFact, len(topology.Modems)),
	}
	copy(result.Readers, topology.Readers)
	for index := range result.Readers {
		result.Readers[index].EUICC = cloneEUICC(topology.Readers[index].EUICC)
	}
	copy(result.Modems, topology.Modems)
	for index := range result.Modems {
		result.Modems[index].SIM.MSISDNs = append([]string(nil), topology.Modems[index].SIM.MSISDNs...)
		if topology.Modems[index].Network.SignalPercent != nil {
			signal := *topology.Modems[index].Network.SignalPercent
			result.Modems[index].Network.SignalPercent = &signal
		}
	}
	sort.Slice(result.Readers, func(left, right int) bool {
		return result.Readers[left].ReaderName < result.Readers[right].ReaderName
	})
	sort.Slice(result.Modems, func(left, right int) bool {
		return result.Modems[left].AttachmentID < result.Modems[right].AttachmentID
	})
	return result
}

func cloneEUICC(source *EUICCFact) *EUICCFact {
	if source == nil {
		return nil
	}
	profiles := make([]EUICCProfileFact, len(source.Profiles))
	copy(profiles, source.Profiles)
	return &EUICCFact{
		EID: source.EID, ProfilesAvailable: source.ProfilesAvailable,
		Profiles: profiles,
	}
}

func (request AKARequest) Validate() error {
	if !validIdentifier(request.OperationID) || !validIdentifier(request.SessionGeneration) ||
		!validCardID(request.CardID) {
		return errors.New("invalid AKA operation, session generation, or card identity")
	}
	if request.Application != AKAApplicationUSIM && request.Application != AKAApplicationISIM {
		return fmt.Errorf("unsupported AKA application %q", request.Application)
	}
	if len(request.RAND) != 16 || len(request.AUTN) != 16 {
		return errors.New("AKA RAND and AUTN must each be 16 bytes")
	}
	return nil
}

func (response AKAResponse) ValidateFor(request AKARequest) error {
	if response.OperationID != request.OperationID || response.SessionGeneration != request.SessionGeneration {
		return errors.New("AKA response identity does not match request")
	}
	if response.Failure != nil {
		if response.Failure.Validate() != nil || len(response.Body) != 0 || response.SW1 != 0 || response.SW2 != 0 {
			return errors.New("invalid failed AKA response")
		}
		return nil
	}
	if len(response.Body) > 1024 || response.SW1 == 0 && response.SW2 == 0 {
		return errors.New("invalid successful AKA response")
	}
	return nil
}

func (failure RemoteError) Validate() error {
	switch failure.Kind {
	case "not_ready", "conflict", "rejected", "transport", "failed":
	default:
		return errors.New("invalid remote error kind")
	}
	if !validIdentifier(failure.Code) || failure.RetryAfter < 0 || failure.RetryAfter > 3_600_000 {
		return errors.New("invalid remote error code or retry delay")
	}
	return nil
}

func validIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-_.:", character) {
			continue
		}
		return false
	}
	return true
}

func validCardID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validEquipmentID(value string) bool {
	if len(value) < 14 || len(value) > 17 {
		return false
	}
	return validCardID(value)
}

func validReaderName(value string) bool {
	if len(value) < 1 || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
