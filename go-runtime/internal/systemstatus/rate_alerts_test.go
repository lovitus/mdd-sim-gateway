package systemstatus

import (
	"os"
	"testing"
	"time"
)

func TestSwapRateNeedsTwoMonotonicSamples(t *testing.T) {
	at := time.Unix(1800000000, 0).UTC()
	later := at.Add(time.Minute)
	previous := Snapshot{SampledAt: &at, Swap: availableSection(SwapInfo{Sin: 100, Sout: 200})}
	current := Snapshot{SampledAt: &later, Swap: availableSection(SwapInfo{Sin: 100 + uint64(os.Getpagesize())*50*60, Sout: 200})}
	if high, known := swapPressure(current, previous); !high || !known {
		t.Fatal("rate threshold lost")
	}
	if _, known := swapPressure(current, Snapshot{}); known {
		t.Fatal("first sample fabricated rate")
	}
	current.Swap.Value.Sin = 1
	if _, known := swapPressure(current, previous); known {
		t.Fatal("counter reset fabricated rate")
	}
	current.SampledAt = &at
	if _, known := swapPressure(current, previous); known {
		t.Fatal("duplicate sample fabricated rate")
	}
}

func TestPowerBitsPreserveCurrentAndSinceBootSemantics(t *testing.T) {
	for _, item := range []struct {
		flags uint64
		codes []string
	}{
		{0, nil}, {1, []string{"undervoltage_now"}}, {0x10000, []string{"undervoltage_seen"}},
		{0x10001, []string{"undervoltage_now"}}, {0xe, []string{"throttled_now"}},
	} {
		alerts := platformAlerts(Snapshot{Power: availableSection(PowerInfo{Flags: item.flags})})
		if len(alerts) != len(item.codes) {
			t.Fatalf("flags=%x alerts=%v", item.flags, alerts)
		}
		for i, code := range item.codes {
			if alerts[i].Code != code {
				t.Fatal(alerts)
			}
		}
	}
	if alerts := platformAlerts(Snapshot{Power: unavailableSection[PowerInfo]("unsupported")}); len(alerts) != 0 {
		t.Fatal("unsupported power fabricated alert")
	}
}

func TestSwapPressureRequiresThreeDistinctHighRateSamples(t *testing.T) {
	sampler := &Sampler{interval: 30 * time.Second}
	start := time.Unix(1800000000, 0).UTC()
	previous := Snapshot{SampledAt: &start, Swap: availableSection(SwapInfo{})}
	for i := 1; i <= 3; i++ {
		at := start.Add(time.Duration(i) * 30 * time.Second)
		current := Snapshot{SampledAt: &at, Swap: availableSection(SwapInfo{Sin: uint64(i) * uint64(os.Getpagesize()) * 60 * 30})}
		sampler.current = &previous
		sampler.addRateAlerts(&current)
		if len(current.Alerts) != 0 && i < 3 || len(current.Alerts) != 1 && i == 3 {
			t.Fatalf("sample %d: %v", i, current.Alerts)
		}
		previous = current
	}
}
