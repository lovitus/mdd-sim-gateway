// SPDX-License-Identifier: AGPL-3.0-only

package media

/*
#cgo pkg-config: opencore-amrnb
#include <opencore-amrnb/interf_dec.h>
#include <opencore-amrnb/interf_enc.h>

static int mdd_amrnb_encode(void *state, int mode, const short *pcm, unsigned char *out) {
	return Encoder_Interface_Encode(state, (enum Mode)mode, pcm, out, 0);
}

static void mdd_amrnb_decode(void *state, const unsigned char *in, short *pcm) {
	Decoder_Interface_Decode(state, in, pcm, 0);
}
*/
import "C"

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"
)

const (
	amrNBDefaultMode = 7 // MR122, matching the previously deployed Asterisk path.
	amrNBCMRNone     = 15
)

// RFC 4867 section 3.6, AMR speech bits for frame types 0 through 8.
var amrNBFrameBits = [...]int{95, 103, 118, 134, 148, 159, 204, 244, 39}

type amrNBCodec struct {
	encoder unsafe.Pointer
	decoder unsafe.Pointer
	mode    atomic.Int32
	close   sync.Once
}

func openAMRNBCodec() (*amrNBCodec, error) {
	codec := &amrNBCodec{
		encoder: C.Encoder_Interface_init(0), // Continuous media; do not create DTX gaps.
		decoder: C.Decoder_Interface_init(),
	}
	if codec.encoder == nil || codec.decoder == nil {
		codec.Close()
		return nil, fmt.Errorf("initialize opencore-amrnb codec")
	}
	codec.mode.Store(amrNBDefaultMode)
	return codec, nil
}

func (codec *amrNBCodec) ValidateRTP(payload []byte) error {
	_, _, _, err := unpackAMRNBFrame(payload)
	return err
}

func (codec *amrNBCodec) EncodePCM(pcm []byte) ([]byte, error) {
	if codec == nil || codec.encoder == nil {
		return nil, ErrClosed
	}
	if len(pcm) != PCMFrameBytes {
		return nil, fmt.Errorf("%w: PCM frame must be %d bytes", ErrInvalidConfig, PCMFrameBytes)
	}
	var samples [FrameSamples]C.short
	for index := range samples {
		samples[index] = C.short(int16(binary.LittleEndian.Uint16(pcm[index*2:])))
	}
	var storage [64]C.uchar
	mode := int(codec.mode.Load())
	if mode < 0 || mode > 7 {
		mode = amrNBDefaultMode
	}
	length := int(C.mdd_amrnb_encode(
		codec.encoder,
		C.int(mode),
		&samples[0],
		&storage[0],
	))
	if length <= 0 || length > len(storage) {
		return nil, fmt.Errorf("opencore-amrnb encode returned %d bytes", length)
	}
	encoded := C.GoBytes(unsafe.Pointer(&storage[0]), C.int(length))
	return packAMRNBFrame(encoded)
}

func (codec *amrNBCodec) DecodeRTP(payload []byte) ([]byte, error) {
	if codec == nil || codec.decoder == nil {
		return nil, ErrClosed
	}
	storage, cmr, _, err := unpackAMRNBFrame(payload)
	if err != nil {
		return nil, err
	}
	if cmr >= 0 && cmr <= 7 {
		codec.mode.Store(int32(cmr))
	}
	var input [64]C.uchar
	for index, value := range storage {
		input[index] = C.uchar(value)
	}
	var samples [FrameSamples]C.short
	C.mdd_amrnb_decode(codec.decoder, &input[0], &samples[0])
	pcm := make([]byte, PCMFrameBytes)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(int16(sample)))
	}
	return pcm, nil
}

func (codec *amrNBCodec) Close() {
	if codec == nil {
		return
	}
	codec.close.Do(func() {
		if codec.encoder != nil {
			C.Encoder_Interface_exit(codec.encoder)
			codec.encoder = nil
		}
		if codec.decoder != nil {
			C.Decoder_Interface_exit(codec.decoder)
			codec.decoder = nil
		}
	})
}

// packAMRNBFrame converts opencore-amr's RFC 4867 storage frame into the
// single-channel, single-frame bandwidth-efficient RTP form used by the old
// production Asterisk codec. No codec algorithm is reimplemented here.
func packAMRNBFrame(storage []byte) ([]byte, error) {
	if len(storage) < 1 {
		return nil, fmt.Errorf("%w: AMR storage frame is truncated", ErrInvalidPayload)
	}
	frameType := int((storage[0] >> 3) & 0x0f)
	quality := int((storage[0] >> 2) & 0x01)
	bits, supported := amrNBFrameBitCount(frameType)
	if storage[0]&0x80 != 0 || !supported {
		return nil, fmt.Errorf("%w: unsupported AMR storage frame type %d", ErrInvalidPayload, frameType)
	}
	storageBytes := (bits + 7) / 8
	if len(storage) != storageBytes+1 {
		return nil, fmt.Errorf("%w: AMR storage frame type %d has %d bytes, want %d", ErrInvalidPayload, frameType, len(storage), storageBytes+1)
	}
	payload := make([]byte, (10+bits+7)/8)
	writeBits(payload, 0, amrNBCMRNone, 4)
	writeBits(payload, 4, 0, 1) // F=0: exactly one frame per RTP packet.
	writeBits(payload, 5, frameType, 4)
	writeBits(payload, 9, quality, 1)
	for bit := 0; bit < bits; bit++ {
		writeBits(payload, 10+bit, readBit(storage[1:], bit), 1)
	}
	return payload, nil
}

func unpackAMRNBFrame(payload []byte) (storage []byte, cmr int, frameType int, err error) {
	if len(payload) < 2 {
		return nil, 0, 0, fmt.Errorf("%w: AMR RTP payload is truncated", ErrInvalidPayload)
	}
	cmr = readBits(payload, 0, 4)
	if readBits(payload, 4, 1) != 0 {
		return nil, 0, 0, fmt.Errorf("%w: compound AMR RTP payload is not supported", ErrInvalidPayload)
	}
	frameType = readBits(payload, 5, 4)
	quality := readBits(payload, 9, 1)
	bits, supported := amrNBFrameBitCount(frameType)
	if !supported {
		return nil, 0, 0, fmt.Errorf("%w: unsupported AMR RTP frame type %d", ErrInvalidPayload, frameType)
	}
	wantBytes := (10 + bits + 7) / 8
	if len(payload) != wantBytes {
		return nil, 0, 0, fmt.Errorf("%w: AMR RTP frame type %d has %d bytes, want %d", ErrInvalidPayload, frameType, len(payload), wantBytes)
	}
	if padding := wantBytes*8 - (10 + bits); padding > 0 && readBits(payload, 10+bits, padding) != 0 {
		return nil, 0, 0, fmt.Errorf("%w: AMR RTP padding is non-zero", ErrInvalidPayload)
	}
	storage = make([]byte, 1+(bits+7)/8)
	storage[0] = byte(frameType<<3 | quality<<2)
	for bit := 0; bit < bits; bit++ {
		writeBits(storage[1:], bit, readBit(payload, 10+bit), 1)
	}
	return storage, cmr, frameType, nil
}

func amrNBFrameBitCount(frameType int) (int, bool) {
	if frameType >= 0 && frameType < len(amrNBFrameBits) {
		return amrNBFrameBits[frameType], true
	}
	if frameType == 15 { // RFC 4867 NO_DATA during remote DTX.
		return 0, true
	}
	return 0, false
}

func readBit(buffer []byte, offset int) int {
	return int((buffer[offset/8] >> (7 - offset%8)) & 1)
}

func readBits(buffer []byte, offset, count int) int {
	value := 0
	for index := 0; index < count; index++ {
		value = value<<1 | readBit(buffer, offset+index)
	}
	return value
}

func writeBits(buffer []byte, offset, value, count int) {
	for index := 0; index < count; index++ {
		bit := (value >> (count - index - 1)) & 1
		position := offset + index
		mask := byte(1 << (7 - position%8))
		if bit == 1 {
			buffer[position/8] |= mask
		} else {
			buffer[position/8] &^= mask
		}
	}
}
