package windowspcm

import (
	"errors"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowspnp"
)

func TestSelectUsesExactPhysicalParentAndUniqueNMEARole(t *testing.T) {
	ports := []windowspnp.Port{
		{Name: "COM14", Product: "Quectel USB AT Port", PhysicalID: "USB-A", USB: true},
		{Name: "COM15", Product: "Quectel USB NMEA Port", PhysicalID: "USB-A", USB: true},
		{Name: "COM33", Product: "Quectel USB NMEA Port", PhysicalID: "USB-B", USB: true},
	}
	selected, err := Select(ports, "usb-a")
	if err != nil || selected.Name != "COM15" {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
	ports = append(ports, windowspnp.Port{Name: "COM16", Product: "USB NMEA Port", PhysicalID: "USB-A", USB: true})
	if _, err := Select(ports, "USB-A"); !errors.Is(err, ErrEndpointAmbiguous) {
		t.Fatalf("ambiguous error=%v", err)
	}
}
