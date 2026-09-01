package systemstatus

import (
	"math"
	"testing"
)

func TestPublicHostAddressPreservesHostAndFiltersLocalAddresses(t *testing.T) {
	for input, expected := range map[string]string{
		"192.0.2.10/24":   "192.0.2.10/24",
		"2001:db8::10/64": "2001:db8::10/64",
		"198.51.100.7":    "198.51.100.7",
	} {
		actual, ok := publicHostAddress(input)
		if !ok || actual != expected {
			t.Errorf("address %q = %q, %t", input, actual, ok)
		}
	}
	for _, input := range []string{"127.0.0.1/8", "::1/128", "fe80::1/64", "0.0.0.0", "invalid"} {
		if actual, ok := publicHostAddress(input); ok {
			t.Errorf("local/invalid address %q accepted as %q", input, actual)
		}
	}
}

func TestPercentageAndTemperatureValidation(t *testing.T) {
	for _, value := range []float64{0, 50, 100} {
		if !validPercent(value) {
			t.Errorf("valid percentage rejected: %v", value)
		}
	}
	for _, value := range []float64{-1, 101, math.NaN(), math.Inf(1)} {
		if validPercent(value) {
			t.Errorf("invalid percentage accepted: %v", value)
		}
	}
	for _, value := range []float64{0.1, 80, 149.9} {
		if !validTemperature(value) {
			t.Errorf("valid temperature rejected: %v", value)
		}
	}
	for _, value := range []float64{0, -1, 150, math.NaN(), math.Inf(-1)} {
		if validTemperature(value) {
			t.Errorf("invalid temperature accepted: %v", value)
		}
	}
}

func TestGopsutilSourceReadsCoreHostSections(t *testing.T) {
	result := (gopsutilSource{}).CollectHost(t.Context(), t.TempDir())
	for name, state := range map[string]string{
		"host": result.Host.State, "cpu": result.CPU.State, "load": result.Load.State,
		"memory": result.Memory.State, "swap": result.Swap.State, "disk": result.Disk.State,
	} {
		if state != SectionAvailable {
			t.Errorf("%s state=%s", name, state)
		}
	}
	if result.Host.Value == nil || result.Host.Value.UptimeSeconds == 0 ||
		result.CPU.Value == nil || result.CPU.Value.LogicalCores < 1 ||
		result.Memory.Value == nil || result.Memory.Value.TotalBytes == 0 ||
		result.Disk.Value == nil || result.Disk.Value.TotalBytes == 0 {
		t.Fatalf("result=%+v", result)
	}
}
