package ikev2

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type mddExchangeFunc func(context.Context, []byte) ([]byte, error)

func (f mddExchangeFunc) ExchangeIKE(ctx context.Context, raw []byte) ([]byte, error) {
	return f(ctx, raw)
}

func TestMDDPFSRekeyDerivesBothDirectionsFromFreshDHAndNonces(t *testing.T) {
	for _, mode := range []string{"valid", "missing KE", "wrong KE", "missing selected DH"} {
		t.Run(mode, func(t *testing.T) {
			init, keys := informationalFixture(t)
			var expected ChildSAKeys
			requests := 0
			var first []byte
			transport := mddExchangeFunc(func(_ context.Context, raw []byte) ([]byte, error) {
				requests++
				if requests == 1 {
					first = append([]byte(nil), raw...)
					return nil, context.DeadlineExceeded
				}
				if !bytes.Equal(raw, first) {
					t.Fatal("retry changed nonce, KE, IV or Message ID")
				}
				message, inner, err := UnprotectMessage(raw, keys, true)
				if err != nil {
					t.Fatal(err)
				}
				selected, found, err := firstSecurityAssociation(inner)
				if err != nil || !found {
					t.Fatal("missing SA")
				}
				group, err := firstDHGroup(selected)
				if err != nil || group != DHGroup2048BitMODP {
					t.Fatal("missing PFS proposal")
				}
				var requestKE KeyExchange
				for _, p := range inner {
					if p.Type == PayloadKE {
						requestKE, err = ParseKeyExchange(p.Body)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				peer, err := newInitDH(group, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				shared, err := peer.shared(requestKE.KeyData)
				if err != nil {
					t.Fatal(err)
				}
				nonceI, err := firstNonce(inner)
				if err != nil {
					t.Fatal(err)
				}
				nonceR := bytes.Repeat([]byte{0x6b}, 32)
				selected.Proposals[0].SPI = []byte{5, 6, 7, 8}
				expected, err = DeriveChildSAKeysWithPFS(keys.Profile.PRF, keys.SKD, shared, nonceI, nonceR, selected)
				if err != nil {
					t.Fatal(err)
				}
				seed := append(append(append([]byte(nil), shared...), nonceI...), nonceR...)
				keymat, err := PRFPlus(keys.Profile.PRF, keys.SKD, seed, expected.Profile.DirectionKeyLength()*2)
				if err != nil {
					t.Fatal(err)
				}
				joined := append(append(append(append([]byte(nil), expected.Outbound.EncryptionKey...), expected.Outbound.IntegrityKey...), expected.Inbound.EncryptionKey...), expected.Inbound.IntegrityKey...)
				if !bytes.Equal(joined, keymat) {
					t.Fatal("RFC key direction or DH/nonce seed ordering changed")
				}
				without, err := DeriveChildSAKeysWithNonces(keys.Profile.PRF, keys.SKD, nonceI, nonceR, selected)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Equal(expected.Inbound.EncryptionKey, without.Inbound.EncryptionKey) {
					t.Fatal("PFS contribution omitted")
				}
				if mode == "missing selected DH" {
					selected.Proposals[0].Transforms = selected.Proposals[0].Transforms[:len(selected.Proposals[0].Transforms)-1]
				}
				sa, err := SecurityAssociationPayload(selected)
				if err != nil {
					t.Fatal(err)
				}
				ti, _ := TrafficSelectorsPayload(PayloadTSi, IPv4AnyTrafficSelectors())
				tr, _ := TrafficSelectorsPayload(PayloadTSr, IPv4AnyTrafficSelectors())
				response := []Payload{sa, NoncePayload(nonceR), ti, tr}
				if mode != "missing KE" {
					g := group
					if mode == "wrong KE" {
						g = DHGroup1024BitMODP
					}
					response = append(response, KeyExchangePayload(g, peer.public))
				}
				_, wire, err := ProtectMessage(createChildHeader(init, message.Header.MessageID, false), keys, false, response, nil)
				return wire, err
			})
			result, err := RunCREATE_CHILD_SA(context.Background(), CreateChildSAConfig{Transport: RetransmitTransport{Transport: transport}, Init: init, MessageID: 7, ChildSPI: []byte{1, 2, 3, 4}, RekeySPI: []byte{9, 10, 11, 12}, PFSGroup: DHGroup2048BitMODP})
			if mode != "valid" {
				if err == nil {
					t.Fatal("invalid PFS response accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if requests != 2 || !bytes.Equal(result.ChildSA.Keys.Outbound.EncryptionKey, expected.Outbound.EncryptionKey) || !bytes.Equal(result.ChildSA.Keys.Inbound.IntegrityKey, expected.Inbound.IntegrityKey) {
				t.Fatal("PFS key derivation or exact retransmission failed")
			}
		})
	}
}

func TestMDDRetransmissionIsBoundedAndDoesNotRetryAResponse(t *testing.T) {
	calls := 0
	transport := RetransmitTransport{Transport: mddExchangeFunc(func(context.Context, []byte) ([]byte, error) { calls++; return nil, context.DeadlineExceeded })}
	if _, err := transport.ExchangeIKE(context.Background(), []byte("immutable test wire")); !errors.Is(err, ErrExchangeUncertain) || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	calls = 0
	transport.Transport = mddExchangeFunc(func(context.Context, []byte) ([]byte, error) {
		calls++
		return []byte("protocol rejection decoded by caller"), nil
	})
	if _, err := transport.ExchangeIKE(context.Background(), nil); err != nil || calls != 1 {
		t.Fatal("response retried")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	if _, err := transport.ExchangeIKE(ctx, nil); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancelled exchange submitted work")
	}
}
