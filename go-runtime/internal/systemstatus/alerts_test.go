package systemstatus

import "testing"

func TestAlertThresholdBoundaries(t *testing.T) {
	for _, test := range []struct {
		name        string
		disk        float64
		temperature float64
		want        []string
	}{
		{"below", 89.99, 79.99, nil},
		{"warnings", 90, 80, []string{"disk_usage_warning", "temperature_warning"}},
		{"still warnings", 95.99, 89.99, []string{"disk_usage_warning", "temperature_warning"}},
		{"critical", 96, 90, []string{"disk_usage_critical", "temperature_critical"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := Snapshot{
				Disk:         availableSection(DiskInfo{UsedPercent: test.disk}),
				Temperatures: availableSection(TemperatureInfo{Sensors: []Temperature{{Sensor: "cpu", Celsius: test.temperature}}}),
			}
			alerts := deriveAlerts(snapshot)
			got := make([]string, len(alerts))
			for index := range alerts {
				got[index] = alerts[index].Code
			}
			if len(got) != len(test.want) {
				t.Fatalf("alerts=%v", alerts)
			}
			for _, expected := range test.want {
				found := false
				for _, actual := range got {
					found = found || actual == expected
				}
				if !found {
					t.Fatalf("alerts=%v missing=%s", alerts, expected)
				}
			}
		})
	}
}

func TestOnlyFailedSystemdUnitCreatesAlert(t *testing.T) {
	units := SystemdInfo{Fixed: []UnitStatus{
		{Name: "inactive.service", ActiveState: "inactive"},
		{Name: "missing.service", LoadState: "not-found", ActiveState: "inactive"},
		{Name: "failed.service", ActiveState: "failed"},
	}}
	alerts := deriveAlerts(Snapshot{Systemd: availableSection(units)})
	if len(alerts) != 1 || alerts[0].Code != "systemd_unit_failed" || alerts[0].Scope != "systemd:failed.service" {
		t.Fatalf("alerts=%+v", alerts)
	}
}
