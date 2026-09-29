// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/imssec"
)

func TestRegistrarReceivesPeerInitiatedTCPAndClosesIt(t *testing.T) {
	client, carrier := openStackPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listener, err := carrier.Listen(ctx, "tcp4", "10.0.0.2:5060")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(conn)
		for i := 0; i < 2; i++ {
			raw, err := voiceclient.ReadSIPStreamMessage(reader)
			if err != nil {
				done <- err
				return
			}
			req, err := voiceclient.ParseSIPRequest(raw)
			if err != nil {
				done <- err
				return
			}
			wire, err := voiceclient.BuildSIPResponseWire(req, 200, "OK", map[string]string{"P-Associated-URI": "<sip:user@ims.test>"}, nil)
			if err == nil {
				_, err = conn.Write(wire)
			}
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	seen := make(chan string, 1)
	registrar, err := NewRegistrar(client, runtimehost.WireIMSRegistrar{
		Network: "tcp4", ServerAddr: "10.0.0.2:5060", ContactHost: "10.0.0.1", ContactPort: 5060,
		Expires: 600, DisableRefresh: true, DisableKeepalive: true, Timeout: time.Second,
		IncomingHandler: voiceclient.SIPIncomingRequestHandlerFunc(func(_ context.Context, request voiceclient.SIPIncomingRequest) []voiceclient.SIPIncomingResponse {
			seen <- request.Method
			return []voiceclient.SIPIncomingResponse{{StatusCode: 180, Reason: "Ringing"}}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	registration, err := registrar.RegisterIMS(ctx, runtimehost.IMSRegistrationConfig{
		DeviceID: "incoming-device", TraceID: "incoming-trace",
		Profile:  identity.Profile{IMSI: "001010123456789", MCC: "001", MNC: "01"},
		Prepared: &identity.PreparedSession{IMSIdentity: identity.IMSIdentityResolution{IMPI: "user@ims.test", IMPU: "sip:user@ims.test", Domain: "ims.test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close(ctx)
	conn, err := carrier.DialContextLocal(ctx, "tcp4", "10.0.0.2:5062", "10.0.0.1:5060")
	if err != nil {
		t.Fatalf("REGISTER succeeded but incoming Contact port cannot be reached: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	wire := []byte("INVITE sip:user@ims.test SIP/2.0\r\nVia: SIP/2.0/TCP 10.0.0.2:5062;branch=z9hG4bK-incoming\r\nFrom: <sip:peer@ims.test>;tag=peer\r\nTo: <sip:user@ims.test>\r\nCall-ID: peer-initiated\r\nCSeq: 1 INVITE\r\nContent-Length: 0\r\n\r\n")
	if _, err = conn.Write(wire); err != nil {
		t.Fatal(err)
	}
	raw, err := voiceclient.ReadSIPStreamMessage(bufio.NewReader(conn))
	if err != nil {
		t.Fatal(err)
	}
	response, err := voiceclient.ParseSIPResponse(raw)
	if err != nil || response.StatusCode != 180 {
		t.Fatalf("incoming response=%+v err=%v", response, err)
	}
	select {
	case method := <-seen:
		if method != "INVITE" {
			t.Fatal(method)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err = registration.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("deregister retained incoming connection")
	}
}

func TestSecurityInstallerAcceptsPeerInitiatedTCP(t *testing.T) {
	client, carrier := openStackPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	key := bytes.Repeat([]byte{0x42}, 16)
	clientAgreement := voiceclient.SecurityAgreement{Protocol: voiceclient.DefaultSecurityProtocol, Algorithm: "hmac-sha-1-96", EncryptionAlgorithm: "null", SPIClient: 1001, SPIServer: 1002, PortClient: 6062, PortServer: 6063}
	serverAgreement := voiceclient.SecurityAgreement{Protocol: voiceclient.DefaultSecurityProtocol, Algorithm: "hmac-sha-1-96", EncryptionAlgorithm: "null", SPIClient: 2001, SPIServer: 2002, PortClient: 7062, PortServer: 7063}
	plan, ok := voiceclient.BuildIMSSecurityAssociationPlanForClient(serverAgreement, clientAgreement)
	if !ok {
		t.Fatal("invalid fixture security plan")
	}
	err := (userspaceSecurityInstaller{stack: client}).InstallSecurityPlanRequest(ctx, voiceclient.IMSSecurityAssociationInstallRequest{
		Plan: plan, ClientAgreement: clientAgreement, Agreement: serverAgreement, AKA: voiceclient.IMSSecurityAKAKeys{IK: key},
		LocalEndpoint: voiceclient.IMSSecurityAssociationEndpoint{Address: "10.0.0.1"}, RemoteEndpoint: voiceclient.IMSSecurityAssociationEndpoint{Address: "10.0.0.2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	protector, err := imssec.New(imssec.Config{LocalAddress: netip.MustParseAddr("10.0.0.2"), RemoteAddress: netip.MustParseAddr("10.0.0.1"), LocalPort: 7062, RemotePort: 6063, SPIClient: 2001, SPIServer: 1002, Authentication: "hmac-sha-1-96", Encryption: "null", IntegrityKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if err = carrier.SetPacketProtector(protector); err != nil {
		t.Fatal(err)
	}
	listener, err := client.Listen(ctx, "tcp4", "10.0.0.1:6063")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := carrier.DialContextLocal(ctx, "tcp4", "10.0.0.2:7062", "10.0.0.1:6063")
	if err != nil {
		t.Fatalf("negotiated peer-initiated ESP pair rejected: %v", err)
	}
	defer conn.Close()
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer accepted.Close()
	_ = accepted.SetDeadline(time.Now().Add(time.Second))
	if _, err = fmt.Fprint(conn, "INVITE"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 6)
	if n, err := accepted.Read(got); err != nil || n != 6 || string(got) != "INVITE" {
		t.Fatalf("protected request=%q err=%v", got, err)
	}
}
