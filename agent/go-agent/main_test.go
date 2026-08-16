package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestIsForbiddenAPDU(t *testing.T) {
	tests := []struct {
		name     string
		apdu     []byte
		expected bool
	}{
		{
			name:     "SELECT ISIM AID",
			apdu:     []byte{0x00, 0xA4, 0x04, 0x00, 0x0A, 0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x04, 0xFF, 0xFF, 0xFF, 0xFF},
			expected: false,
		},
		{
			name:     "AUTHENTICATE AKA",
			apdu:     []byte{0x00, 0x88, 0x00, 0x81, 0x22, 0x10, 0x01, 0x02, 0x03},
			expected: false,
		},
		{
			name:     "SGP.22 ES10c.DeleteProfile (tag 0xBF33)",
			apdu:     []byte{0x80, 0xE2, 0x91, 0x00, 0x05, 0xBF, 0x33, 0x02, 0x5A, 0x00},
			expected: true,
		},
		{
			name:     "ISO 7816 DELETE FILE (INS 0xE4)",
			apdu:     []byte{0x00, 0xE4, 0x00, 0x00, 0x02, 0x3F, 0x00},
			expected: true,
		},
		{
			name:     "Short APDU",
			apdu:     []byte{0x00, 0xA4},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isForbiddenAPDU(tt.apdu)
			if result != tt.expected {
				t.Errorf("isForbiddenAPDU(%X) = %v; want %v", tt.apdu, result, tt.expected)
			}
		})
	}
}

func TestVPCDHeaderFraming(t *testing.T) {
	data := []byte("hello world")
	buf := new(bytes.Buffer)

	// Write 2-byte big-endian length
	lengthHeader := make([]byte, 2)
	binary.BigEndian.PutUint16(lengthHeader, uint16(len(data)))
	buf.Write(lengthHeader)
	buf.Write(data)

	// Decode length
	readLen := binary.BigEndian.Uint16(buf.Next(2))
	if readLen != uint16(len(data)) {
		t.Fatalf("expected length %d, got %d", len(data), readLen)
	}

	readBody := buf.Next(int(readLen))
	if !bytes.Equal(readBody, data) {
		t.Fatalf("expected body %v, got %v", data, readBody)
	}
}
