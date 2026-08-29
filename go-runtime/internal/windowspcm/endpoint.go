package windowspcm

import (
	"errors"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowspnp"
)

var (
	ErrEndpointUnavailable = errors.New("the modem has no serial voice PCM endpoint")
	ErrEndpointAmbiguous   = errors.New("the modem has multiple serial voice PCM endpoints")
)

func Select(ports []windowspnp.Port, physicalID string) (windowspnp.Port, error) {
	physicalID = strings.ToUpper(strings.TrimSpace(physicalID))
	matches := []windowspnp.Port{}
	for _, port := range ports {
		if strings.ToUpper(port.PhysicalID) == physicalID && port.USB &&
			strings.Contains(strings.ToUpper(port.Product), "NMEA") {
			matches = append(matches, port)
		}
	}
	if len(matches) == 0 {
		return windowspnp.Port{}, ErrEndpointUnavailable
	}
	if len(matches) != 1 {
		return windowspnp.Port{}, ErrEndpointAmbiguous
	}
	return matches[0], nil
}
