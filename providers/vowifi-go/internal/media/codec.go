// SPDX-License-Identifier: AGPL-3.0-only

package media

import (
	"errors"
	"fmt"

	"github.com/zaf/g711"
)

var ErrInvalidPayload = errors.New("invalid RTP audio payload")

// frameCodec owns the stateful conversion between the browser's 20 ms PCM
// frames and one negotiated RTP payload. A Bridge has exactly one encoder and
// decoder pair and closes it only after both RTP goroutines have stopped.
type frameCodec interface {
	ValidateRTP([]byte) error
	EncodePCM([]byte) ([]byte, error)
	DecodeRTP([]byte) ([]byte, error)
	Close()
}

func openFrameCodec(codec Codec) (frameCodec, error) {
	switch codec {
	case CodecPCMU, CodecPCMA:
		return g711FrameCodec{codec: codec}, nil
	case CodecAMR:
		return openAMRNBCodec()
	default:
		return nil, fmt.Errorf("%w: unsupported codec %q", ErrInvalidConfig, codec)
	}
}

type g711FrameCodec struct {
	codec Codec
}

func (codec g711FrameCodec) ValidateRTP(payload []byte) error {
	if len(payload) != FrameSamples {
		return fmt.Errorf("%w: G.711 frame must be %d bytes", ErrInvalidPayload, FrameSamples)
	}
	return nil
}

func (codec g711FrameCodec) EncodePCM(pcm []byte) ([]byte, error) {
	if len(pcm) != PCMFrameBytes {
		return nil, fmt.Errorf("%w: PCM frame must be %d bytes", ErrInvalidConfig, PCMFrameBytes)
	}
	if codec.codec == CodecPCMA {
		return g711.EncodeAlaw(pcm), nil
	}
	return g711.EncodeUlaw(pcm), nil
}

func (codec g711FrameCodec) DecodeRTP(payload []byte) ([]byte, error) {
	if err := codec.ValidateRTP(payload); err != nil {
		return nil, err
	}
	if codec.codec == CodecPCMA {
		return g711.DecodeAlaw(payload), nil
	}
	return g711.DecodeUlaw(payload), nil
}

func (g711FrameCodec) Close() {}
