//go:build linux

package linuxdataguard

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/jsimonetti/rtnetlink"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

func TestRealmInventoryRejectsIncompleteAndAmbiguousEvidence(t *testing.T) {
	const magic = 0x45311224
	route := realmRoute{realm: 0x4d010001, RouteMessage: rtnetlink.RouteMessage{Family: unix.AF_INET, Attributes: rtnetlink.RouteAttributes{OutIface: 42, Table: 0x4d010001}}}
	payload, err := route.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	message := netlink.Message{Header: netlink.Header{Length: uint32(unix.NLMSG_HDRLEN + len(payload)), Type: unix.RTM_NEWROUTE}, Data: payload}
	wire, err := message.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	stream := make([]byte, 4)
	binary.NativeEndian.PutUint32(stream, magic)
	stream = append(stream, wire...)
	for _, test := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"valid", func(b []byte) []byte { return b }},
		{"wrong-magic", func(b []byte) []byte { b[0] ^= 1; return b }},
		{"truncated", func(b []byte) []byte { return b[:len(b)-1] }},
		{"trailing", func(b []byte) []byte { return append(b, 0) }},
		{"wrong-type", func(b []byte) []byte { binary.NativeEndian.PutUint16(b[8:10], unix.RTM_NEWRULE); return b }},
		{"wrong-family", func(b []byte) []byte { b[4+unix.NLMSG_HDRLEN] = unix.AF_INET6; return b }},
		{"interrupted", func(b []byte) []byte {
			binary.NativeEndian.PutUint16(b[10:12], uint16(netlink.DumpInterrupted))
			return b
		}},
		{"length-overflow", func(b []byte) []byte { binary.NativeEndian.PutUint32(b[4:8], ^uint32(0)); return b }},
	} {
		t.Run(test.name, func(t *testing.T) {
			messages, err := decodeInventory(test.edit(append([]byte(nil), stream...)), magic, unix.RTM_NEWROUTE)
			if test.name != "valid" {
				if err == nil {
					t.Fatal("unsafe inventory accepted")
				}
				return
			}
			if err != nil || len(messages) != 1 {
				t.Fatalf("valid inventory rejected: %v", err)
			}
			var actual rtnetlink.RouteMessage
			if err := actual.UnmarshalBinary(messages[0].Data); err != nil || actual.Attributes.OutIface != 42 {
				t.Fatalf("numeric interface identity lost: %+v %v", actual, err)
			}
		})
	}
	// A limit at an exact message boundary still cannot prove completeness.
	output := &inventoryOutput{limit: len(stream)}
	output.Write(stream)
	output.Write(wire)
	if !output.overflow || output.buffer.Len() != len(stream) {
		t.Fatal("complete-message prefix hid inventory overflow")
	}
	// A zero-exit producer can report DONE-only interruption exclusively on
	// stderr. Exercise the real bounded command/read path, not a mock verifier.
	directory := t.TempDir()
	fixture := filepath.Join(directory, "inventory")
	if err := os.WriteFile(fixture, stream, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("MDD_TEST_INVENTORY", fixture)
	for _, warning := range []bool{false, true} {
		script := "#!/bin/sh\n/bin/cat \"$MDD_TEST_INVENTORY\"\n"
		if warning {
			script += "printf 'Dump was interrupted and may be inconsistent.\\n' >&2\n"
		}
		if err := os.WriteFile(filepath.Join(directory, "ip"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		messages, err := realmDump(context.Background(), unix.RTM_NEWROUTE)
		if warning && err == nil {
			t.Fatal("zero-exit interrupted inventory accepted")
		}
		if !warning && (err != nil || len(messages) != 1) {
			t.Fatalf("complete producer rejected: %v", err)
		}
	}
	flow, err := netlink.MarshalAttributes([]netlink.Attribute{{Type: unix.RTA_FLOW, Data: binary.NativeEndian.AppendUint32(nil, route.realm)}})
	if err != nil {
		t.Fatal(err)
	}
	hop := make([]byte, 8)
	binary.NativeEndian.PutUint16(hop[:2], uint16(len(hop)+len(flow)))
	binary.NativeEndian.PutUint32(hop[4:8], 42)
	hop = append(hop, flow...)
	multipath, err := netlink.MarshalAttributes([]netlink.Attribute{{Type: unix.RTA_MULTIPATH, Data: hop}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		data  []byte
		valid bool
	}{
		{"ordinary", flow, true},
		{"inline-hop", multipath, true},
		{"duplicate", append(append([]byte(nil), flow...), flow...), false},
		{"truncated-hop", multipath[:len(multipath)-1], false},
	} {
		t.Run(test.name, func(t *testing.T) {
			realm, hops, err := routeRealms(test.data)
			if !test.valid {
				if err == nil {
					t.Fatal("ambiguous route realm accepted")
				}
				return
			}
			if err != nil || test.name == "ordinary" && realm != route.realm || test.name == "inline-hop" && (len(hops) != 1 || hops[0] != route.realm) {
				t.Fatalf("route realm lost: %x %v %v", realm, hops, err)
			}
		})
	}
}

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
