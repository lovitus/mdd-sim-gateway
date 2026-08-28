// SPDX-License-Identifier: AGPL-3.0-only

package provider

import (
	"context"
	"errors"
	"sync"
	"testing"

	upstreamsim "github.com/boa-z/vowifi-go/engine/sim"
	upstreamswu "github.com/boa-z/vowifi-go/engine/swu"
)

type fakeManager struct {
	mu      sync.Mutex
	config  upstreamswu.TunnelConfig
	session upstreamswu.TunnelSession
	err     error
}

type cancelingManager struct {
	cancel  context.CancelFunc
	session upstreamswu.TunnelSession
}

func (manager cancelingManager) EstablishTunnel(context.Context, upstreamswu.TunnelConfig) (upstreamswu.TunnelSession, error) {
	manager.cancel()
	return manager.session, nil
}

func (manager *fakeManager) EstablishTunnel(_ context.Context, config upstreamswu.TunnelConfig) (upstreamswu.TunnelSession, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.config = config
	return manager.session, manager.err
}

type fakePacketSession struct {
	mu       sync.Mutex
	result   upstreamswu.TunnelResult
	inbound  []byte
	outbound []byte
	closed   bool
}

func (session *fakePacketSession) Result() upstreamswu.TunnelResult { return session.result }
func (session *fakePacketSession) MOBIKE(context.Context, upstreamswu.MOBIKERequest) (upstreamswu.MOBIKEResult, error) {
	return upstreamswu.MOBIKEResult{}, nil
}
func (session *fakePacketSession) Close(context.Context) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.closed = true
	return nil
}
func (session *fakePacketSession) SendInnerPacket(_ context.Context, packet []byte) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.outbound = append([]byte(nil), packet...)
	return nil
}
func (session *fakePacketSession) SendInnerPacketWithNextHeader(ctx context.Context, _ uint8, packet []byte) error {
	return session.SendInnerPacket(ctx, packet)
}
func (session *fakePacketSession) ReceiveESPPacket(context.Context, []byte) (upstreamswu.PacketTunnelPacket, error) {
	return upstreamswu.PacketTunnelPacket{}, nil
}
func (session *fakePacketSession) PacketStats() upstreamswu.PacketTunnelStats {
	return upstreamswu.PacketTunnelStats{}
}
func (session *fakePacketSession) ReadInnerPacket(context.Context) (upstreamswu.PacketTunnelPacket, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	return upstreamswu.PacketTunnelPacket{Payload: append([]byte(nil), session.inbound...)}, nil
}

type fakeAKA struct{}

func (fakeAKA) CalculateAKA([]byte, []byte) (upstreamsim.AKAResult, error) {
	return upstreamsim.AKAResult{}, nil
}

type fakePlainSession struct {
	result upstreamswu.TunnelResult
	closed bool
}

func (session *fakePlainSession) Result() upstreamswu.TunnelResult { return session.result }
func (session *fakePlainSession) MOBIKE(context.Context, upstreamswu.MOBIKERequest) (upstreamswu.MOBIKEResult, error) {
	return upstreamswu.MOBIKEResult{}, nil
}
func (session *fakePlainSession) Close(context.Context) error {
	session.closed = true
	return nil
}

func readySession() *fakePacketSession {
	return &fakePacketSession{
		result: upstreamswu.TunnelResult{
			Ready: true, IKEEstablished: true, IPsecEstablished: true,
			Mode:        upstreamswu.DataplaneModeUserspace,
			EPDGAddress: "epdg.example", LocalInnerIP: "10.0.0.2",
			DNSServers:   []string{"10.0.0.53"},
			PCSCFServers: []string{"10.0.0.54"},
		},
		inbound: []byte{0x45, 0, 0, 20},
	}
}

func validConfig() Config {
	return Config{LineID: "line-1", DeviceID: "device-1", IMSI: "234101234567890", MCC: "234", MNC: "10"}
}

func TestOpenForcesUserspaceAndBridgesPackets(t *testing.T) {
	base := readySession()
	manager := &fakeManager{session: base}
	provider, err := NewWithManager(manager)
	if err != nil {
		t.Fatal(err)
	}
	session, err := provider.Open(context.Background(), validConfig())
	if err != nil {
		t.Fatal(err)
	}
	info := session.Info()
	if len(info.PCSCFServers) != 1 || info.PCSCFServers[0] != "10.0.0.54" {
		t.Fatalf("P-CSCF info=%+v", info.PCSCFServers)
	}
	info.PCSCFServers[0] = "198.51.100.54"
	if got := session.Info().PCSCFServers[0]; got != "10.0.0.54" {
		t.Fatalf("Info() leaked P-CSCF slice, got %q", got)
	}
	manager.mu.Lock()
	opened := manager.config
	manager.mu.Unlock()
	if opened.Mode != upstreamswu.DataplaneModeUserspace || opened.LocalInterface != "" {
		t.Fatalf("upstream tunnel config = %+v", opened)
	}
	outbound := []byte{0x60, 0, 0, 0}
	if err := session.Send(context.Background(), outbound); err != nil {
		t.Fatal(err)
	}
	outbound[0] = 0
	base.mu.Lock()
	if base.outbound[0] != 0x60 {
		t.Fatal("outbound packet was not copied")
	}
	base.mu.Unlock()
	inbound, err := session.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	inbound[0] = 0
	base.mu.Lock()
	if base.inbound[0] != 0x45 {
		t.Fatal("inbound packet escaped without a copy")
	}
	base.mu.Unlock()
	info = session.Info()
	info.DNSServers[0] = "modified"
	if session.Info().DNSServers[0] != "10.0.0.53" {
		t.Fatal("session info DNS escaped without a copy")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.Send(context.Background(), []byte{0x45}); !errors.Is(err, ErrProviderSessionClose) {
		t.Fatalf("send after close error = %v", err)
	}
}

func TestOpenRejectsNonPacketOrKernelSessionAndClosesIt(t *testing.T) {
	for name, mutate := range map[string]func(*fakePacketSession){
		"not ready": func(session *fakePacketSession) { session.result.Ready = false },
		"kernel":    func(session *fakePacketSession) { session.result.Mode = upstreamswu.DataplaneModeKernel },
	} {
		t.Run(name, func(t *testing.T) {
			base := readySession()
			mutate(base)
			provider, _ := NewWithManager(&fakeManager{session: base})
			if _, err := provider.Open(context.Background(), validConfig()); err == nil {
				t.Fatal("invalid upstream session was accepted")
			}
			base.mu.Lock()
			defer base.mu.Unlock()
			if !base.closed {
				t.Fatal("rejected upstream session was not closed")
			}
		})
	}
}

func TestOpenRejectsTunnelWithoutPacketInterface(t *testing.T) {
	plain := &fakePlainSession{result: upstreamswu.TunnelResult{
		Ready: true, IKEEstablished: true, IPsecEstablished: true,
		Mode: upstreamswu.DataplaneModeUserspace,
	}}
	provider, _ := NewWithManager(&fakeManager{session: plain})
	if _, err := provider.Open(context.Background(), validConfig()); !errors.Is(err, ErrPacketSessionNeeded) {
		t.Fatalf("plain session error = %v", err)
	}
	if !plain.closed {
		t.Fatal("rejected plain session was not closed")
	}
}

func TestOpenClosesSessionWhenCallerCancelsDuringEstablishment(t *testing.T) {
	base := readySession()
	ctx, cancel := context.WithCancel(context.Background())
	provider, _ := NewWithManager(cancelingManager{cancel: cancel, session: base})
	if _, err := provider.Open(ctx, validConfig()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled establishment error = %v", err)
	}
	base.mu.Lock()
	defer base.mu.Unlock()
	if !base.closed {
		t.Fatal("session returned after cancellation was not closed")
	}
}

func TestNewUpstreamRequiresAgentAKAProvider(t *testing.T) {
	if _, err := NewUpstream(upstreamswu.IKEPacketTunnelManagerConfig{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing SIM error = %v", err)
	}
	provider, err := NewUpstream(upstreamswu.IKEPacketTunnelManagerConfig{SIM: fakeAKA{}})
	if err != nil || provider == nil {
		t.Fatalf("real upstream adapter did not compile: provider=%v error=%v", provider, err)
	}
}
