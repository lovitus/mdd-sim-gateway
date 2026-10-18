// Package pintls constructs HTTPS/WSS clients that authenticate one exact
// SHA-256 certificate fingerprint, including self-signed MDD deployments.
package pintls

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func NewHTTPClient(rawURL, fingerprint string, timeout time.Duration) (*http.Client, error) {
	return NewPolicyHTTPClient(rawURL, "pin", fingerprint, timeout)
}

// Empty mode retains the legacy exact-pin contract, never system fallback.
func NewPolicyHTTPClient(rawURL, mode, fingerprint string, timeout time.Duration) (*http.Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "wss" || parsed.User != nil || parsed.Hostname() == "" || parsed.Port() == "" ||
		parsed.Path == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("pinned TLS client requires an exact wss URL")
	}
	if timeout <= 0 || timeout > time.Minute {
		return nil, errors.New("pinned TLS timeout must be between zero and one minute")
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}
	switch mode {
	case "system":
		if strings.TrimSpace(fingerprint) != "" {
			return nil, errors.New("system TLS mode cannot also contain a certificate pin")
		}
	case "", "pin":
		expected, err := normalizeFingerprint(fingerprint)
		if err != nil {
			return nil, err
		}
		tlsConfig.InsecureSkipVerify = true // #nosec G402 -- exact leaf certificate verification below.
		tlsConfig.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("TLS peer supplied no certificate")
			}
			actual := sha256.Sum256(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
				return errors.New("TLS certificate fingerprint mismatch")
			}
			return nil
		}
	default:
		return nil, errors.New("TLS mode must be system or pin")
	}
	transport := &http.Transport{
		TLSClientConfig:     tlsConfig,
		DialContext:         (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: timeout,
		ForceAttemptHTTP2:   false,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("Agent endpoint redirect refused")
	}}, nil
}

func normalizeFingerprint(value string) ([]byte, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), ":", "")
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("TLS fingerprint must contain exactly 32 SHA-256 bytes")
	}
	return decoded, nil
}
