package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type recordingConn struct{ bytes.Buffer }

func (c *recordingConn) Read(p []byte) (int, error)         { return c.Buffer.Read(p) }
func (c *recordingConn) Write(p []byte) (int, error)        { return c.Buffer.Write(p) }
func (c *recordingConn) Close() error                       { return nil }
func (c *recordingConn) LocalAddr() net.Addr                { return nil }
func (c *recordingConn) RemoteAddr() net.Addr               { return nil }
func (c *recordingConn) SetDeadline(time.Time) error        { return nil }
func (c *recordingConn) SetReadDeadline(time.Time) error    { return nil }
func (c *recordingConn) SetWriteDeadline(time.Time) error   { return nil }

func TestAgentMetadataUsesStableIdentityAndAutoSlot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := getAgentID()
	if first == "" || first != getAgentID() {
		t.Fatal("agent installation id was not persisted")
	}
	if stableReaderID(" USB  Reader ") != stableReaderID("usb reader") {
		t.Fatal("reader id normalization is not stable")
	}
	u, err := url.Parse(agentWSPath("/mdd/api/vpcd/ws", "USB Reader"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("slot") != "auto" || u.Query().Get("agent_id") != first || u.Query().Get("reader_name") != "USB Reader" {
		t.Fatalf("unexpected agent metadata: %s", u.RawQuery)
	}
}

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

func TestWSConnConvertsMessageFramesAndVPCDStream(t *testing.T) {
	payload := []byte{0x00, 0xA4, 0x04, 0x00}
	serverFrame := append([]byte{0x82, byte(len(payload))}, payload...)
	w := &wsConn{br: bufio.NewReader(bytes.NewReader(serverFrame))}
	header := make([]byte, 2)
	if _, err := io.ReadFull(w, header); err != nil || binary.BigEndian.Uint16(header) != uint16(len(payload)) {
		t.Fatalf("bad VPCD header: %x err=%v", header, err)
	}
	body := make([]byte, len(payload))
	if _, err := io.ReadFull(w, body); err != nil || !bytes.Equal(body, payload) {
		t.Fatalf("bad VPCD payload: %x err=%v", body, err)
	}

	recorded := &recordingConn{}
	out := &wsConn{conn: recorded}
	if _, err := out.Write(header); err != nil { t.Fatal(err) }
	if recorded.Len() != 0 { t.Fatal("partial VPCD frame was sent early") }
	if _, err := out.Write(payload); err != nil { t.Fatal(err) }
	frame := recorded.Bytes()
	if len(frame) < 6 || frame[0] != 0x82 || frame[1]&0x80 == 0 {
		t.Fatalf("not a masked binary WebSocket frame: %x", frame)
	}
}

func TestFormatFingerprint(t *testing.T) {
	rawCert := []byte("dummy-test-certificate-bytes-12345")
	fp := formatFingerprint(rawCert)
	if len(fp) != 95 { // 32 bytes * 2 hex chars + 31 colons = 95 chars
		t.Errorf("expected fingerprint length 95, got %d (%s)", len(fp), fp)
	}
	if !strings.Contains(fp, ":") {
		t.Errorf("expected colon-separated fingerprint, got %s", fp)
	}
}

func TestTOFUVerificationAndMismatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mdd-tofu-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	oldHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	host := "198.51.100.1"
	cert1 := []byte("server-cert-version-1")
	cert2 := []byte("server-cert-version-2-changed")

	// 1. First use -> should pin and succeed
	err = verifyOrPinFingerprint(host, cert1, "", false)
	if err != nil {
		t.Fatalf("first connection TOFU failed: %v", err)
	}

	// 2. Second use with same cert -> should succeed
	err = verifyOrPinFingerprint(host, cert1, "", false)
	if err != nil {
		t.Fatalf("matching certificate verification failed: %v", err)
	}

	// 3. Changed cert without resetPin -> should FAIL with security alert
	err = verifyOrPinFingerprint(host, cert2, "", false)
	if err == nil {
		t.Fatal("expected certificate mismatch error, but verification passed!")
	}
	if !strings.Contains(err.Error(), "MISMATCH") {
		t.Errorf("expected MISMATCH in error message, got: %v", err)
	}

	// 4. Reset pin -> should overwrite and succeed
	err = verifyOrPinFingerprint(host, cert2, "", true)
	if err != nil {
		t.Fatalf("resetPin failed to overwrite fingerprint: %v", err)
	}

	// 5. Explicit pin match
	fp2 := formatFingerprint(cert2)
	err = verifyOrPinFingerprint(host, cert2, fp2, false)
	if err != nil {
		t.Fatalf("explicit pin matching failed: %v", err)
	}

	// 6. Explicit pin mismatch
	err = verifyOrPinFingerprint(host, cert2, "00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF", false)
	if err == nil {
		t.Fatal("expected explicit pin mismatch error, but got nil")
	}
}
