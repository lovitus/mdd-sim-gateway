package windowspcm

import (
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf16"
)

var (
	ErrUACUnavailable = errors.New("the modem has no matching full-duplex UAC endpoint")
	ErrUACAmbiguous   = errors.New("the modem has ambiguous UAC endpoints")
)

type uacEndpoint struct {
	Kind       string
	InstanceID string
	Status     string
}

type helperDevice struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func selectUAC(endpoints []uacEndpoint, devices []helperDevice) (playbackID, captureID string, err error) {
	byInstance := map[string]string{}
	for _, endpoint := range endpoints {
		if !strings.EqualFold(endpoint.Status, "OK") || (endpoint.Kind != "playback" && endpoint.Kind != "capture") {
			continue
		}
		parts := strings.SplitN(endpoint.InstanceID, `\`, 3)
		if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
			continue
		}
		key := strings.ToLower(parts[2])
		if previous, exists := byInstance[key]; exists && previous != endpoint.Kind {
			return "", "", ErrUACAmbiguous
		}
		byInstance[key] = endpoint.Kind
	}
	for _, device := range devices {
		decoded := strings.ToLower(decodeMiniaudioID(device.ID))
		kind, exists := byInstance[decoded]
		if !exists || kind != device.Kind {
			continue
		}
		switch kind {
		case "playback":
			if playbackID != "" && !strings.EqualFold(playbackID, device.ID) {
				return "", "", ErrUACAmbiguous
			}
			playbackID = device.ID
		case "capture":
			if captureID != "" && !strings.EqualFold(captureID, device.ID) {
				return "", "", ErrUACAmbiguous
			}
			captureID = device.ID
		}
	}
	if playbackID == "" || captureID == "" {
		return "", "", ErrUACUnavailable
	}
	return playbackID, captureID, nil
}

func decodeMiniaudioID(value string) string {
	raw, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(raw) == 0 {
		return ""
	}
	if len(raw)%2 != 0 {
		raw = append(raw, 0)
	}
	words := make([]uint16, len(raw)/2)
	for index := range words {
		words[index] = uint16(raw[index*2]) | uint16(raw[index*2+1])<<8
	}
	return strings.TrimRight(string(utf16.Decode(words)), "\x00")
}
