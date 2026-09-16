package ikev2

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
)

// RunIKESARekey adapts MDD state_ue_create_sa/generate_new_ike_keying_material.
// It never performs another SIM authentication or modifies the CHILD SAs.
func RunIKESARekey(ctx context.Context, transport InitTransport, previous InitResult, messageID uint32, random io.Reader) (InitResult, error) {
	if transport == nil || previous.InitiatorSPI == 0 || previous.ResponderSPI == 0 {
		return InitResult{}, fmt.Errorf("%w: invalid IKE rekey identity", ErrInvalidCreateChild)
	}
	if err := validateKeySet(previous.Keys); err != nil {
		return InitResult{}, err
	}
	if random == nil {
		random = rand.Reader
	}
	offered := cloneSecurityAssociation(previous.SelectedSA)
	if len(offered.Proposals) != 1 || offered.Proposals[0].ProtocolID != ProtocolIKE {
		return InitResult{}, fmt.Errorf("%w: missing selected IKE proposal", ErrInvalidCreateChild)
	}
	group, err := firstDHGroup(offered)
	if err != nil {
		return InitResult{}, err
	}
	dh, err := newInitDH(group, nil, random)
	if err != nil {
		return InitResult{}, err
	}
	spiI, err := randomSPI(random)
	if err != nil {
		return InitResult{}, err
	}
	if spiI == previous.InitiatorSPI {
		return InitResult{}, fmt.Errorf("%w: replacement SPI already owned", ErrInvalidCreateChild)
	}
	spi := make([]byte, 8)
	binary.BigEndian.PutUint64(spi, spiI)
	offered.Proposals[0].SPI = spi
	nonceI, err := randomBytes(random, DefaultNonceLength)
	if err != nil {
		return InitResult{}, err
	}
	sa, err := SecurityAssociationPayload(offered)
	if err != nil {
		return InitResult{}, err
	}
	iv, err := RandomIV(random, previous.Keys.Profile)
	if err != nil {
		return InitResult{}, err
	}
	_, request, err := ProtectMessage(createChildHeader(previous, messageID, true), previous.Keys, true, []Payload{sa, NoncePayload(nonceI), KeyExchangePayload(group, dh.public)}, iv)
	if err != nil {
		return InitResult{}, err
	}
	response, err := transport.ExchangeIKE(ctx, request)
	if err != nil {
		return InitResult{}, err
	}
	_, inner, err := unprotectCreateChildResponse(response, previous, previous.Keys, messageID)
	if err != nil {
		return InitResult{}, err
	}
	if err := FirstNotifyError(inner); err != nil {
		return InitResult{}, err
	}
	var selected SecurityAssociation
	var nonceR []byte
	var peer *KeyExchange
	for _, payload := range inner {
		switch payload.Type {
		case PayloadSA:
			if len(selected.Proposals) != 0 {
				return InitResult{}, fmt.Errorf("%w: duplicate SA", ErrInvalidCreateChild)
			}
			selected, err = ParseSecurityAssociation(payload.Body)
		case PayloadNonce:
			if nonceR != nil {
				return InitResult{}, fmt.Errorf("%w: duplicate nonce", ErrInvalidCreateChild)
			}
			nonceR = append([]byte(nil), payload.Body...)
		case PayloadKE:
			if peer != nil {
				return InitResult{}, fmt.Errorf("%w: duplicate KE", ErrInvalidCreateChild)
			}
			var key KeyExchange
			key, err = ParseKeyExchange(payload.Body)
			peer = &key
		}
		if err != nil {
			return InitResult{}, err
		}
	}
	if err := ValidateSelectedSA(offered, selected); err != nil {
		return InitResult{}, err
	}
	if len(selected.Proposals[0].SPI) != 8 || len(nonceR) < 16 || len(nonceR) > 256 || peer == nil || peer.DHGroup != group {
		return InitResult{}, fmt.Errorf("%w: incomplete IKE rekey response", ErrInvalidCreateChild)
	}
	spiR := binary.BigEndian.Uint64(selected.Proposals[0].SPI)
	if spiR == 0 || spiR == previous.ResponderSPI {
		return InitResult{}, fmt.Errorf("%w: responder reused live SPI", ErrInvalidCreateChild)
	}
	shared, err := dh.shared(peer.KeyData)
	if err != nil {
		return InitResult{}, err
	}
	profile, err := KeyMaterialProfileFromSA(selected)
	if err != nil {
		return InitResult{}, err
	}
	seed := append(append(append([]byte(nil), shared...), nonceI...), nonceR...)
	skeyseed, err := PRF(previous.Keys.Profile.PRF, previous.Keys.SKD, seed)
	if err != nil {
		return InitResult{}, err
	}
	material, err := DeriveIKESAKeyMaterial(profile.PRF, skeyseed, nonceI, nonceR, spiI, spiR, profile.RequiredLength())
	if err != nil {
		return InitResult{}, err
	}
	keys, err := SplitIKEKeys(profile, material)
	if err != nil {
		return InitResult{}, err
	}
	return InitResult{SelectedSA: selected, InitiatorSPI: spiI, ResponderSPI: spiR, NonceI: nonceI, NonceR: nonceR, Keys: keys, PRF: profile.PRF,
		PublicKeyI: dh.public, PublicKeyR: peer.KeyData, MOBIKESupported: previous.MOBIKESupported, NATDetected: previous.NATDetected}, nil
}
