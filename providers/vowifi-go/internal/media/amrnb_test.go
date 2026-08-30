// SPDX-License-Identifier: AGPL-3.0-only

package media

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestAMRNBBandwidthEfficientSingleFrameRoundTrip(t *testing.T) {
	for frameType, bits := range amrNBFrameBits {
		storage := make([]byte, 1+(bits+7)/8)
		storage[0] = byte(frameType<<3 | 1<<2)
		for index := 1; index < len(storage); index++ {
			storage[index] = byte(index*29 + frameType)
		}
		if padding := len(storage[1:])*8 - bits; padding > 0 {
			storage[len(storage)-1] &= 0xff << padding
		}
		payload, err := packAMRNBFrame(storage)
		if err != nil {
			t.Fatalf("frame type %d: pack: %v", frameType, err)
		}
		wantPayloadBytes := (10 + bits + 7) / 8
		if len(payload) != wantPayloadBytes {
			t.Fatalf("frame type %d: payload bytes=%d, want %d", frameType, len(payload), wantPayloadBytes)
		}
		decoded, cmr, gotType, err := unpackAMRNBFrame(payload)
		if err != nil {
			t.Fatalf("frame type %d: unpack: %v", frameType, err)
		}
		if cmr != amrNBCMRNone || gotType != frameType || !bytes.Equal(decoded, storage) {
			t.Fatalf("frame type %d: round trip cmr=%d type=%d equal=%v", frameType, cmr, gotType, bytes.Equal(decoded, storage))
		}
	}
}

func TestAMRNBBandwidthEfficientKnownHeader(t *testing.T) {
	// RFC 4867 section 4.3.5.1 uses CMR=15, F=0, FT=4, Q=1.
	storage := make([]byte, 1+(amrNBFrameBits[4]+7)/8)
	storage[0] = 4<<3 | 1<<2
	payload, err := packAMRNBFrame(storage)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 20 || payload[0] != 0xf2 || payload[1] != 0x40 {
		t.Fatalf("RFC 4867 header=%x len=%d, want f240 len=20", payload[:2], len(payload))
	}
}

func TestAMRNBBandwidthEfficientNoDataFrame(t *testing.T) {
	payload, err := packAMRNBFrame([]byte{15<<3 | 1<<2})
	if err != nil {
		t.Fatal(err)
	}
	storage, cmr, frameType, err := unpackAMRNBFrame(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || len(storage) != 1 || cmr != amrNBCMRNone || frameType != 15 {
		t.Fatalf("NO_DATA payload=%x storage=%x cmr=%d type=%d", payload, storage, cmr, frameType)
	}
}

func TestAMRNBBandwidthEfficientRejectsCompoundAndBadPadding(t *testing.T) {
	storage := make([]byte, 1+(amrNBFrameBits[0]+7)/8)
	storage[0] = 1 << 2
	payload, err := packAMRNBFrame(storage)
	if err != nil {
		t.Fatal(err)
	}
	compound := append([]byte(nil), payload...)
	compound[0] |= 1 << 3
	if _, _, _, err := unpackAMRNBFrame(compound); err == nil {
		t.Fatal("compound AMR payload was accepted")
	}
	badPadding := append([]byte(nil), payload...)
	badPadding[len(badPadding)-1] |= 1
	if _, _, _, err := unpackAMRNBFrame(badPadding); err == nil {
		t.Fatal("non-zero AMR padding was accepted")
	}
	if _, _, _, err := unpackAMRNBFrame(payload[:len(payload)-1]); err == nil {
		t.Fatal("truncated AMR payload was accepted")
	}
}

func TestAMRNBCodecCarriesBidirectionalNonSilentAudioOverUserspaceStack(t *testing.T) {
	clientStack, peerStack := openMediaStackPair(t)
	peerRTP, err := peerStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5100")
	if err != nil {
		t.Fatal(err)
	}
	defer peerRTP.Close()
	peerRTCP, err := peerStack.ListenPacket(context.Background(), "udp4", "10.0.0.2:5101")
	if err != nil {
		t.Fatal(err)
	}
	defer peerRTCP.Close()
	bridge, err := Open(context.Background(), clientStack, Config{
		LocalRTP: "10.0.0.1:4100", LocalRTCP: "10.0.0.1:4101",
		RemoteRTP: "10.0.0.2:5100", RemoteRTCP: "10.0.0.2:5101",
		Codec: CodecAMR, BufferMS: 2000, SSRC: 0x10203040,
		InitialSequence: 100, InitialTimestamp: 1000, RTCPInterval: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close(context.Background()) })
	peerCodec, err := openAMRNBCodec()
	if err != nil {
		t.Fatal(err)
	}
	defer peerCodec.Close()

	accepted, err := bridge.WritePCM(nonSilentPCM(12000), time.Now())
	if err != nil || !accepted {
		t.Fatalf("WritePCM() = %v, %v", accepted, err)
	}
	_ = peerRTP.SetReadDeadline(time.Now().Add(2 * time.Second))
	wire := make([]byte, 2048)
	size, source, err := peerRTP.ReadFrom(wire)
	if err != nil {
		t.Fatal(err)
	}
	var outbound rtp.Packet
	if err := outbound.Unmarshal(wire[:size]); err != nil {
		t.Fatal(err)
	}
	decoded, err := peerCodec.DecodeRTP(outbound.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if outbound.PayloadType != CodecAMR.PayloadType() || outbound.SequenceNumber != 100 ||
		outbound.Timestamp != 1000 || len(outbound.Payload) != 32 || !hasSignal(decoded) {
		t.Fatalf("outbound AMR RTP=%+v bytes=%d signal=%v", outbound.Header, len(outbound.Payload), hasSignal(decoded))
	}

	responsePayload, err := peerCodec.EncodePCM(nonSilentPCM(-9000))
	if err != nil {
		t.Fatal(err)
	}
	response := rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: CodecAMR.PayloadType(), SequenceNumber: 900,
		Timestamp: 8000, SSRC: 0x55667788,
	}, Payload: responsePayload}
	responseWire, err := response.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peerRTP.WriteTo(responseWire, source); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-bridge.PCM():
		if len(received.Data) != PCMFrameBytes || !hasSignal(received.Data) {
			t.Fatalf("received PCM bytes=%d signal=%v", len(received.Data), hasSignal(received.Data))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for AMR IMS-to-browser PCM")
	}
}
