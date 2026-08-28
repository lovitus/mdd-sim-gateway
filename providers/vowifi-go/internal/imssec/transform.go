// SPDX-License-Identifier: AGPL-3.0-only

// Package imssec applies one IMS Security-Agree ESP transport-mode pair to
// full IP packets travelling through the in-memory SWu stack.
package imssec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"

	upstreamesp "github.com/boa-z/vowifi-go/engine/swu/esp"
)

const (
	protocolTCP = 6
	protocolUDP = 17
	protocolESP = 50
)

var (
	ErrInvalidConfig      = errors.New("invalid IMS userspace security config")
	ErrProtectionRequired = errors.New("IMS packet requires ESP protection")
)

type Config struct {
	LocalAddress       netip.Addr
	RemoteAddress      netip.Addr
	LocalPort          uint16
	RemotePort         uint16
	SPIClient          uint32
	SPIServer          uint32
	Authentication     string
	Encryption         string
	IntegrityKey       []byte
	ConfidentialityKey []byte
}

type Transformer struct {
	mu         sync.Mutex
	local      netip.Addr
	remote     netip.Addr
	localPort  uint16
	remotePort uint16
	outbound   *upstreamesp.SA
	inbound    *upstreamesp.SA
}

func New(config Config) (*Transformer, error) {
	config.LocalAddress = config.LocalAddress.Unmap()
	config.RemoteAddress = config.RemoteAddress.Unmap()
	if !config.LocalAddress.IsValid() || !config.RemoteAddress.IsValid() ||
		config.LocalAddress.Is4() != config.RemoteAddress.Is4() ||
		config.LocalPort == 0 || config.RemotePort == 0 ||
		config.SPIClient == 0 || config.SPIServer == 0 || len(config.IntegrityKey) != 16 {
		return nil, ErrInvalidConfig
	}
	integrity, err := integrityAlgorithm(config.Authentication)
	if err != nil {
		return nil, err
	}
	encryption, blockSize, err := encryptionAlgorithm(config.Encryption, config.ConfidentialityKey)
	if err != nil {
		return nil, err
	}
	newSA := func(spi uint32, replay uint32) (*upstreamesp.SA, error) {
		return upstreamesp.NewSA(upstreamesp.SA{
			SPI: spi, Encryption: encryption,
			EncryptionKey: append([]byte(nil), config.ConfidentialityKey...),
			IntegrityKey:  append([]byte(nil), config.IntegrityKey...), Integrity: integrity,
			ICVLength: 12, BlockSize: blockSize, ReplayWindowSize: replay,
		})
	}
	outbound, err := newSA(config.SPIServer, 0)
	if err != nil {
		return nil, err
	}
	inbound, err := newSA(config.SPIClient, 64)
	if err != nil {
		zeroSA(outbound)
		return nil, err
	}
	return &Transformer{
		local: config.LocalAddress, remote: config.RemoteAddress,
		localPort: config.LocalPort, remotePort: config.RemotePort,
		outbound: outbound, inbound: inbound,
	}, nil
}

func (transformer *Transformer) Protect(packet []byte) ([]byte, bool, error) {
	transformer.mu.Lock()
	defer transformer.mu.Unlock()
	view, err := parsePacket(packet)
	if err != nil {
		return nil, false, err
	}
	if view.source != transformer.local || view.destination != transformer.remote {
		return nil, false, nil
	}
	if view.fragmented && (view.protocol == protocolUDP || view.protocol == protocolTCP) {
		return nil, true, fmt.Errorf("%w: fragmented protected packet", ErrInvalidConfig)
	}
	if !portsMatch(view.payload, view.protocol, transformer.localPort, transformer.remotePort) {
		return nil, false, nil
	}
	sealed, err := transformer.outbound.Seal(view.protocol, view.payload, upstreamesp.SealOptions{})
	if err != nil {
		return nil, true, err
	}
	return rebuildPacket(view, protocolESP, sealed)
}

func (transformer *Transformer) Unprotect(packet []byte) ([]byte, bool, error) {
	transformer.mu.Lock()
	defer transformer.mu.Unlock()
	view, err := parsePacket(packet)
	if err != nil {
		return nil, false, err
	}
	if view.source != transformer.remote || view.destination != transformer.local {
		return nil, false, nil
	}
	if view.protocol == protocolESP {
		opened, err := transformer.inbound.Open(view.payload)
		if err != nil {
			return nil, true, err
		}
		if !portsMatch(opened.Payload, opened.NextHeader, transformer.remotePort, transformer.localPort) {
			return nil, true, fmt.Errorf("%w: protected transport selector mismatch", ErrInvalidConfig)
		}
		return rebuildPacket(view, opened.NextHeader, opened.Payload)
	}
	if view.fragmented && (view.protocol == protocolUDP || view.protocol == protocolTCP) {
		return nil, true, ErrProtectionRequired
	}
	if portsMatch(view.payload, view.protocol, transformer.remotePort, transformer.localPort) {
		return nil, true, ErrProtectionRequired
	}
	return nil, false, nil
}

func (transformer *Transformer) Close() error {
	if transformer == nil {
		return nil
	}
	transformer.mu.Lock()
	zeroSA(transformer.outbound)
	zeroSA(transformer.inbound)
	transformer.outbound = nil
	transformer.inbound = nil
	transformer.mu.Unlock()
	return nil
}

type packetView struct {
	packet      []byte
	version     byte
	headerLen   int
	protocol    uint8
	source      netip.Addr
	destination netip.Addr
	payload     []byte
	fragmented  bool
}

func parsePacket(packet []byte) (packetView, error) {
	if len(packet) == 0 {
		return packetView{}, fmt.Errorf("%w: empty IP packet", ErrInvalidConfig)
	}
	switch packet[0] >> 4 {
	case 4:
		if len(packet) < 20 {
			return packetView{}, fmt.Errorf("%w: short IPv4 packet", ErrInvalidConfig)
		}
		headerLen := int(packet[0]&0x0f) * 4
		totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
		if headerLen < 20 || totalLen < headerLen || totalLen > len(packet) {
			return packetView{}, fmt.Errorf("%w: malformed IPv4 length", ErrInvalidConfig)
		}
		return packetView{
			packet: packet[:totalLen], version: 4, headerLen: headerLen, protocol: packet[9],
			source: netip.AddrFrom4([4]byte(packet[12:16])), destination: netip.AddrFrom4([4]byte(packet[16:20])),
			payload: packet[headerLen:totalLen], fragmented: binary.BigEndian.Uint16(packet[6:8])&0x3fff != 0,
		}, nil
	case 6:
		if len(packet) < 40 {
			return packetView{}, fmt.Errorf("%w: short IPv6 packet", ErrInvalidConfig)
		}
		totalLen := 40 + int(binary.BigEndian.Uint16(packet[4:6]))
		if totalLen < 40 || totalLen > len(packet) {
			return packetView{}, fmt.Errorf("%w: malformed IPv6 length", ErrInvalidConfig)
		}
		return packetView{
			packet: packet[:totalLen], version: 6, headerLen: 40, protocol: packet[6],
			source: netip.AddrFrom16([16]byte(packet[8:24])), destination: netip.AddrFrom16([16]byte(packet[24:40])),
			payload: packet[40:totalLen],
		}, nil
	default:
		return packetView{}, fmt.Errorf("%w: unknown IP version", ErrInvalidConfig)
	}
}

func rebuildPacket(view packetView, protocol uint8, payload []byte) ([]byte, bool, error) {
	totalLen := view.headerLen + len(payload)
	if totalLen > 65535 {
		return nil, true, fmt.Errorf("%w: transformed packet too large", ErrInvalidConfig)
	}
	out := make([]byte, totalLen)
	copy(out, view.packet[:view.headerLen])
	copy(out[view.headerLen:], payload)
	if view.version == 4 {
		binary.BigEndian.PutUint16(out[2:4], uint16(totalLen))
		out[9] = protocol
		out[10], out[11] = 0, 0
		binary.BigEndian.PutUint16(out[10:12], ipv4Checksum(out[:view.headerLen]))
	} else {
		binary.BigEndian.PutUint16(out[4:6], uint16(len(payload)))
		out[6] = protocol
	}
	return out, true, nil
}

func portsMatch(payload []byte, protocol uint8, source, destination uint16) bool {
	return (protocol == protocolUDP || protocol == protocolTCP) && len(payload) >= 4 &&
		binary.BigEndian.Uint16(payload[0:2]) == source && binary.BigEndian.Uint16(payload[2:4]) == destination
}

func ipv4Checksum(header []byte) uint16 {
	var sum uint32
	for index := 0; index+1 < len(header); index += 2 {
		sum += uint32(binary.BigEndian.Uint16(header[index : index+2]))
	}
	if len(header)%2 != 0 {
		sum += uint32(header[len(header)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

func integrityAlgorithm(value string) (upstreamesp.IntegrityAlgorithm, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hmac-sha-1-96":
		return upstreamesp.IntegrityHMACSHA1_96, nil
	case "hmac-md5-96":
		return upstreamesp.IntegrityHMACMD5_96, nil
	default:
		return 0, fmt.Errorf("%w: unsupported integrity %q", ErrInvalidConfig, value)
	}
}

func encryptionAlgorithm(value string, key []byte) (upstreamesp.EncryptionAlgorithm, int, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "null":
		if len(key) != 0 {
			return 0, 0, fmt.Errorf("%w: null encryption has a key", ErrInvalidConfig)
		}
		return upstreamesp.EncryptionNull, 4, nil
	case "aes-cbc":
		if len(key) != 16 {
			return 0, 0, fmt.Errorf("%w: AES-CBC key length %d", ErrInvalidConfig, len(key))
		}
		return upstreamesp.EncryptionAESCBC, 16, nil
	default:
		return 0, 0, fmt.Errorf("%w: unsupported encryption %q", ErrInvalidConfig, value)
	}
}

func zeroSA(sa *upstreamesp.SA) {
	if sa == nil {
		return
	}
	for index := range sa.EncryptionKey {
		sa.EncryptionKey[index] = 0
	}
	for index := range sa.IntegrityKey {
		sa.IntegrityKey[index] = 0
	}
}
