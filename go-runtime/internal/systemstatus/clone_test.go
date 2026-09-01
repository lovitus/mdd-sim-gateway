package systemstatus

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCloneSnapshotPreservesEmptyAndNilCollectionContracts(t *testing.T) {
	empty := Snapshot{
		Alerts: []Alert{}, Errors: []string{},
		Network:      availableSection(NetworkInfo{Interfaces: []NetworkInterface{}, Errors: []string{}}),
		Temperatures: availableSection(TemperatureInfo{Sensors: []Temperature{}, Errors: []string{}}),
		Systemd: availableSection(SystemdInfo{
			Fixed: []UnitStatus{}, Providers: []UnitStatus{}, Errors: []string{},
		}),
	}
	cloned := cloneSnapshot(empty)
	if cloned.Alerts == nil || cloned.Errors == nil || cloned.Network.Value == nil ||
		cloned.Network.Value.Interfaces == nil || cloned.Network.Value.Errors == nil ||
		cloned.Temperatures.Value == nil || cloned.Temperatures.Value.Sensors == nil ||
		cloned.Temperatures.Value.Errors == nil || cloned.Systemd.Value == nil ||
		cloned.Systemd.Value.Fixed == nil || cloned.Systemd.Value.Providers == nil ||
		cloned.Systemd.Value.Errors == nil {
		t.Fatalf("non-nil empty collection was lost: %+v", cloned)
	}
	payload, err := json.Marshal(cloned)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range [][]byte{
		[]byte(`"alerts":[]`), []byte(`"errors":[]`), []byte(`"interfaces":[]`),
		[]byte(`"sensors":[]`), []byte(`"fixed":[]`), []byte(`"providers":[]`),
	} {
		if !bytes.Contains(payload, expected) {
			t.Fatalf("JSON %s does not contain %s", payload, expected)
		}
	}

	nilCollections := cloneSnapshot(Snapshot{Systemd: availableSection(SystemdInfo{})})
	if nilCollections.Alerts != nil || nilCollections.Errors != nil || nilCollections.Systemd.Value == nil ||
		nilCollections.Systemd.Value.Fixed != nil || nilCollections.Systemd.Value.Providers != nil ||
		nilCollections.Systemd.Value.Errors != nil {
		t.Fatalf("nil collection changed meaning: %+v", nilCollections)
	}
}

func TestCloneSnapshotKeepsNestedCollectionsIndependent(t *testing.T) {
	source := Snapshot{
		Alerts: []Alert{{Code: "alert-first"}}, Errors: []string{"snapshot-first"},
		Network: availableSection(NetworkInfo{Interfaces: []NetworkInterface{{
			Name: "eth0", Addresses: []string{"192.0.2.10/24"},
		}}}),
		Systemd: availableSection(SystemdInfo{Fixed: []UnitStatus{{
			Name: "mdd-core.service", Errors: []string{"first"},
		}}}),
	}
	cloned := cloneSnapshot(source)
	cloned.Alerts[0].Code = "alert-changed"
	cloned.Errors[0] = "snapshot-changed"
	cloned.Network.Value.Interfaces[0].Addresses[0] = "198.51.100.1/24"
	cloned.Systemd.Value.Fixed[0].Errors[0] = "changed"
	if source.Alerts[0].Code != "alert-first" || source.Errors[0] != "snapshot-first" ||
		source.Network.Value.Interfaces[0].Addresses[0] != "192.0.2.10/24" ||
		source.Systemd.Value.Fixed[0].Errors[0] != "first" {
		t.Fatalf("clone mutation escaped: source=%+v cloned=%+v", source, cloned)
	}
}
