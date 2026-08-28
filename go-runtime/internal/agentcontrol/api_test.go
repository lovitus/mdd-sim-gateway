package agentcontrol

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

const testControlToken = "0123456789abcdef0123456789abcdef"

func testAPI(t *testing.T, worker *fakeWorker) (*API, *Controller) {
	t.Helper()
	controller, err := New(worker, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewAPI(controller, testControlToken, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	return api, controller
}

type staticTopology struct{ snapshot agentlink.TopologySnapshot }

func (provider staticTopology) Topology() agentlink.TopologySnapshot { return provider.snapshot }

func TestTopologyClientUsesTheAuthenticatedLocalFact(t *testing.T) {
	worker := &fakeWorker{ready: true, exit: make(chan error)}
	controller, err := New(worker, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	expected := agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{{
		ReaderName: "Reader B", IdentityState: agentlink.CardAbsent,
	}, {
		ReaderName: "Reader A", IdentityState: agentlink.CardAbsent,
	}}}
	api, err := NewAPI(controller, testControlToken, time.Second, staticTopology{expected})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api)
	defer server.Close()
	client, err := NewClient(server.URL, testControlToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	topology, err := client.Topology(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Readers) != 2 || topology.Readers[0].ReaderName != "Reader A" || topology.Readers[1].ReaderName != "Reader B" {
		t.Fatalf("topology=%+v", topology)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = client.Topology(context.Background())
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Code != "topology_unavailable" {
		t.Fatalf("stopped topology error=%#v", err)
	}
}

func TestTopologyUnavailableIsExplicit(t *testing.T) {
	api, _ := testAPI(t, &fakeWorker{ready: true, exit: make(chan error)})
	server := httptest.NewServer(api)
	defer server.Close()
	client, err := NewClient(server.URL, testControlToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Topology(context.Background())
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Status != http.StatusServiceUnavailable || apiError.Code != "topology_unavailable" {
		t.Fatalf("topology error=%#v", err)
	}
}

func TestServiceCLIAndGUIClientShareOneController(t *testing.T) {
	worker := &fakeWorker{ready: true, exit: make(chan error)}
	api, _ := testAPI(t, worker)
	server := httptest.NewServer(api)
	defer server.Close()
	client, err := NewClient(server.URL, testControlToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if status, err := client.Status(context.Background()); err != nil || status.State != StateStopped {
		t.Fatalf("initial status = %+v, error = %v", status, err)
	}
	started, err := client.Start(context.Background())
	if err != nil || started.State != StateRunning {
		t.Fatalf("started = %+v, error = %v", started, err)
	}
	_, err = client.Start(context.Background())
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Status != http.StatusConflict || apiError.Code != "runtime_conflict" {
		t.Fatalf("duplicate start error = %#v", err)
	}
	stopped, err := client.Stop(context.Background())
	if err != nil || stopped.State != StateStopped {
		t.Fatalf("stopped = %+v, error = %v", stopped, err)
	}
	if worker.runCount() != 1 {
		t.Fatalf("worker runs = %d", worker.runCount())
	}
}

func TestUnauthorizedResponseNeverEchoesToken(t *testing.T) {
	api, _ := testAPI(t, &fakeWorker{ready: true, exit: make(chan error)})
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	request.Header.Set("Authorization", "Bearer wrong-"+testControlToken)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusUnauthorized || strings.Contains(string(body), testControlToken) ||
		string(body) != "{\"code\":\"unauthorized\"}\n" {
		t.Fatalf("response = %d %q", response.Code, body)
	}
}

func TestClientRefusesToSendTokenOffLoopback(t *testing.T) {
	for _, target := range []string{"http://10.44.0.23:39091", "https://127.0.0.1:39091", "http://example.com:39091", "http://localhost:39091"} {
		if _, err := NewClient(target, testControlToken, nil); err == nil {
			t.Errorf("accepted unsafe target %s", target)
		}
	}
}

func TestFixedLoopbackListenerIsTheCrossProcessSingleton(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	first, err := ListenLoopback(address)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := ListenLoopback(address); err == nil {
		second.Close()
		t.Fatal("a second Agent control host bound the same address")
	}
}

func TestListenerRequiresLiteralLoopbackAndFixedPort(t *testing.T) {
	for _, address := range []string{"0.0.0.0:39091", "10.44.0.23:39091", "127.0.0.1:0", "localhost:39091", "bad"} {
		if listener, err := ListenLoopback(address); err == nil {
			listener.Close()
			t.Errorf("accepted unsafe singleton address %s", address)
		}
	}
}
