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
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const SchemaVersion = 1

type AKAApplication string

type AKADeviceKind string

const (
	AKAApplicationUSIM AKAApplication = "usim"
	AKAApplicationISIM AKAApplication = "isim"
	AKADeviceReader    AKADeviceKind  = "reader"
	AKADeviceModem     AKADeviceKind  = "modem"
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
// Mutations use a separate request/response envelope fenced to this topology.
type EUICCProfileFact struct {
	ICCID               string            `json:"iccid"`
	State               EUICCProfileState `json:"state"`
	Nickname            string            `json:"nickname,omitempty"`
	ServiceProviderName string            `json:"service_provider_name,omitempty"`
	ProfileName         string            `json:"profile_name,omitempty"`
}

// EUICCFact identifies the inserted eUICC independently from its reader and
// active profile. ProfilesAvailable distinguishes a blank eUICC from a failed
// profile query.
type EUICCFact struct {
	EID                   string             `json:"eid"`
	ProfilesAvailable     bool               `json:"profiles_available"`
	ProfileManagement     bool               `json:"profile_management,omitempty"`
	ProfileDownload       bool               `json:"profile_download,omitempty"`
	ProfileDiscovery      bool               `json:"profile_discovery,omitempty"`
	NotificationInventory bool               `json:"notification_inventory,omitempty"`
	NotificationDelivery  bool               `json:"notification_delivery,omitempty"`
	NotificationRemoval   bool               `json:"notification_removal,omitempty"`
	Download              *EUICCDownloadFact `json:"download,omitempty"`
	Profiles              []EUICCProfileFact `json:"profiles"`
}

// EUICCSlotFact identifies one independently addressable secure element in a
// physical removable eUICC. EID remains the durable operation target; SlotID
// and Label only distinguish the secure elements carried by one reader.
type EUICCSlotFact struct {
	SlotID string    `json:"slot_id"`
	Label  string    `json:"label"`
	EUICC  EUICCFact `json:"euicc"`
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
	State             string   `json:"state"`
	SessionGeneration string   `json:"session_generation,omitempty"`
	ICCID             string   `json:"iccid,omitempty"`
	IMSI              string   `json:"imsi,omitempty"`
	MSISDNs           []string `json:"msisdns,omitempty"`
	PINState          string   `json:"pin_state,omitempty"`
	PINConfigured     bool     `json:"pin_configured"`
	PINAttempts       *uint32  `json:"pin_attempts_remaining,omitempty"`
	PINRecovery       string   `json:"pin_recovery,omitempty"`
	Configured        bool     `json:"sms_configured"`
	SMSC              string   `json:"smsc,omitempty"`
	SMSError          string   `json:"sms_error,omitempty"`
}

type ModemNetworkFact struct {
	Registration    string  `json:"registration"`
	OperatorID      string  `json:"operator_id,omitempty"`
	OperatorName    string  `json:"operator_name,omitempty"`
	SignalPercent   *uint32 `json:"signal_percent,omitempty"`
	SoftwareRadio   string  `json:"software_radio"`
	HardwareRadio   string  `json:"hardware_radio"`
	Data            string  `json:"data"`
	Profile         string  `json:"profile,omitempty"`
	DataGuard       string  `json:"data_guard,omitempty"`
	DataGuardDetail string  `json:"data_guard_detail,omitempty"`
}

type ModemATControlFact struct {
	State           string `json:"state"`
	Port            string `json:"port,omitempty"`
	Detail          string `json:"detail,omitempty"`
	CallSignalling  bool   `json:"call_signalling"`
	SMS             bool   `json:"sms"`
	SIMAPDU         bool   `json:"sim_apdu"`
	SIMAPDUOnDemand bool   `json:"sim_apdu_on_demand,omitempty"`
}

// ModemFact reports one local modem attachment. AttachmentID identifies the
// current Windows MBN attachment; SIM.ICCID identifies the inserted card.
type ModemFact struct {
	AttachmentID        string             `json:"attachment_id"`
	EquipmentID         string             `json:"equipment_id,omitempty"`
	Manufacturer        string             `json:"manufacturer,omitempty"`
	Model               string             `json:"model,omitempty"`
	Firmware            string             `json:"firmware,omitempty"`
	Condition           string             `json:"condition"`
	Detail              string             `json:"detail,omitempty"`
	LastContinuityIssue string             `json:"last_continuity_issue,omitempty"`
	Capabilities        ModemCapabilities  `json:"capabilities"`
	AT                  ModemATControlFact `json:"at_control"`
	SIM                 ModemSIMFact       `json:"sim"`
	Network             ModemNetworkFact   `json:"network"`
	Policy              *ModemPolicyFact   `json:"policy,omitempty"`
}

// RawUSBSessionFact reports transport ownership without publishing the same
// inserted SIM twice. Only the importing Agent may publish the re-enumerated
// modem as a normal ModemFact; the source reports this fenced session while
// sing-usbip owns the complete USB device.
type RawUSBSessionFact struct {
	Role                    RawUSBRole `json:"role"`
	SourceAgentID           string     `json:"source_agent_id"`
	SourceProcessGeneration string     `json:"source_process_generation"`
	AttachmentID            string     `json:"attachment_id"`
	SessionGeneration       string     `json:"session_generation"`
	EquipmentID             string     `json:"equipment_id"`
	CardID                  string     `json:"card_id"`
	USBSessionID            string     `json:"usb_session_id"`
	CaptureGeneration       string     `json:"capture_generation"`
	State                   string     `json:"state"`
}

// RawUSBRecoveryFact is a durable source reservation after transport or the
// Agent process disappeared. It is not a usable modem fact: only the raw
// binding reconciler may consume it to resume the same Agent+equipment+ICCID
// capture. Explicit disable removes the reservation and restores adapted mode.
type RawUSBRecoveryFact struct {
	AttachmentID      string       `json:"attachment_id"`
	SessionGeneration string       `json:"session_generation"`
	EquipmentID       string       `json:"equipment_id"`
	CardID            string       `json:"card_id"`
	USBSessionID      string       `json:"usb_session_id"`
	CaptureGeneration string       `json:"capture_generation"`
	Device            RawUSBDevice `json:"device"`
	State             string       `json:"state"`
}

// ReaderFact describes one current PC/SC attachment. ReaderName is only a
// local attachment label. SessionGeneration fences one insertion, while
// CardID is the durable ICCID when the card exposes one.
type ReaderSIMFact struct {
	IdentityState string `json:"identity_state"`
	IMSI          string `json:"imsi,omitempty"`
	MCC           string `json:"mcc,omitempty"`
	MNC           string `json:"mnc,omitempty"`
	SMSC          string `json:"smsc,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
}

type ReaderFact struct {
	ReaderName        string            `json:"reader_name"`
	CardPresent       bool              `json:"card_present"`
	SessionGeneration string            `json:"session_generation,omitempty"`
	CardID            string            `json:"card_id,omitempty"`
	SIM               *ReaderSIMFact    `json:"sim,omitempty"`
	EUICC             *EUICCFact        `json:"euicc,omitempty"`
	SecureElements    []EUICCSlotFact   `json:"secure_elements,omitempty"`
	IdentityState     CardIdentityState `json:"identity_state"`
	IdentityDetail    string            `json:"identity_detail,omitempty"`
	ATRSHA256         string            `json:"atr_sha256,omitempty"`
}

type AgentStorageFact struct {
	State       string `json:"state"`
	TotalBytes  uint64 `json:"total_bytes,omitempty"`
	FreeBytes   uint64 `json:"free_bytes,omitempty"`
	UsedPercent uint32 `json:"used_percent,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
}

// AgentHostFact is a cached, non-invasive description of the Agent process
// and its configuration filesystem. Hardware truth remains in Readers and
// Modems; this fact must never probe or mutate a device.
type AgentHostFact struct {
	SchemaVersion   int              `json:"schema_version"`
	Platform        string           `json:"platform"`
	Architecture    string           `json:"architecture"`
	BuildVersion    string           `json:"build_version"`
	HostMode        string           `json:"host_mode"`
	Manager         string           `json:"manager"`
	SessionScope    string           `json:"session_scope"`
	ConfigState     string           `json:"config_state"`
	TokenConfigured bool             `json:"token_configured"`
	ModemEnabled    bool             `json:"modem_enabled"`
	Storage         AgentStorageFact `json:"storage"`
}

type TopologySnapshot struct {
	Host            *AgentHostFact  `json:"host,omitempty"`
	ReaderCondition ReaderCondition `json:"reader_condition"`
	ReaderDetail    string          `json:"reader_detail,omitempty"`
	Readers         []ReaderFact    `json:"readers"`
	// Empty ModemCondition is the legacy schema-1 representation and is valid
	// only with no modem facts. This keeps already deployed PC/SC Agents wire
	// compatible while new Agents explicitly report disabled/starting/ready.
	ModemCondition   ModemCondition       `json:"modem_condition,omitempty"`
	ModemDetail      string               `json:"modem_detail,omitempty"`
	Modems           []ModemFact          `json:"modems,omitempty"`
	RawUSBSource     bool                 `json:"raw_usb_source,omitempty"`
	RawUSBImporter   bool                 `json:"raw_usb_importer,omitempty"`
	RawUSBRecoveries []RawUSBRecoveryFact `json:"raw_usb_recoveries,omitempty"`
	RawUSBSessions   []RawUSBSessionFact  `json:"raw_usb_sessions,omitempty"`
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

const ModemEventSchemaVersion = 1

type ModemEventKind string

const (
	ModemEventKindSMS  ModemEventKind = "cellular_sms"
	ModemEventKindCall ModemEventKind = "cellular_call"
)

// ModemEvent is an Agent-originated durable business observation. EventID is
// the outbox delivery identity. Current attachment/session fields are
// transport fences and are intentionally separate from the durable SMS
// fingerprint or incoming-call occurrence identity.
type ModemEvent struct {
	SchemaVersion        int             `json:"schema_version"`
	EventID              string          `json:"event_id"`
	Kind                 ModemEventKind  `json:"kind"`
	AttachmentID         string          `json:"attachment_id"`
	EquipmentID          string          `json:"equipment_id"`
	CardID               string          `json:"card_id"`
	SIMSessionGeneration string          `json:"sim_session_generation"`
	ObservedAt           time.Time       `json:"observed_at"`
	SMS                  *ModemEventSMS  `json:"sms,omitempty"`
	Call                 *ModemEventCall `json:"call,omitempty"`
}

type ModemEventSMS struct {
	Index          int    `json:"index"`
	StorageIndices []int  `json:"storage_indices"`
	Fingerprint    string `json:"fingerprint"`
	State          string `json:"state"`
	Direction      string `json:"direction"`
	Peer           string `json:"peer"`
	Body           string `json:"body,omitempty"`
	Reference      int    `json:"reference,omitempty"`
	Delivery       string `json:"delivery,omitempty"`
}

type ModemEventCall struct {
	IncomingEventID string    `json:"incoming_event_id"`
	Occurrence      uint64    `json:"occurrence"`
	Revision        uint64    `json:"revision"`
	NativeIndex     int       `json:"native_call_index"`
	State           string    `json:"state"`
	Direction       string    `json:"direction"`
	Number          string    `json:"number,omitempty"`
	FirstObservedAt time.Time `json:"first_observed_at"`
	Notify          bool      `json:"notify"`
}

type ModemEventAck struct {
	EventID   string `json:"event_id"`
	Accepted  bool   `json:"accepted"`
	Retryable bool   `json:"retryable,omitempty"`
	Code      string `json:"code,omitempty"`
}

func (event ModemEvent) Validate() error {
	if event.SchemaVersion != ModemEventSchemaVersion || !validIdentifier(event.EventID) ||
		!validIdentifier(event.AttachmentID) || !validEquipmentID(event.EquipmentID) ||
		!validCardID(event.CardID) || !validIdentifier(event.SIMSessionGeneration) || event.ObservedAt.IsZero() {
		return errors.New("invalid modem event identity or fence")
	}
	switch event.Kind {
	case ModemEventKindSMS:
		if event.SMS == nil || event.Call != nil || event.SMS.Index < 1 || !validStorageIndices(event.SMS.StorageIndices) ||
			event.SMS.StorageIndices[0] != event.SMS.Index || !validHexDigest(event.SMS.Fingerprint) ||
			!oneOf(event.SMS.State, "received", "delivery") || !oneOf(event.SMS.Direction, "in", "out") ||
			strings.TrimSpace(event.SMS.Peer) == "" || len(event.SMS.Peer) > 64 || len(event.SMS.Body) > 16<<10 ||
			event.SMS.Reference < 0 || event.SMS.Reference > 255 {
			return errors.New("invalid cellular SMS event")
		}
		if event.SMS.State == "received" {
			if event.SMS.Direction != "in" || strings.TrimSpace(event.SMS.Body) == "" || event.SMS.Delivery != "" {
				return errors.New("invalid received cellular SMS event")
			}
		} else if event.SMS.Body != "" || !oneOf(event.SMS.Delivery,
			"delivered", "temporary_failure", "permanent_failure", "unknown") {
			return errors.New("invalid cellular delivery event")
		}
	case ModemEventKindCall:
		if event.Call == nil || event.SMS != nil || !validIdentifier(event.Call.IncomingEventID) ||
			event.Call.Occurrence == 0 || event.Call.Revision == 0 || event.Call.NativeIndex < 1 || event.Call.NativeIndex > 255 ||
			event.Call.Direction != "in" || !oneOf(event.Call.State, "ringing_in", "active", "held", "idle", "unavailable") ||
			len(event.Call.Number) > 64 || event.Call.FirstObservedAt.IsZero() ||
			event.Call.Notify && event.Call.State != "ringing_in" {
			return errors.New("invalid cellular call event")
		}
	default:
		return errors.New("invalid modem event kind")
	}
	return nil
}

func validStorageIndices(indices []int) bool {
	if len(indices) < 1 || len(indices) > 7 {
		return false
	}
	previous := 0
	for _, index := range indices {
		if index < 1 || index <= previous {
			return false
		}
		previous = index
	}
	return true
}

func (ack ModemEventAck) Validate() error {
	if !validIdentifier(ack.EventID) || ack.Accepted && (ack.Retryable || ack.Code != "") ||
		!ack.Accepted && (!validIdentifier(ack.Code) || ack.Retryable && ack.Code == "") {
		return errors.New("invalid modem event acknowledgement")
	}
	return nil
}

type ModemEventSource interface {
	PendingModemEvents(time.Time, int) ([]ModemEvent, error)
	AckModemEvent(string) error
	RejectModemEvent(string, string) error
	ModemEventWake() <-chan struct{}
}

type AgentEventContext struct {
	AgentID           string
	ProcessGeneration string
}

type ModemEventDisposition struct {
	Accepted  bool
	Retryable bool
	Code      string
}

type ModemEventSink interface {
	AcceptModemEvent(context.Context, AgentEventContext, ModemEvent) ModemEventDisposition
}

// AKARequest targets one exact live card attachment. SessionGeneration is
// replaced on removal/reinsertion even when reader name and ATR are unchanged.
// CardID is the active ICCID selected by Core and must match the session's
// discovered identity. An eUICC EID alone is never sufficient for AKA.
type AKARequest struct {
	OperationID       string         `json:"operation_id"`
	SessionGeneration string         `json:"session_generation"`
	CardID            string         `json:"card_id"`
	DeviceKind        AKADeviceKind  `json:"device_kind,omitempty"`
	AttachmentID      string         `json:"attachment_id,omitempty"`
	EquipmentID       string         `json:"equipment_id,omitempty"`
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

type EUICCProfileAction string

const (
	EUICCProfileEnable   EUICCProfileAction = "enable"
	EUICCProfileDisable  EUICCProfileAction = "disable"
	EUICCProfileNickname EUICCProfileAction = "nickname"
)

type EUICCProfileOutcome string

const (
	EUICCProfileAlreadyApplied EUICCProfileOutcome = "already_applied"
	EUICCProfileRefreshPending EUICCProfileOutcome = "refresh_pending"
	EUICCProfileUncertain      EUICCProfileOutcome = "uncertain"
)

// EUICCProfileCommand is the stable browser/Core intent. Core resolves the
// exact live Agent process and insertion generation from current topology.
type EUICCProfileCommand struct {
	OperationID      string             `json:"operation_id"`
	EID              string             `json:"eid"`
	ICCID            string             `json:"iccid"`
	Action           EUICCProfileAction `json:"action"`
	ExpectedState    EUICCProfileState  `json:"expected_state"`
	Nickname         string             `json:"nickname,omitempty"`
	ExpectedNickname string             `json:"expected_nickname,omitempty"`
}

// EUICCProfileRequest adds the current card insertion fence selected by Core.
// Reader names are intentionally absent: they are attachment labels, not card
// identity, and the Agent routes only by its opaque insertion generation.
type EUICCProfileRequest struct {
	OperationID       string             `json:"operation_id"`
	SessionGeneration string             `json:"session_generation"`
	EID               string             `json:"eid"`
	ICCID             string             `json:"iccid"`
	Action            EUICCProfileAction `json:"action"`
	ExpectedState     EUICCProfileState  `json:"expected_state"`
	Nickname          string             `json:"nickname,omitempty"`
	ExpectedNickname  string             `json:"expected_nickname,omitempty"`
}

// EUICCProfileResponse never claims the post-refresh state before the card is
// re-read. A successful ES10c response is refresh_pending; a transport loss
// after submission is uncertain. Both cause only the matching card session to
// reconnect and republish authoritative topology.
type EUICCProfileResponse struct {
	OperationID       string              `json:"operation_id"`
	SessionGeneration string              `json:"session_generation"`
	EID               string              `json:"eid"`
	ICCID             string              `json:"iccid"`
	Action            EUICCProfileAction  `json:"action"`
	Outcome           EUICCProfileOutcome `json:"outcome,omitempty"`
	State             EUICCProfileState   `json:"state,omitempty"`
	Nickname          string              `json:"nickname,omitempty"`
	Changed           bool                `json:"changed"`
	Failure           *RemoteError        `json:"failure,omitempty"`
}

type EUICCDownloadAction string

type EUICCDownloadState string

type EUICCDownloadStage string

const (
	EUICCDownloadStart  EUICCDownloadAction = "start"
	EUICCDownloadStatus EUICCDownloadAction = "status"
	EUICCDownloadCancel EUICCDownloadAction = "cancel"
)

const (
	EUICCDownloadQueued     EUICCDownloadState = "queued"
	EUICCDownloadRunning    EUICCDownloadState = "running"
	EUICCDownloadCancelling EUICCDownloadState = "cancelling"
	EUICCDownloadCompleted  EUICCDownloadState = "completed"
	EUICCDownloadFailed     EUICCDownloadState = "failed"
	EUICCDownloadCanceled   EUICCDownloadState = "canceled"
	EUICCDownloadUncertain  EUICCDownloadState = "uncertain"
)

const (
	EUICCDownloadStageQueued             EUICCDownloadStage = "queued"
	EUICCDownloadStageAuthenticateClient EUICCDownloadStage = "authenticate_client"
	EUICCDownloadStageAuthenticateServer EUICCDownloadStage = "authenticate_server"
	EUICCDownloadStageInstall            EUICCDownloadStage = "install"
	EUICCDownloadStageCompleted          EUICCDownloadStage = "completed"
)

type EUICCDownloadMetadata struct {
	ICCID               string `json:"iccid,omitempty"`
	ServiceProviderName string `json:"service_provider_name,omitempty"`
	ProfileName         string `json:"profile_name,omitempty"`
}

// EUICCDownloadJob is the observable, secret-free state of one download.
// Activation and confirmation codes are never echoed into status/topology.
type EUICCDownloadJob struct {
	State     EUICCDownloadState     `json:"state"`
	Stage     EUICCDownloadStage     `json:"stage"`
	Code      string                 `json:"code,omitempty"`
	StartedAt time.Time              `json:"started_at"`
	UpdatedAt time.Time              `json:"updated_at"`
	Metadata  *EUICCDownloadMetadata `json:"metadata,omitempty"`
}

type EUICCDownloadFact struct {
	OperationID string           `json:"operation_id"`
	Job         EUICCDownloadJob `json:"job"`
}

// EUICCDownloadCommand is a Core intent. Only start carries one-use secrets;
// status and cancel carry only the durable operation identity.
type EUICCDownloadCommand struct {
	OperationID      string              `json:"operation_id"`
	EID              string              `json:"eid"`
	Action           EUICCDownloadAction `json:"action"`
	ActivationCode   string              `json:"activation_code,omitempty"`
	ConfirmationCode string              `json:"confirmation_code,omitempty"`
	IMEI             string              `json:"imei,omitempty"`
}

// EUICCDownloadRequest adds the insertion fence selected from current Agent
// topology. Reader names remain attachment labels and are never routed on.
type EUICCDownloadRequest struct {
	OperationID       string              `json:"operation_id"`
	SessionGeneration string              `json:"session_generation"`
	EID               string              `json:"eid"`
	Action            EUICCDownloadAction `json:"action"`
	ActivationCode    string              `json:"activation_code,omitempty"`
	ConfirmationCode  string              `json:"confirmation_code,omitempty"`
	IMEI              string              `json:"imei,omitempty"`
}

type EUICCDownloadResponse struct {
	OperationID       string              `json:"operation_id"`
	SessionGeneration string              `json:"session_generation"`
	EID               string              `json:"eid"`
	Action            EUICCDownloadAction `json:"action"`
	Job               *EUICCDownloadJob   `json:"job,omitempty"`
	Failure           *RemoteError        `json:"failure,omitempty"`
}

// EUICCDiscoveryCommand is one manual, read-only SM-DS query. It is not a
// download job and is never retried or persisted by Core.
type EUICCDiscoveryCommand struct {
	OperationID string `json:"operation_id"`
	EID         string `json:"eid"`
	SMDS        string `json:"smds,omitempty"`
	IMEI        string `json:"imei,omitempty"`
}

// EUICCDiscoveryRequest adds the exact live insertion selected by Core.
type EUICCDiscoveryRequest struct {
	OperationID       string `json:"operation_id"`
	SessionGeneration string `json:"session_generation"`
	EID               string `json:"eid"`
	SMDS              string `json:"smds,omitempty"`
	IMEI              string `json:"imei,omitempty"`
}

type EUICCDiscoveryEntry struct {
	EventID          string `json:"event_id"`
	RSPServerAddress string `json:"rsp_server_address"`
}

type EUICCDiscoveryResponse struct {
	OperationID       string                `json:"operation_id"`
	SessionGeneration string                `json:"session_generation"`
	EID               string                `json:"eid"`
	SMDS              string                `json:"smds,omitempty"`
	Entries           []EUICCDiscoveryEntry `json:"entries,omitempty"`
	Failure           *RemoteError          `json:"failure,omitempty"`
}

type EUICCNotificationAction string

const (
	EUICCNotificationDeliver EUICCNotificationAction = "deliver"
	EUICCNotificationRemove  EUICCNotificationAction = "remove"
)

// EUICCNotificationCommand is one manual inventory, one explicitly confirmed
// delivery, or the removal half of a delivery already acknowledged by the
// receiver. All mutations carry the complete expected card entry.
type EUICCNotificationCommand struct {
	OperationID string                  `json:"operation_id"`
	EID         string                  `json:"eid"`
	Action      EUICCNotificationAction `json:"action,omitempty"`
	Expected    *EUICCNotificationEntry `json:"expected,omitempty"`
}

// EUICCNotificationRequest adds the exact live insertion selected by Core.
type EUICCNotificationRequest struct {
	OperationID       string                  `json:"operation_id"`
	SessionGeneration string                  `json:"session_generation"`
	EID               string                  `json:"eid"`
	Action            EUICCNotificationAction `json:"action,omitempty"`
	Expected          *EUICCNotificationEntry `json:"expected,omitempty"`
}

type EUICCNotificationEntry struct {
	SequenceNumber int64  `json:"sequence_number"`
	Event          string `json:"event"`
	ICCID          string `json:"iccid,omitempty"`
	Address        string `json:"address"`
}

func (entry EUICCNotificationEntry) Validate() error {
	if entry.SequenceNumber < 0 || !validEUICCNotificationEvent(entry.Event) ||
		entry.Address == "" || !validSecretText(entry.Address, 512) ||
		(entry.ICCID != "" && !validCardID(entry.ICCID)) {
		return errors.New("invalid eUICC notification entry")
	}
	return nil
}

func validEUICCNotificationEvent(value string) bool {
	if value == "install" || value == "enable" || value == "disable" || value == "delete" || value == "rpm" {
		return true
	}
	number, err := strconv.Atoi(strings.TrimPrefix(value, "event-"))
	return err == nil && strings.HasPrefix(value, "event-") && number >= 8 && number < 255
}

type EUICCNotificationResponse struct {
	OperationID       string                   `json:"operation_id"`
	SessionGeneration string                   `json:"session_generation"`
	EID               string                   `json:"eid"`
	Entries           []EUICCNotificationEntry `json:"entries,omitempty"`
	Acknowledged      bool                     `json:"acknowledged,omitempty"`
	Removed           bool                     `json:"removed,omitempty"`
	Failure           *RemoteError             `json:"failure,omitempty"`
}

type ModemAction string

const (
	ModemCallStatus ModemAction = "call_status"
	ModemCallHangup ModemAction = "call_hangup"
	ModemCallDial   ModemAction = "call_dial"
	ModemCallAnswer ModemAction = "call_answer"
	ModemCallReject ModemAction = "call_reject"
	ModemCallRenew  ModemAction = "call_renew"
	ModemCallDTMF   ModemAction = "call_dtmf"
	ModemSMSList    ModemAction = "sms_list"
	ModemSMSSend    ModemAction = "sms_send"
)

type ModemMediaAction string

const (
	ModemMediaPrepare ModemMediaAction = "media_prepare"
	ModemMediaStop    ModemMediaAction = "media_stop"
)

// ModemDataAction controls one explicit, non-persistent cellular data borrowing
// session. Prepare and stop own the MBN connection lifetime; open creates one
// exact TCP or UDP flow inside that already prepared session.
type ModemDataAction string

const (
	ModemDataPrepare ModemDataAction = "data_prepare"
	ModemDataRenew   ModemDataAction = "data_renew"
	ModemDataOpen    ModemDataAction = "data_open"
	ModemDataStop    ModemDataAction = "data_stop"
)

// RawUSBAction controls one whole-modem USB/IP session. It is deliberately
// separate from cellular data borrowing: the exporter transfers the complete
// physical USB device and the service-host Agent imports it before the normal
// MDD modem adapter provides call, SMS, VoWiFi, media and data capabilities.
type RawUSBAction string

type RawUSBRole string

const (
	RawUSBExportStart RawUSBAction = "raw_usb_export_start"
	RawUSBImportStart RawUSBAction = "raw_usb_import_start"
	RawUSBStop        RawUSBAction = "raw_usb_stop"

	RawUSBExporter RawUSBRole = "exporter"
	RawUSBImporter RawUSBRole = "importer"
)

// ModemCommand is the stable Core-side target. Core resolves it to one exact
// Agent process and current MBN attachment immediately before forwarding it.
type ModemCommand struct {
	OperationID          string      `json:"operation_id"`
	EquipmentID          string      `json:"equipment_id"`
	CardID               string      `json:"card_id"`
	Action               ModemAction `json:"action"`
	LeaseID              string      `json:"lease_id,omitempty"`
	Number               string      `json:"number,omitempty"`
	Signal               string      `json:"signal,omitempty"`
	Body                 string      `json:"body,omitempty"`
	IncomingEventID      string      `json:"incoming_event_id,omitempty"`
	SIMSessionGeneration string      `json:"sim_session_generation,omitempty"`
	NativeCallIndex      int         `json:"native_call_index,omitempty"`
	CallOccurrence       uint64      `json:"call_occurrence,omitempty"`
}

// ModemRequest adds the attachment fence selected from the Agent's current
// topology. The Agent rechecks equipment and SIM identity before touching AT.
type ModemRequest struct {
	OperationID          string      `json:"operation_id"`
	AttachmentID         string      `json:"attachment_id"`
	EquipmentID          string      `json:"equipment_id"`
	CardID               string      `json:"card_id"`
	Action               ModemAction `json:"action"`
	LeaseID              string      `json:"lease_id,omitempty"`
	Number               string      `json:"number,omitempty"`
	Signal               string      `json:"signal,omitempty"`
	Body                 string      `json:"body,omitempty"`
	IncomingEventID      string      `json:"incoming_event_id,omitempty"`
	SIMSessionGeneration string      `json:"sim_session_generation,omitempty"`
	NativeCallIndex      int         `json:"native_call_index,omitempty"`
	CallOccurrence       uint64      `json:"call_occurrence,omitempty"`
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

type ModemDataCommand struct {
	OperationID string          `json:"operation_id"`
	EquipmentID string          `json:"equipment_id"`
	CardID      string          `json:"card_id"`
	Action      ModemDataAction `json:"action"`
	SessionID   string          `json:"session_id"`
	StreamID    string          `json:"stream_id,omitempty"`
	StreamToken string          `json:"stream_token,omitempty"`
	Network     string          `json:"network,omitempty"`
	Address     string          `json:"address,omitempty"`
	Profile     string          `json:"profile,omitempty"`
	Purpose     string          `json:"purpose,omitempty"`
	ExpiresAt   time.Time       `json:"expires_at,omitempty"`
	MaxBytes    uint64          `json:"max_bytes,omitempty"`
}

type ModemDataRequest struct {
	OperationID          string          `json:"operation_id"`
	AttachmentID         string          `json:"attachment_id"`
	EquipmentID          string          `json:"equipment_id"`
	CardID               string          `json:"card_id"`
	SIMSessionGeneration string          `json:"sim_session_generation,omitempty"`
	Action               ModemDataAction `json:"action"`
	SessionID            string          `json:"session_id"`
	StreamID             string          `json:"stream_id,omitempty"`
	StreamToken          string          `json:"stream_token,omitempty"`
	Network              string          `json:"network,omitempty"`
	Address              string          `json:"address,omitempty"`
	Profile              string          `json:"profile,omitempty"`
	Purpose              string          `json:"purpose,omitempty"`
	ExpiresAt            time.Time       `json:"expires_at,omitempty"`
	MaxBytes             uint64          `json:"max_bytes,omitempty"`
}

type ModemDataResponse struct {
	OperationID          string       `json:"operation_id"`
	AttachmentID         string       `json:"attachment_id"`
	EquipmentID          string       `json:"equipment_id"`
	CardID               string       `json:"card_id"`
	SIMSessionGeneration string       `json:"sim_session_generation,omitempty"`
	SessionID            string       `json:"session_id"`
	StreamID             string       `json:"stream_id,omitempty"`
	State                string       `json:"state,omitempty"`
	Profile              string       `json:"profile,omitempty"`
	ExpiresAt            *time.Time   `json:"expires_at,omitempty"`
	Failure              *RemoteError `json:"failure,omitempty"`
}

type ModemPolicyAction string

const (
	ModemPolicyRead           ModemPolicyAction = "read"
	ModemPolicySet            ModemPolicyAction = "set"
	ModemPolicyProfiles       ModemPolicyAction = "profiles"
	ModemPolicyProfileSave    ModemPolicyAction = "profile_save"
	ModemPolicyPrepareSIMAPDU ModemPolicyAction = "prepare_sim_apdu"
)

type ModemPolicyDesired struct {
	CellularEnabled   bool   `json:"cellular_enabled"`
	ConnectionEnabled bool   `json:"connection_enabled"`
	FlightMode        bool   `json:"flight_mode"`
	RoamingEnabled    bool   `json:"roaming_enabled"`
	SelectedProfile   string `json:"selected_profile,omitempty"`
}

type ModemPolicyPatch struct {
	CellularEnabled   *bool   `json:"cellular_enabled,omitempty"`
	ConnectionEnabled *bool   `json:"connection_enabled,omitempty"`
	FlightMode        *bool   `json:"flight_mode,omitempty"`
	RoamingEnabled    *bool   `json:"roaming_enabled,omitempty"`
	SelectedProfile   *string `json:"selected_profile,omitempty"`
}

type ModemProfileInput struct {
	Name        string `json:"name"`
	APN         string `json:"apn"`
	Auth        string `json:"auth"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	PasswordSet bool   `json:"password_set"`
}

type ModemProfileView struct {
	Name               string `json:"name"`
	APN                string `json:"apn,omitempty"`
	Auth               string `json:"auth,omitempty"`
	Username           string `json:"username,omitempty"`
	PasswordConfigured bool   `json:"password_configured"`
	System             bool   `json:"system"`
	Source             string `json:"source,omitempty"`
	PDPType            string `json:"pdp_type,omitempty"`
}

type ModemPolicyDataLease struct {
	SessionID string `json:"session_id"`
	Purpose   string `json:"purpose"`
	State     string `json:"state"`
}

type ModemPolicyFact struct {
	SchemaVersion       int                   `json:"schema_version"`
	EquipmentID         string                `json:"equipment_id"`
	CardID              string                `json:"card_id"`
	Revision            uint64                `json:"revision"`
	Persisted           bool                  `json:"persisted"`
	Desired             ModemPolicyDesired    `json:"desired"`
	ProfileMode         string                `json:"profile_mode"`
	ConnectionAvailable bool                  `json:"connection_available"`
	ConnectionActive    bool                  `json:"connection_active"`
	State               string                `json:"state"`
	Code                string                `json:"code,omitempty"`
	RetryAt             time.Time             `json:"retry_at,omitempty"`
	UpdatedAt           time.Time             `json:"updated_at,omitempty"`
	DataLease           *ModemPolicyDataLease `json:"data_lease,omitempty"`
}

type ModemPolicyCommand struct {
	OperationID      string            `json:"operation_id"`
	EquipmentID      string            `json:"equipment_id"`
	CardID           string            `json:"card_id"`
	Action           ModemPolicyAction `json:"action"`
	ExpectedRevision uint64            `json:"expected_revision"`
	Patch            ModemPolicyPatch  `json:"patch,omitempty"`
	Profile          ModemProfileInput `json:"profile,omitempty"`
}

type ModemPolicyRequest struct {
	OperationID          string            `json:"operation_id"`
	AttachmentID         string            `json:"attachment_id"`
	EquipmentID          string            `json:"equipment_id"`
	CardID               string            `json:"card_id"`
	SIMSessionGeneration string            `json:"sim_session_generation"`
	Action               ModemPolicyAction `json:"action"`
	ExpectedRevision     uint64            `json:"expected_revision"`
	Patch                ModemPolicyPatch  `json:"patch,omitempty"`
	Profile              ModemProfileInput `json:"profile,omitempty"`
}

type ModemPolicyResponse struct {
	OperationID          string             `json:"operation_id"`
	AttachmentID         string             `json:"attachment_id"`
	EquipmentID          string             `json:"equipment_id"`
	CardID               string             `json:"card_id"`
	SIMSessionGeneration string             `json:"sim_session_generation"`
	Policy               *ModemPolicyFact   `json:"policy,omitempty"`
	Profiles             []ModemProfileView `json:"profiles,omitempty"`
	SIMAPDUReady         *bool              `json:"sim_apdu_ready,omitempty"`
	Failure              *RemoteError       `json:"failure,omitempty"`
}

// RawUSBDevice is the ephemeral USB/IP inventory returned by sing-usbip after
// the source Agent has captured the exact modem. It is never a durable modem
// identity; EquipmentID and CardID remain the authoritative binding.
type RawUSBDevice struct {
	BusID     string `json:"bus_id"`
	VendorID  uint16 `json:"vendor_id"`
	ProductID uint16 `json:"product_id"`
	Serial    string `json:"serial,omitempty"`
}

// RawUSBRequest is fenced to the source Agent process, current modem
// attachment, inserted-card generation, equipment identity and ICCID. The
// importer receives the same source fence plus the ephemeral exported device.
type RawUSBRequest struct {
	OperationID             string        `json:"operation_id"`
	Action                  RawUSBAction  `json:"action"`
	Role                    RawUSBRole    `json:"role"`
	SourceAgentID           string        `json:"source_agent_id"`
	SourceProcessGeneration string        `json:"source_process_generation"`
	AttachmentID            string        `json:"attachment_id"`
	SessionGeneration       string        `json:"session_generation"`
	EquipmentID             string        `json:"equipment_id"`
	CardID                  string        `json:"card_id"`
	USBSessionID            string        `json:"usb_session_id"`
	CaptureGeneration       string        `json:"capture_generation"`
	StreamID                string        `json:"stream_id,omitempty"`
	StreamToken             string        `json:"stream_token,omitempty"`
	Device                  *RawUSBDevice `json:"device,omitempty"`
	Recovering              bool          `json:"recovering,omitempty"`
}

type RawUSBResponse struct {
	OperationID             string        `json:"operation_id"`
	Action                  RawUSBAction  `json:"action"`
	Role                    RawUSBRole    `json:"role"`
	SourceAgentID           string        `json:"source_agent_id"`
	SourceProcessGeneration string        `json:"source_process_generation"`
	AttachmentID            string        `json:"attachment_id"`
	SessionGeneration       string        `json:"session_generation"`
	EquipmentID             string        `json:"equipment_id"`
	CardID                  string        `json:"card_id"`
	USBSessionID            string        `json:"usb_session_id"`
	CaptureGeneration       string        `json:"capture_generation"`
	StreamID                string        `json:"stream_id,omitempty"`
	State                   string        `json:"state,omitempty"`
	Device                  *RawUSBDevice `json:"device,omitempty"`
	Failure                 *RemoteError  `json:"failure,omitempty"`
}

type ModemCallResult struct {
	State             string    `json:"state"`
	Direction         string    `json:"direction,omitempty"`
	Number            string    `json:"number,omitempty"`
	NativeCallIndex   int       `json:"native_call_index,omitempty"`
	VoiceCalls        int       `json:"voice_calls"`
	IncomingCalls     int       `json:"incoming_calls"`
	ObservedAt        time.Time `json:"observed_at"`
	Authoritative     bool      `json:"authoritative"`
	TerminalConfirmed bool      `json:"terminal_confirmed"`
	Strategy          string    `json:"strategy,omitempty"`
}

type ModemLeaseResult struct {
	LeaseID   string    `json:"lease_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type ModemSMSMessage struct {
	Index       int       `json:"index"`
	State       string    `json:"state"`
	Direction   string    `json:"direction"`
	Peer        string    `json:"peer"`
	Body        string    `json:"body,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
	Fingerprint string    `json:"fingerprint"`
	Reference   int       `json:"reference,omitempty"`
	Delivery    string    `json:"delivery,omitempty"`
}

type ModemSMSResult struct {
	State      string            `json:"state"`
	Messages   []ModemSMSMessage `json:"messages"`
	References []int             `json:"references"`
}

type ModemResponse struct {
	OperationID  string            `json:"operation_id"`
	AttachmentID string            `json:"attachment_id"`
	EquipmentID  string            `json:"equipment_id"`
	CardID       string            `json:"card_id"`
	Call         *ModemCallResult  `json:"call,omitempty"`
	Lease        *ModemLeaseResult `json:"lease,omitempty"`
	SMS          *ModemSMSResult   `json:"sms,omitempty"`
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

type SIMPINExecutor interface {
	ExecuteSIMPIN(context.Context, SIMPINRequest) SIMPINResponse
}

type ModemMediaExecutor interface {
	ExecuteModemMedia(context.Context, ModemMediaRequest) ModemMediaResponse
}

type ModemDataExecutor interface {
	ExecuteModemData(context.Context, ModemDataRequest) ModemDataResponse
}

type ModemPolicyExecutor interface {
	ExecuteModemPolicy(context.Context, ModemPolicyRequest) ModemPolicyResponse
}

type RawUSBExecutor interface {
	ExecuteRawUSB(context.Context, RawUSBRequest) RawUSBResponse
}

type EUICCProfileExecutor interface {
	ExecuteEUICCProfile(context.Context, EUICCProfileRequest) EUICCProfileResponse
}

type EUICCDownloadExecutor interface {
	ExecuteEUICCDownload(context.Context, EUICCDownloadRequest) EUICCDownloadResponse
}

type EUICCDiscoveryExecutor interface {
	ExecuteEUICCDiscovery(context.Context, EUICCDiscoveryRequest) EUICCDiscoveryResponse
}

type EUICCNotificationExecutor interface {
	ExecuteEUICCNotification(context.Context, EUICCNotificationRequest) EUICCNotificationResponse
}

func (command EUICCProfileCommand) Validate() error {
	if !validIdentifier(command.OperationID) || !validEID(command.EID) ||
		!validCardID(command.ICCID) || !validEUICCProfileAction(command.Action) {
		return errors.New("invalid eUICC profile command identity or action")
	}
	if command.Action == EUICCProfileNickname {
		if command.ExpectedState != "" || !validProfileNickname(command.Nickname) ||
			!validProfileNickname(command.ExpectedNickname) {
			return errors.New("invalid eUICC profile nickname command")
		}
		return nil
	}
	if command.Nickname != "" || command.ExpectedNickname != "" {
		return errors.New("eUICC profile state command contains nickname fields")
	}
	want := EUICCProfileDisabled
	if command.Action == EUICCProfileDisable {
		want = EUICCProfileEnabled
	}
	if command.ExpectedState != want {
		return errors.New("eUICC profile command has an inconsistent expected state")
	}
	return nil
}

func (command EUICCProfileCommand) requestFor(sessionGeneration string) EUICCProfileRequest {
	return EUICCProfileRequest{
		OperationID: command.OperationID, SessionGeneration: sessionGeneration,
		EID: command.EID, ICCID: command.ICCID, Action: command.Action,
		ExpectedState: command.ExpectedState, Nickname: command.Nickname,
		ExpectedNickname: command.ExpectedNickname,
	}
}

func (request EUICCProfileRequest) Validate() error {
	command := EUICCProfileCommand{
		OperationID: request.OperationID, EID: request.EID, ICCID: request.ICCID,
		Action: request.Action, ExpectedState: request.ExpectedState,
		Nickname: request.Nickname, ExpectedNickname: request.ExpectedNickname,
	}
	if !validIdentifier(request.SessionGeneration) || command.Validate() != nil {
		return errors.New("invalid eUICC profile request")
	}
	return nil
}

func (response EUICCProfileResponse) ValidateFor(request EUICCProfileRequest) error {
	if response.OperationID != request.OperationID || response.SessionGeneration != request.SessionGeneration ||
		response.EID != request.EID || response.ICCID != request.ICCID || response.Action != request.Action {
		return errors.New("eUICC profile response identity does not match request")
	}
	if response.Failure != nil {
		if response.Failure.Validate() != nil || response.Outcome != "" || response.State != "" ||
			response.Nickname != "" || response.Changed {
			return errors.New("invalid failed eUICC profile response")
		}
		return nil
	}
	if request.Action == EUICCProfileNickname {
		switch response.Outcome {
		case EUICCProfileAlreadyApplied:
			if response.State != "" || response.Nickname != request.Nickname || response.Changed {
				return errors.New("invalid already-applied eUICC profile nickname response")
			}
		case EUICCProfileRefreshPending:
			if response.State != "" || response.Nickname != "" || !response.Changed {
				return errors.New("invalid refresh-pending eUICC profile nickname response")
			}
		case EUICCProfileUncertain:
			if response.State != "" || response.Nickname != "" || response.Changed {
				return errors.New("invalid uncertain eUICC profile nickname response")
			}
		default:
			return errors.New("invalid eUICC profile nickname response outcome")
		}
		return nil
	}
	if response.Nickname != "" {
		return errors.New("eUICC profile state response contains nickname")
	}
	desired := EUICCProfileEnabled
	if request.Action == EUICCProfileDisable {
		desired = EUICCProfileDisabled
	}
	switch response.Outcome {
	case EUICCProfileAlreadyApplied:
		if response.State != desired || response.Changed {
			return errors.New("invalid already-applied eUICC profile response")
		}
	case EUICCProfileRefreshPending:
		if response.State != "" || !response.Changed {
			return errors.New("invalid refresh-pending eUICC profile response")
		}
	case EUICCProfileUncertain:
		if response.State != "" || response.Changed {
			return errors.New("invalid uncertain eUICC profile response")
		}
	default:
		return errors.New("invalid eUICC profile response outcome")
	}
	return nil
}

func (command EUICCDownloadCommand) Validate() error {
	if !validIdentifier(command.OperationID) || !validEID(command.EID) || !validEUICCDownloadAction(command.Action) {
		return errors.New("invalid eUICC download command identity or action")
	}
	if command.Action == EUICCDownloadStart {
		if !validActivationCode(command.ActivationCode) || len(command.IMEI) != 15 || !validCardID(command.IMEI) ||
			!validSecretText(command.ConfirmationCode, 128) {
			return errors.New("invalid eUICC download start parameters")
		}
		return nil
	}
	if command.ActivationCode != "" || command.ConfirmationCode != "" || command.IMEI != "" {
		return errors.New("eUICC download status or cancel carries start parameters")
	}
	return nil
}

func (command EUICCDownloadCommand) requestFor(sessionGeneration string) EUICCDownloadRequest {
	return EUICCDownloadRequest{
		OperationID: command.OperationID, SessionGeneration: sessionGeneration, EID: command.EID,
		Action: command.Action, ActivationCode: command.ActivationCode,
		ConfirmationCode: command.ConfirmationCode, IMEI: command.IMEI,
	}
}

func (request EUICCDownloadRequest) Validate() error {
	command := EUICCDownloadCommand{
		OperationID: request.OperationID, EID: request.EID, Action: request.Action,
		ActivationCode: request.ActivationCode, ConfirmationCode: request.ConfirmationCode, IMEI: request.IMEI,
	}
	if !validIdentifier(request.SessionGeneration) || command.Validate() != nil {
		return errors.New("invalid eUICC download request")
	}
	return nil
}

func (response EUICCDownloadResponse) ValidateFor(request EUICCDownloadRequest) error {
	if response.OperationID != request.OperationID || response.SessionGeneration != request.SessionGeneration ||
		response.EID != request.EID || response.Action != request.Action {
		return errors.New("eUICC download response identity does not match request")
	}
	if response.Failure != nil {
		if response.Failure.Validate() != nil || response.Job != nil {
			return errors.New("invalid failed eUICC download response")
		}
		return nil
	}
	if response.Job == nil || response.Job.Validate() != nil {
		return errors.New("invalid eUICC download job response")
	}
	return nil
}

func (command EUICCDiscoveryCommand) Validate() error {
	if !validIdentifier(command.OperationID) || !validEID(command.EID) || !validSMDSAddress(command.SMDS) ||
		(command.IMEI != "" && (len(command.IMEI) != 15 || !validCardID(command.IMEI))) {
		return errors.New("invalid eUICC discovery command")
	}
	return nil
}

func (command EUICCDiscoveryCommand) requestFor(sessionGeneration string) EUICCDiscoveryRequest {
	return EUICCDiscoveryRequest{
		OperationID: command.OperationID, SessionGeneration: sessionGeneration,
		EID: command.EID, SMDS: command.SMDS, IMEI: command.IMEI,
	}
}

func (request EUICCDiscoveryRequest) Validate() error {
	command := EUICCDiscoveryCommand{
		OperationID: request.OperationID, EID: request.EID, SMDS: request.SMDS, IMEI: request.IMEI,
	}
	if !validIdentifier(request.SessionGeneration) || command.Validate() != nil {
		return errors.New("invalid eUICC discovery request")
	}
	return nil
}

func (response EUICCDiscoveryResponse) ValidateFor(request EUICCDiscoveryRequest) error {
	if response.OperationID != request.OperationID || response.SessionGeneration != request.SessionGeneration ||
		response.EID != request.EID {
		return errors.New("eUICC discovery response identity does not match request")
	}
	if response.Failure != nil {
		if response.Failure.Validate() != nil || response.SMDS != "" || len(response.Entries) != 0 {
			return errors.New("invalid failed eUICC discovery response")
		}
		return nil
	}
	if response.SMDS == "" || !validSMDSAddress(response.SMDS) || len(response.Entries) > 64 {
		return errors.New("invalid eUICC discovery response")
	}
	for _, entry := range response.Entries {
		if entry.EventID == "" || !validSecretText(entry.EventID, 512) ||
			entry.RSPServerAddress == "" || !validSMDSAddress(entry.RSPServerAddress) {
			return errors.New("invalid eUICC discovery entry")
		}
	}
	return nil
}

func (command EUICCNotificationCommand) Validate() error {
	if !validIdentifier(command.OperationID) || !validEID(command.EID) {
		return errors.New("invalid eUICC notification command")
	}
	if command.Action == "" {
		if command.Expected != nil {
			return errors.New("eUICC notification inventory contains delivery fields")
		}
		return nil
	}
	if (command.Action != EUICCNotificationDeliver && command.Action != EUICCNotificationRemove) ||
		command.Expected == nil || command.Expected.Validate() != nil {
		return errors.New("invalid eUICC notification command")
	}
	return nil
}

func (command EUICCNotificationCommand) requestFor(sessionGeneration string) EUICCNotificationRequest {
	return EUICCNotificationRequest{
		OperationID: command.OperationID, SessionGeneration: sessionGeneration, EID: command.EID,
		Action: command.Action, Expected: cloneEUICCNotificationEntry(command.Expected),
	}
}

func (request EUICCNotificationRequest) Validate() error {
	command := EUICCNotificationCommand{
		OperationID: request.OperationID, EID: request.EID, Action: request.Action,
		Expected: cloneEUICCNotificationEntry(request.Expected),
	}
	if !validIdentifier(request.SessionGeneration) || command.Validate() != nil {
		return errors.New("invalid eUICC notification request")
	}
	return nil
}

func (response EUICCNotificationResponse) ValidateFor(request EUICCNotificationRequest) error {
	if response.OperationID != request.OperationID || response.SessionGeneration != request.SessionGeneration ||
		response.EID != request.EID {
		return errors.New("eUICC notification response identity does not match request")
	}
	if response.Failure != nil {
		invalidOutcome := response.Removed ||
			request.Action == EUICCNotificationRemove && response.Acknowledged ||
			request.Action == "" && (response.Acknowledged || response.Removed)
		if response.Failure.Validate() != nil || len(response.Entries) != 0 || invalidOutcome {
			return errors.New("invalid failed eUICC notification response")
		}
		return nil
	}
	if request.Action == EUICCNotificationDeliver {
		if len(response.Entries) != 0 || !response.Acknowledged || !response.Removed {
			return errors.New("invalid successful eUICC notification delivery response")
		}
		return nil
	}
	if request.Action == EUICCNotificationRemove {
		if len(response.Entries) != 0 || response.Acknowledged || !response.Removed {
			return errors.New("invalid successful eUICC notification removal response")
		}
		return nil
	}
	if response.Acknowledged || response.Removed {
		return errors.New("eUICC notification inventory contains delivery outcome")
	}
	if len(response.Entries) > 128 {
		return errors.New("invalid eUICC notification response")
	}
	seen := make(map[int64]struct{}, len(response.Entries))
	for _, entry := range response.Entries {
		if entry.Validate() != nil {
			return errors.New("invalid eUICC notification entry")
		}
		if _, duplicate := seen[entry.SequenceNumber]; duplicate {
			return errors.New("duplicate eUICC notification sequence number")
		}
		seen[entry.SequenceNumber] = struct{}{}
	}
	return nil
}

func cloneEUICCNotificationEntry(source *EUICCNotificationEntry) *EUICCNotificationEntry {
	if source == nil {
		return nil
	}
	copy := *source
	return &copy
}

func (job EUICCDownloadJob) Validate() error {
	if !validEUICCDownloadState(job.State) || !validEUICCDownloadStage(job.Stage) ||
		job.StartedAt.IsZero() || job.UpdatedAt.Before(job.StartedAt) ||
		(job.Code != "" && !validIdentifier(job.Code)) {
		return errors.New("invalid eUICC download job state")
	}
	switch job.State {
	case EUICCDownloadQueued:
		if job.Stage != EUICCDownloadStageQueued || job.Code != "" {
			return errors.New("queued eUICC download has inconsistent stage or code")
		}
	case EUICCDownloadRunning:
		if job.Stage != EUICCDownloadStageAuthenticateClient && job.Stage != EUICCDownloadStageAuthenticateServer &&
			job.Stage != EUICCDownloadStageInstall || job.Code != "" {
			return errors.New("running eUICC download has inconsistent stage or code")
		}
	case EUICCDownloadCancelling:
		if job.Stage == EUICCDownloadStageCompleted || job.Code != "cancel_requested" {
			return errors.New("cancelling eUICC download has inconsistent stage or code")
		}
	case EUICCDownloadCompleted:
		if job.Stage != EUICCDownloadStageCompleted || job.Code != "" {
			return errors.New("completed eUICC download has inconsistent stage or code")
		}
	case EUICCDownloadFailed, EUICCDownloadCanceled:
		if job.Stage == EUICCDownloadStageCompleted || job.Code == "" {
			return errors.New("terminal eUICC download has inconsistent stage or code")
		}
	case EUICCDownloadUncertain:
		if job.Code == "" {
			return errors.New("uncertain eUICC download is missing its reason")
		}
	}
	if job.Metadata != nil && ((!validCardID(job.Metadata.ICCID) && job.Metadata.ICCID != "") ||
		!validDisplayText(job.Metadata.ServiceProviderName, 256) || !validDisplayText(job.Metadata.ProfileName, 256)) {
		return errors.New("invalid eUICC download metadata")
	}
	return nil
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

func (command ModemDataCommand) Validate() error {
	if !validIdentifier(command.OperationID) || !validEquipmentID(command.EquipmentID) ||
		!validCardID(command.CardID) || !validIdentifier(command.SessionID) ||
		!validModemDataAction(command.Action) {
		return errors.New("invalid modem data command")
	}
	return validateModemDataFields(command.Action, command.StreamID, command.StreamToken,
		command.Network, command.Address, command.Profile, command.Purpose, command.ExpiresAt, command.MaxBytes)
}

func (command ModemDataCommand) requestFor(target ModemTarget) ModemDataRequest {
	return ModemDataRequest{
		OperationID: command.OperationID, AttachmentID: target.AttachmentID,
		EquipmentID: command.EquipmentID, CardID: command.CardID, Action: command.Action,
		SIMSessionGeneration: target.SIMSessionGeneration,
		SessionID:            command.SessionID, StreamID: command.StreamID, StreamToken: command.StreamToken,
		Network: command.Network, Address: command.Address, Profile: command.Profile, Purpose: command.Purpose,
		ExpiresAt: command.ExpiresAt, MaxBytes: command.MaxBytes,
	}
}

func (request ModemDataRequest) Validate() error {
	command := ModemDataCommand{
		OperationID: request.OperationID, EquipmentID: request.EquipmentID, CardID: request.CardID,
		Action: request.Action, SessionID: request.SessionID, StreamID: request.StreamID,
		StreamToken: request.StreamToken, Network: request.Network, Address: request.Address,
		Profile: request.Profile, Purpose: request.Purpose, ExpiresAt: request.ExpiresAt, MaxBytes: request.MaxBytes,
	}
	if !validIdentifier(request.AttachmentID) ||
		(request.SIMSessionGeneration != "" && !validIdentifier(request.SIMSessionGeneration)) || command.Validate() != nil {
		return errors.New("invalid modem data request")
	}
	return nil
}

func (response ModemDataResponse) ValidateFor(request ModemDataRequest) error {
	if response.OperationID != request.OperationID || response.AttachmentID != request.AttachmentID ||
		response.EquipmentID != request.EquipmentID || response.CardID != request.CardID ||
		response.SIMSessionGeneration != request.SIMSessionGeneration ||
		response.SessionID != request.SessionID || response.StreamID != request.StreamID {
		return errors.New("modem data response identity does not match request")
	}
	if response.Failure != nil {
		if response.Failure.Validate() != nil || response.State != "" || response.Profile != "" || response.ExpiresAt != nil {
			return errors.New("invalid failed modem data response")
		}
		return nil
	}
	want := "ready"
	if request.Action == ModemDataOpen {
		want = "open"
	} else if request.Action == ModemDataStop {
		want = "stopped"
	}
	if response.State != want || request.Action != ModemDataPrepare && response.Profile != "" ||
		request.Action == ModemDataPrepare && strings.TrimSpace(response.Profile) == "" ||
		(request.Action == ModemDataRenew || request.Action == ModemDataPrepare && request.Purpose != "") &&
			(response.ExpiresAt == nil || !response.ExpiresAt.Equal(request.ExpiresAt)) ||
		request.Action != ModemDataRenew && !(request.Action == ModemDataPrepare && request.Purpose != "") && response.ExpiresAt != nil {
		return errors.New("invalid successful modem data response")
	}
	return nil
}

func (command ModemPolicyCommand) Validate() error {
	if !validIdentifier(command.OperationID) || !validEquipmentID(command.EquipmentID) ||
		!validCardID(command.CardID) || !validModemPolicyAction(command.Action) {
		return errors.New("invalid modem policy command")
	}
	return validateModemPolicyFields(command.Action, command.Patch, command.Profile)
}

func (command ModemPolicyCommand) requestFor(target ModemTarget, session string) ModemPolicyRequest {
	return ModemPolicyRequest{OperationID: command.OperationID, AttachmentID: target.AttachmentID,
		EquipmentID: command.EquipmentID, CardID: command.CardID, SIMSessionGeneration: session,
		Action: command.Action, ExpectedRevision: command.ExpectedRevision, Patch: command.Patch, Profile: command.Profile}
}

func (request ModemPolicyRequest) Validate() error {
	command := ModemPolicyCommand{OperationID: request.OperationID, EquipmentID: request.EquipmentID,
		CardID: request.CardID, Action: request.Action, ExpectedRevision: request.ExpectedRevision,
		Patch: request.Patch, Profile: request.Profile}
	if !validIdentifier(request.AttachmentID) || !validIdentifier(request.SIMSessionGeneration) || command.Validate() != nil {
		return errors.New("invalid modem policy request")
	}
	return nil
}

func (response ModemPolicyResponse) ValidateFor(request ModemPolicyRequest) error {
	if response.OperationID != request.OperationID || response.AttachmentID != request.AttachmentID ||
		response.EquipmentID != request.EquipmentID || response.CardID != request.CardID ||
		response.SIMSessionGeneration != request.SIMSessionGeneration {
		return errors.New("modem policy response identity does not match request")
	}
	if response.Failure != nil {
		if response.Failure.Validate() != nil || response.Policy != nil || response.Profiles != nil || response.SIMAPDUReady != nil {
			return errors.New("invalid failed modem policy response")
		}
		return nil
	}
	if response.Policy == nil || response.Policy.Validate() != nil || response.Policy.EquipmentID != request.EquipmentID ||
		response.Policy.CardID != request.CardID {
		return errors.New("invalid successful modem policy response")
	}
	needsProfiles := request.Action == ModemPolicyProfiles || request.Action == ModemPolicyProfileSave
	if needsProfiles != (response.Profiles != nil) {
		return errors.New("modem policy response profile inventory is inconsistent")
	}
	for _, profile := range response.Profiles {
		if profile.Validate() != nil {
			return errors.New("invalid modem profile view")
		}
	}
	needsSIMAPDU := request.Action == ModemPolicyPrepareSIMAPDU
	if needsSIMAPDU != (response.SIMAPDUReady != nil) || response.SIMAPDUReady != nil && !*response.SIMAPDUReady {
		return errors.New("modem policy response SIM APDU readiness is inconsistent")
	}
	return nil
}

func (fact ModemPolicyFact) Validate() error {
	if fact.SchemaVersion != 1 || !validEquipmentID(fact.EquipmentID) || !validCardID(fact.CardID) ||
		!oneOf(fact.State, "ready", "recovering", "error") ||
		!oneOf(fact.ProfileMode, "agent", "system", "system_managed") || len(fact.Code) > 128 ||
		!validSecretText(fact.Desired.SelectedProfile, 100) ||
		(fact.Persisted != (fact.Revision > 0)) {
		return errors.New("invalid modem policy fact")
	}
	if fact.DataLease != nil {
		if !validIdentifier(fact.DataLease.SessionID) || !validIdentifier(fact.DataLease.Purpose) ||
			!oneOf(fact.DataLease.State, "preparing", "active", "cleanup") {
			return errors.New("invalid modem policy data lease")
		}
	}
	return nil
}

func (profile ModemProfileView) Validate() error {
	if strings.TrimSpace(profile.Name) == "" || len(profile.Name) > 100 || len(profile.APN) > 100 ||
		!oneOf(strings.ToUpper(strings.TrimSpace(profile.Auth)), "", "NONE", "PAP", "CHAP", "MSCHAPV2") ||
		!oneOf(profile.Source, "", "saved", "system", "modem", "network", "provider") ||
		len(profile.PDPType) > 16 || !validSecretText(profile.PDPType, 16) ||
		len(profile.Username) > 200 || !validSecretText(profile.Name, 100) || !validSecretText(profile.APN, 100) ||
		!validSecretText(profile.Username, 200) {
		return errors.New("invalid modem profile view")
	}
	return nil
}

func validModemPolicyAction(action ModemPolicyAction) bool {
	return action == ModemPolicyRead || action == ModemPolicySet || action == ModemPolicyProfiles ||
		action == ModemPolicyProfileSave || action == ModemPolicyPrepareSIMAPDU
}

func validateModemPolicyFields(action ModemPolicyAction, patch ModemPolicyPatch, profile ModemProfileInput) error {
	patchFields := 0
	for _, field := range []*bool{patch.CellularEnabled, patch.ConnectionEnabled, patch.FlightMode, patch.RoamingEnabled} {
		if field != nil {
			patchFields++
		}
	}
	if patch.SelectedProfile != nil {
		patchFields++
	}
	profilePresent := profile.Name != "" || profile.APN != "" || profile.Auth != "" || profile.Username != "" ||
		profile.Password != "" || profile.PasswordSet
	switch action {
	case ModemPolicySet:
		if patchFields == 0 || profilePresent {
			return errors.New("policy set requires only a nonempty patch")
		}
	case ModemPolicyProfileSave:
		if patchFields != 0 || validateProfileFields(profile.Name, profile.APN, profile.Auth,
			profile.Username, profile.Password, profile.PasswordSet) != nil {
			return errors.New("invalid modem profile input")
		}
	case ModemPolicyRead, ModemPolicyProfiles, ModemPolicyPrepareSIMAPDU:
		if patchFields != 0 || profilePresent {
			return errors.New("policy read does not accept mutation fields")
		}
	default:
		return errors.New("unsupported modem policy action")
	}
	return nil
}

func validateProfileFields(name, apn, auth, username, password string, passwordSet bool) error {
	name, apn, auth = strings.TrimSpace(name), strings.TrimSpace(apn), strings.ToUpper(strings.TrimSpace(auth))
	if name == "" || len(name) > 100 || apn == "" || len(apn) > 100 ||
		!oneOf(auth, "NONE", "PAP", "CHAP", "MSCHAPV2") || len(username) > 200 || len(password) > 500 ||
		!validSecretText(name, 100) || !validSecretText(apn, 100) || !validSecretText(username, 200) || !validSecretText(password, 500) ||
		!passwordSet && password != "" {
		return errors.New("invalid modem profile fields")
	}
	return nil
}

func (request RawUSBRequest) Validate() error {
	for _, value := range []string{
		request.OperationID, request.SourceAgentID, request.SourceProcessGeneration,
		request.AttachmentID, request.SessionGeneration, request.USBSessionID, request.CaptureGeneration,
	} {
		if !validIdentifier(value) {
			return errors.New("invalid raw USB request identity")
		}
	}
	if !validEquipmentID(request.EquipmentID) || !validCardID(request.CardID) {
		return errors.New("invalid raw USB modem identity")
	}
	switch request.Action {
	case RawUSBExportStart:
		if request.Role != RawUSBExporter || request.Device != nil {
			return errors.New("invalid raw USB exporter request")
		}
	case RawUSBImportStart:
		if request.Role != RawUSBImporter || !validRawUSBDevice(request.Device) || request.Recovering {
			return errors.New("invalid raw USB importer request")
		}
	case RawUSBStop:
		if request.Role != RawUSBExporter && request.Role != RawUSBImporter || request.Device != nil {
			return errors.New("invalid raw USB stop request")
		}
	default:
		return errors.New("invalid raw USB action")
	}
	if request.Action == RawUSBStop {
		if request.StreamID != "" || request.StreamToken != "" {
			return errors.New("raw USB stop request contains stream credentials")
		}
		return nil
	}
	if !validIdentifier(request.StreamID) || len(request.StreamToken) < 32 || len(request.StreamToken) > 512 {
		return errors.New("invalid raw USB stream credentials")
	}
	return nil
}

func (response RawUSBResponse) ValidateFor(request RawUSBRequest) error {
	if response.OperationID != request.OperationID || response.Action != request.Action || response.Role != request.Role ||
		response.SourceAgentID != request.SourceAgentID || response.SourceProcessGeneration != request.SourceProcessGeneration ||
		response.AttachmentID != request.AttachmentID || response.SessionGeneration != request.SessionGeneration ||
		response.EquipmentID != request.EquipmentID || response.CardID != request.CardID ||
		response.USBSessionID != request.USBSessionID || response.CaptureGeneration != request.CaptureGeneration ||
		response.StreamID != request.StreamID {
		return errors.New("raw USB response identity does not match request")
	}
	if response.Failure != nil {
		if response.Failure.Validate() != nil || response.State != "" || response.Device != nil {
			return errors.New("invalid failed raw USB response")
		}
		return nil
	}
	if request.Action == RawUSBStop {
		if response.State != "stopped" || response.Device != nil {
			return errors.New("invalid successful raw USB stop response")
		}
		return nil
	}
	want := "prepared"
	if request.Action == RawUSBImportStart {
		want = "starting"
	}
	if response.State != want || !validRawUSBDevice(response.Device) {
		return errors.New("invalid successful raw USB start response")
	}
	if request.Action == RawUSBImportStart && *response.Device != *request.Device {
		return errors.New("raw USB importer response changed the exported device")
	}
	return nil
}

func validRawUSBDevice(device *RawUSBDevice) bool {
	if device == nil || device.VendorID == 0 || device.ProductID == 0 ||
		len(device.BusID) < 1 || len(device.BusID) > 128 ||
		len(device.Serial) > 256 || !utf8.ValidString(device.BusID) || !utf8.ValidString(device.Serial) {
		return false
	}
	for _, character := range device.BusID {
		if character <= 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func (command ModemCommand) Validate() error {
	if !validIdentifier(command.OperationID) || !validEquipmentID(command.EquipmentID) ||
		!validCardID(command.CardID) || !validModemAction(command.Action) {
		return errors.New("invalid modem command identity, target, or action")
	}
	if err := validateModemActionFields(command.Action, command.LeaseID, command.Number, command.Signal, command.Body); err != nil {
		return err
	}
	if err := validateIncomingActionFields(command.Action, command.IncomingEventID,
		command.SIMSessionGeneration, command.NativeCallIndex, command.CallOccurrence); err != nil {
		return err
	}
	return nil
}

func (command ModemCommand) requestFor(target ModemTarget) ModemRequest {
	session := command.SIMSessionGeneration
	if command.Action == ModemSMSList || command.Action == ModemSMSSend {
		session = target.SIMSessionGeneration
	}
	return ModemRequest{
		OperationID: command.OperationID, AttachmentID: target.AttachmentID,
		EquipmentID: command.EquipmentID, CardID: command.CardID, Action: command.Action,
		LeaseID: command.LeaseID, Number: command.Number, Signal: command.Signal, Body: command.Body,
		IncomingEventID: command.IncomingEventID, SIMSessionGeneration: session,
		NativeCallIndex: command.NativeCallIndex, CallOccurrence: command.CallOccurrence,
	}
}

func (request ModemRequest) Validate() error {
	if !validIdentifier(request.OperationID) || !validIdentifier(request.AttachmentID) ||
		!validEquipmentID(request.EquipmentID) || !validCardID(request.CardID) ||
		!validModemAction(request.Action) {
		return errors.New("invalid modem request identity, attachment, target, or action")
	}
	if err := validateModemActionFields(request.Action, request.LeaseID, request.Number, request.Signal, request.Body); err != nil {
		return err
	}
	if err := validateIncomingActionFields(request.Action, request.IncomingEventID,
		request.SIMSessionGeneration, request.NativeCallIndex, request.CallOccurrence); err != nil {
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
		if response.Failure.Validate() != nil || response.Call != nil || response.Lease != nil || response.SMS != nil {
			return errors.New("invalid failed modem response")
		}
		return nil
	}
	if request.Action == ModemCallRenew {
		if response.Call != nil || response.SMS != nil || response.Lease == nil || response.Lease.ValidateFor(request.LeaseID) != nil {
			return errors.New("invalid successful modem lease renewal")
		}
		return nil
	}
	if request.Action == ModemSMSList || request.Action == ModemSMSSend {
		if response.Call != nil || response.Lease != nil || response.SMS == nil || response.SMS.ValidateFor(request.Action) != nil {
			return errors.New("invalid successful modem SMS response")
		}
		return nil
	}
	if response.Call == nil || response.SMS != nil || response.Call.ValidateFor(request.Action) != nil {
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

func (result ModemSMSResult) ValidateFor(action ModemAction) error {
	if action == ModemSMSList {
		if result.State != "listed" || result.References != nil || result.Messages == nil || len(result.Messages) > 500 {
			return errors.New("invalid modem SMS list result")
		}
		for _, message := range result.Messages {
			if message.Index < 0 || !oneOf(message.State, "received", "stored", "delivery") ||
				!oneOf(message.Direction, "in", "out") || strings.TrimSpace(message.Peer) == "" ||
				len(message.Peer) > 64 || len(message.Body) > 16<<10 || message.ObservedAt.IsZero() ||
				message.Reference < 0 || message.Reference > 255 ||
				(message.State == "delivery") != oneOf(message.Delivery, "delivered", "temporary_failure", "permanent_failure", "unknown") ||
				!validHexDigest(message.Fingerprint) {
				return errors.New("invalid modem SMS message")
			}
		}
		return nil
	}
	if action != ModemSMSSend || result.State != "submitted" || result.Messages != nil ||
		len(result.References) < 1 || len(result.References) > 7 {
		return errors.New("invalid modem SMS submit result")
	}
	for _, reference := range result.References {
		if reference < 0 || reference > 255 {
			return errors.New("invalid modem SMS reference")
		}
	}
	return nil
}

func (result ModemCallResult) ValidateFor(action ModemAction) error {
	if !oneOf(result.State, "idle", "active", "held", "dialing", "ringing_out", "ringing_in", "waiting") ||
		!oneOf(result.Direction, "", "in", "out") || len(result.Number) > 64 ||
		result.ObservedAt.IsZero() || result.ObservedAt.After(time.Now().Add(time.Minute)) || !result.Authoritative {
		return errors.New("invalid modem call result")
	}
	if action == ModemCallHangup || action == ModemCallReject {
		if !result.TerminalConfirmed || result.State != "idle" ||
			(action == ModemCallHangup && !oneOf(result.Strategy, "already_idle", "chup", "chup_ath") ||
				action == ModemCallReject && result.Strategy != "incoming_chup") {
			return errors.New("modem hangup lacks terminal confirmation")
		}
	} else if result.TerminalConfirmed || result.Strategy != "" {
		return errors.New("modem status contains hangup state")
	}
	hasInventory := result.NativeCallIndex != 0 || result.VoiceCalls != 0 || result.IncomingCalls != 0
	if result.State == "idle" && (result.Direction != "" || result.Number != "") ||
		result.VoiceCalls < 0 || result.IncomingCalls < 0 || result.IncomingCalls > result.VoiceCalls ||
		(hasInventory && result.State == "idle") ||
		(hasInventory && result.State != "idle" && (result.NativeCallIndex < 1 || result.VoiceCalls < 1)) ||
		action == ModemCallAnswer && !hasInventory ||
		action == ModemCallDial && result.State != "idle" && result.Direction != "out" ||
		action == ModemCallAnswer && result.State != "idle" && result.Direction != "in" {
		return errors.New("modem call result direction is inconsistent")
	}
	return nil
}

func validModemAction(value ModemAction) bool {
	return value == ModemCallStatus || value == ModemCallHangup || value == ModemCallDial ||
		value == ModemCallAnswer || value == ModemCallReject || value == ModemCallRenew || value == ModemCallDTMF ||
		value == ModemSMSList || value == ModemSMSSend
}

func validModemMediaAction(value ModemMediaAction) bool {
	return value == ModemMediaPrepare || value == ModemMediaStop
}

func validModemDataAction(value ModemDataAction) bool {
	return value == ModemDataPrepare || value == ModemDataRenew || value == ModemDataOpen || value == ModemDataStop
}

func validateModemDataFields(action ModemDataAction, streamID, streamToken, network, address, profile, purpose string,
	expiresAt time.Time, maxBytes uint64) error {
	if purpose != "" && !validIdentifier(purpose) {
		return errors.New("data session purpose is invalid")
	}
	if action == ModemDataStop {
		if streamID != "" || streamToken != "" || network != "" || address != "" || profile != "" ||
			!expiresAt.IsZero() || maxBytes != 0 {
			return errors.New("data stop accepts only a session identity")
		}
		return nil
	}
	if expiresAt.IsZero() || expiresAt.Before(time.Now().Add(-time.Minute)) ||
		expiresAt.After(time.Now().Add(25*time.Hour)) || maxBytes < 1024 || maxBytes > 1<<40 ||
		len(profile) > 256 || strings.ContainsAny(profile, "\r\n\x00") {
		return errors.New("invalid modem data lifetime, quota, or profile")
	}
	if action == ModemDataPrepare {
		if streamID != "" || streamToken != "" || network != "" || address != "" {
			return errors.New("data prepare contains stream fields")
		}
		return nil
	}
	if action == ModemDataRenew {
		if !validIdentifier(purpose) || streamID != "" || streamToken != "" || network != "" || address != "" || profile != "" {
			return errors.New("data renew contains stream or profile fields")
		}
		return nil
	}
	if !validIdentifier(streamID) || len(streamToken) < minimumTokenBytes || len(streamToken) > 512 ||
		!oneOf(network, "tcp", "udp") || profile != "" {
		return errors.New("invalid modem data stream")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" || len(host) > 253 || port == "" {
		return errors.New("invalid modem data destination")
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return errors.New("invalid modem data destination port")
	}
	return nil
}

func validateModemActionFields(action ModemAction, leaseID, number, signal, body string) error {
	switch action {
	case ModemCallStatus, ModemCallHangup:
		if leaseID != "" || number != "" || signal != "" || body != "" {
			return errors.New("status and hangup do not accept lease or number fields")
		}
	case ModemCallDial:
		if !validIdentifier(leaseID) || !validTelephone(number) || signal != "" || body != "" {
			return errors.New("dial requires a valid lease and telephone number")
		}
	case ModemCallAnswer:
		if !validIdentifier(leaseID) || number != "" && !validTelephone(number) || signal != "" || body != "" {
			return errors.New("answer requires a valid lease and optional expected peer")
		}
	case ModemCallReject:
		if leaseID != "" || number != "" && !validTelephone(number) || signal != "" || body != "" {
			return errors.New("reject accepts only an optional expected peer")
		}
	case ModemCallRenew:
		if !validIdentifier(leaseID) || number != "" || signal != "" || body != "" {
			return errors.New("renewal requires only a valid lease")
		}
	case ModemCallDTMF:
		if !validIdentifier(leaseID) || number != "" || !validDTMFSignal(signal) || body != "" {
			return errors.New("DTMF requires a valid call lease and one signal")
		}
	case ModemSMSList:
		if leaseID != "" || number != "" || signal != "" || body != "" {
			return errors.New("SMS list does not accept lease, number, or body fields")
		}
	case ModemSMSSend:
		if leaseID != "" || !validTelephone(number) || signal != "" || strings.TrimSpace(body) == "" || len(body) > 16<<10 {
			return errors.New("SMS send requires a valid number and bounded body")
		}
	}
	return nil
}

func validateIncomingActionFields(action ModemAction, eventID, session string, nativeIndex int, occurrence uint64) error {
	if action == ModemCallAnswer || action == ModemCallReject {
		if !validIdentifier(eventID) || !validIdentifier(session) || nativeIndex < 1 || nativeIndex > 255 || occurrence == 0 {
			return errors.New("incoming call action requires an exact event, session, index, and occurrence")
		}
		return nil
	}
	if action == ModemSMSList || action == ModemSMSSend {
		if eventID != "" || session != "" && !validIdentifier(session) || nativeIndex != 0 || occurrence != 0 {
			return errors.New("SMS action contains an invalid session fence")
		}
		return nil
	}
	if eventID != "" || session != "" || nativeIndex != 0 || occurrence != 0 {
		return errors.New("non-incoming modem action contains incoming-call fields")
	}
	return nil
}

func validDTMFSignal(value string) bool {
	return len(value) == 1 && strings.Contains("0123456789*#ABCD", strings.ToUpper(value))
}

func validHexDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
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

func (challenge AKAChallenge) requestForModem(sessionGeneration, attachmentID, equipmentID string) AKARequest {
	return AKARequest{
		OperationID: challenge.OperationID, SessionGeneration: sessionGeneration,
		CardID: challenge.CardID, DeviceKind: AKADeviceModem,
		AttachmentID: attachmentID, EquipmentID: equipmentID, Application: challenge.Application,
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
	if topology.Host != nil {
		if err := topology.Host.Validate(); err != nil {
			return err
		}
	}
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
		if err := validateReaderSIM(reader.SIM); err != nil {
			return err
		}
		if reader.EUICC != nil && len(reader.SecureElements) != 0 {
			return errors.New("Agent topology mixes legacy and multi-SE eUICC facts")
		}
		if err := validateEUICC(reader.EUICC); err != nil {
			return err
		}
		if len(reader.SecureElements) > 8 {
			return errors.New("Agent topology has too many secure elements")
		}
		previousSlot := ""
		eids := make(map[string]struct{}, len(reader.SecureElements))
		for slotIndex, slot := range reader.SecureElements {
			if !validIdentifier(slot.SlotID) || slotIndex > 0 && slot.SlotID <= previousSlot ||
				len(slot.Label) == 0 || len(slot.Label) > 64 || !utf8.ValidString(slot.Label) {
				return errors.New("Agent topology contains invalid or unsorted secure elements")
			}
			if err := validateEUICC(&slot.EUICC); err != nil {
				return err
			}
			if _, duplicate := eids[slot.EUICC.EID]; duplicate {
				return errors.New("Agent topology contains duplicate secure-element EIDs")
			}
			eids[slot.EUICC.EID] = struct{}{}
			previousSlot = slot.SlotID
		}
		hasEUICC := reader.EUICC != nil || len(reader.SecureElements) != 0
		switch reader.IdentityState {
		case CardAbsent:
			if reader.CardPresent || reader.SessionGeneration != "" || reader.CardID != "" || reader.SIM != nil || hasEUICC || reader.ATRSHA256 != "" {
				return errors.New("absent topology attachment contains card state")
			}
		case CardIdentityDiscovering, CardIdentityUnavailable:
			if !reader.CardPresent || reader.SessionGeneration == "" || reader.CardID != "" || reader.SIM != nil || hasEUICC {
				return errors.New("unidentified topology card has inconsistent state")
			}
		case CardIdentified:
			if !reader.CardPresent || reader.SessionGeneration == "" || reader.CardID == "" && !hasEUICC {
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
	if len(topology.RawUSBRecoveries) > 64 || len(topology.RawUSBRecoveries) != 0 && !topology.RawUSBSource {
		return errors.New("Agent topology has invalid raw USB recovery reservations")
	}
	previousRecovery := ""
	for index, recovery := range topology.RawUSBRecoveries {
		key := recovery.EquipmentID + "/" + recovery.CardID
		if index > 0 && key <= previousRecovery || recovery.State != "capture_reserved" ||
			!validIdentifier(recovery.AttachmentID) || !validIdentifier(recovery.SessionGeneration) ||
			!validIdentifier(recovery.USBSessionID) || !validIdentifier(recovery.CaptureGeneration) ||
			!validEquipmentID(recovery.EquipmentID) || !validCardID(recovery.CardID) ||
			!validRawUSBDevice(&recovery.Device) {
			return errors.New("Agent topology contains invalid or unsorted raw USB recovery reservations")
		}
		previousRecovery = key
	}
	if len(topology.RawUSBSessions) > 64 {
		return errors.New("Agent topology has too many raw USB sessions")
	}
	previousRaw := ""
	for index, session := range topology.RawUSBSessions {
		key := string(session.Role) + "/" + session.USBSessionID
		if index > 0 && key <= previousRaw ||
			(session.Role != RawUSBExporter && session.Role != RawUSBImporter) || session.State != "transport_active" {
			return errors.New("Agent topology contains invalid or unsorted raw USB sessions")
		}
		for _, value := range []string{
			session.SourceAgentID, session.SourceProcessGeneration, session.AttachmentID,
			session.SessionGeneration, session.USBSessionID, session.CaptureGeneration,
		} {
			if !validIdentifier(value) {
				return errors.New("Agent topology contains an invalid raw USB session identity")
			}
		}
		if !validEquipmentID(session.EquipmentID) || !validCardID(session.CardID) {
			return errors.New("Agent topology contains an invalid raw USB modem identity")
		}
		if session.Role == RawUSBExporter && !topology.RawUSBSource || session.Role == RawUSBImporter && !topology.RawUSBImporter {
			return errors.New("Agent topology reports a raw USB session without the matching capability")
		}
		previousRaw = key
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
			len(modem.Network.OperatorName) > 256 || len(modem.Network.Profile) > 256 ||
			len(modem.Network.DataGuardDetail) > 1024 || len(modem.SIM.MSISDNs) > 16 {
			return errors.New("Agent topology contains an invalid modem fact")
		}
		previous = modem.AttachmentID
		if modem.Condition != "ready" && modem.Condition != "degraded" ||
			modem.Condition == "ready" && modem.Detail != "" || modem.Condition == "degraded" && modem.Detail == "" {
			return errors.New("Agent topology contains an inconsistent modem condition")
		}
		if !oneOf(modem.LastContinuityIssue, "", "isolation_check_failed", "sim_pin_state_failed",
			"sim_card_identity_failed", "modem_identity_probe_failed", "sim_event_source_failed",
			"sim_insertion_changed") {
			return errors.New("Agent topology contains an invalid modem continuity issue")
		}
		if modem.SIM.ICCID != "" && !validCardID(modem.SIM.ICCID) || modem.SIM.IMSI != "" && !validCardID(modem.SIM.IMSI) {
			return errors.New("Agent topology contains an invalid modem SIM identity")
		}
		if !oneOf(modem.SIM.PINState, "", "unknown", "not_required", "pin_required", "puk_required", "other_lock") ||
			!oneOf(modem.SIM.PINRecovery, "", "configured", "attempting", "blocked", "unlocked", "status_unavailable") ||
			modem.SIM.PINAttempts != nil && *modem.SIM.PINAttempts > 255 {
			return errors.New("Agent topology contains an invalid modem SIM PIN fact")
		}
		if modem.SIM.SessionGeneration != "" && (!validIdentifier(modem.SIM.SessionGeneration) ||
			modem.SIM.State != "ready" || modem.SIM.ICCID == "") ||
			(modem.AT.SIMAPDU || modem.AT.SIMAPDUOnDemand) &&
				(modem.SIM.SessionGeneration == "" || !validEquipmentID(modem.EquipmentID)) {
			return errors.New("Agent topology contains an invalid modem SIM generation")
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
			!oneOf(modem.Network.Data, "unknown", "disconnected", "connecting", "connected", "disconnecting") ||
			!oneOf(modem.Network.DataGuard, "", "unmanaged", "protected", "failed") ||
			(modem.Network.DataGuard == "failed") != (modem.Network.DataGuardDetail != "") {
			return errors.New("Agent topology contains an invalid modem machine state")
		}
		if err := validateModemAT(modem.AT); err != nil {
			return err
		}
		if modem.Policy != nil && (modem.Policy.Validate() != nil || modem.Policy.EquipmentID != modem.EquipmentID ||
			modem.Policy.CardID != modem.SIM.ICCID) {
			return errors.New("Agent topology contains an invalid modem policy fact")
		}
	}
	return nil
}

func validateModemAT(value ModemATControlFact) error {
	if len(value.Port) > 64 || len(value.Detail) > 1024 ||
		!oneOf(value.State, "", "unknown", "ready", "busy", "unavailable", "degraded") {
		return errors.New("Agent topology contains an invalid modem AT control fact")
	}
	capable := value.CallSignalling || value.SMS || value.SIMAPDU || value.SIMAPDUOnDemand
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
	if euicc.Download != nil && (!validIdentifier(euicc.Download.OperationID) || euicc.Download.Job.Validate() != nil) {
		return errors.New("Agent topology contains an invalid eUICC download fact")
	}
	previous := ""
	for index, profile := range euicc.Profiles {
		if !validCardID(profile.ICCID) || index > 0 && profile.ICCID <= previous ||
			profile.State != EUICCProfileEnabled && profile.State != EUICCProfileDisabled ||
			len(profile.Nickname) > 256 || len(profile.ServiceProviderName) > 256 || len(profile.ProfileName) > 256 ||
			!utf8.ValidString(profile.Nickname) || !utf8.ValidString(profile.ServiceProviderName) || !utf8.ValidString(profile.ProfileName) {
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
		if err := report.Topology.Validate(); err != nil {
			return errors.New("Agent health topology is invalid")
		}
	}
	return nil
}

func NormalizeTopology(topology TopologySnapshot) TopologySnapshot {
	result := TopologySnapshot{
		Host:            topology.Host,
		ReaderCondition: topology.ReaderCondition, ReaderDetail: topology.ReaderDetail,
		Readers:        make([]ReaderFact, len(topology.Readers)),
		ModemCondition: topology.ModemCondition, ModemDetail: topology.ModemDetail,
		Modems:       make([]ModemFact, len(topology.Modems)),
		RawUSBSource: topology.RawUSBSource, RawUSBImporter: topology.RawUSBImporter,
		RawUSBRecoveries: make([]RawUSBRecoveryFact, len(topology.RawUSBRecoveries)),
		RawUSBSessions:   make([]RawUSBSessionFact, len(topology.RawUSBSessions)),
	}
	if topology.Host != nil {
		host := *topology.Host
		result.Host = &host
	}
	copy(result.Readers, topology.Readers)
	for index := range result.Readers {
		if topology.Readers[index].SIM != nil {
			sim := *topology.Readers[index].SIM
			result.Readers[index].SIM = &sim
		}
		result.Readers[index].EUICC = cloneEUICC(topology.Readers[index].EUICC)
		result.Readers[index].SecureElements = cloneEUICCSlots(topology.Readers[index].SecureElements)
	}
	copy(result.Modems, topology.Modems)
	copy(result.RawUSBRecoveries, topology.RawUSBRecoveries)
	copy(result.RawUSBSessions, topology.RawUSBSessions)
	for index := range result.Modems {
		result.Modems[index].SIM.MSISDNs = append([]string(nil), topology.Modems[index].SIM.MSISDNs...)
		if topology.Modems[index].Policy != nil {
			policy := *topology.Modems[index].Policy
			if policy.DataLease != nil {
				lease := *policy.DataLease
				policy.DataLease = &lease
			}
			result.Modems[index].Policy = &policy
		}
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
	sort.Slice(result.RawUSBSessions, func(left, right int) bool {
		leftKey := string(result.RawUSBSessions[left].Role) + "/" + result.RawUSBSessions[left].USBSessionID
		rightKey := string(result.RawUSBSessions[right].Role) + "/" + result.RawUSBSessions[right].USBSessionID
		return leftKey < rightKey
	})
	sort.Slice(result.RawUSBRecoveries, func(left, right int) bool {
		leftKey := result.RawUSBRecoveries[left].EquipmentID + "/" + result.RawUSBRecoveries[left].CardID
		rightKey := result.RawUSBRecoveries[right].EquipmentID + "/" + result.RawUSBRecoveries[right].CardID
		return leftKey < rightKey
	})
	return result
}

func validateReaderSIM(sim *ReaderSIMFact) error {
	if sim == nil {
		return nil
	}
	validDigits := func(value string, minimum, maximum int) bool {
		return len(value) >= minimum && len(value) <= maximum && validCardID(value)
	}
	validSMSC := sim.SMSC == ""
	if !validSMSC {
		value := sim.SMSC
		if value[0] == '+' {
			value = value[1:]
		}
		validSMSC = validDigits(value, 1, 32)
	}
	validIdentity := validDigits(sim.IMSI, 5, 15) && validDigits(sim.MCC, 3, 3) &&
		(sim.MNC == "" || validDigits(sim.MNC, 2, 3)) && strings.HasPrefix(sim.IMSI, sim.MCC+sim.MNC)
	if len(sim.ErrorCode) > 128 || !validSMSC {
		return errors.New("Agent topology contains an invalid reader SIM fact")
	}
	switch sim.IdentityState {
	case "ready":
		if !validIdentity || sim.MNC == "" || sim.ErrorCode != "" {
			return errors.New("Agent topology contains an inconsistent ready reader SIM fact")
		}
	case "partial":
		if !validIdentity || sim.MNC != "" || sim.ErrorCode == "" {
			return errors.New("Agent topology contains an inconsistent partial reader SIM fact")
		}
	case "pin_required", "unavailable":
		if sim.IMSI != "" || sim.MCC != "" || sim.MNC != "" || sim.SMSC != "" || sim.ErrorCode == "" {
			return errors.New("Agent topology contains an inconsistent unavailable reader SIM fact")
		}
	default:
		return errors.New("Agent topology contains an unknown reader SIM identity state")
	}
	return nil
}

func (fact AgentHostFact) Validate() error {
	if fact.SchemaVersion != 1 || !oneOf(fact.Platform, "windows", "macos", "linux") ||
		!oneOf(fact.HostMode, "service", "gui", "cli") ||
		!oneOf(fact.Manager, "scm", "systemd", "gui", "cli") ||
		!oneOf(fact.SessionScope, "machine", "user") || fact.ConfigState != "ok" ||
		!fact.TokenConfigured || !validSecretText(fact.Architecture, 40) || fact.Architecture == "" ||
		!validSecretText(fact.BuildVersion, 128) || fact.BuildVersion == "" {
		return errors.New("Agent topology contains invalid host health metadata")
	}
	if fact.HostMode == "service" != (fact.SessionScope == "machine") ||
		fact.HostMode == "service" && !oneOf(fact.Manager, "scm", "systemd") ||
		fact.HostMode == "gui" && fact.Manager != "gui" || fact.HostMode == "cli" && fact.Manager != "cli" ||
		fact.Platform == "windows" && fact.HostMode == "service" && fact.Manager != "scm" ||
		fact.Platform == "linux" && fact.HostMode == "service" && fact.Manager != "systemd" ||
		fact.Manager == "scm" && fact.Platform != "windows" || fact.Manager == "systemd" && fact.Platform != "linux" {
		return errors.New("Agent topology host manager is inconsistent")
	}
	storage := fact.Storage
	if !oneOf(storage.State, "ok", "warning", "critical", "unknown") || storage.UsedPercent > 100 || len(storage.ErrorCode) > 128 {
		return errors.New("Agent topology contains invalid storage health")
	}
	if storage.State == "unknown" {
		if storage.TotalBytes != 0 || storage.FreeBytes != 0 || storage.UsedPercent != 0 || storage.ErrorCode == "" {
			return errors.New("unknown Agent storage contains fabricated values")
		}
	} else if storage.TotalBytes == 0 || storage.FreeBytes > storage.TotalBytes || storage.ErrorCode != "" {
		return errors.New("known Agent storage is incomplete")
	}
	return nil
}

func cloneEUICCSlots(source []EUICCSlotFact) []EUICCSlotFact {
	if source == nil {
		return nil
	}
	result := make([]EUICCSlotFact, len(source))
	for index := range source {
		result[index] = EUICCSlotFact{SlotID: source[index].SlotID, Label: source[index].Label,
			EUICC: *cloneEUICC(&source[index].EUICC)}
	}
	return result
}

// ReaderEUICCs normalizes legacy single-eUICC and multi-SE topology without
// inventing a secure-element identity for older Agents.
func ReaderEUICCs(reader ReaderFact) []EUICCSlotFact {
	if len(reader.SecureElements) != 0 {
		return cloneEUICCSlots(reader.SecureElements)
	}
	if reader.EUICC == nil {
		return nil
	}
	return []EUICCSlotFact{{EUICC: *cloneEUICC(reader.EUICC)}}
}

func cloneEUICC(source *EUICCFact) *EUICCFact {
	if source == nil {
		return nil
	}
	profiles := make([]EUICCProfileFact, len(source.Profiles))
	copy(profiles, source.Profiles)
	return &EUICCFact{
		EID: source.EID, ProfilesAvailable: source.ProfilesAvailable, ProfileManagement: source.ProfileManagement,
		ProfileDownload: source.ProfileDownload, ProfileDiscovery: source.ProfileDiscovery,
		NotificationInventory: source.NotificationInventory, NotificationDelivery: source.NotificationDelivery,
		NotificationRemoval: source.NotificationRemoval,
		Download:            cloneEUICCDownloadFact(source.Download), Profiles: profiles,
	}
}

func cloneEUICCDownloadFact(source *EUICCDownloadFact) *EUICCDownloadFact {
	if source == nil {
		return nil
	}
	copy := *source
	copy.Job = cloneEUICCDownloadJob(source.Job)
	return &copy
}

func cloneEUICCDownloadJob(source EUICCDownloadJob) EUICCDownloadJob {
	copy := source
	if source.Metadata != nil {
		metadata := *source.Metadata
		copy.Metadata = &metadata
	}
	return copy
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
	kind := request.DeviceKind
	if kind == "" {
		kind = AKADeviceReader
	}
	switch kind {
	case AKADeviceReader:
		if request.AttachmentID != "" || request.EquipmentID != "" {
			return errors.New("reader AKA request contains modem target fields")
		}
	case AKADeviceModem:
		if !validIdentifier(request.AttachmentID) || !validEquipmentID(request.EquipmentID) {
			return errors.New("modem AKA request lacks an exact attachment target")
		}
	default:
		return errors.New("AKA request has an unknown device kind")
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

func validEID(value string) bool {
	return len(value) == 32 && validCardID(value)
}

func validEUICCProfileAction(action EUICCProfileAction) bool {
	return action == EUICCProfileEnable || action == EUICCProfileDisable || action == EUICCProfileNickname
}

func validProfileNickname(value string) bool {
	return len(value) <= 64 && utf8.ValidString(value)
}

func validEUICCDownloadAction(action EUICCDownloadAction) bool {
	return action == EUICCDownloadStart || action == EUICCDownloadStatus || action == EUICCDownloadCancel
}

func validEUICCDownloadState(state EUICCDownloadState) bool {
	switch state {
	case EUICCDownloadQueued, EUICCDownloadRunning, EUICCDownloadCancelling, EUICCDownloadCompleted,
		EUICCDownloadFailed, EUICCDownloadCanceled, EUICCDownloadUncertain:
		return true
	default:
		return false
	}
}

func validEUICCDownloadStage(stage EUICCDownloadStage) bool {
	switch stage {
	case EUICCDownloadStageQueued, EUICCDownloadStageAuthenticateClient,
		EUICCDownloadStageAuthenticateServer, EUICCDownloadStageInstall, EUICCDownloadStageCompleted:
		return true
	default:
		return false
	}
}

func validActivationCode(value string) bool {
	return len(value) >= len("LPA:1$a$b") && len(value) <= 2048 && strings.HasPrefix(value, "LPA:1$") &&
		strings.Count(value, "$") >= 2 && validSecretText(value, 2048)
}

func validSMDSAddress(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 512 || !validSecretText(value, 512) {
		return false
	}
	raw := value
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil &&
		parsed.Opaque == "" && parsed.RawQuery == "" && parsed.Fragment == "" &&
		(parsed.Path == "" || parsed.Path == "/")
}

func validSecretText(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validDisplayText(value string, maximum int) bool {
	return len(value) <= maximum && utf8.ValidString(value)
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
