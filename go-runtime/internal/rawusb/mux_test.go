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
	for index := 0; index < 3; index++ {
		logicalClient, err := client.DialContext(context.Background(), N.NetworkTCP,
			M.ParseSocksaddrHostPort("usbip.mdd.internal", 3240))
		if err != nil {
			t.Fatal(err)
		}
		logicalServer, err := exporter.listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		want := []byte{byte(index + 1), 0x55, 0xaa}
		go func() { _, _ = logicalClient.Write(want) }()
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
