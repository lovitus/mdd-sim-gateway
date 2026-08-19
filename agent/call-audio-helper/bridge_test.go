package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestNormalizePin(t *testing.T) {
	plain := strings.Repeat("ab", 32)
	colon := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	for _, input := range []string{plain, colon} {
		value, err := normalizePin(input)
		if err != nil || value != plain {
			t.Fatalf("normalizePin(%q) = %q, %v", input, value, err)
		}
	}
	if _, err := normalizePin("insecure"); err == nil {
		t.Fatal("invalid pin was accepted")
	}
}

func TestPCMBufferBoundsLatencyAndPadsSilence(t *testing.T) {
	buffer := &pcmBuffer{}
	input := bytes.Repeat([]byte{0x5a}, 8000*2*3)
	buffer.append(input)
	if len(buffer.data) != 8000*2*2 {
		t.Fatalf("buffer retained %d bytes", len(buffer.data))
	}
	retained := len(buffer.data)
	output := bytes.Repeat([]byte{0xff}, retained+16)
	buffer.read(output)
	if !bytes.Equal(output[:retained], bytes.Repeat([]byte{0x5a}, retained)) {
		t.Fatal("buffer did not preserve the newest audio")
	}
	if !bytes.Equal(output[retained:], make([]byte, 16)) {
		t.Fatal("short read was not padded with silence")
	}
}
