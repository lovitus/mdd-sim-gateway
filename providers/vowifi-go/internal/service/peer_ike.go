// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/netip"
	"sort"
	"sync"

	"github.com/boa-z/vowifi-go/engine/swu/ikev2"
	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/outerudp"
)

const deviceIdentityNotify uint16 = 41101
const peerResponseCacheSize = 16 // ec620942 swu_ike.py:_response_cache_max

type peerIKECache struct {
	fingerprint [32]byte
	reply       outerudp.PeerReply
}

type peerIKEResponder struct {
	mu              sync.Mutex
	init            ikev2.InitResult
	profile         identity.Profile
	child           ikev2.ChildSAResult
	updatePCSCF     func([]string)
	cache           map[uint32]peerIKECache
	order           []uint32
	last            uint32
	seen            bool
	recoveryPending bool
}

func newPeerIKEResponder(init ikev2.InitResult, profile identity.Profile, updatePCSCF func([]string)) *peerIKEResponder {
	return &peerIKEResponder{init: init, profile: profile, updatePCSCF: updatePCSCF, cache: make(map[uint32]peerIKECache)}
}

func (peer *peerIKEResponder) setChild(local, remote []byte) {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	peer.child.LocalSPI = append([]byte(nil), local...)
	peer.child.RemoteSPI = append([]byte(nil), remote...)
}

// Adapted from ec620942 swu_ike.py's INFORMATIONAL and response-cache flow.
// All crypto, payload parsing and recovery classification use the upstream API.
func (peer *peerIKEResponder) handle(raw []byte) (outerudp.PeerReply, error) {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	header, err := ikev2.ParseHeader(raw)
	if err != nil || header.Version>>4 != 2 || header.Length != uint32(len(raw)) ||
		header.InitiatorSPI != peer.init.InitiatorSPI || header.ResponderSPI != peer.init.ResponderSPI ||
		header.Flags&(ikev2.FlagInitiator|ikev2.FlagResponse) != 0 {
		return outerudp.PeerReply{}, errors.New("peer IKE identity mismatch")
	}
	digest := sha256.Sum256(raw)
	if prior, ok := peer.cache[header.MessageID]; ok {
		if prior.fingerprint != digest {
			return outerudp.PeerReply{}, errors.New("peer IKE message ID reused")
		}
		return clonePeerReply(prior.reply), nil
	}
	if peer.seen && header.MessageID <= peer.last {
		return outerudp.PeerReply{}, errors.New("stale peer IKE request")
	}
	_, inner, err := ikev2.UnprotectMessage(raw, peer.init.Keys, false)
	if err != nil {
		return outerudp.PeerReply{}, errors.New("peer IKE authentication failed")
	}
	var payloads []ikev2.Payload
	var abort error
	var addresses []string
	switch header.ExchangeType {
	case ikev2.ExchangeINFORMATIONAL:
		payloads, addresses, abort, err = peer.informational(inner)
	case ikev2.ExchangeCREATE_CHILD_SA:
		// Do not silently accept an unimplemented peer-initiated rekey.
		// RFC 7296 section 4 permits this explicit, protected rejection.
		payloads = []ikev2.Payload{ikev2.NotifyWithZeroSPI(ikev2.NotifyNoAdditionalSAs, nil)}
	default:
		err = errors.New("unsupported peer IKE exchange")
	}
	if err != nil {
		return outerudp.PeerReply{}, err
	}
	header.Flags = ikev2.FlagInitiator | ikev2.FlagResponse
	_, packet, err := ikev2.ProtectMessage(header, peer.init.Keys, true, payloads, nil)
	if err != nil {
		return outerudp.PeerReply{}, err
	}
	reply := outerudp.PeerReply{Packet: packet, Abort: abort}
	var stage *StageError
	rebind := errors.As(abort, &stage) && stage.Code == vowifiipc.PeerPCSCFChanged
	if rebind {
		reply.Abort = nil
	}
	if len(addresses) > 0 {
		var once sync.Once
		reply.AfterSend = func() {
			once.Do(func() {
				if peer.updatePCSCF != nil {
					peer.updatePCSCF(append([]string(nil), addresses...))
				}
				if rebind {
					peer.mu.Lock()
					peer.recoveryPending = true
					peer.mu.Unlock()
				}
			})
		}
	}
	peer.cache[header.MessageID] = peerIKECache{fingerprint: digest, reply: reply}
	peer.order = append(peer.order, header.MessageID)
	if len(peer.order) > peerResponseCacheSize {
		delete(peer.cache, peer.order[0])
		peer.order = peer.order[1:]
	}
	peer.last, peer.seen = header.MessageID, true
	return clonePeerReply(reply), nil
}

func clonePeerReply(reply outerudp.PeerReply) outerudp.PeerReply {
	reply.Packet = append([]byte(nil), reply.Packet...)
	return reply
}

func (peer *peerIKEResponder) needsRebind() bool {
	if peer == nil {
		return false
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	return peer.recoveryPending
}

func peerTunnelFailure(code string) error {
	return &StageError{Layer: "tunnel", Code: code, Err: errors.New("authenticated peer requested tunnel recovery")}
}

func (peer *peerIKEResponder) informational(inner []ikev2.Payload) ([]ikev2.Payload, []string, error, error) {
	for _, payload := range inner {
		if payload.Critical && payload.Type != ikev2.PayloadNotify && payload.Type != ikev2.PayloadDelete && payload.Type != ikev2.PayloadCP {
			return []ikev2.Payload{ikev2.NotifyWithZeroSPI(ikev2.NotifyUnsupportedCriticalPayload, []byte{payload.Type})}, nil, nil, nil
		}
	}
	content, err := ikev2.ParseInformationalContent(inner)
	if err != nil {
		return nil, nil, nil, err
	}
	plan, err := ikev2.PlanInformationalRecovery(content, peer.child)
	if err != nil {
		return nil, nil, nil, err
	}
	payloads := plan.Response.Payloads
	var abort error
	if plan.DeleteIKE {
		return nil, nil, peerTunnelFailure("peer_ike_deleted"), nil
	}
	if plan.DeleteCurrentChild {
		ack, err := ikev2.ESPDeletePayload(peer.child.LocalSPI)
		if err != nil {
			return nil, nil, nil, err
		}
		payloads = append(payloads, ack)
		abort = peerTunnelFailure("peer_child_deleted")
	} else if plan.RecreateIKE || plan.RecreateChild || plan.Reauthenticate || plan.Action == ikev2.InformationalRecoveryAbort {
		abort = peerTunnelFailure("peer_tunnel_recovery_requested")
	}
	var addresses []string
	for _, payload := range inner {
		if payload.Type != ikev2.PayloadCP {
			continue
		}
		configuration, err := ikev2.ParseConfiguration(payload.Body)
		if err != nil || configuration.Type != ikev2.CFGRequest {
			return nil, nil, nil, errors.New("invalid peer configuration request")
		}
		reply := ikev2.Configuration{Type: ikev2.CFGReply}
		for _, attribute := range configuration.Attributes {
			reply.Attributes = append(reply.Attributes, ikev2.ConfigurationAttribute{Type: attribute.Type})
			if len(attribute.Value) == 0 {
				continue
			}
			if attribute.Type != ikev2.ConfigPCSCFIPv4Address && attribute.Type != ikev2.ConfigPCSCFIPv6Address {
				continue
			}
			if attribute.Type == ikev2.ConfigPCSCFIPv4Address && len(attribute.Value) != 4 || attribute.Type == ikev2.ConfigPCSCFIPv6Address && len(attribute.Value) != 16 {
				return nil, nil, nil, errors.New("invalid peer P-CSCF address length")
			}
			address, ok := netip.AddrFromSlice(attribute.Value)
			if !ok || !address.IsGlobalUnicast() || address.IsLoopback() {
				return nil, nil, nil, errors.New("invalid peer P-CSCF address")
			}
			addresses = append(addresses, address.String())
		}
		cp, err := ikev2.ConfigurationPayload(reply)
		if err != nil {
			return nil, nil, nil, err
		}
		payloads = append(payloads, cp)
	}
	if len(addresses) > 0 {
		// Match the retired P-CSCF restoration preference without changing
		// durable desired configuration or routing outside the SWu stack.
		sort.SliceStable(addresses, func(i, j int) bool {
			return netip.MustParseAddr(addresses[i]).Is6() && !netip.MustParseAddr(addresses[j]).Is6()
		})
		if abort == nil {
			abort = peerTunnelFailure(vowifiipc.PeerPCSCFChanged)
		}
	}
	for _, notify := range content.Notifies {
		if notify.NotifyType != deviceIdentityNotify || len(notify.NotificationData) > 3 {
			continue
		}
		kind := byte(2)
		if len(notify.NotificationData) > 0 && notify.NotificationData[len(notify.NotificationData)-1] == 1 {
			kind = 1
		}
		data, err := peer.deviceIdentity(kind)
		if err != nil {
			return nil, nil, nil, err
		}
		payloads = append(payloads, ikev2.NotifyWithZeroSPI(deviceIdentityNotify, data))
	}
	return payloads, addresses, abort, nil
}

func (peer *peerIKEResponder) deviceIdentity(kind byte) ([]byte, error) {
	digits := peer.profile.IMEI
	if !exactDigits(digits, 15, 15) {
		return nil, errors.New("configured equipment identity unavailable")
	}
	if kind == 2 {
		digits = peer.profile.IMEISV
		// Retain the legacy configured-identity fallback, not a hardware readback.
		if digits == "" {
			digits = peer.profile.IMEI[:14] + "00"
		}
		if !exactDigits(digits, 16, 16) {
			return nil, errors.New("configured software identity unavailable")
		}
	} else {
		digits += "F"
	}
	data := make([]byte, 11)
	binary.BigEndian.PutUint16(data, 9)
	data[2] = kind
	for i := 0; i < 8; i++ {
		low := digits[i*2] - '0'
		high := digits[i*2+1] - '0'
		if digits[i*2+1] == 'F' {
			high = 15
		}
		data[3+i] = low | (high << 4)
	}
	return data, nil
}
