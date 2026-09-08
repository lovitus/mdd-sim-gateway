package agentcontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type RuntimeConfig struct {
	ModemBackend   string `json:"modem_backend"`
	ProfilesSHA256 string `json:"profiles_sha256"`
	ModemEnabled   bool   `json:"modem_enabled"`
	SIMAPDUEnabled bool   `json:"sim_apdu_enabled"`
}

func ModemRuntimeConfig(backend string, profiles []byte, enabled, apdu bool) (RuntimeConfig, error) {
	if backend == "" {
		backend = "auto"
	}
	if backend != "auto" && backend != "serial" {
		return RuntimeConfig{}, errors.New("invalid modem backend")
	}
	var value any
	if len(profiles) > 0 {
		if err := json.Unmarshal(profiles, &value); err != nil {
			return RuntimeConfig{}, errors.New("invalid modem profiles")
		}
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return RuntimeConfig{}, err
	}
	digest := sha256.Sum256(canonical)
	return RuntimeConfig{ModemBackend: backend, ProfilesSHA256: hex.EncodeToString(digest[:]), ModemEnabled: enabled, SIMAPDUEnabled: apdu}, nil
}
