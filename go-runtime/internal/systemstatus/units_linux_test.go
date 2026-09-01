//go:build linux

package systemstatus

import (
	"context"
	"errors"
	"testing"

	systemddb "github.com/coreos/go-systemd/v22/dbus"
)

type fakeSystemdConnection struct {
	fixed      []systemddb.UnitStatus
	providers  []systemddb.UnitStatus
	properties map[string]map[string]any
	closed     bool
}

func (connection *fakeSystemdConnection) ListUnitsByNamesContext(context.Context, []string) ([]systemddb.UnitStatus, error) {
	return connection.fixed, nil
}

func (connection *fakeSystemdConnection) ListUnitsByPatternsContext(context.Context, []string, []string) ([]systemddb.UnitStatus, error) {
	return connection.providers, nil
}

func (connection *fakeSystemdConnection) GetUnitTypePropertiesContext(_ context.Context, unit, _ string) (map[string]any, error) {
	properties, found := connection.properties[unit]
	if !found {
		return nil, errors.New("missing fixture")
	}
	return properties, nil
}

func (connection *fakeSystemdConnection) Close() { connection.closed = true }

func TestSystemdSourcePreservesFixedMissingUnitsAndStrictProviders(t *testing.T) {
	connection := &fakeSystemdConnection{
		fixed: []systemddb.UnitStatus{
			{Name: "mdd-core.service", Description: "Core", LoadState: "loaded", ActiveState: "active", SubState: "running"},
			{Name: "mdd-cellular-guard.service", LoadState: "not-found", ActiveState: "inactive", SubState: "dead"},
		},
		providers: []systemddb.UnitStatus{
			{Name: "mdd-vowifi@line-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.service", LoadState: "loaded", ActiveState: "active", SubState: "running"},
			{Name: "mdd-vowifi@not-a-line.service", LoadState: "loaded", ActiveState: "active", SubState: "running"},
		},
		properties: map[string]map[string]any{
			"mdd-core.service": {
				"MainPID": uint32(123), "NRestarts": uint32(0), "Result": "success",
				"ExecMainStartTimestamp": uint64(1_700_000_000_000_000),
			},
			"mdd-vowifi@line-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.service": {
				"MainPID": uint32(456), "NRestarts": uint32(1), "Result": "success",
				"ExecMainStartTimestamp": uint64(1_700_000_001_000_000),
			},
		},
	}
	source := systemdUnitSource{connect: func(context.Context) (systemdConnection, error) { return connection, nil }}
	section := source.CollectUnits(t.Context())
	if section.State != SectionAvailable || section.Value == nil || !connection.closed ||
		len(section.Value.Fixed) != len(fixedUnits) || len(section.Value.Providers) != 1 ||
		!section.Value.ProvidersLoadedOnly {
		t.Fatalf("section=%+v closed=%t", section, connection.closed)
	}
	if section.Value.Fixed[0].Name != "mdd-core.service" || section.Value.Fixed[0].MainPID == nil ||
		*section.Value.Fixed[0].MainPID != 123 || section.Value.Fixed[0].NRestarts == nil ||
		*section.Value.Fixed[0].NRestarts != 0 || section.Value.Fixed[0].StartedAt == nil {
		t.Fatalf("core=%+v", section.Value.Fixed[0])
	}
	if section.Value.Fixed[1].LoadState != "not-found" || section.Value.Fixed[1].MainPID != nil {
		t.Fatalf("guard=%+v", section.Value.Fixed[1])
	}
	for _, missing := range section.Value.Fixed[2:] {
		if missing.LoadState != "not-found" || len(missing.Errors) != 1 ||
			missing.Errors[0] != "systemd_fixed_unit_missing_from_response" {
			t.Fatalf("missing=%+v", missing)
		}
	}
}

func TestSystemdPropertyTypeErrorsRemainOnOneUnit(t *testing.T) {
	connection := &fakeSystemdConnection{
		fixed: []systemddb.UnitStatus{{Name: "mdd-core.service", LoadState: "loaded", ActiveState: "active", SubState: "running"}},
		properties: map[string]map[string]any{"mdd-core.service": {
			"MainPID": uint64(123), "NRestarts": "zero", "Result": uint32(0),
			"ExecMainStartTimestamp": "today",
		}},
	}
	source := systemdUnitSource{connect: func(context.Context) (systemdConnection, error) { return connection, nil }}
	section := source.CollectUnits(t.Context())
	if section.State != SectionAvailable || section.Value == nil || len(section.Value.Fixed[0].Errors) != 4 ||
		section.Value.Fixed[0].MainPID != nil || section.Value.Fixed[0].NRestarts != nil {
		t.Fatalf("section=%+v", section)
	}
}

func TestSystemdBusFailureIsExplicit(t *testing.T) {
	source := systemdUnitSource{connect: func(context.Context) (systemdConnection, error) {
		return nil, errors.New("denied")
	}}
	section := source.CollectUnits(t.Context())
	if section.State != SectionError || section.Code != "systemd_bus_unavailable" || section.Value != nil {
		t.Fatalf("section=%+v", section)
	}
}
