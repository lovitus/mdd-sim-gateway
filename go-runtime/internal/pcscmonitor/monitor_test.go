package pcscmonitor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ebfe/scard"
)

type fakePCSC struct {
	mu       sync.Mutex
	names    []string
	states   map[string]scard.ReaderState
	waitErr  error
	canceled bool
	released bool
}

func (fake *fakePCSC) ListReaders() ([]string, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.names...), nil
}

func (fake *fakePCSC) GetStatusChange(states []scard.ReaderState, timeout time.Duration) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if timeout > 0 && fake.waitErr != nil {
		return fake.waitErr
	}
	for index := range states {
		state, exists := fake.states[states[index].Reader]
		if !exists {
			return scard.ErrUnknownReader
		}
		states[index].EventState = state.EventState
		states[index].Atr = append([]byte(nil), state.Atr...)
	}
	return nil
}

func (fake *fakePCSC) Cancel() error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.canceled = true
	return nil
}

func (fake *fakePCSC) Release() error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.released = true
	return nil
}

func TestScanPreservesSessionUntilCardGenerationChanges(t *testing.T) {
	fake := &fakePCSC{
		names: []string{"same-model 00 00", "same-model 00 01"},
		states: map[string]scard.ReaderState{
			"same-model 00 00": {EventState: scard.StatePresent | scard.StateFlag(1<<16), Atr: []byte{1, 2}},
			"same-model 00 01": {EventState: scard.StateEmpty | scard.StateFlag(1<<16)},
		},
	}
	monitor := newMonitor(fake, "monitor")
	first, err := monitor.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !first[0].CardPresent || first[0].SessionGeneration == "" || first[1].CardPresent {
		t.Fatalf("unexpected first snapshot: %+v", first)
	}
	second, err := monitor.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second[0].SessionGeneration != first[0].SessionGeneration {
		t.Fatalf("unchanged card generation changed: %q -> %q", first[0].SessionGeneration, second[0].SessionGeneration)
	}

	fake.mu.Lock()
	fake.states["same-model 00 00"] = scard.ReaderState{
		EventState: scard.StatePresent | scard.StateFlag(2<<16), Atr: []byte{1, 2},
	}
	fake.mu.Unlock()
	third, err := monitor.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if third[0].SessionGeneration == first[0].SessionGeneration {
		t.Fatal("PC/SC card event did not replace the transient session generation")
	}
}

func TestRemovalAndReinsertWithSameATRCreatesNewGeneration(t *testing.T) {
	fake := &fakePCSC{
		names: []string{"reader"},
		states: map[string]scard.ReaderState{
			"reader": {EventState: scard.StatePresent, Atr: []byte{9}},
		},
	}
	monitor := newMonitor(fake, "monitor")
	first, _ := monitor.Scan(context.Background())
	fake.states["reader"] = scard.ReaderState{EventState: scard.StateEmpty}
	removed, _ := monitor.Scan(context.Background())
	if removed[0].CardPresent || removed[0].SessionGeneration != "" {
		t.Fatalf("removed card remains active: %+v", removed[0])
	}
	fake.states["reader"] = scard.ReaderState{EventState: scard.StatePresent, Atr: []byte{9}}
	reinserted, _ := monitor.Scan(context.Background())
	if reinserted[0].SessionGeneration == first[0].SessionGeneration {
		t.Fatal("same-ATR reinsert reused a stopped session generation")
	}
}

func TestWaitTimeoutIsARescanNotFailure(t *testing.T) {
	fake := &fakePCSC{
		names: []string{"reader"}, states: map[string]scard.ReaderState{
			"reader": {EventState: scard.StateEmpty},
		}, waitErr: scard.ErrTimeout,
	}
	monitor := newMonitor(fake, "monitor")
	if _, err := monitor.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Wait(context.Background(), nil, time.Millisecond); err != nil {
		t.Fatalf("timeout should trigger a normal rescan: %v", err)
	}
}

func TestEmptyReaderSetWaitHonorsCancellation(t *testing.T) {
	monitor := newMonitor(&fakePCSC{}, "monitor")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := monitor.Wait(ctx, nil, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
}

func TestCloseCancelsAndReleasesOnce(t *testing.T) {
	fake := &fakePCSC{}
	monitor := newMonitor(fake, "monitor")
	if err := monitor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Close(); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.canceled || !fake.released {
		t.Fatalf("cancel=%v release=%v", fake.canceled, fake.released)
	}
}
