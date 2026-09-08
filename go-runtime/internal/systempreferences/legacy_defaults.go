package systempreferences

import (
	"bytes"
	"encoding/json"
	"errors"
)

// ReadLegacyDefaults copies ec620942 device_state.py's persisted defaults.
// Per-device choices are deliberately not reinterpreted as future-device defaults.
func ReadLegacyDefaults(payload []byte) (NewDeviceDefaults, error) {
	var document struct {
		Defaults json.RawMessage `json:"defaults"`
	}
	if len(payload) > 1<<20 || json.Unmarshal(payload, &document) != nil || len(document.Defaults) == 0 || bytes.Equal(bytes.TrimSpace(document.Defaults), []byte("null")) {
		return NewDeviceDefaults{}, errors.New("legacy device defaults document required")
	}
	var fields struct {
		Cellular *bool `json:"cellular_enabled"`
		VoWiFi   *bool `json:"vowifi_enabled"`
		Flight   *bool `json:"flight_mode"`
		Roaming  *bool `json:"roaming_enabled"`
	}
	if err := json.Unmarshal(document.Defaults, &fields); err != nil {
		return NewDeviceDefaults{}, errors.New("invalid legacy device defaults")
	}
	value := NewDeviceDefaults{VoWiFiEnabled: true}
	if fields.Cellular != nil {
		value.ConnectionEnabled = *fields.Cellular
	}
	if fields.VoWiFi != nil {
		value.VoWiFiEnabled = *fields.VoWiFi
	}
	if fields.Flight != nil {
		value.FlightMode = *fields.Flight
	}
	if fields.Roaming != nil {
		value.RoamingEnabled = *fields.Roaming
	}
	return value, nil
}

func (store *Store) ImportLegacyDefaults(value NewDeviceDefaults) (Snapshot, bool, error) {
	current, err := store.Snapshot()
	if err != nil {
		return Snapshot{}, false, err
	}
	if current.Preferences.NewDeviceDefaults != nil {
		if *current.Preferences.NewDeviceDefaults == value {
			return current, false, nil
		}
		return Snapshot{}, false, errors.New("existing device defaults must not be overwritten by import")
	}
	current.Preferences.NewDeviceDefaults = &value
	saved, err := store.PutExpected(current.Preferences, current.Revision)
	return saved, err == nil, err
}
