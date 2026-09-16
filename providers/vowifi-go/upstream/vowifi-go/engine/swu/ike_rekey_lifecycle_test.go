package swu

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/engine/swu/ikev2"
)

func TestMDDIKECutoverDeletesOldSAAndResetsNewMessageSpace(t *testing.T) {
	for _, badDelete := range []bool{false, true} {
		old := ikeControlInit(t)
		old.SelectedSA = ikev2.DefaultIKEProposalForDH(ikev2.DHGroupCurve25519)
		child := packetChildSA(true)
		var next ikev2.InitResult
		installed, retired := false, false
		control := &ikePacketTunnelControl{init: old, keys: old.Keys, child: child, nextMessageID: 7,
			ikeInstalled: func(previous, current ikev2.InitResult) error {
				if previous.InitiatorSPI != old.InitiatorSPI {
					t.Fatal("wrong prior SA")
				}
				next = current
				installed = true
				return nil
			},
			ikeRetired: func(previous ikev2.InitResult) {
				if previous.InitiatorSPI != old.InitiatorSPI {
					t.Fatal("retired wrong SA")
				}
				retired = true
			},
		}
		control.transport = mddIKEExchangeFunc(func(_ context.Context, raw []byte) ([]byte, error) {
			header, err := ikev2.ParseHeader(raw)
			if err != nil {
				t.Fatal(err)
			}
			if header.ExchangeType == ikev2.ExchangeCREATE_CHILD_SA {
				_, inner, err := ikev2.UnprotectMessage(raw, old.Keys, true)
				if err != nil {
					t.Fatal(err)
				}
				var sa ikev2.SecurityAssociation
				var ke ikev2.KeyExchange
				for _, p := range inner {
					switch p.Type {
					case ikev2.PayloadSA:
						sa, err = ikev2.ParseSecurityAssociation(p.Body)
					case ikev2.PayloadKE:
						ke, err = ikev2.ParseKeyExchange(p.Body)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if ke.DHGroup != ikev2.DHGroupCurve25519 {
					t.Fatal("changed negotiated group")
				}
				private, err := ecdh.X25519().GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				sa.Proposals[0].SPI = make([]byte, 8)
				binary.BigEndian.PutUint64(sa.Proposals[0].SPI, 0x3132333435363738)
				payload, err := ikev2.SecurityAssociationPayload(sa)
				if err != nil {
					t.Fatal(err)
				}
				header.Flags = ikev2.FlagResponse
				_, response, err := ikev2.ProtectMessage(header, old.Keys, false, []ikev2.Payload{payload, ikev2.NoncePayload(bytes.Repeat([]byte{0x73}, 32)), ikev2.KeyExchangePayload(ke.DHGroup, private.PublicKey().Bytes())}, nil)
				return response, err
			}
			if header.InitiatorSPI == old.InitiatorSPI {
				if !installed || header.MessageID != 8 {
					t.Fatal("DELETE before new context installation")
				}
				_, inner, err := ikev2.ParseInformationalRequest(raw, old, old.Keys, 8)
				if err != nil {
					t.Fatal(err)
				}
				deleted, err := ikev2.ParseDelete(inner[0].Body)
				if err != nil || deleted.ProtocolID != ikev2.ProtocolIKE {
					t.Fatal("old retirement altered CHILD SAs")
				}
				var response []ikev2.Payload
				if badDelete {
					p, _ := ikev2.ESPDeletePayload(child.RemoteSPI)
					response = []ikev2.Payload{p}
				}
				_, wire, err := ikev2.BuildInformationalResponse(old, old.Keys, 8, response, nil)
				return wire, err
			}
			if !retired || header.MessageID != 0 || header.InitiatorSPI != next.InitiatorSPI {
				t.Fatal("new request reused old IKE message counter")
			}
			if _, _, err := ikev2.ParseInformationalRequest(raw, next, next.Keys, 0); err != nil {
				t.Fatal(err)
			}
			_, wire, err := ikev2.BuildInformationalResponse(next, next.Keys, 0, nil, nil)
			return wire, err
		})
		err := control.rekeyIKE(context.Background())
		if badDelete {
			if !errors.Is(err, ikev2.ErrExchangeUncertain) || retired {
				t.Fatal("bad retirement reported success")
			}
			continue
		}
		if err != nil || !installed || !retired {
			t.Fatal(err)
		}
		if !bytes.Equal(control.child.Keys.Outbound.EncryptionKey, child.Keys.Outbound.EncryptionKey) || !bytes.Equal(control.child.RemoteSPI, child.RemoteSPI) {
			t.Fatal("IKE rekey replaced live CHILD")
		}
		if err := control.dpd(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMDDIKEScheduleAndFailurePreserveOrInvalidateCorrectState(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		calls := 0
		session, err := NewPacketSession(PacketSessionConfig{ChildSA: packetChildSA(true), Transport: &captureESPPacketTransport{}, IKERekeyLifetime: 10 * time.Hour,
			IKERekeyHandler: func(context.Context) error {
				calls++
				if uncertain {
					return ikev2.ErrExchangeUncertain
				}
				return ikev2.ErrNotifyNoProposalChosen
			}})
		if err != nil {
			t.Fatal(err)
		}
		due, enabled := session.NextIKESARekeyDue()
		if !enabled {
			t.Fatal("enabled IKE timer missing")
		}
		if err := session.RunIKESARekeyDue(context.Background(), due.Add(-time.Second)); err != nil || calls != 0 {
			t.Fatal("IKE rekey ran early")
		}
		if err := session.RunIKESARekeyDue(context.Background(), due); err == nil {
			t.Fatal("failure hidden")
		}
		if session.Result().Ready == uncertain {
			t.Fatal("uncertainty/refusal lost its lifecycle distinction")
		}
	}
	control := &ikePacketTunnelControl{nextMessageID: ^uint32(0)}
	if id, err := control.reserveMessageID(); err != nil || id != ^uint32(0) {
		t.Fatal(err)
	}
	if _, err := control.reserveMessageID(); err == nil {
		t.Fatal("counter silently wrapped")
	}
}
