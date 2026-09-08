package agentlink

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const deviceDefaultsFeature = "new-device-defaults-v1"
const kindDeviceDefaults = "device_defaults"

type DeviceDefaults struct {
	Authorization     string `json:"authorization,omitempty"`
	Authority         string `json:"authority"`
	Revision          uint64 `json:"revision"`
	ConnectionEnabled bool   `json:"connection_enabled"`
	VoWiFiEnabled     bool   `json:"vowifi_enabled"`
	FlightMode        bool   `json:"flight_mode"`
	RoamingEnabled    bool   `json:"roaming_enabled"`
}

func (defaults DeviceDefaults) Validate() error {
	if defaults.Revision == 0 || defaults.Authority == "" || len(defaults.Authority) > 128 ||
		strings.IndexFunc(defaults.Authority, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return errors.New("invalid initial device default authority")
	}
	if defaults.Authorization != "" {
		if decoded, err := hex.DecodeString(defaults.Authorization); err != nil || len(decoded) != sha256.Size {
			return errors.New("invalid device default authorization")
		}
	}
	return nil
}

// Authorize binds every template field to Core's durable authority. The key
// stays on Core; an Agent only carries the resulting authentication tag back.
func (defaults DeviceDefaults) Authorize(key []byte) (DeviceDefaults, error) {
	if len(key) < 32 {
		return DeviceDefaults{}, errors.New("device default signing key is too short")
	}
	defaults.Authorization = ""
	if err := defaults.Validate(); err != nil {
		return DeviceDefaults{}, err
	}
	payload, err := json.Marshal(defaults)
	if err != nil {
		return DeviceDefaults{}, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("mdd-new-device-defaults-v1\x00"))
	_, _ = mac.Write(payload)
	defaults.Authorization = hex.EncodeToString(mac.Sum(nil))
	return defaults, nil
}

func (defaults DeviceDefaults) AuthorizedBy(key []byte) bool {
	expected, err := defaults.Authorize(key)
	if err != nil {
		return false
	}
	actual, err := hex.DecodeString(defaults.Authorization)
	if err != nil || len(actual) != sha256.Size {
		return false
	}
	want, _ := hex.DecodeString(expected.Authorization)
	return hmac.Equal(actual, want)
}

type deviceDefaultsUpdate struct {
	Template *DeviceDefaults `json:"template"`
}

// DeviceEnrollment reports a frozen default and its hardware-policy outcome.
// It is observed evidence, not independent authority to create or start a line.
type DeviceEnrollment struct {
	FirstSeen   time.Time       `json:"first_seen"`
	Protected   bool            `json:"protected"`
	Initialized bool            `json:"initialized"`
	Template    *DeviceDefaults `json:"template,omitempty"`
}

func (value DeviceEnrollment) Validate() error {
	if value.FirstSeen.IsZero() || (value.Initialized && value.Template == nil) {
		return errors.New("invalid device enrollment fact")
	}
	if value.Template != nil {
		return value.Template.Validate()
	}
	return nil
}

func (value DeviceEnrollment) Clone() *DeviceEnrollment {
	if value.Template != nil {
		copy := *value.Template
		value.Template = &copy
	}
	return &value
}

// SetDeviceDefaultsSource is configured before accepting Agent connections.
func (server *Server) SetDeviceDefaultsSource(source func() (*DeviceDefaults, error)) error {
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.agents) != 0 {
		return errors.New("device defaults source cannot change with connected Agents")
	}
	server.deviceDefaultsSource = source
	return nil
}

func (server *Server) syncDeviceDefaults(ctx context.Context, connection *serverConnection) error {
	if !featureEnabled(strings.Join(connection.capabilities, ","), deviceDefaultsFeature) {
		return nil
	}
	server.mu.RLock()
	source := server.deviceDefaultsSource
	server.mu.RUnlock()
	var template *DeviceDefaults
	if source != nil {
		value, err := source()
		if err == nil && value != nil && value.Validate() == nil {
			copy := *value
			template = &copy
		}
	}
	if connection.defaultsSent && ((template == nil && connection.lastDefaults == nil) ||
		(template != nil && connection.lastDefaults != nil && *template == *connection.lastDefaults)) {
		return nil
	}
	writeContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection.writeMu.Lock()
	err := writeEnvelope(writeContext, connection.socket, envelope{Kind: kindDeviceDefaults, DeviceDefaults: &deviceDefaultsUpdate{Template: template}})
	connection.writeMu.Unlock()
	if err == nil {
		connection.defaultsSent = true
		connection.lastDefaults = template
	}
	return err
}
