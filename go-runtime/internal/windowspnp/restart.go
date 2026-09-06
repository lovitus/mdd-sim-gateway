package windowspnp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

var ErrRestartUnavailable = errors.New("Windows modem soft restart is unavailable")

type restartAdapter struct {
	GUID        string `json:"GUID"`
	PNPDeviceID string `json:"PNPDeviceID"`
}

func parseRestartTarget(payload []byte, attachmentID string) (string, error) {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || len(payload) > 1<<20 {
		return "", ErrRestartUnavailable
	}
	var rows []restartAdapter
	if payload[0] == '[' {
		if json.Unmarshal(payload, &rows) != nil {
			return "", ErrRestartUnavailable
		}
	} else {
		var row restartAdapter
		if json.Unmarshal(payload, &row) != nil {
			return "", ErrRestartUnavailable
		}
		rows = []restartAdapter{row}
	}
	wanted := normalizeGUID(attachmentID)
	matches := []string{}
	for _, row := range rows {
		pnp := strings.ToUpper(strings.TrimSpace(row.PNPDeviceID))
		if normalizeGUID(row.GUID) == wanted && (strings.HasPrefix(pnp, `USB\`) || strings.HasPrefix(pnp, `PCI\`)) &&
			!strings.ContainsAny(pnp, "\r\n\x00") {
			matches = append(matches, pnp)
		}
	}
	if wanted == "" || len(matches) != 1 {
		return "", ErrRestartUnavailable
	}
	return matches[0], nil
}

func normalizeGUID(value string) string {
	return strings.ToUpper(strings.Trim(strings.TrimSpace(value), "{}"))
}
