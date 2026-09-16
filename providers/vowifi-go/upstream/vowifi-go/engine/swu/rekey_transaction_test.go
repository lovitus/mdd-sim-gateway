package swu

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/engine/swu/esp"
	"github.com/boa-z/vowifi-go/engine/swu/ikev2"
)

type mddIKEExchangeFunc func(context.Context, []byte) ([]byte, error)

func (f mddIKEExchangeFunc) ExchangeIKE(ctx context.Context, packet []byte) ([]byte, error) {
	return f(ctx, packet)
}

func TestMDDTransactionalHooksDoNotEnableDisabledRekeySchedule(t *testing.T) {
	init := ikeControlInit(t)
	child := packetChildSA(true)
	var captured PacketSessionConfig
	manager := NewIKEPacketTunnelManager(IKEPacketTunnelManagerConfig{
		SIM: ikeTunnelAKAProvider{}, Transport: ikeTunnelNoopTransport{}, ESPTransport: &captureESPPacketTransport{},
		TransactionalChildRekey: true, ChildSARekeyDHGroup: ikev2.DHGroup2048BitMODP, ChildSARekey: ChildSARekeyPolicy{Disabled: true},
		InitRunner: func(context.Context, ikev2.InitConfig) (ikev2.InitResult, error) { return init, nil },
		AuthRunner: func(_ context.Context, cfg ikev2.FullAuthConfig) (ikev2.FullAuthResult, error) {
			child.LocalSPI = append([]byte(nil), cfg.ChildSPI...)
			return ikev2.FullAuthResult{ChildSA: &child, NextMessageID: 7}, nil
		},
		PacketSessionFactory: func(cfg PacketSessionConfig) (TunnelSession, error) { captured = cfg; return NewPacketSession(cfg) },
	})
	session, err := manager.EstablishTunnel(context.Background(), TunnelConfig{DeviceID: "dev-1", Mode: DataplaneModeUserspace, EPDGAddress: "192.0.2.10", IMSI: "310280233641503", MCC: "310", MNC: "280"})
	if err != nil {
		t.Fatal(err)
	}
	if captured.RekeyCommitted == nil || captured.RekeyHandler == nil {
		t.Fatal("transaction hooks missing")
	}
	if _, enabled := session.(*PacketSession).NextChildSARekeyDue(); enabled {
		t.Fatal("transaction support enabled a disabled timer")
	}
}

func TestMDDDeleteWaitsForInstalledChildAndReplaysExactRequest(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		init := ikeControlInit(t)
		old, next := packetChildSA(true), packetChildSA(true)
		next.LocalSPI = []byte{1, 2, 3, 4}
		next.RemoteSPI = []byte{5, 6, 7, 8}
		calls := 0
		var first []byte
		control := &ikePacketTunnelControl{init: init, keys: init.Keys, child: old, nextMessageID: 8, pendingChild: &next, transactional: true}
		control.transport = mddIKEExchangeFunc(func(_ context.Context, packet []byte) ([]byte, error) {
			calls++
			if !bytes.Equal(control.child.LocalSPI, next.LocalSPI) {
				t.Fatal("DELETE before control commit")
			}
			message, inner, err := ikev2.ParseInformationalRequest(packet, init, init.Keys, 8)
			if err != nil {
				t.Fatal(err)
			}
			deleted, err := ikev2.ParseDelete(inner[0].Body)
			if err != nil || !bytes.Equal(deleted.SPIs[0], old.LocalSPI) {
				t.Fatal("DELETE targets wrong inbound SPI")
			}
			if calls == 1 {
				first = append([]byte(nil), packet...)
				return nil, context.DeadlineExceeded
			}
			if !bytes.Equal(packet, first) {
				t.Fatal("DELETE retry regenerated nonce/IV/message ID")
			}
			spi := old.RemoteSPI
			if wrong {
				spi = next.RemoteSPI
			}
			payload, err := ikev2.ESPDeletePayload(spi)
			if err != nil {
				t.Fatal(err)
			}
			_, response, err := ikev2.BuildInformationalResponse(init, init.Keys, message.Header.MessageID, []ikev2.Payload{payload}, nil)
			return response, err
		})
		mismatch := next
		mismatch.LocalSPI = []byte{0, 0, 0, 1}
		if err := control.commitChildSA(context.Background(), mismatch); err == nil || calls != 0 {
			t.Fatal("uninstalled child sent DELETE")
		}
		err := control.commitChildSA(context.Background(), next)
		if (err != nil) != wrong || calls != 2 || control.nextMessageID != 9 {
			t.Fatalf("wrong=%v calls=%d err=%v", wrong, calls, err)
		}
	}
}

func TestMDDRekeyReceivesBothSAsUntilAcknowledgedGraceExpires(t *testing.T) {
	old, next := packetChildSA(true), packetChildSA(true)
	next.LocalSPI = []byte{1, 2, 3, 4}
	next.RemoteSPI = []byte{5, 6, 7, 8}
	next.Keys.Inbound.EncryptionKey = bytes.Repeat([]byte{0x5a}, len(next.Keys.Inbound.EncryptionKey))
	oldPeer := packetChildSA(false)
	newPeer := next
	newPeer.LocalSPI, newPeer.RemoteSPI = next.RemoteSPI, next.LocalSPI
	newPeer.Keys.Outbound, newPeer.Keys.Inbound = next.Keys.Inbound, next.Keys.Outbound
	oldSender, err := esp.NewOutboundSAFromChild(oldPeer)
	if err != nil {
		t.Fatal(err)
	}
	newSender, err := esp.NewOutboundSAFromChild(newPeer)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte{0x45, 0, 0, 20, 1, 2, 3, 4}
	seal := func(sa *esp.SA) []byte {
		t.Helper()
		p, err := sa.Seal(esp.NextHeaderIPv4, payload, esp.SealOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	var session *PacketSession
	commitCalls := 0
	session, err = NewPacketSession(PacketSessionConfig{ChildSA: old, Transport: &captureESPPacketTransport{},
		RekeyHandler: func(context.Context) (ikev2.ChildSAResult, error) { return next, nil },
		RekeyCommitted: func(_ context.Context, child ikev2.ChildSAResult) error {
			commitCalls++
			// These acquire the packet mutex; retirement must not hold it across I/O.
			if _, err := session.ReceiveESPPacket(context.Background(), seal(oldSender)); err != nil {
				t.Fatal(err)
			}
			if _, err := session.ReceiveESPPacket(context.Background(), seal(newSender)); err != nil {
				t.Fatal(err)
			}
			if session.Result().ChildSAIdentifier != childSAIdentifier(child) {
				t.Fatal("retirement began before data-plane install")
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	oldFirst := seal(oldSender)
	if _, err := session.ReceiveESPPacket(context.Background(), oldFirst); err != nil {
		t.Fatal(err)
	}
	if _, err := session.RekeyChildSA(context.Background()); err != nil {
		t.Fatal(err)
	}
	if commitCalls != 1 || session.previousInbound == nil || time.Until(session.previousUntil) > 5*time.Second {
		t.Fatal("retirement grace not established")
	}
	if _, err := session.ReceiveESPPacket(context.Background(), oldFirst); !errors.Is(err, esp.ErrReplay) {
		t.Fatalf("old replay window reset: %v", err)
	}
	if _, err := session.ReceiveESPPacket(context.Background(), seal(oldSender)); err != nil {
		t.Fatal("old packet lost during grace", err)
	}
	if _, err := session.RekeyChildSA(context.Background()); !errors.Is(err, ErrChildSAOverlap) {
		t.Fatal("unbounded overlapping SA replacement")
	}
	session.mu.Lock()
	session.previousUntil = time.Now().Add(-time.Second)
	session.mu.Unlock()
	if _, err := session.ReceiveESPPacket(context.Background(), seal(oldSender)); err == nil {
		t.Fatal("retired SPI accepted after grace")
	}
	if _, err := session.ReceiveESPPacket(context.Background(), seal(newSender)); err != nil {
		t.Fatal("new SA broken after retirement", err)
	}
}

func TestMDDUnconfirmedRetirementFailsClosedButExplicitRejectionKeepsSA(t *testing.T) {
	old, next := packetChildSA(true), packetChildSA(true)
	next.LocalSPI = []byte{1, 2, 3, 4}
	next.RemoteSPI = []byte{5, 6, 7, 8}
	for _, phase := range []string{"rejected", "uncertain", "install", "delete"} {
		t.Run(phase, func(t *testing.T) {
			session, err := NewPacketSession(PacketSessionConfig{ChildSA: old, Transport: &captureESPPacketTransport{},
				RekeyHandler: func(context.Context) (ikev2.ChildSAResult, error) {
					switch phase {
					case "rejected":
						return ikev2.ChildSAResult{}, ikev2.ErrNotifyNoProposalChosen
					case "uncertain":
						return ikev2.ChildSAResult{}, ikev2.ErrExchangeUncertain
					case "install":
						return ikev2.ChildSAResult{}, nil
					}
					return next, nil
				}, RekeyCommitted: func(context.Context, ikev2.ChildSAResult) error { return context.DeadlineExceeded }})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := session.RekeyChildSA(context.Background()); err == nil {
				t.Fatal("expected incomplete transaction")
			}
			if phase == "rejected" {
				if !session.Result().Ready || session.rekeyFailure != nil {
					t.Fatal("explicit refusal destroyed current SA")
				}
			} else if session.Result().Ready || !errors.Is(session.rekeyFailure, ErrChildSARetirement) {
				t.Fatal("uncertain transaction remained healthy")
			}
		})
	}
}
