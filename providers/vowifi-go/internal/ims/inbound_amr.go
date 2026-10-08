// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
)

// RFC 4867 sections 8.1 and 8.3.1: interleaving is enabled by presence,
// whereas mode-set must be echoed unchanged and enforced by the encoder.
func inboundAMRFormat(fmtp string) (string, media.AMRNBConfig, error) {
	config := media.AMRNBConfig{}
	answer := make(map[string]string)
	for key, value := range voicehost.ParseSDPFmtpParameters(fmtp) {
		valid := true
		switch key {
		case "octet-align", "crc", "robust-sorting":
			valid = value == "0"
		case "interleaving":
			valid = false
		case "channels":
			valid = value == "1"
		case "mode-set":
			for _, text := range strings.Split(value, ",") {
				mode, err := strconv.Atoi(strings.TrimSpace(text))
				if err != nil || mode < 0 || mode > 7 {
					valid = false
					break
				}
				config.ModeSet |= 1 << mode
			}
		case "mode-change-period":
			valid = value == "1" || value == "2"
			config.ChangePeriod, _ = strconv.Atoi(value)
		case "mode-change-neighbor":
			valid = value == "0" || value == "1"
			config.NeighborOnly = value == "1"
		case "mode-change-capability":
			valid = value == "1" || value == "2"
		case "max-red":
			maximum, err := strconv.Atoi(value)
			valid = err == nil && maximum >= 0 && maximum <= 65535
		default:
			continue // Unknown offer parameters must not be echoed in an answer.
		}
		if !valid {
			return "", config, fmt.Errorf("%w: unsupported AMR %s", ErrMediaNegotiation, key)
		}
		answer[key] = value
	}
	answer["mode-change-capability"] = "2"
	answer["max-red"] = "0" // One frame per packet, with no redundant transmission.
	return voicehost.BuildSDPFmtpParameters(answer), config, nil
}
