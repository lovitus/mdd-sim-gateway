package systemstatus

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type collectorFunc func(context.Context) Snapshot

func (function collectorFunc) Collect(ctx context.Context) Snapshot { return function(ctx) }

func TestSamplerBoundsHungCollectorAndClose(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var calls atomic.Int32
	collector := collectorFunc(func(context.Context) Snapshot {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return completeTestSnapshot()
	})
	sampler := newTestSampler(t, collector, 15*time.Millisecond, 5*time.Millisecond, time.Now)
	sampler.Start()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collector did not start")
	}
	time.Sleep(70 * time.Millisecond)
	if calls.Load() != 1 || !sampler.collecting.Load() {
		t.Fatalf("calls=%d collecting=%t", calls.Load(), sampler.collecting.Load())
	}
	closed := make(chan struct{})
	go func() { sampler.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Close waited for a blocked collector")
	}
	if snapshot := sampler.Snapshot(time.Now()); snapshot.State != "unavailable" || snapshot.Code != "status_unavailable" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	close(release)
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("late worker caused another collection: %d", calls.Load())
	}
}

func TestSamplerPublishesImmutableSnapshotAndMarksItStale(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 9, 1, 3, 4, 5, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	collector := collectorFunc(func(context.Context) Snapshot { return completeTestSnapshot() })
	sampler := newTestSampler(t, collector, 20*time.Millisecond, 5*time.Millisecond, clock)
	sampler.Start()
	defer sampler.Close()
	deadline := time.Now().Add(time.Second)
	var first Snapshot
	for time.Now().Before(deadline) {
		first = sampler.Snapshot(clock())
		if first.State == "complete" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if first.State != "complete" || first.SampledAt == nil || *first.SampledAt != now || first.Stale {
		t.Fatalf("first=%+v", first)
	}
	first.Errors = append(first.Errors, "mutated")
	first.Network.Value.Interfaces[0].Addresses[0] = "203.0.113.1/32"
	first.Systemd.Value.Fixed[0].Errors = append(first.Systemd.Value.Fixed[0].Errors, "mutated")
	second := sampler.Snapshot(clock())
	if len(second.Errors) != 0 || second.Network.Value.Interfaces[0].Addresses[0] != "192.0.2.10/24" ||
		len(second.Systemd.Value.Fixed[0].Errors) != 0 {
		t.Fatalf("snapshot was mutable: %+v", second)
	}
	mu.Lock()
	now = now.Add(41 * time.Millisecond)
	mu.Unlock()
	stale := sampler.Snapshot(clock())
	if !stale.Stale || stale.State != "stale" || stale.Code != "status_stale" ||
		stale.SampledAt == nil || first.SampledAt == nil || *stale.SampledAt != *first.SampledAt {
		t.Fatalf("stale=%+v", stale)
	}
}

func TestSamplerLateResultUsesCompletionTime(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	now := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	collector := collectorFunc(func(context.Context) Snapshot {
		close(started)
		<-release
		return completeTestSnapshot()
	})
	sampler := newTestSampler(t, collector, 20*time.Millisecond, 5*time.Millisecond, clock)
	sampler.Start()
	defer sampler.Close()
	<-started
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	now = now.Add(time.Minute)
	mu.Unlock()
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result := sampler.Snapshot(clock())
		if result.State == "complete" {
			if result.SampledAt == nil || *result.SampledAt != clock() {
				t.Fatalf("sampled_at=%v completion=%s", result.SampledAt, clock())
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("late result was not accepted")
}

func TestSamplerDoesNotStartAfterClose(t *testing.T) {
	var calls atomic.Int32
	sampler := newTestSampler(t, collectorFunc(func(context.Context) Snapshot {
		calls.Add(1)
		return completeTestSnapshot()
	}), time.Second, 100*time.Millisecond, time.Now)
	sampler.Close()
	sampler.Start()
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("collector calls after Close then Start=%d", calls.Load())
	}
}

func newTestSampler(t *testing.T, collector Collector, interval, timeout time.Duration,
	now func() time.Time) *Sampler {
	t.Helper()
	sampler, err := New(Config{
		Context: t.Context(), DataPath: "/var/lib/mdd-test", Collector: collector,
		Interval: interval, Timeout: timeout, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sampler
}

func completeTestSnapshot() Snapshot {
	network := NetworkInfo{Interfaces: []NetworkInterface{{
		Name: "eth0", MTU: 1500, Addresses: []string{"192.0.2.10/24"}, IOAvailable: true,
	}}}
	units := SystemdInfo{Fixed: []UnitStatus{{
		Name: "mdd-core.service", Role: "core", LoadState: "loaded", ActiveState: "active",
		SubState: "running", Errors: []string{},
	}}, Providers: []UnitStatus{}, ProvidersLoadedOnly: true, Errors: []string{}}
	return Snapshot{
		SchemaVersion: SchemaVersion, State: "complete", Code: "sample_complete",
		Provenance: Provenance{Kind: "release", State: "verified", Verified: true, Code: "release_manifest_verified"},
		Host:       availableSection(HostInfo{Platform: "linux", UptimeSeconds: 1}),
		CPU:        availableSection(CPUInfo{LogicalCores: 1}), Load: availableSection(LoadInfo{}),
		Memory: availableSection(MemoryInfo{TotalBytes: 1}), Swap: availableSection(SwapInfo{}),
		Disk: availableSection(DiskInfo{TotalBytes: 1}), Network: availableSection(network),
		Temperatures: unavailableSection[TemperatureInfo]("temperature_unavailable"),
		Systemd:      availableSection(units), Alerts: []Alert{}, Errors: []string{},
	}
}
