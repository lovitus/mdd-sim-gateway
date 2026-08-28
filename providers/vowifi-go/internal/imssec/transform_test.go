// SPDX-License-Identifier: AGPL-3.0-only

package imssec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
)

func TestIPv4ESPTransportRoundTripAndPlaintextRejection(t *testing.T) {
	client, server := testPair(t, netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("10.0.0.3"))
	defer client.Close()
	defer server.Close()
	original := testIPPacket(t, client.local, server.local, protocolUDP, 5062, 5063, []byte("REGISTER sip:ims.example SIP/2.0"))
	protected, handled, err := client.Protect(original)
	if err != nil || !handled {
		t.Fatalf("Protect() handled=%v err=%v", handled, err)
	}
	if protected[9] != protocolESP {
		t.Fatalf("protected packet was not ESP: %x", protected)
	}
	opened, handled, err := server.Unprotect(protected)
	if err != nil || !handled || !bytes.Equal(opened, original) {
		t.Fatalf("Unprotect() handled=%v err=%v\n got=%x\nwant=%x", handled, err, opened, original)
	}
	if _, handled, err := server.Unprotect(original); !handled || !errors.Is(err, ErrProtectionRequired) {
		t.Fatalf("plaintext handled=%v err=%v", handled, err)
	}
	if _, handled, err := server.Unprotect(protected); !handled || err == nil {
		t.Fatalf("replay handled=%v err=%v", handled, err)
	}
}

func TestIPv6AESCBCRoundTripAndSelectorIsolation(t *testing.T) {
	client, server := testPairWithEncryption(t, netip.MustParseAddr("2001:db8::2"), netip.MustParseAddr("2001:db8::3"), "aes-cbc")
	defer client.Close()
	defer server.Close()
	original := testIPPacket(t, client.local, server.local, protocolUDP, 5062, 5063, []byte("MESSAGE"))
	protected, handled, err := client.Protect(original)
	if err != nil || !handled || protected[6] != protocolESP {
		t.Fatalf("Protect() handled=%v err=%v", handled, err)
	}
	opened, handled, err := server.Unprotect(protected)
	if err != nil || !handled || !bytes.Equal(opened, original) {
		t.Fatalf("Unprotect() handled=%v err=%v", handled, err)
	}
	other := testIPPacket(t, client.local, server.local, protocolUDP, 6000, 6001, []byte("not IMS"))
	if got, handled, err := client.Protect(other); err != nil || handled || got != nil {
		t.Fatalf("unmatched selector got=%x handled=%v err=%v", got, handled, err)
	}
}

func TestTamperedESPPacketIsConsumedAndRejected(t *testing.T) {
	client, server := testPair(t, netip.MustParseAddr("10.1.0.2"), netip.MustParseAddr("10.1.0.3"))
	defer client.Close()
	defer server.Close()
	original := testIPPacket(t, client.local, server.local, protocolUDP, 5062, 5063, []byte("INVITE"))
	protected, _, err := client.Protect(original)
	if err != nil {
		t.Fatal(err)
	}
	protected[len(protected)-1] ^= 0xff
	if _, handled, err := server.Unprotect(protected); !handled || err == nil {
		t.Fatalf("tamper handled=%v err=%v", handled, err)
	}
}

func testPair(t *testing.T, clientIP, serverIP netip.Addr) (*Transformer, *Transformer) {
	t.Helper()
	return testPairWithEncryption(t, clientIP, serverIP, "null")
}

func testPairWithEncryption(t *testing.T, clientIP, serverIP netip.Addr, encryption string) (*Transformer, *Transformer) {
	t.Helper()
	key := []byte(nil)
	if encryption == "aes-cbc" {
		key = bytes.Repeat([]byte{0x71}, 16)
	}
	client, err := New(Config{
		LocalAddress: clientIP, RemoteAddress: serverIP, LocalPort: 5062, RemotePort: 5063,
		SPIClient: 101, SPIServer: 202, Authentication: "hmac-sha-1-96", Encryption: encryption,
		IntegrityKey: bytes.Repeat([]byte{0x31}, 16), ConfidentialityKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		LocalAddress: serverIP, RemoteAddress: clientIP, LocalPort: 5063, RemotePort: 5062,
		SPIClient: 202, SPIServer: 101, Authentication: "hmac-sha-1-96", Encryption: encryption,
		IntegrityKey: bytes.Repeat([]byte{0x31}, 16), ConfidentialityKey: key,
	})
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	return client, server
}

func testIPPacket(t *testing.T, source, destination netip.Addr, protocol uint8, sourcePort, destinationPort uint16, body []byte) []byte {
	t.Helper()
	transport := make([]byte, 8+len(body))
	binary.BigEndian.PutUint16(transport[0:2], sourcePort)
	binary.BigEndian.PutUint16(transport[2:4], destinationPort)
	binary.BigEndian.PutUint16(transport[4:6], uint16(len(transport)))
	copy(transport[8:], body)
	if source.Is4() {
		packet := make([]byte, 20+len(transport))
		packet[0] = 0x45
		packet[8] = 64
		packet[9] = protocol
		binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
		source4, destination4 := source.As4(), destination.As4()
		copy(packet[12:16], source4[:])
		copy(packet[16:20], destination4[:])
		binary.BigEndian.PutUint16(packet[10:12], ipv4Checksum(packet[:20]))
		copy(packet[20:], transport)
		return packet
	}
	packet := make([]byte, 40+len(transport))
	packet[0] = 0x60
	packet[6] = protocol
	packet[7] = 64
	binary.BigEndian.PutUint16(packet[4:6], uint16(len(transport)))
	source16, destination16 := source.As16(), destination.As16()
	copy(packet[8:24], source16[:])
	copy(packet[24:40], destination16[:])
	copy(packet[40:], transport)
	return packet
}
