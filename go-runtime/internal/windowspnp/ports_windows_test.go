//go:build windows

package windowspnp

import "testing"

func TestPresentPortsHaveUniqueNamesAndPhysicalParents(t *testing.T) {
	ports, err := Ports()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]struct{}{}
	for _, port := range ports {
		t.Logf("port=%s product=%q physical=%s", port.Name, port.Product, port.PhysicalID)
		if port.Name == "" || port.InstanceID == "" || port.PhysicalID == "" {
			t.Fatalf("incomplete PnP port: %+v", port)
		}
		if _, exists := seen[port.Name]; exists {
			t.Fatalf("duplicate PnP port: %s", port.Name)
		}
		seen[port.Name] = struct{}{}
	}
}
