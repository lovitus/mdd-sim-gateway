package systemstatus

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fixedProvenanceSource struct{ value Provenance }

func (source fixedProvenanceSource) CollectProvenance(context.Context) Provenance {
	return source.value
}

type fixedHostSource struct{ value HostMeasurements }

func (source fixedHostSource) CollectHost(context.Context, string) HostMeasurements {
	return source.value
}

type fixedUnitSource struct{ value Section[SystemdInfo] }

func (source fixedUnitSource) CollectUnits(context.Context) Section[SystemdInfo] { return source.value }

type blockingProvenanceSource struct {
	started chan struct{}
	release chan struct{}
}

func (source blockingProvenanceSource) CollectProvenance(context.Context) Provenance {
	close(source.started)
	<-source.release
	return Provenance{Kind: "release", State: "verified", Verified: true, Code: "release_manifest_verified"}
}

type observedHostSource struct {
	calls *atomic.Int32
	at    chan time.Time
}

func (source observedHostSource) CollectHost(context.Context, string) HostMeasurements {
	source.calls.Add(1)
	source.at <- time.Now()
	return completeHostMeasurements()
}

func TestCombinedCollectorKeepsOptionalTemperatureUnavailableWithoutFalsePartial(t *testing.T) {
	host := completeHostMeasurements()
	collector := combinedCollector{
		dataPath: "/var/lib/mdd", provenance: fixedProvenanceSource{value: Provenance{
			Kind: "release", State: "verified", Verified: true, Code: "release_manifest_verified",
		}}, host: fixedHostSource{value: host}, units: fixedUnitSource{value: availableSection(SystemdInfo{
			Fixed: []UnitStatus{}, Providers: []UnitStatus{}, ProvidersLoadedOnly: true,
		})},
	}
	result := collector.Collect(t.Context())
	if result.State != "complete" || result.Code != "sample_complete" || len(result.Errors) != 0 ||
		result.Temperatures.State != SectionUnavailable {
		t.Fatalf("result=%+v", result)
	}
	host.Network.Value.Errors = []string{"network_counter_unavailable"}
	collector.host = fixedHostSource{value: host}
	result = collector.Collect(t.Context())
	if result.State != "partial" || len(result.Errors) != 1 || result.Errors[0] != "network_counter_unavailable" {
		t.Fatalf("network partial=%+v", result)
	}
}

func TestCombinedCollectorDoesNotHideInvalidRelease(t *testing.T) {
	collector := combinedCollector{
		dataPath: "/var/lib/mdd", provenance: fixedProvenanceSource{value: Provenance{
			Kind: "release", State: "release_invalid", Code: "release_validation_failed",
		}}, host: fixedHostSource{value: completeHostMeasurements()},
		units: fixedUnitSource{value: availableSection(SystemdInfo{})},
	}
	result := collector.Collect(t.Context())
	if result.State != "partial" || result.Provenance.Kind != "release" || result.Provenance.Verified ||
		len(result.Errors) != 1 || result.Errors[0] != "provenance:release_validation_failed" {
		t.Fatalf("result=%+v", result)
	}
}

func TestCombinedCollectorSamplesHostOnlyAfterSlowProvenance(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	hostAt := make(chan time.Time, 1)
	var hostCalls atomic.Int32
	collector := combinedCollector{
		dataPath:   "/var/lib/mdd",
		provenance: blockingProvenanceSource{started: started, release: release},
		host:       observedHostSource{calls: &hostCalls, at: hostAt},
		units:      fixedUnitSource{value: availableSection(SystemdInfo{})},
	}
	done := make(chan Snapshot, 1)
	go func() { done <- collector.Collect(t.Context()) }()
	<-started
	time.Sleep(10 * time.Millisecond)
	if hostCalls.Load() != 0 {
		t.Fatalf("host sampled before provenance: %d", hostCalls.Load())
	}
	releasedAt := time.Now()
	close(release)
	select {
	case sampledAt := <-hostAt:
		if sampledAt.Before(releasedAt) {
			t.Fatalf("host sampled at %s before provenance released at %s", sampledAt, releasedAt)
		}
	case <-time.After(time.Second):
		t.Fatal("host was not sampled after provenance")
	}
	select {
	case result := <-done:
		if result.State != "complete" {
			t.Fatalf("result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not complete")
	}
}

func completeHostMeasurements() HostMeasurements {
	network := NetworkInfo{Interfaces: []NetworkInterface{{Name: "eth0", IOAvailable: true}}}
	return HostMeasurements{
		Host:         availableSection(HostInfo{UptimeSeconds: 1}),
		CPU:          availableSection(CPUInfo{LogicalCores: 1}),
		Load:         availableSection(LoadInfo{}),
		Memory:       availableSection(MemoryInfo{TotalBytes: 1}),
		Swap:         availableSection(SwapInfo{}),
		Disk:         availableSection(DiskInfo{TotalBytes: 1}),
		Network:      availableSection(network),
		Temperatures: unavailableSection[TemperatureInfo]("temperature_unavailable"),
	}
}
