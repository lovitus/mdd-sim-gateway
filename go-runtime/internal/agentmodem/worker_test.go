package agentmodem

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

type sequenceProber struct {
	mu      sync.Mutex
	results []probeResult
}

type closingProber struct {
	sequenceProber
	closed chan struct{}
}

func (prober *closingProber) Close() error {
	close(prober.closed)
	return nil
}

type probeResult struct {
	facts []Fact
	err   error
}

func (prober *sequenceProber) Probe(context.Context) ([]Fact, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	result := prober.results[0]
	if len(prober.results) > 1 {
		prober.results = prober.results[1:]
	}
	return result.facts, result.err
}

func TestWorkerDropsStaleFactsDuringBoundedRecovery(t *testing.T) {
	prober := &sequenceProber{results: []probeResult{
		{facts: []Fact{{AttachmentID: "mbn-1", Condition: DeviceReady}}},
		{err: errors.New("WwanSvc temporarily unavailable")},
		{facts: []Fact{{AttachmentID: "mbn-2", Condition: DeviceReady}}},
	}}
	observations := make(chan Observation, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Worker{
			Prober: prober, Interval: time.Millisecond,
			Recovery: recovery.Policy{Base: time.Millisecond, Cap: time.Millisecond},
			Observed: func(observation Observation) { observations <- observation },
		}).Run(ctx)
	}()
	want := []Condition{ConditionStarting, ConditionReady, ConditionRecovering, ConditionReady}
	for index, condition := range want {
		select {
		case observation := <-observations:
			if observation.Condition != condition {
				t.Fatalf("observation %d condition=%s, want %s", index, observation.Condition, condition)
			}
			if condition == ConditionRecovering && (len(observation.Modems) != 0 || observation.Detail == "") {
				t.Fatalf("recovering observation retained facts or omitted detail: %+v", observation)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for observation %d", index)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() err=%v", err)
	}
}

func TestWorkerReleasesPersistentModemOwnerOnStop(t *testing.T) {
	prober := &closingProber{
		sequenceProber: sequenceProber{results: []probeResult{{facts: []Fact{}}}},
		closed:         make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Worker{
			Prober: prober, Interval: time.Millisecond,
			Recovery: recovery.Policy{Base: time.Millisecond, Cap: time.Millisecond},
			Observed: func(Observation) {},
		}).Run(ctx)
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() err=%v", err)
	}
	select {
	case <-prober.closed:
	case <-time.After(time.Second):
		t.Fatal("persistent modem owner was not closed")
	}
}
