package pintls

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPinnedClientAcceptsExactSelfSignedCertificateAndRejectsMismatch(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	certificate := server.Certificate().Raw
	fingerprint := sha256.Sum256(certificate)
	client, err := NewHTTPClient(strings.Replace(server.URL, "https://", "wss://", 1)+"/v1/agent/ws", hex.EncodeToString(fingerprint[:]), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.ForceAttemptHTTP2 || transport.TLSNextProto == nil ||
		len(transport.TLSClientConfig.NextProtos) != 1 || transport.TLSClientConfig.NextProtos[0] != "http/1.1" {
		t.Fatalf("pinned WSS transport permits HTTP/2: %+v", transport)
	}
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	response, err := client.Do(request)
	if err != nil || response.StatusCode != http.StatusNoContent {
		t.Fatalf("pinned request response=%v err=%v", response, err)
	}
	_ = response.Body.Close()

	wrong := strings.Repeat("00", sha256.Size)
	client, _ = NewHTTPClient(strings.Replace(server.URL, "https://", "wss://", 1)+"/v1/agent/ws", wrong, time.Second)
	_, err = client.Do(request)
	if err == nil {
		t.Fatal("mismatched certificate pin was accepted")
	}
}

func TestPinnedClientRejectsInvalidURLPinAndTimeout(t *testing.T) {
	validPin := strings.Repeat("00", sha256.Size)
	for _, test := range []struct {
		url, pin string
		timeout  time.Duration
	}{
		{"https://example.com:443/path", validPin, time.Second},
		{"wss://example.com/path", validPin, time.Second},
		{"wss://example.com:443/path", "00", time.Second},
		{"wss://example.com:443/path", validPin, 0},
	} {
		if _, err := NewHTTPClient(test.url, test.pin, test.timeout); err == nil {
			t.Fatalf("NewHTTPClient(%q) accepted invalid configuration", test.url)
		}
	}
}
