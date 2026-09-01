package systemstatus

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
)

type Collector interface {
	Collect(context.Context) Snapshot
}

type combinedCollector struct {
	dataPath   string
	provenance ProvenanceSource
	host       HostSource
	units      UnitSource
}

func newDefaultCollector(dataPath string) Collector {
	return &combinedCollector{
		dataPath: filepath.Clean(dataPath), provenance: newProvenanceSource(),
		host: newHostSource(), units: newUnitSource(),
	}
}

func (collector *combinedCollector) Collect(ctx context.Context) Snapshot {
	provenance := collector.provenance.CollectProvenance(ctx)
	measurements := collector.host.CollectHost(ctx, collector.dataPath)
	snapshot := Snapshot{
		SchemaVersion: SchemaVersion,
		Provenance:    provenance,
		Host:          measurements.Host,
		CPU:           measurements.CPU,
		Load:          measurements.Load,
		Memory:        measurements.Memory,
		Swap:          measurements.Swap,
		Disk:          measurements.Disk,
		Network:       measurements.Network,
		Temperatures:  measurements.Temperatures,
		Systemd:       collector.units.CollectUnits(ctx),
		Alerts:        []Alert{}, Errors: []string{},
	}
	for _, section := range []struct {
		state    string
		code     string
		key      string
		optional bool
	}{
		{snapshot.Host.State, snapshot.Host.Code, "host", false},
		{snapshot.CPU.State, snapshot.CPU.Code, "cpu", false},
		{snapshot.Load.State, snapshot.Load.Code, "load", false},
		{snapshot.Memory.State, snapshot.Memory.Code, "memory", false},
		{snapshot.Swap.State, snapshot.Swap.Code, "swap", false},
		{snapshot.Disk.State, snapshot.Disk.Code, "disk", false},
		{snapshot.Network.State, snapshot.Network.Code, "network", false},
		{snapshot.Temperatures.State, snapshot.Temperatures.Code, "temperatures", true},
		{snapshot.Systemd.State, snapshot.Systemd.Code, "systemd", false},
	} {
		if !section.optional && (section.state == SectionError || section.state == SectionUnavailable) {
			snapshot.Errors = append(snapshot.Errors, section.key+":"+section.code)
		}
	}
	if snapshot.Network.Value != nil {
		snapshot.Errors = append(snapshot.Errors, snapshot.Network.Value.Errors...)
	}
	if snapshot.Systemd.Value != nil {
		snapshot.Errors = append(snapshot.Errors, snapshot.Systemd.Value.Errors...)
	}
	if snapshot.Provenance.State == "release_invalid" || snapshot.Provenance.State == "unavailable" {
		snapshot.Errors = append(snapshot.Errors, "provenance:"+snapshot.Provenance.Code)
	}
	snapshot.Errors = compactCodes(snapshot.Errors)
	if len(snapshot.Errors) == 0 {
		snapshot.State, snapshot.Code = "complete", "sample_complete"
	} else {
		snapshot.State, snapshot.Code = "partial", "sample_partial"
	}
	snapshot.Alerts = deriveAlerts(snapshot)
	return snapshot
}

func compactCodes(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 256 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
