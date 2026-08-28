// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/engine/swu"
	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
	"golang.org/x/net/dns/dnsmessage"
)

type memorySession struct {
	inbound chan []byte
	peer    *memorySession
	closed  chan struct{}
	once    sync.Once
}

func memorySessionPair() (*memorySession, *memorySession) {
	first := &memorySession{inbound: make(chan []byte, 128), closed: make(chan struct{})}
	second := &memorySession{inbound: make(chan []byte, 128), closed: make(chan struct{})}
	first.peer = second
	second.peer = first
	return first, second
}

func (session *memorySession) Send(ctx context.Context, packet []byte) error {
	copyOfPacket := append([]byte(nil), packet...)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-session.peer.closed:
		return usernet.ErrClosed
	case session.peer.inbound <- copyOfPacket:
		return nil
	}
}

func (session *memorySession) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-session.closed:
		return nil, usernet.ErrClosed
	case packet := <-session.inbound:
		return append([]byte(nil), packet...), nil
	}
}

func (session *memorySession) Close(context.Context) error {
	session.once.Do(func() { close(session.closed) })
	return nil
}

func openStackPair(t *testing.T) (*usernet.Stack, *usernet.Stack) {
	t.Helper()
	clientPackets, serverPackets := memorySessionPair()
	client, err := usernet.Open(context.Background(), clientPackets, usernet.Config{
		Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.1")},
		DNS:       []netip.Addr{netip.MustParseAddr("10.0.0.2")},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := usernet.Open(context.Background(), serverPackets, usernet.Config{
		Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.2")},
	})
	if err != nil {
		_ = client.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = client.Close(ctx)
		_ = server.Close(ctx)
	})
	return client, server
}

func TestRegistrarRegistersAndDeregistersEntirelyOverUserspaceStack(t *testing.T) {
	clientStack, serverStack := openStackPair(t)
	dnsConn, err := serverStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:53")
	if err != nil {
		t.Fatal(err)
	}
	defer dnsConn.Close()
	var dnsRequests atomic.Int32
	dnsDone := make(chan error, 1)
	go serveDNS(dnsConn, &dnsRequests, dnsDone)

	packetConn, err := serverStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5060")
	if err != nil {
		t.Fatal(err)
	}
	defer packetConn.Close()
	_ = packetConn.SetDeadline(time.Now().Add(5 * time.Second))

	requests := make(chan voiceclient.SIPIncomingRequest, 2)
	serverDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 65535)
		for count := 0; count < 2; count++ {
			size, source, err := packetConn.ReadFrom(buffer)
			if err != nil {
				serverDone <- err
				return
			}
			request, err := voiceclient.ParseSIPRequest(buffer[:size])
			if err != nil {
				serverDone <- err
				return
			}
			requests <- request
			wire, err := voiceclient.BuildSIPResponseWire(request, 200, "OK", map[string]string{
				"P-Associated-URI": "<sip:user@ims.test>",
			}, nil)
			if err == nil {
				_, err = packetConn.WriteTo(wire, source)
			}
			if err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	registrar, err := NewRegistrar(clientStack, runtimehost.WireIMSRegistrar{
		Network:          "udp4",
		ContactHost:      "10.0.0.1",
		Expires:          600,
		DisableRefresh:   true,
		DisableKeepalive: true,
		Timeout:          2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := registrar.RegisterIMS(context.Background(), runtimehost.IMSRegistrationConfig{
		DeviceID: "device-1",
		TraceID:  "trace-1",
		Profile:  identity.Profile{IMSI: "001010123456789", MCC: "001", MNC: "01"},
		Prepared: &identity.PreparedSession{IMSIdentity: identity.IMSIdentityResolution{
			IMPI: "user@ims.test", IMPU: "sip:user@ims.test", Domain: "ims.test",
		}, PCSCFFQDNs: []string{"pcscf.ims.test"}},
		Tunnel: swu.TunnelResult{DNSServers: []string{"10.0.0.2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Registered || result.StatusCode != 200 || result.Close == nil {
		t.Fatalf("registration result = %+v", result)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := result.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	close(requests)
	var got []voiceclient.SIPIncomingRequest
	for request := range requests {
		got = append(got, request)
	}
	if len(got) != 2 || got[0].Method != "REGISTER" || got[1].Method != "REGISTER" {
		t.Fatalf("requests = %+v", got)
	}
	if expires := firstHeader(got[0].Headers, "Expires"); expires != "600" {
		t.Fatalf("initial Expires = %q", expires)
	}
	if expires := firstHeader(got[1].Headers, "Expires"); expires != "0" {
		t.Fatalf("deregister Expires = %q", expires)
	}
	if dnsRequests.Load() == 0 {
		t.Fatal("P-CSCF lookup did not traverse userspace DNS")
	}
	if err := dnsConn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-dnsDone; err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

func serveDNS(connection net.PacketConn, requests *atomic.Int32, done chan<- error) {
	buffer := make([]byte, 512)
	for {
		count, source, err := connection.ReadFrom(buffer)
		if err != nil {
			done <- err
			return
		}
		requests.Add(1)
		var parser dnsmessage.Parser
		header, err := parser.Start(buffer[:count])
		if err != nil {
			done <- err
			return
		}
		question, err := parser.Question()
		if err != nil {
			done <- err
			return
		}
		builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
			ID: header.ID, Response: true, RecursionAvailable: true,
		})
		builder.EnableCompression()
		if err = builder.StartQuestions(); err == nil {
			err = builder.Question(question)
		}
		if err == nil && question.Type == dnsmessage.TypeA {
			err = builder.StartAnswers()
			if err == nil {
				err = builder.AResource(dnsmessage.ResourceHeader{
					Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30,
				}, dnsmessage.AResource{A: [4]byte{10, 0, 0, 2}})
			}
		}
		var response []byte
		if err == nil {
			response, err = builder.Finish()
		}
		if err == nil {
			_, err = connection.WriteTo(response, source)
		}
		if err != nil {
			done <- err
			return
		}
	}
}

func TestRegistrarRejectsNetworkingThatCouldEscapeUserspaceStack(t *testing.T) {
	clientStack, _ := openStackPair(t)
	registrar, err := NewRegistrar(clientStack, runtimehost.WireIMSRegistrar{
		LocalAddr: "127.0.0.1:0",
	})
	if registrar != nil || !errors.Is(err, ErrUntrustedNetworking) {
		t.Fatalf("NewRegistrar() = %v, %v", registrar, err)
	}
}

func firstHeader(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}
