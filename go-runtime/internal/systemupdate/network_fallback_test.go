package systemupdate

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/updatenetwork"
	socks5 "github.com/things-go/go-socks5"
)

type unavailableUpdateTransport struct{ calls atomic.Int32 }

func (transport *unavailableUpdateTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return nil, errors.New("fixture direct route unavailable")
}

type preserveUpdateHost struct{}

func (preserveUpdateHost) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

func TestCheckerFallsBackThroughAuthenticatedSOCKSWithoutExternalNetwork(t *testing.T) {
	var proxied atomic.Int32
	var received atomic.Int32
	archive := []byte("deliberately invalid fixture archive")
	digest := sha256.Sum256(archive)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		switch r.URL.Path {
		case "/repos/example/project/releases/latest":
			_, _ = w.Write([]byte(`{"tag_name":"v2.4.0","html_url":"https://example.invalid/release"}`))
		case "/repos/example/project/releases/tags/v2.4.0":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v2.4.0", "assets": []map[string]any{{"name": "mdd-2.4.0-linux-amd64.tar", "browser_download_url": "https://api.github.com/fixture-asset", "digest": "sha256:" + hex.EncodeToString(digest[:]), "size": len(archive)}}})
		case "/fixture-asset":
			_, _ = w.Write(archive)
		default:
			t.Error("unexpected release route")
			w.WriteHeader(404)
		}
	}))
	defer target.Close()
	certificate := target.Certificate()
	if len(certificate.DNSNames) == 0 {
		t.Fatal("fixture certificate has no DNS name")
	}
	pool := x509.NewCertPool()
	pool.AddCert(certificate)
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, ServerName: certificate.DNSNames[0]}
	http.DefaultTransport = transport
	defer func() { transport.CloseIdleConnections(); http.DefaultTransport = previous }()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := socks5.NewServer(socks5.WithCredential(socks5.StaticCredentials{"fixture-user": "fixture-secret"}),
		socks5.WithResolver(preserveUpdateHost{}), socks5.WithDial(func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "api.github.com:443" {
				return nil, errors.New("unexpected fixture destination")
			}
			proxied.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, target.Listener.Addr().String())
		}))
	done := make(chan struct{})
	go func() { _ = server.Serve(listener); close(done) }()
	defer func() { _ = listener.Close(); <-done }()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	config := egressconfig.Snapshot{Revision: 1, Config: egressconfig.Config{Profiles: map[string]egressconfig.Profile{
		"fixture-proxy": {Type: "socks5", Server: host, Port: port, Username: "fixture-user", Password: "fixture-secret"},
	}}}
	direct := &unavailableUpdateTransport{}
	checker, err := NewChecker("example/project", "2.3.0", &http.Client{Transport: direct}, func() (string, []updatenetwork.Route, error) {
		routes, err := updatenetwork.Candidates(updatenetwork.Selection{Mode: "auto"}, config, egressstatus.Snapshot{}, "")
		return "fixture-policy", routes, err
	})
	if err != nil {
		t.Fatal(err)
	}
	result := checker.Check(t.Context(), true)
	if !result.OK || result.Network == nil || result.Network.ProfileID != "fixture-proxy" || direct.calls.Load() != 1 || proxied.Load() != 1 {
		t.Fatal("direct-to-SOCKS fallback failed", result, direct.calls.Load(), proxied.Load())
	}
	wire, err := json.Marshal(result.Network)
	if err != nil {
		t.Fatal(err)
	}
	var recorded updatenetwork.Route
	if err := json.Unmarshal(wire, &recorded); err != nil {
		t.Fatal(err)
	}
	resolved, err := updatenetwork.ResolveRecorded(recorded, config, egressstatus.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := resolved.Client(0)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if _, err := FetchAndStage(t.Context(), "example/project", "2.4.0", filepath.Join(t.TempDir(), "staged"), client); err == nil {
		t.Fatal("invalid downloaded archive was accepted")
	}
	if received.Load() != 3 || proxied.Load() != 2 || direct.calls.Load() != 1 {
		t.Fatal("download did not reuse the verified proxy route", received.Load(), proxied.Load())
	}
}
