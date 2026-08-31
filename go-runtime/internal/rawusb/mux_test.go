//go:build linux || windows

package rawusb

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func TestMultiplexCarriesManyUSBIPStreamsOverOneConnection(t *testing.T) {
	physicalClient, physicalServer := net.Pipe()
	exporter := &Exporter{listener: newConnListener()}
	serverDone := make(chan error, 1)
	go func() { serverDone <- exporter.ServeMultiplexed(context.Background(), physicalServer) }()
	client, err := newMuxClient(physicalClient)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	for index := 0; index < 3; index++ {
		dialContext, cancelDial := context.WithTimeout(context.Background(), 2*time.Second)
		logicalClient, err := client.DialContext(dialContext, N.NetworkTCP,
			M.ParseSocksaddrHostPort("usbip.mdd.internal", 3240))
		cancelDial()
		if err != nil {
			t.Fatal(err)
		}
		want := []byte{byte(index + 1), 0x55, 0xaa}
		go func(payload []byte) { _, _ = logicalClient.Write(payload) }(want)
		accepted := make(chan acceptResult, 1)
		go func() {
			conn, acceptErr := exporter.listener.Accept()
			accepted <- acceptResult{conn: conn, err: acceptErr}
		}()
		var logicalServer net.Conn
		select {
		case result := <-accepted:
			if result.err != nil {
				t.Fatal(result.err)
			}
			logicalServer = result.conn
		case <-time.After(2 * time.Second):
			t.Fatal("multiplex client did not open its logical USB/IP stream")
		}
		got := make([]byte, len(want))
		if _, err := io.ReadFull(logicalServer, got); err != nil || string(got) != string(want) {
			t.Fatalf("stream %d got=%x err=%v", index, got, err)
		}
		_ = logicalClient.Close()
		_ = logicalServer.Close()
	}
	_ = client.Close()
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("multiplex server did not stop with its one physical connection")
	}
}
