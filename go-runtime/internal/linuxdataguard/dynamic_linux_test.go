//go:build linux

package linuxdataguard

import (
	"net"
	"testing"

	"github.com/jsimonetti/rtnetlink"
)

func TestDynamicCleanupRetainsOwnerUntilInterfaceAbsenceIsKnown(t *testing.T) {
	conn, err := rtnetlink.Dial(nil)
	if err != nil {
		t.Fatal(err)
	}
	// No kernel objects are mutated. The closed real netlink transport models
	// a failed read, for which even an actually absent index remains unknown.
	conn.Close()
	address := &rtnetlink.AddressMessage{Index: 2147483647}
	route := DataRoute{linkEnabled: true, dynamic: &dynamicRoute{iface: net.Interface{Index: int(address.Index)}, address: address}}
	if err := cleanupDynamicRouteOn(conn, &route); err == nil || route.dynamic.address != address || !route.linkEnabled {
		t.Fatalf("failed inventory released cleanup owner: %v %+v", err, route)
	}
	conn, err = rtnetlink.Dial(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	links, err := conn.Link.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range links {
		if link.Index == address.Index {
			t.Fatal("unexpected occupied fixture index")
		}
	}
	if err := cleanupDynamicRouteOn(conn, &route); err != nil || route.dynamic.address != nil || route.linkEnabled {
		t.Fatalf("successful absent readback did not release cleanup: %v %+v", err, route)
	}
}
