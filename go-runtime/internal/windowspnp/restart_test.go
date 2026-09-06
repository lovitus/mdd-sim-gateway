package windowspnp

import (
	"errors"
	"testing"
)

func TestParseRestartTargetRequiresOneExactPhysicalAdapter(t *testing.T) {
	payload := []byte(`[{"GUID":"{AABB-CCDD}","PNPDeviceID":"USB\\VID_1234&PID_5678\\1"},{"GUID":"OTHER","PNPDeviceID":"PCI\\VEN_1234"}]`)
	target, err := parseRestartTarget(payload, "aabb-ccdd")
	if err != nil || target != `USB\VID_1234&PID_5678\1` {
		t.Fatalf("target=%q err=%v", target, err)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"GUID":"AABB-CCDD","PNPDeviceID":"ROOT\\VIRTUAL"}`),
		[]byte(`[{"GUID":"AABB-CCDD","PNPDeviceID":"USB\\ONE"},{"GUID":"AABB-CCDD","PNPDeviceID":"USB\\TWO"}]`),
		[]byte(`null`),
	} {
		if _, err := parseRestartTarget(invalid, "AABB-CCDD"); !errors.Is(err, ErrRestartUnavailable) {
			t.Fatalf("payload=%s err=%v", invalid, err)
		}
	}
}
