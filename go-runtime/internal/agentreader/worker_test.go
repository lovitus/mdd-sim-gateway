package agentreader

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

type monitorStep struct {
	readers []Reader
	err     error
}

type fakeMonitor struct {
	mu      sync.Mutex
	steps   []monitorStep
	index   int
	changes chan struct{}
	closed  bool
}

func (monitor *fakeMonitor) Scan(context.Context) ([]Reader, error) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	step := monitor.steps[monitor.index]
	return append([]Reader(nil), step.readers...), step.err
}

func (monitor *fakeMonitor) Wait(ctx context.Context, _ []Reader, _ time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-monitor.changes:
		monitor.mu.Lock()
		if monitor.index+1 < len(monitor.steps) {
			monitor.index++
		}
		monitor.mu.Unlock()
		return nil
	}
}

func (monitor *fakeMonitor) Close() error {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.closed = true
	return nil
}

type fakeFactory struct{ monitor Monitor }

func (factory fakeFactory) Open(context.Context) (Monitor, error) { return factory.monitor, nil }

type sessionEvent struct {
	reader string
	start  bool
}

type fakeSessions struct {
	events chan sessionEvent
	runs   chan error
}

func (sessions *fakeSessions) Run(ctx context.Context, reader Reader) error {
	sessions.events <- sessionEvent{reader: reader.Name, start: true}
	select {
	case <-ctx.Done():
		sessions.events <- sessionEvent{reader: reader.Name, start: false}
		return ctx.Err()
	case err := <-sessions.runs:
		return err
	}
}

func testWorker(monitor Monitor, sessions SessionRunner) Worker {
	return Worker{
		Monitors: fakeFactory{monitor: monitor}, Sessions: sessions,
		ScanInterval: time.Millisecond,
		Recovery:     recovery.Policy{Base: time.Millisecond, Cap: 4 * time.Millisecond},
	}
}

func TestHotplugStartsAndRemovalStopsOnlyMatchingSession(t *testing.T) {
	monitor := &fakeMonitor{
		steps: []monitorStep{
			{readers: []Reader{{Name: "reader-a", CardPresent: true, SessionGeneration: "a1"}}},
			{readers: []Reader{
				{Name: "reader-a", CardPresent: false},
				{Name: "reader-b", CardPresent: true, SessionGeneration: "b1"},
			}},
		},
		changes: make(chan struct{}, 1),
	}
	sessions := &fakeSessions{events: make(chan sessionEvent, 8), runs: make(chan error)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() { done <- testWorker(monitor, sessions).Run(ctx, func() { close(ready) }) }()
	<-ready
	if event := <-sessions.events; event != (sessionEvent{reader: "reader-a", start: true}) {
		t.Fatalf("first event = %+v", event)
	}
	monitor.changes <- struct{}{}
	want := map[sessionEvent]bool{
		{reader: "reader-a", start: false}: true,
		{reader: "reader-b", start: true}:  true,
	}
	for range 2 {
		delete(want, <-sessions.events)
	}
	if len(want) != 0 {
		t.Fatalf("missing events: %+v", want)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker error = %v", err)
	}
	if event := <-sessions.events; event != (sessionEvent{reader: "reader-b", start: false}) {
		t.Fatalf("final event = %+v", event)
	}
}

func TestCardGenerationChangeReplacesSession(t *testing.T) {
	monitor := &fakeMonitor{
		steps: []monitorStep{
			{readers: []Reader{{Name: "reader", CardPresent: true, SessionGeneration: "g1"}}},
			{readers: []Reader{{Name: "reader", CardPresent: true, SessionGeneration: "g2"}}},
		}, changes: make(chan struct{}, 1),
	}
	sessions := &fakeSessions{events: make(chan sessionEvent, 8), runs: make(chan error)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- testWorker(monitor, sessions).Run(ctx, func() {}) }()
	if event := <-sessions.events; !event.start {
		t.Fatalf("first event = %+v", event)
	}
	monitor.changes <- struct{}{}
	seenStop, seenSecondStart := false, false
	for range 2 {
		event := <-sessions.events
		seenStop = seenStop || !event.start
		seenSecondStart = seenSecondStart || event.start
	}
	if !seenStop || !seenSecondStart {
		t.Fatal("generation replacement did not stop and start the reader session")
	}
	cancel()
	<-done
	<-sessions.events
}

func TestSessionFailureRetriesWithoutRestartingReaderWorker(t *testing.T) {
	monitor := &fakeMonitor{
		steps:   []monitorStep{{readers: []Reader{{Name: "reader", CardPresent: true, SessionGeneration: "g1"}}}},
		changes: make(chan struct{}),
	}
	sessions := &fakeSessions{events: make(chan sessionEvent, 8), runs: make(chan error, 1)}
	sessions.runs <- errors.New("transport disconnected")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- testWorker(monitor, sessions).Run(ctx, func() {}) }()
	if first := <-sessions.events; !first.start {
		t.Fatalf("first event = %+v", first)
	}
	select {
	case second := <-sessions.events:
		if !second.start {
			t.Fatalf("retry event = %+v", second)
		}
	case <-time.After(time.Second):
		t.Fatal("session was not retried")
	}
	cancel()
	<-done
	<-sessions.events
}

func TestDuplicateAttachmentFailsInsteadOfGuessing(t *testing.T) {
	monitor := &fakeMonitor{
		steps: []monitorStep{{readers: []Reader{
			{Name: "same", CardPresent: true, SessionGeneration: "g1"},
			{Name: "same", CardPresent: true, SessionGeneration: "g2"},
		}}}, changes: make(chan struct{}),
	}
	worker := testWorker(monitor, &fakeSessions{events: make(chan sessionEvent), runs: make(chan error)})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := worker.Run(ctx, func() {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("worker should retry monitor until caller deadline, got %v", err)
	}
}
