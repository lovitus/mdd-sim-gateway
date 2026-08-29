//go:build windows

package windowspcm

import (
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowspnp"
)

func TestPresentQuectelATPortHasOnePhysicalPCMEndpoint(t *testing.T) {
	ports, err := windowspnp.Ports()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, port := range ports {
		if !strings.Contains(strings.ToUpper(port.Product), "QUECTEL USB AT PORT") {
			continue
		}
		endpoint, selectErr := Select(ports, port.PhysicalID)
		if selectErr != nil {
			t.Fatalf("AT %s physical %s: %v", port.Name, port.PhysicalID, selectErr)
		}
		t.Logf("AT=%s PCM=%s physical=%s", port.Name, endpoint.Name, port.PhysicalID)
		found++
	}
	if found == 0 {
		t.Skip("no present Quectel AT port")
	}
}
