// SPDX-License-Identifier: AGPL-3.0-only
package service

// Test-only process boundary lets the Core integration test use the real
// Provider Backend without violating Go internal-package ownership. Everything
// is in-memory or literal loopback. There is no modem, SIM, paid call or SMS.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	swu "github.com/boa-z/vowifi-go/engine/swu"
	"github.com/boa-z/vowifi-go/engine/swu/ikev2"
	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/provider"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
)

const livenessSimulatorToken = "liveness-simulator-loopback-token-32"

type incidentCounterRuntime struct{ *upstreamRuntime }

func (*incidentCounterRuntime) IKEEvidence() *vowifiipc.IKEExchangeEvidence {
	// Exact production counterexample, not a claimed authenticated response count.
	return &vowifiipc.IKEExchangeEvidence{RequestsSent: 10, ResponseDatagrams: 5, ResponseTimeouts: 6}
}

type livenessSimulatorFactory struct {
	t         *testing.T
	starts    atomic.Int32
	probes    atomic.Int32
	registers atomic.Int32
	drop      atomic.Bool
	imsFail   atomic.Bool
	sip       net.PacketConn
}
type livenessSimulatorTransport struct{}

func (livenessSimulatorTransport) SendESPPacket(context.Context, []byte) error { return nil }
func (livenessSimulatorTransport) ReadESPPacket(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (livenessSimulatorTransport) SendNATTKeepalive(context.Context) error { return nil }
func (livenessSimulatorTransport) Close(context.Context) error             { return nil }

type livenessSimulatorManager struct{ session swu.TunnelSession }

func (m livenessSimulatorManager) EstablishTunnel(context.Context, swu.TunnelConfig) (swu.TunnelSession, error) {
	return m.session, nil
}

type livenessExchangeFunc func(context.Context, []byte) ([]byte, error)

func (f livenessExchangeFunc) ExchangeIKE(ctx context.Context, b []byte) ([]byte, error) {
	return f(ctx, b)
}

func (f *livenessSimulatorFactory) Start(ctx context.Context) (Runtime, error) {
	f.starts.Add(1)
	// Model restored connectivity after the existing Core initiates a fresh
	// line session. The fault-injection endpoints control the established one.
	f.drop.Store(false)
	f.imsFail.Store(false)
	profile, err := ikev2.KeyMaterialProfileFromSA(ikev2.DefaultIKEProposal())
	if err != nil {
		return nil, err
	}
	keys, err := ikev2.SplitIKEKeys(profile, bytes.Repeat([]byte{0x51}, profile.RequiredLength()))
	if err != nil {
		return nil, err
	}
	init := ikev2.InitResult{InitiatorSPI: 0x0102030405060708, ResponderSPI: 0x1112131415161718, Keys: keys}
	var messageID uint32
	exchange := livenessExchangeFunc(func(ctx context.Context, raw []byte) ([]byte, error) {
		f.probes.Add(1)
		if f.drop.Load() {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		header, err := ikev2.ParseHeader(raw)
		if err != nil {
			return nil, err
		}
		if _, _, err := ikev2.ParseInformationalRequest(raw, init, keys, header.MessageID); err != nil {
			return nil, err
		}
		_, wire, err := ikev2.BuildInformationalResponse(init, keys, header.MessageID, nil, nil)
		return wire, err
	})
	state, err := swu.NewIKELivenessState(swu.IKELivenessConfig{DisableKeepalive: true, DPDInterval: 30 * time.Millisecond, DPDTimeout: 10 * time.Millisecond, MaxMissedDPDProbes: 3}, time.Now())
	if err != nil {
		return nil, err
	}
	child := ikev2.ChildSAResult{LocalSPI: []byte{1, 1, 1, 1}, RemoteSPI: []byte{2, 2, 2, 2}, Keys: ikev2.ChildSAKeys{Profile: ikev2.ESPKeyProfile{IntegrityID: ikev2.INTEG_HMAC_SHA2_256_128}, Outbound: ikev2.ESPKeys{EncryptionKey: bytes.Repeat([]byte{0x10}, 16), IntegrityKey: bytes.Repeat([]byte{0x20}, 32)}, Inbound: ikev2.ESPKeys{EncryptionKey: bytes.Repeat([]byte{0x30}, 16), IntegrityKey: bytes.Repeat([]byte{0x40}, 32)}}}
	packet, err := swu.NewPacketSession(swu.PacketSessionConfig{ChildSA: child, Transport: livenessSimulatorTransport{}, Liveness: state, RekeyPolicy: swu.ChildSARekeyPolicy{Disabled: true}, IKERekeyLifetime: 0, Result: swu.TunnelResult{Ready: true, Mode: swu.DataplaneModeUserspace, LocalInnerIP: "10.0.0.2", EPDGAddress: "192.0.2.1", IKEEstablished: true, IPsecEstablished: true}, DPDHandler: func(ctx context.Context) error {
		messageID++
		_, err := ikev2.RunLivenessCheck(ctx, ikev2.InformationalConfig{Init: init, Keys: keys, MessageID: messageID, Transport: exchange})
		return err
	}})
	if err != nil {
		return nil, err
	}
	if _, enabled := packet.NextChildSARekeyDue(); enabled {
		return nil, errors.New("CHILD rekey unexpectedly enabled")
	}
	if _, enabled := packet.NextIKESARekeyDue(); enabled {
		return nil, errors.New("IKE rekey unexpectedly enabled")
	}
	p, _ := provider.NewWithManager(livenessSimulatorManager{packet})
	session, err := p.Open(ctx, provider.Config{LineID: "line-1", DeviceID: "fixture", IMSI: "234100000000001", MCC: "234", MNC: "10"})
	if err != nil {
		return nil, err
	}
	stack, err := usernet.Open(ctx, session, usernet.Config{Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.2")}})
	if err != nil {
		return nil, err
	}
	// The real SIP maintainer uses only this fixture's loopback UDP server.
	registration, err := (runtimehost.WireIMSRegistrar{Network: "udp", ServerAddr: f.sip.LocalAddr().String(), ContactHost: "127.0.0.1", Expires: 1, RefreshInterval: 40 * time.Millisecond, RefreshRetryInterval: 20 * time.Millisecond, RecoveryBackoffInitial: 20 * time.Millisecond, RecoveryBackoffMax: 40 * time.Millisecond, Timeout: 100 * time.Millisecond, DisableKeepalive: true}).RegisterIMS(ctx, runtimehost.IMSRegistrationConfig{DeviceID: "fixture", Profile: identity.Profile{IMSI: "234100000000001", MCC: "234", MNC: "10"}})
	if err != nil {
		_ = stack.Close(ctx)
		return nil, err
	}
	run := &upstreamRuntime{packetSession: session, stack: stack, registration: registration, closeTimeout: time.Second}
	go run.observeStack()
	if os.Getenv("MDD_LIVENESS_INCIDENT_COUNTERS") == "1" {
		return &incidentCounterRuntime{run}, nil
	}
	return run, nil
}

func TestLivenessSimulatorProcess(t *testing.T) {
	if os.Getenv("MDD_LIVENESS_SIMULATOR_CHILD") != "1" {
		t.Skip("invoked only by the loopback Core-to-Provider integration test")
	}
	sip, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sip.Close()
	f := &livenessSimulatorFactory{t: t, sip: sip}
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, addr, err := sip.ReadFrom(buffer)
			if err != nil {
				return
			}
			wire := string(buffer[:n])
			if !strings.HasPrefix(wire, "REGISTER ") {
				continue
			}
			f.registers.Add(1)
			status := "200 OK"
			if f.imsFail.Load() && !strings.Contains(wire, "Expires: 0\r\n") {
				status = "503 Service Unavailable"
			}
			response := "SIP/2.0 " + status + "\r\nContact: <sip:user@127.0.0.1:5060>;expires=1\r\nP-Associated-URI: <sip:user@ims.example>\r\nContent-Length: 0\r\n\r\n"
			_, _ = sip.WriteTo([]byte(response), addr)
		}
	}()
	backend, err := NewBackend("line-1", "provider-1", "provider-generation-1", f)
	if err != nil {
		t.Fatal(err)
	}
	api, err := vowifiipc.NewAPI(backend, livenessSimulatorToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", api)
	done := make(chan struct{}, 1)
	mux.HandleFunc("/simulate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+livenessSimulatorToken {
			http.Error(w, "unauthorized", 401)
			return
		}
		switch r.URL.Query().Get("fault") {
		case "drop":
			f.drop.Store(true)
		case "ims":
			f.imsFail.Store(true)
		case "shutdown":
			select {
			case done <- struct{}{}:
			default:
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]int32{"starts": f.starts.Load(), "probes": f.probes.Load(), "registers": f.registers.Load()})
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			done <- struct{}{}
		}
	}()
	fmt.Println("SIMULATOR_URL=http://" + listener.Addr().String())
	select {
	case <-done:
	case <-time.After(25 * time.Second):
		t.Error("simulator parent did not finish")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = backend.Stop(ctx, vowifiipc.LifecycleRequest{OperationID: "fixture-shutdown"})
	_ = server.Shutdown(ctx)
}
