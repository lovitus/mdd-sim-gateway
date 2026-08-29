package egressprobe

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestProbePassesWhenOneOfTwoAppliedUDPPathsAnswers(t *testing.T) {
	proxyURL, closeProxy := startTestSOCKS(t, netip.MustParseAddr("8.8.8.8"))
	defer closeProxy()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := Probe(ctx, proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	if result.Target != "8.8.8.8" || result.LatencyMS < 1 || len(result.AttemptedTargets) != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestProbeFailsWhenNeitherAppliedUDPPathAnswers(t *testing.T) {
	proxyURL, closeProxy := startTestSOCKS(t, netip.Addr{})
	defer closeProxy()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := Probe(ctx, proxyURL)
	if err == nil || !strings.Contains(err.Error(), "1.1.1.1: timed out") ||
		!strings.Contains(err.Error(), "8.8.8.8: timed out") {
		t.Fatalf("error=%v", err)
	}
}

func TestProbeRejectsNonLoopbackEndpoint(t *testing.T) {
	if _, err := Probe(context.Background(), "socks5://192.0.2.1:1080"); err == nil {
		t.Fatal("non-loopback proxy was accepted")
	}
}

func startTestSOCKS(t *testing.T, answering netip.Addr) (string, func()) {
	t.Helper()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = udp.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		connection, err := tcp.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		greeting := make([]byte, 3)
		if _, err := io.ReadFull(connection, greeting); err != nil {
			return
		}
		_, _ = connection.Write([]byte{5, 0})
		request := make([]byte, 10)
		if _, err := io.ReadFull(connection, request); err != nil {
			return
		}
		relay := udp.LocalAddr().(*net.UDPAddr)
		response := []byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}
		binary.BigEndian.PutUint16(response[8:10], uint16(relay.Port))
		_, _ = connection.Write(response)
		<-done
	}()
	go func() {
		buffer := make([]byte, maximumPacketBytes)
		for {
			count, client, err := udp.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			remote, query, err := parseSOCKSUDP(buffer[:count])
			if err != nil || !answering.IsValid() || remote.Addr() != answering || len(query) < 12 {
				continue
			}
			question := append([]byte(nil), query[12:]...)
			answer := make([]byte, 12, 12+len(question)+16)
			copy(answer[:2], query[:2])
			answer[2], answer[3], answer[5], answer[7] = 0x81, 0x80, 1, 1
			answer = append(answer, question...)
			answer = append(answer, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4, 192, 0, 2, 1)
			packet := socksUDPRequest(answering, 53, answer)
			_, _ = udp.WriteToUDP(packet, client)
		}
	}()
	return "socks5://" + tcp.Addr().String(), func() {
		close(done)
		_ = tcp.Close()
		_ = udp.Close()
	}
}
