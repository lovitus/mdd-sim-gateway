package systemstatus

import "os"

// Ported from MDD sysinfo.swap_paging_rate and main._sustained_alerts.
// The counters are bytes, so use the actual host page size rather than
// assuming 4096-byte pages on every supported machine.
func swapPressure(current, previous Snapshot) (bool, bool) {
	if current.SampledAt == nil || previous.SampledAt == nil ||
		current.Swap.State != SectionAvailable || previous.Swap.State != SectionAvailable ||
		current.Swap.Value == nil || previous.Swap.Value == nil {
		return false, false
	}
	elapsed := current.SampledAt.Sub(*previous.SampledAt).Seconds()
	if elapsed <= 0 || elapsed > 120 {
		return false, false
	}
	now, before := current.Swap.Value, previous.Swap.Value
	if now.Sin < before.Sin || now.Sout < before.Sout {
		return false, false
	}
	rate := (float64(now.Sin-before.Sin) + float64(now.Sout-before.Sout)) / float64(os.Getpagesize()) / elapsed
	return rate >= 50, true
}

func (sampler *Sampler) addRateAlerts(result *Snapshot) {
	previous := sampler.Snapshot(*result.SampledAt)
	if !previous.Stale && previous.DefaultRoute.State == SectionAvailable && result.DefaultRoute.State == SectionAvailable && previous.DefaultRoute.Value != nil && result.DefaultRoute.Value != nil && previous.DefaultRoute.Value.Primary != result.DefaultRoute.Value.Primary {
		result.Alerts = append(result.Alerts, Alert{Severity: "warning", Code: "default_route_changed", Scope: "host.default_route"})
	}
	high, known := swapPressure(*result, previous)
	result.SwapRateKnown = known
	if !known || !high {
		sampler.swapPressureStreak = 0
		return
	}
	count := sampler.swapPressureStreak + 1
	if count > 3 {
		count = 3
	}
	sampler.swapPressureStreak = count
	if count >= 3 {
		result.Alerts = append(result.Alerts, Alert{Severity: "warning", Code: "swap_pressure", Scope: "host.swap"})
	}
}
