package cellulardata

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
)

type failedProbeStop struct {
	fakeAgents
	stops   int
	failure error
}

func (agent *failedProbeStop) ExecuteModemData(context.Context, string, string, agentlink.ModemDataRequest) (agentlink.ModemDataResponse, error) {
	agent.stops++
	return agentlink.ModemDataResponse{}, agent.failure
}

func TestProbeStopRetainsUnconfirmedCleanupError(t *testing.T) {
	broker, err := agentdata.NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return internalTestToken, nil }), nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	agents := &failedProbeStop{failure: errors.New("stop unconfirmed")}
	service := &Service{config: Config{Agents: agents, Broker: broker}, byLine: map[string]*session{}, byID: map[string]*session{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current := &session{service: service, id: "probe-session", lineID: "line", purpose: "probe:once", ctx: ctx, cancel: cancel, listener: listener, streams: map[string]net.Conn{}}
	service.byLine["line"], service.byID[current.id] = current, current
	for range 2 {
		if err := current.stop("probe_complete"); !errors.Is(err, agents.failure) {
			t.Fatalf("cleanup error lost: %v", err)
		}
	}
	if agents.stops != 1 || len(service.byID) != 0 || ctx.Err() == nil {
		t.Fatalf("stops=%d remaining=%d", agents.stops, len(service.byID))
	}
}
