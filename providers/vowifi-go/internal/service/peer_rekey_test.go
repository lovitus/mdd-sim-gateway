// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"bytes"
	"testing"

	"github.com/boa-z/vowifi-go/engine/swu/ikev2"
)

func TestPeerRekeyPreservesLegacyDefaultClassificationWithoutReplacingSA(t *testing.T) {
	for _, test := range []struct {
		name          string
		spi           []byte
		ike, previous bool
		want          uint16
	}{
		{name: "additional bearer", want: ikev2.NotifyNoAdditionalSAs},
		{name: "known ESP rekey", spi: []byte{5, 6, 7, 8}, want: ikev2.NotifyNoProposalChosen},
		{name: "previous ESP rekey", spi: []byte{5, 6, 7, 8}, previous: true, want: ikev2.NotifyNoProposalChosen},
		{name: "unknown ESP SPI", spi: []byte{9, 9, 9, 9}, want: ikev2.NotifyInvalidSPI},
		{name: "local SPI is not peer SPI", spi: []byte{1, 2, 3, 4}, want: ikev2.NotifyInvalidSPI},
		{name: "IKE rekey", ike: true, want: ikev2.NotifyNoProposalChosen},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer, init := peerFixture(t)
			if test.previous {
				peer.setChild([]byte{9, 10, 11, 12}, []byte{13, 14, 15, 16})
			}
			beforeLocal, beforeRemote := append([]byte(nil), peer.child.LocalSPI...), append([]byte(nil), peer.child.RemoteSPI...)
			payloads, _, _, err := ikev2.BuildCreateChildSAPayloads(ikev2.CreateChildSAConfig{ChildSPI: []byte{21, 22, 23, 24}, RekeySPI: test.spi, Nonce: bytes.Repeat([]byte{0x42}, 32)})
			if err != nil {
				t.Fatal(err)
			}
			if test.ike {
				sa := ikev2.DefaultIKEProposal()
				sa.Proposals[0].SPI = []byte{31, 32, 33, 34, 35, 36, 37, 38}
				p, err := ikev2.SecurityAssociationPayload(sa)
				if err != nil {
					t.Fatal(err)
				}
				payloads = []ikev2.Payload{p, ikev2.NoncePayload(bytes.Repeat([]byte{0x43}, 32))}
			}
			_, request, err := ikev2.ProtectMessage(ikev2.Header{InitiatorSPI: init.InitiatorSPI, ResponderSPI: init.ResponderSPI, ExchangeType: ikev2.ExchangeCREATE_CHILD_SA, MessageID: 0}, init.Keys, false, payloads, nil)
			if err != nil {
				t.Fatal(err)
			}
			reply, err := peer.handle(request)
			if err != nil {
				t.Fatal(err)
			}
			message, inner, err := ikev2.UnprotectMessage(reply.Packet, init.Keys, true)
			if err != nil || message.Header.ExchangeType != ikev2.ExchangeCREATE_CHILD_SA || message.Header.MessageID != 0 || len(inner) != 1 {
				t.Fatal("invalid protected rejection")
			}
			notify, err := ikev2.ParseNotify(inner[0].Body)
			if err != nil || notify.NotifyType != test.want {
				t.Fatalf("notify=%d want=%d err=%v", notify.NotifyType, test.want, err)
			}
			if reply.Abort != nil || reply.AfterSend != nil || !bytes.Equal(peer.child.LocalSPI, beforeLocal) || !bytes.Equal(peer.child.RemoteSPI, beforeRemote) {
				t.Fatal("refusal changed live SA or triggered recovery")
			}
			duplicate, err := peer.handle(request)
			if err != nil || !bytes.Equal(reply.Packet, duplicate.Packet) {
				t.Fatal("refusal retransmission was not exact")
			}
		})
	}
}
