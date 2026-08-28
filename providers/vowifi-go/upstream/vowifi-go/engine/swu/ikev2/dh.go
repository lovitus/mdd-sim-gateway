package ikev2

import (
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
)

// RFC 3526 section 3 group 14. This implementation was cross-checked against
// the independently tested MIT implementations in xen0bit/veepin and
// n0madic/go-ipsec; keeping the primitive here avoids importing either VPN
// control plane into the VoWiFi provider.
const modp2048PrimeHex = "" +
	"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1" +
	"29024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
	"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245" +
	"E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
	"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3D" +
	"C2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5F" +
	"83655D23DCA3AD961C62F356208552BB9ED529077096966D" +
	"670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B" +
	"E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9" +
	"DE2BCBF6955817183995497CEA956AE515D2261898FA0510" +
	"15728E5A8AACAA68FFFFFFFFFFFFFFFF"

const modp2048Size = 256

var (
	modp2048Prime, _  = new(big.Int).SetString(modp2048PrimeHex, 16)
	modp2048Generator = big.NewInt(2)
	modp2048MaxPeer   = new(big.Int).Sub(modp2048Prime, big.NewInt(2))
)

type initDH struct {
	group  uint16
	public []byte
	shared func([]byte) ([]byte, error)
}

func firstDHGroup(sa SecurityAssociation) (uint16, error) {
	if len(sa.Proposals) == 0 {
		return 0, fmt.Errorf("%w: no IKE proposal", ErrInvalidInitConfig)
	}
	for _, transform := range sa.Proposals[0].Transforms {
		if transform.Type == TransformDHRGroup {
			return transform.ID, nil
		}
	}
	return 0, fmt.Errorf("%w: first IKE proposal has no DH group", ErrInvalidInitConfig)
}

func newInitDH(group uint16, x25519Private []byte, random io.Reader) (initDH, error) {
	if random == nil {
		random = cryptorand.Reader
	}
	switch group {
	case DHGroupCurve25519:
		private, err := x25519PrivateKey(x25519Private, random)
		if err != nil {
			return initDH{}, err
		}
		return initDH{
			group: group, public: private.PublicKey().Bytes(),
			shared: func(peer []byte) ([]byte, error) {
				public, err := private.Curve().NewPublicKey(peer)
				if err != nil {
					return nil, err
				}
				return private.ECDH(public)
			},
		}, nil
	case DHGroup2048BitMODP:
		// rand.Int returns [0,n); shifting by two yields [2,p-2].
		rangeSize := new(big.Int).Sub(modp2048Prime, big.NewInt(3))
		private, err := cryptorand.Int(random, rangeSize)
		if err != nil {
			return initDH{}, err
		}
		private.Add(private, big.NewInt(2))
		public := new(big.Int).Exp(modp2048Generator, private, modp2048Prime)
		return initDH{
			group: group, public: leftPadMODP(public.Bytes()),
			shared: func(peerBytes []byte) ([]byte, error) {
				if len(peerBytes) != modp2048Size {
					return nil, errors.New("MODP-2048 peer public value has the wrong length")
				}
				peer := new(big.Int).SetBytes(peerBytes)
				if peer.Cmp(big.NewInt(1)) <= 0 || peer.Cmp(modp2048MaxPeer) > 0 {
					return nil, errors.New("MODP-2048 peer public value is out of range")
				}
				return leftPadMODP(new(big.Int).Exp(peer, private, modp2048Prime).Bytes()), nil
			},
		}, nil
	default:
		return initDH{}, fmt.Errorf("%w: unsupported DH group %d", ErrInvalidInitConfig, group)
	}
}

func leftPadMODP(value []byte) []byte {
	out := make([]byte, modp2048Size)
	copy(out[len(out)-len(value):], value)
	return out
}
