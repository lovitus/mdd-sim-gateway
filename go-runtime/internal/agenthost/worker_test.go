package agenthost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsim"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

const hostTestToken = "0123456789abcdef0123456789abcdef"

type emptyMonitorFactory struct{}
type emptyMonitor struct{}
type unusedConnector struct{}

func (emptyMonitorFactory) Open(context.Context) (agentreader.Monitor, error) {
	return emptyMonitor{}, nil
}
func (emptyMonitor) Scan(context.Context) ([]agentreader.Reader, error) { return nil, nil }
func (emptyMonitor) Wait(ctx context.Context, _ []agentreader.Reader, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}
func (emptyMonitor) Close() error { return nil }
func (unusedConnector) Connect(string) (agentsim.Card, error) {
	return nil, errors.New("unexpected card connection")
}

func TestAgentHostBecomesLocallyReadyWhileCoreIsOffline(t *testing.T) {
	worker, err := New(testHostConfig("ws://127.0.0.1:1/v1/agent/ws", http.DefaultClient))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx, func() { close(ready) }) }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("offline Agent host did not become locally ready")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Agent host did not stop")
	}
}

func TestAgentHostConnectsOutboundWSSWithoutOwningInboundHardwarePort(t *testing.T) {
	server, _ := agentlink.NewServer(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) {
		return hostTestToken, nil
	}))
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	worker, err := New(testHostConfig("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/v1/agent/ws", http.DefaultClient))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx, func() {}) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, found := server.Status("agent-1")
		if found && status.ProcessGeneration != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Agent host did not establish outbound WSS")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() err=%v", err)
	}
}

func testHostConfig(serverURL string, client *http.Client) Config {
	return Config{
		ServerURL: serverURL, ServerToken: hostTestToken, AgentID: "agent-1", HTTPClient: client,
		Monitors: emptyMonitorFactory{}, Connector: unusedConnector{}, ScanEvery: 10 * time.Millisecond,
		Recovery: recovery.Policy{Base: time.Millisecond, Cap: 10 * time.Millisecond},
	}
}
