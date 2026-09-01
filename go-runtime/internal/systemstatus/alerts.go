package systemstatus

import "sort"

func deriveAlerts(snapshot Snapshot) []Alert {
	alerts := make([]Alert, 0)
	if snapshot.Disk.State == SectionAvailable && snapshot.Disk.Value != nil {
		used := snapshot.Disk.Value.UsedPercent
		switch {
		case used >= 96:
			alerts = append(alerts, Alert{Severity: "critical", Code: "disk_usage_critical", Scope: "host.disk"})
		case used >= 90:
			alerts = append(alerts, Alert{Severity: "warning", Code: "disk_usage_warning", Scope: "host.disk"})
		}
	}
	if snapshot.Temperatures.State == SectionAvailable && snapshot.Temperatures.Value != nil {
		for _, sensor := range snapshot.Temperatures.Value.Sensors {
			switch {
			case sensor.Celsius >= 90:
				alerts = append(alerts, Alert{Severity: "critical", Code: "temperature_critical", Scope: "host.temperature:" + sensor.Sensor})
			case sensor.Celsius >= 80:
				alerts = append(alerts, Alert{Severity: "warning", Code: "temperature_warning", Scope: "host.temperature:" + sensor.Sensor})
			}
		}
	}
	if snapshot.Systemd.State == SectionAvailable && snapshot.Systemd.Value != nil {
		units := append(append([]UnitStatus(nil), snapshot.Systemd.Value.Fixed...), snapshot.Systemd.Value.Providers...)
		for _, unit := range units {
			if unit.ActiveState == "failed" {
				alerts = append(alerts, Alert{Severity: "critical", Code: "systemd_unit_failed", Scope: "systemd:" + unit.Name})
			}
		}
	}
	sort.Slice(alerts, func(left, right int) bool {
		leftRank, rightRank := alertRank(alerts[left].Severity), alertRank(alerts[right].Severity)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if alerts[left].Code != alerts[right].Code {
			return alerts[left].Code < alerts[right].Code
		}
		return alerts[left].Scope < alerts[right].Scope
	})
	result := alerts[:0]
	seen := make(map[string]struct{}, len(alerts))
	for _, alert := range alerts {
		key := alert.Severity + "\x00" + alert.Code + "\x00" + alert.Scope
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, alert)
	}
	return result
}

func alertRank(severity string) int {
	if severity == "critical" {
		return 0
	}
	return 1
}
