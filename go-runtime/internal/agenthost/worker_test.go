package agenthost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsim"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

const hostTestToken = "0123456789abcdef0123456789abcdef"

type emptyMonitorFactory struct{}
type emptyMonitor struct{}
type unusedConnector struct{}
type acceptingEventSink struct{}
type fakeModemSIMRuntime struct {
	requests []agentmodem.SIMAKARequest
}

type fakePINStatusProber struct{ facts []agentmodem.Fact }
type fakeModemRestarter struct {
	target agentmodem.RecoveryTarget
	err    error
}

func (restarter *fakeModemRestarter) SoftRestart(_ context.Context, target agentmodem.RecoveryTarget) error {
	restarter.target = target
	return restarter.err
}

func (prober *fakePINStatusProber) Probe(context.Context) ([]agentmodem.Fact, error) {
	return prober.facts, nil
}
func (prober *fakePINStatusProber) ProbeSIMPINStatus(context.Context) ([]agentmodem.Fact, error) {
	return prober.facts, nil
}

func (acceptingEventSink) AcceptModemEvent(context.Context, agentlink.AgentEventContext, agentlink.ModemEvent) agentlink.ModemEventDisposition {
	return agentlink.ModemEventDisposition{Accepted: true}
}

func (runtime *fakeModemSIMRuntime) Probe(context.Context) ([]agentmodem.Fact, error) {
	return nil, nil
}
func (runtime *fakeModemSIMRuntime) AuthenticateSIMAKA(_ context.Context, request agentmodem.SIMAKARequest) (agentmodem.SIMAKAResult, error) {
	runtime.requests = append(runtime.requests, request)
	return agentmodem.SIMAKAResult{Body: []byte{0xdb, 0x01, 0x01}, SW1: 0x90, SW2: 0}, nil
}

type passAuxiliary struct{}

func (passAuxiliary) DoAuxiliary(ctx context.Context, _ string, callback func(context.Context) error) error {
	return callback(ctx)
}

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
	if topology := worker.Topology(); topology.ReaderCondition != agentlink.ReaderStarting || len(topology.Readers) != 0 {
		t.Fatalf("topology before Run=%+v", topology)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx, func() { close(ready) }) }()
	select {
	case <-ready:
		if topology := worker.Topology(); topology.ReaderCondition != agentlink.ReaderReady || len(topology.Readers) != 0 {
			t.Fatalf("topology while running=%+v", topology)
		}
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
	if topology := worker.Topology(); topology.ReaderCondition != agentlink.ReaderStarting || len(topology.Readers) != 0 {
		t.Fatalf("topology after Run=%+v", topology)
	}
}

func TestAgentHostReadsExactModemPINStatusWithoutCredential(t *testing.T) {
	attempts := uint32(3)
	fact := agentmodem.Fact{AttachmentID: "attachment-1", EquipmentID: "862547055201716",
		Condition: agentmodem.DeviceReady, SessionGenerationAuthority: true,
		AT: agentmodem.ATControlFact{State: agentmodem.ATControlReady},
		SIM: agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: "89010000000000000001",
			SessionGeneration: "session-1", PINState: "pin_required", PINAttempts: &attempts}}
	config := testHostConfig("ws://127.0.0.1:1/v1/agent/ws", http.DefaultClient)
	config.Modems = &fakePINStatusProber{facts: []agentmodem.Fact{fact}}
	config.ModemAuxiliary = passAuxiliary{}
	worker, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	worker.modems.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady, Modems: []agentmodem.Fact{fact}})
	response := worker.ExecuteSIMPIN(context.Background(), agentlink.SIMPINRequest{
		OperationID: "pin-status-operation", ProcessGeneration: "process-1", CardID: fact.SIM.ICCID,
		AttachmentID: fact.AttachmentID, EquipmentID: fact.EquipmentID,
		SIMSessionGeneration: fact.SIM.SessionGeneration, Action: agentlink.SIMPINStatus,
	})
	if response.Failure != nil || response.State != "pin_required" ||
		response.AttemptsRemaining == nil || *response.AttemptsRemaining != 3 {
		t.Fatalf("response=%+v", response)
	}
}

func TestAgentHostForwardsExactModemRecoveryFence(t *testing.T) {
	restarter := &fakeModemRestarter{}
	config := testHostConfig("ws://127.0.0.1:1/v1/agent/ws", http.DefaultClient)
	config.Modems = &fakePINStatusProber{}
	config.ModemAuxiliary = passAuxiliary{}
	config.ModemRecovery = restarter
	worker, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	request := agentlink.ModemRecoveryRequest{ModemRecoveryCommand: agentlink.ModemRecoveryCommand{
		OperationID: "restart-1", EquipmentID: "862547055201716", CardID: "8985200000000000001",
		Action: agentlink.ModemSoftRestart}, ProcessGeneration: "process-1",
		AttachmentID: "attachment-1", SIMSessionGeneration: "session-1"}
	response := worker.ExecuteModemRecovery(t.Context(), request)
	if response.Failure != nil || response.State != "accepted" || restarter.target.SIMSessionGeneration != "session-1" ||
		restarter.target.AttachmentID != "attachment-1" {
		t.Fatalf("response=%+v target=%+v", response, restarter.target)
	}
}

func TestAgentHostConnectsOutboundWSSWithoutOwningInboundHardwarePort(t *testing.T) {
	server, _ := agentlink.NewServer(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) {
		return hostTestToken, nil
	}))
	if err := server.SetModemEventSink(acceptingEventSink{}); err != nil {
		t.Fatal(err)
	}
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
		if found && status.ProcessGeneration != "" && !status.LastReport.IsZero() && status.Topology != nil {
			if len(status.Capabilities) != 2 || !slices.Contains(status.Capabilities, "reader-readback-v1") ||
				!slices.Contains(status.Capabilities, "sim-pin-v1") {
				t.Fatalf("PC/SC-only Agent capability=%+v, want reader readback and SIM PIN", status.Capabilities)
			}
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

type closingHostModem struct {
	mu         sync.Mutex
	closeCount int
}

func (modem *closingHostModem) Probe(ctx context.Context) ([]agentmodem.Fact, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (modem *closingHostModem) Close() error {
	modem.mu.Lock()
	modem.closeCount++
	modem.mu.Unlock()
	return nil
}

func (modem *closingHostModem) closes() int {
	modem.mu.Lock()
	defer modem.mu.Unlock()
	return modem.closeCount
}

func TestAgentHostClosesPersistentModemOwnerExactlyOnce(t *testing.T) {
	modem := &closingHostModem{}
	config := testHostConfig("ws://127.0.0.1:1/v1/agent/ws", http.DefaultClient)
	config.Modems = modem
	worker, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx, func() {}) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() err=%v", err)
	}
	if count := modem.closes(); count != 0 {
		t.Fatalf("runtime loop closed persistent modem owner %d times", count)
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	if count := modem.closes(); count != 1 {
		t.Fatalf("Agent host closed persistent modem owner %d times", count)
	}
}

func TestAgentHostRoutesOnlyCurrentTypedModemSIMGeneration(t *testing.T) {
	runtime := &fakeModemSIMRuntime{}
	config := testHostConfig("ws://127.0.0.1:1/v1/agent/ws", http.DefaultClient)
	config.Modems, config.ModemSIMs, config.ModemAuxiliary = runtime, runtime, passAuxiliary{}
	worker, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	fact := agentmodem.Fact{
		AttachmentID: "mbn-a", EquipmentID: "862547055201716", Condition: agentmodem.DeviceReady,
		AT:  agentmodem.ATControlFact{State: agentmodem.ATControlReady, Port: "COM16", SIMAPDU: true},
		SIM: agentmodem.SIMFact{State: agentmodem.SIMReady, ICCID: "8985200000000000001"},
		Network: agentmodem.NetworkFact{Registration: agentmodem.RegistrationRoaming,
			SoftwareRadio: agentmodem.RadioOn, HardwareRadio: agentmodem.RadioOn, Data: agentmodem.DataDisconnected},
	}
	worker.modems.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady, Modems: []agentmodem.Fact{fact}})
	topology := worker.Topology()
	generation := topology.Modems[0].SIM.SessionGeneration
	request := agentlink.AKARequest{
		OperationID: "aka-1", SessionGeneration: generation, CardID: fact.SIM.ICCID,
		DeviceKind: agentlink.AKADeviceModem, AttachmentID: fact.AttachmentID, EquipmentID: fact.EquipmentID,
		Application: agentlink.AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
	}
	response := worker.AuthenticateAKA(context.Background(), request)
	if response.Failure != nil || response.SW1 != 0x90 || len(runtime.requests) != 1 {
		t.Fatalf("response=%+v requests=%+v", response, runtime.requests)
	}
	worker.modems.observe(agentmodem.Observation{Condition: agentmodem.ConditionReady})
	response = worker.AuthenticateAKA(context.Background(), request)
	if response.Failure == nil || response.Failure.Code != "modem_sim_session_replaced" || len(runtime.requests) != 1 {
		t.Fatalf("stale response=%+v requests=%+v", response, runtime.requests)
	}
}

func testHostConfig(serverURL string, client *http.Client) Config {
	return Config{
		ServerURL: serverURL, ServerToken: hostTestToken, AgentID: "agent-1", HTTPClient: client,
		Monitors: emptyMonitorFactory{}, Connector: unusedConnector{}, ScanEvery: 10 * time.Millisecond,
		Recovery: recovery.Policy{Base: time.Millisecond, Cap: 10 * time.Millisecond},
	}
}
