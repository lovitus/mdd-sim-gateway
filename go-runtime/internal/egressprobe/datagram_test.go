package egressprobe

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestDatagramProbeUsesOneInjectedRemotePath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	result, err := ProbeDatagram(ctx, func(_ context.Context, network, address string) (net.Conn, error) {
		calls++
		if network != "udp" || address != "1.1.1.1:53" {
			t.Fatalf("unexpected path %s %s", network, address)
		}
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			packet := make([]byte, 4096)
			n, err := server.Read(packet)
			if err != nil {
				return
			}
			reply := append([]byte(nil), packet[:n]...)
			reply[2] |= 0x80
			binary.BigEndian.PutUint16(reply[6:8], 1)
			reply = append(reply, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 1, 0, 4, 1, 1, 1, 1)
			_, _ = server.Write(reply)
		}()
		return client, nil
	})
	if err != nil || calls != 1 || result.Target != "1.1.1.1" || len(result.AttemptedTargets) != 1 {
		t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
	}
}
