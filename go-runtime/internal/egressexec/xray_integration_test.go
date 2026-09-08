package egressexec

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressprobe"
)

// CI runs this binary in a fresh network namespace with only lo. The DNS
// responder is a labelled fixture; actual Xray client/server carry its UDP.
func TestXrayRealProcessXHTTPUDP(t *testing.T) {
	binary := os.Getenv("MDD_TEST_XRAY")
	if binary == "" {
		t.Skip("requires the pinned CI Xray binary and isolated network namespace")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("Xray test binary must be absolute")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, network := range interfaces {
		if network.Flags&net.FlagLoopback == 0 {
			t.Fatal("integration test requires loopback-only namespace")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	camouflage := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	camouflage.EnableHTTP2 = true
	camouflage.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	camouflage.StartTLS()
	t.Cleanup(camouflage.Close)
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		for {
			n, client, err := udp.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			query := buffer[:n]
			answer := make([]byte, 12, n+16)
			copy(answer[:2], query[:2])
			answer[2], answer[3], answer[5], answer[7] = 0x81, 0x80, 1, 1
			answer = append(answer, query[12:]...)
			answer = append(answer, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4, 192, 0, 2, 1)
			_, _ = udp.WriteToUDP(answer, client)
		}
	}()
	t.Cleanup(func() { _ = udp.Close(); <-done })
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverPort, err := availableLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	const uuid = "00000000-0000-4000-8000-000000000001"
	server := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []map[string]any{{"listen": "127.0.0.1", "port": serverPort, "protocol": "vless",
			"settings": map[string]any{"clients": []map[string]any{{"id": uuid}}, "decryption": "none"},
			"streamSettings": map[string]any{"network": "xhttp", "security": "reality",
				"xhttpSettings": map[string]any{"path": "/fixture", "mode": "auto"},
				"realitySettings": map[string]any{"target": camouflage.Listener.Addr().String(), "serverNames": []string{"example.com"},
					"privateKey": base64.RawURLEncoding.EncodeToString(key.Bytes()), "shortIds": []string{"abcd"}}}}},
		"outbounds": []map[string]any{{"protocol": "freedom", "settings": map[string]any{"redirect": udp.LocalAddr().String()}}},
	}
	controller := xrayProcessController{}
	start := func(name string, payload []byte, ports []int) managedProcess {
		t.Helper()
		path := filepath.Join(t.TempDir(), name+".json")
		if err := atomicWrite(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := controller.Check(ctx, binary, path); err != nil {
			t.Fatal(name, err)
		}
		child, err := controller.Start(binary, path)
		if err != nil {
			t.Fatal(name, err)
		}
		t.Cleanup(func() {
			if err := child.Stop(3 * time.Second); err != nil && child.Running() {
				t.Error(name, "did not stop")
			}
		})
		if err := controller.WaitReady(ctx, ports, child, 5*time.Second); err != nil {
			t.Fatal(name, err)
		}
		return child
	}
	payload, err := json.Marshal(server)
	if err != nil {
		t.Fatal(err)
	}
	start("server", payload, []int{serverPort})
	node := subscriptionNode{Type: "vless", Network: "xhttp", Server: "127.0.0.1", Port: serverPort, UUID: uuid,
		ServerName: "example.com", Fingerprint: "chrome", PacketEncoding: "xudp",
		Reality: map[string]string{"public-key": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "short-id": "abcd"},
		XHTTP:   xhttpOptions{Path: "/fixture", Mode: "packet-up"}}
	bridges := &xhttpBridges{allocatePort: availableLoopbackPort, reserved: map[int]bool{serverPort: true}}
	bridge, err := bridges.outbound(node, "fixture", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	payload, err = bridges.config()
	if err != nil {
		t.Fatal(err)
	}
	port := bridge["server_port"].(int)
	start("client", payload, []int{port})
	result, err := egressprobe.Probe(ctx, fmt.Sprintf("socks5://127.0.0.1:%d", port))
	if err != nil || result.Target == "" {
		t.Fatal("real XHTTP UDP fixture failed", err)
	}
	// This is not a public resolver observation: Xray redirects to our fixture,
	// and the network namespace prevents accidental external connectivity.
}
