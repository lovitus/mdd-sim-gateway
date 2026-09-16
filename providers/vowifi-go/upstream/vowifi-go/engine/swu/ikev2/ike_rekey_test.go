package ikev2

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
)

func TestMDDIKERekeyAuthenticatesOldSAAndDerivesFreshKeys(t *testing.T) {
	for _, mode := range []string{"valid", "old SPI", "wrong KE", "missing nonce", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			old, keys := informationalFixture(t)
			old.SelectedSA = DefaultIKEProposalForDH(DHGroup2048BitMODP)
			old.NATDetected = true
			oldKey := append([]byte(nil), keys.SKD...)
			var expected IKEKeys
			var first []byte
			calls := 0
			transport := mddExchangeFunc(func(_ context.Context, request []byte) ([]byte, error) {
				calls++
				if calls == 1 {
					first = append([]byte(nil), request...)
					return nil, context.DeadlineExceeded
				}
				if !bytes.Equal(first, request) {
					t.Fatal("IKE rekey regenerated retransmission")
				}
				message, inner, err := UnprotectMessage(request, keys, true)
				if err != nil {
					t.Fatal(err)
				}
				if message.Header.InitiatorSPI != old.InitiatorSPI || message.Header.ResponderSPI != old.ResponderSPI || message.Header.MessageID != 7 {
					t.Fatal("request not protected by old IKE SA")
				}
				if mode == "rejected" {
					_, raw, err := ProtectMessage(createChildHeader(old, 7, false), keys, false, []Payload{NotifyWithZeroSPI(NotifyNoProposalChosen, nil)}, nil)
					return raw, err
				}
				selected, _, err := firstSecurityAssociation(inner)
				if err != nil {
					t.Fatal(err)
				}
				if selected.Proposals[0].ProtocolID != ProtocolIKE || len(selected.Proposals[0].SPI) != 8 {
					t.Fatal("not an IKE proposal")
				}
				spiI := binary.BigEndian.Uint64(selected.Proposals[0].SPI)
				nonceI, err := firstNonce(inner)
				if err != nil {
					t.Fatal(err)
				}
				var ke KeyExchange
				for _, p := range inner {
					if p.Type == PayloadTSi || p.Type == PayloadTSr {
						t.Fatal("IKE rekey changed CHILD selectors")
					}
					if p.Type == PayloadKE {
						ke, err = ParseKeyExchange(p.Body)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				peer, err := newInitDH(ke.DHGroup, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				shared, err := peer.shared(ke.KeyData)
				if err != nil {
					t.Fatal(err)
				}
				spiR := uint64(0x2122232425262728)
				if mode == "old SPI" {
					spiR = old.ResponderSPI
				}
				selected.Proposals[0].SPI = make([]byte, 8)
				binary.BigEndian.PutUint64(selected.Proposals[0].SPI, spiR)
				nonceR := bytes.Repeat([]byte{0x72}, 32)
				seed := append(append(append([]byte(nil), shared...), nonceI...), nonceR...)
				skeyseed, err := PRF(keys.Profile.PRF, keys.SKD, seed)
				if err != nil {
					t.Fatal(err)
				}
				profile, err := KeyMaterialProfileFromSA(selected)
				if err != nil {
					t.Fatal(err)
				}
				material, err := DeriveIKESAKeyMaterial(profile.PRF, skeyseed, nonceI, nonceR, spiI, spiR, profile.RequiredLength())
				if err != nil {
					t.Fatal(err)
				}
				expected, err = SplitIKEKeys(profile, material)
				if err != nil {
					t.Fatal(err)
				}
				sa, err := SecurityAssociationPayload(selected)
				if err != nil {
					t.Fatal(err)
				}
				group := ke.DHGroup
				if mode == "wrong KE" {
					group = DHGroup1024BitMODP
				}
				payloads := []Payload{sa, KeyExchangePayload(group, peer.public)}
				if mode != "missing nonce" {
					payloads = append(payloads, NoncePayload(nonceR))
				}
				_, raw, err := ProtectMessage(createChildHeader(old, 7, false), keys, false, payloads, nil)
				return raw, err
			})
			next, err := RunIKESARekey(context.Background(), RetransmitTransport{Transport: transport}, old, 7, nil)
			if mode != "valid" {
				if err == nil {
					t.Fatal("invalid rekey accepted")
				}
				if mode == "rejected" && !errors.Is(err, ErrNotifyNoProposalChosen) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || next.InitiatorSPI == old.InitiatorSPI || next.ResponderSPI == old.ResponderSPI || !bytes.Equal(next.Keys.SKD, expected.SKD) || !bytes.Equal(next.Keys.SKEr, expected.SKEr) || !bytes.Equal(old.Keys.SKD, oldKey) || !next.NATDetected {
				t.Fatal("key derivation or old-SA preservation failed")
			}
		})
	}
}
