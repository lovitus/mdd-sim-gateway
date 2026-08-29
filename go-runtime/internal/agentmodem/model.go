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

const (
	DataUnknown       DataState = "unknown"
	DataDisconnected  DataState = "disconnected"
	DataConnecting    DataState = "connecting"
	DataConnected     DataState = "connected"
	DataDisconnecting DataState = "disconnecting"
)

type Capabilities struct {
	CellularData  bool   `json:"cellular_data"`
	SMSReceive    bool   `json:"sms_receive"`
	SMSSend       bool   `json:"sms_send"`
	MBNVoiceClass string `json:"mbn_voice_class,omitempty"`
}

type SIMFact struct {
	State      SIMState `json:"state"`
	ICCID      string   `json:"iccid,omitempty"`
	IMSI       string   `json:"imsi,omitempty"`
	MSISDNs    []string `json:"msisdns,omitempty"`
	Configured bool     `json:"sms_configured"`
	SMSC       string   `json:"smsc,omitempty"`
	SMSError   string   `json:"sms_error,omitempty"`
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
}

// Fact separates the local attachment ID from the SIM identity. Neither the
// Windows MBN interface ID nor equipment ID is a durable card identity.
type Fact struct {
	AttachmentID string          `json:"attachment_id"`
	EquipmentID  string          `json:"equipment_id,omitempty"`
	Manufacturer string          `json:"manufacturer,omitempty"`
	Model        string          `json:"model,omitempty"`
	Firmware     string          `json:"firmware,omitempty"`
	Condition    DeviceCondition `json:"condition"`
	Detail       string          `json:"detail,omitempty"`
	Capabilities Capabilities    `json:"capabilities"`
	SIM          SIMFact         `json:"sim"`
	Network      NetworkFact     `json:"network"`
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
