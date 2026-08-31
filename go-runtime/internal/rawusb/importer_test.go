//go:build linux || windows

package rawusb

import (
	"context"
	"net"
	"testing"

	M "github.com/sagernet/sing/common/metadata"
)

func TestImporterRejectsIncompleteExactIdentity(t *testing.T) {
	dial := func(context.Context) (net.Conn, error) { return nil, context.Canceled }
	for _, device := range []Device{
		{},
		{BusID: "1-2", VendorID: 0x2c7c},
		{BusID: "1-2", ProductID: 0x0125},
	} {
		if _, err := NewImporter(context.Background(), device, dial); err == nil {
			t.Fatalf("incomplete device accepted: %+v", device)
		}
	}
}

func TestStreamDialerUsesOneExplicitByteStream(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	calls := 0
	dialer := streamDialer{dial: func(context.Context) (net.Conn, error) {
		calls++
		return left, nil
	}}
	got, err := dialer.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("127.0.0.1", 3240))
	if err != nil || got != left || calls != 1 {
		t.Fatalf("got=%v calls=%d err=%v", got, calls, err)
	}
	if _, err := dialer.DialContext(context.Background(), "udp", M.Socksaddr{}); err == nil {
		t.Fatal("packet-like USB/IP stream accepted")
	}
}
