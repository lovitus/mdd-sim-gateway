// Package agentmodem owns platform-neutral modem observations. Platform
// adapters report facts here; transport mapping remains in agenthost.
package agentmodem

import "context"

type Condition string

const (
	ConditionDisabled   Condition = "disabled"
	ConditionStarting   Condition = "starting"
	ConditionReady      Condition = "ready"
	ConditionRecovering Condition = "recovering"
)

type DeviceCondition string

const (
	DeviceReady    DeviceCondition = "ready"
	DeviceDegraded DeviceCondition = "degraded"
)

type SIMState string

const (
	SIMUnknown SIMState = "unknown"
	SIMReady   SIMState = "ready"
	SIMAbsent  SIMState = "absent"
	SIMLocked  SIMState = "locked"
	SIMFailed  SIMState = "failed"
)

type RegistrationState string

const (
	RegistrationUnknown      RegistrationState = "unknown"
	RegistrationUnregistered RegistrationState = "unregistered"
	RegistrationSearching    RegistrationState = "searching"
	RegistrationHome         RegistrationState = "home"
	RegistrationRoaming      RegistrationState = "roaming"
	RegistrationDenied       RegistrationState = "denied"
)

type RadioState string

const (
	RadioUnknown RadioState = "unknown"
	RadioOff     RadioState = "off"
	RadioOn      RadioState = "on"
)

type DataState string

type DataGuardState string

const (
	DataUnknown       DataState = "unknown"
	DataDisconnected  DataState = "disconnected"
	DataConnecting    DataState = "connecting"
	DataConnected     DataState = "connected"
	DataDisconnecting DataState = "disconnecting"
)

const (
	DataGuardUnmanaged DataGuardState = "unmanaged"
	DataGuardProtected DataGuardState = "protected"
	DataGuardFailed    DataGuardState = "failed"
)

type DataGuardFact struct {
	State  DataGuardState `json:"state"`
	Detail string         `json:"detail,omitempty"`
}

type ATControlState string

const (
	ATControlUnknown     ATControlState = "unknown"
	ATControlReady       ATControlState = "ready"
	ATControlBusy        ATControlState = "busy"
	ATControlUnavailable ATControlState = "unavailable"
	ATControlDegraded    ATControlState = "degraded"
)

// ATControlFact is independent of Windows MBN. In particular, MBN voice
// class does not determine whether this auxiliary 3GPP control function can
// signal calls or SMS.
type ATControlFact struct {
	State           ATControlState `json:"state"`
	Port            string         `json:"port,omitempty"`
	Detail          string         `json:"detail,omitempty"`
	CallSignalling  bool           `json:"call_signalling"`
	SMS             bool           `json:"sms"`
	SIMAPDU         bool           `json:"sim_apdu"`
	SIMAPDUOnDemand bool           `json:"sim_apdu_on_demand"`
}

type Capabilities struct {
	CellularData  bool   `json:"cellular_data"`
	SMSReceive    bool   `json:"sms_receive"`
	SMSSend       bool   `json:"sms_send"`
	MBNVoiceClass string `json:"mbn_voice_class,omitempty"`
}

type SIMFact struct {
	State             SIMState `json:"state"`
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

type NetworkFact struct {
	Registration  RegistrationState `json:"registration"`
	OperatorID    string            `json:"operator_id,omitempty"`
	OperatorName  string            `json:"operator_name,omitempty"`
	SignalPercent *uint32           `json:"signal_percent,omitempty"`
	SoftwareRadio RadioState        `json:"software_radio"`
	HardwareRadio RadioState        `json:"hardware_radio"`
	Data          DataState         `json:"data"`
	Profile       string            `json:"profile,omitempty"`
	Guard         DataGuardFact     `json:"data_guard"`
}

// Fact separates the local attachment ID from the SIM identity. Neither the
// Windows MBN interface ID nor equipment ID is a durable card identity.
type Fact struct {
	AttachmentID string `json:"attachment_id"`
	EquipmentID  string `json:"equipment_id,omitempty"`
	// ContinuityEpoch is a platform-private attachment/owner generation. It is
	// deliberately not serialized: SIMInsertionTracker turns it into the
	// opaque session generation that crosses the Agent protocol boundary.
	ContinuityEpoch string `json:"-"`
	// SessionGenerationAuthority prevents Agent host compatibility fallback
	// from turning an explicit platform unknown into an operable session.
	SessionGenerationAuthority bool            `json:"-"`
	LastContinuityIssue        string          `json:"last_continuity_issue,omitempty"`
	Manufacturer               string          `json:"manufacturer,omitempty"`
	Model                      string          `json:"model,omitempty"`
	Firmware                   string          `json:"firmware,omitempty"`
	Condition                  DeviceCondition `json:"condition"`
	Detail                     string          `json:"detail,omitempty"`
	Capabilities               Capabilities    `json:"capabilities"`
	AT                         ATControlFact   `json:"at_control"`
	SIM                        SIMFact         `json:"sim"`
	Network                    NetworkFact     `json:"network"`
}

type Observation struct {
	Condition Condition `json:"condition"`
	Detail    string    `json:"detail,omitempty"`
	Modems    []Fact    `json:"modems"`
}

// Prober performs one read-only, fresh platform observation. It must not
// connect data, mutate PIN/SMS state, dial, hang up, or alter host networking.
type Prober interface {
	Probe(context.Context) ([]Fact, error)
}

// SIMAKARequest is the only UICC operation exposed by a modem adapter. It is
// fenced to one live MBN attachment, equipment identity and inserted ICCID;
// raw AT and raw APDU are deliberately not part of this contract.
type SIMAKARequest struct {
	AttachmentID string
	EquipmentID  string
	CardID       string
	Application  string
	RAND         []byte
	AUTN         []byte
}

type SIMAKAResult struct {
	Body []byte
	SW1  byte
	SW2  byte
}

type SIMAuthenticator interface {
	AuthenticateSIMAKA(context.Context, SIMAKARequest) (SIMAKAResult, error)
}

type SIMPINRequest struct {
	AttachmentID string
	EquipmentID  string
	CardID       string
	PIN          string
}

type SIMPINResult struct {
	Attempted         bool
	Ready             bool
	AttemptsRemaining *uint32
}

// SIMPINRuntime exposes only one PIN1 entry attempt for an exact locked SIM.
// Implementations must perform a fresh identity/state/retry-count check before
// sending the credential and must report whether a command may have reached
// the card.
type SIMPINRuntime interface {
	EnterSIMPIN(context.Context, SIMPINRequest) (SIMPINResult, error)
}

// PINRecoverer may annotate PIN recovery facts and perform at most the typed,
// durable recovery action configured for an exact card. It runs after a
// successful read-only probe and before those facts are published.
type PINRecoverer interface {
	RecoverPINs(context.Context, []Fact) error
}

type PolicyReconciler interface {
	ReconcilePolicies(context.Context, []Fact)
}

// AuxiliaryCoordinator serializes non-call modem work with paid-call
// start/stop and its watchdog. The implementation must reject the callback
// while a durable paid-call lease exists for the same equipment.
type AuxiliaryCoordinator interface {
	DoAuxiliary(context.Context, string, func(context.Context) error) error
}

// IncomingCallFence binds a browser action to the exact persistent call
// occurrence previously observed by this Agent. Ephemeral attachment/session
// values are current transport fences, not the durable event identity.
type IncomingCallFence struct {
	EventID              string
	AttachmentID         string
	EquipmentID          string
	CardID               string
	SIMSessionGeneration string
	NativeCallIndex      int
	CallOccurrence       uint64
	Number               string
}

type IncomingCallVerifier interface {
	RequireIncomingCall(IncomingCallFence) error
}

// BackgroundScanCoordinator grants one low-priority scan only while no paid
// call lease exists anywhere in this Agent. The implementation shares the
// paid-call operation lock with dial/answer/renew/hangup.
type BackgroundScanCoordinator interface {
	DoBackgroundScan(context.Context, func(context.Context) error) error
}
