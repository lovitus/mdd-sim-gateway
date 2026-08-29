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
	EID               string             `json:"eid"`
	ProfilesAvailable bool               `json:"profiles_available"`
	ProfileManagement bool               `json:"profile_management,omitempty"`
	ProfileDownload   bool               `json:"profile_download,omitempty"`
	Download          *EUICCDownloadFact `json:"download,omitempty"`
	Profiles          []EUICCProfileFact `json:"profiles"`
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
	SecureElements    []EUICCSlotFact   `json:"secure_elements,omitempty"`
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
	EUICCProfileEnable  EUICCProfileAction = "enable"
	EUICCProfileDisable EUICCProfileAction = "disable"
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
	OperationID   string             `json:"operation_id"`
	EID           string             `json:"eid"`
	ICCID         string             `json:"iccid"`
	Action        EUICCProfileAction `json:"action"`
	ExpectedState EUICCProfileState  `json:"expected_state"`
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

type ModemAction string

const (
	ModemCallStatus ModemAction = "call_status"
	ModemCallHangup ModemAction = "call_hangup"
	ModemCallDial   ModemAction = "call_dial"
	ModemCallAnswer ModemAction = "call_answer"
	ModemCallRenew  ModemAction = "call_renew"
	ModemSMSList    ModemAction = "sms_list"
	ModemSMSSend    ModemAction = "sms_send"
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
	Body        string      `json:"body,omitempty"`
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
	Body         string      `json:"body,omitempty"`
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

type ModemMediaExecutor interface {
	ExecuteModemMedia(context.Context, ModemMediaRequest) ModemMediaResponse
}

type EUICCProfileExecutor interface {
	ExecuteEUICCProfile(context.Context, EUICCProfileRequest) EUICCProfileResponse
}

type EUICCDownloadExecutor interface {
	ExecuteEUICCDownload(context.Context, EUICCDownloadRequest) EUICCDownloadResponse
}

func (command EUICCProfileCommand) Validate() error {
	if !validIdentifier(command.OperationID) || !validEID(command.EID) ||
		!validCardID(command.ICCID) || !validEUICCProfileAction(command.Action) {
		return errors.New("invalid eUICC profile command identity or action")
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
		ExpectedState: command.ExpectedState,
	}
}

func (request EUICCProfileRequest) Validate() error {
	command := EUICCProfileCommand{
		OperationID: request.OperationID, EID: request.EID, ICCID: request.ICCID,
		Action: request.Action, ExpectedState: request.ExpectedState,
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
		if response.Failure.Validate() != nil || response.Outcome != "" || response.State != "" || response.Changed {
			return errors.New("invalid failed eUICC profile response")
		}
		return nil
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

func (command ModemCommand) Validate() error {
	if !validIdentifier(command.OperationID) || !validEquipmentID(command.EquipmentID) ||
		!validCardID(command.CardID) || !validModemAction(command.Action) {
		return errors.New("invalid modem command identity, target, or action")
	}
	if err := validateModemActionFields(command.Action, command.LeaseID, command.Number, command.Body); err != nil {
		return err
	}
	return nil
}

func (command ModemCommand) requestFor(attachmentID string) ModemRequest {
	return ModemRequest{
		OperationID: command.OperationID, AttachmentID: attachmentID,
		EquipmentID: command.EquipmentID, CardID: command.CardID, Action: command.Action,
		LeaseID: command.LeaseID, Number: command.Number, Body: command.Body,
	}
}

func (request ModemRequest) Validate() error {
	if !validIdentifier(request.OperationID) || !validIdentifier(request.AttachmentID) ||
		!validEquipmentID(request.EquipmentID) || !validCardID(request.CardID) ||
		!validModemAction(request.Action) {
		return errors.New("invalid modem request identity, attachment, target, or action")
	}
	if err := validateModemActionFields(request.Action, request.LeaseID, request.Number, request.Body); err != nil {
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
		value == ModemCallAnswer || value == ModemCallRenew || value == ModemSMSList || value == ModemSMSSend
}

func validModemMediaAction(value ModemMediaAction) bool {
	return value == ModemMediaPrepare || value == ModemMediaStop
}

func validateModemActionFields(action ModemAction, leaseID, number, body string) error {
	switch action {
	case ModemCallStatus, ModemCallHangup:
		if leaseID != "" || number != "" || body != "" {
			return errors.New("status and hangup do not accept lease or number fields")
		}
	case ModemCallDial:
		if !validIdentifier(leaseID) || !validTelephone(number) || body != "" {
			return errors.New("dial requires a valid lease and telephone number")
		}
	case ModemCallAnswer, ModemCallRenew:
		if !validIdentifier(leaseID) || number != "" || body != "" {
			return errors.New("answer and renewal require only a valid lease")
		}
	case ModemSMSList:
		if leaseID != "" || number != "" || body != "" {
			return errors.New("SMS list does not accept lease, number, or body fields")
		}
	case ModemSMSSend:
		if leaseID != "" || !validTelephone(number) || strings.TrimSpace(body) == "" || len(body) > 16<<10 {
			return errors.New("SMS send requires a valid number and bounded body")
		}
	}
	return nil
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
			if reader.CardPresent || reader.SessionGeneration != "" || reader.CardID != "" || hasEUICC || reader.ATRSHA256 != "" {
				return errors.New("absent topology attachment contains card state")
			}
		case CardIdentityDiscovering, CardIdentityUnavailable:
			if !reader.CardPresent || reader.SessionGeneration == "" || reader.CardID != "" || hasEUICC {
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
		if !oneOf(modem.SIM.PINState, "", "unknown", "not_required", "pin_required", "puk_required", "other_lock") ||
			!oneOf(modem.SIM.PINRecovery, "", "configured", "attempting", "blocked", "unlocked", "status_unavailable") ||
			modem.SIM.PINAttempts != nil && *modem.SIM.PINAttempts > 255 {
			return errors.New("Agent topology contains an invalid modem SIM PIN fact")
		}
		if modem.SIM.SessionGeneration != "" && (!validIdentifier(modem.SIM.SessionGeneration) ||
			modem.SIM.State != "ready" || modem.SIM.ICCID == "") ||
			modem.AT.SIMAPDU && modem.SIM.SessionGeneration == "" {
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
		ReaderCondition: topology.ReaderCondition, ReaderDetail: topology.ReaderDetail,
		Readers:        make([]ReaderFact, len(topology.Readers)),
		ModemCondition: topology.ModemCondition, ModemDetail: topology.ModemDetail,
		Modems: make([]ModemFact, len(topology.Modems)),
	}
	copy(result.Readers, topology.Readers)
	for index := range result.Readers {
		result.Readers[index].EUICC = cloneEUICC(topology.Readers[index].EUICC)
		result.Readers[index].SecureElements = cloneEUICCSlots(topology.Readers[index].SecureElements)
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
		ProfileDownload: source.ProfileDownload,
		Download:        cloneEUICCDownloadFact(source.Download), Profiles: profiles,
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
	return action == EUICCProfileEnable || action == EUICCProfileDisable
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
