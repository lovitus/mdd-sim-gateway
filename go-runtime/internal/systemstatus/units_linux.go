//go:build linux

package systemstatus

import (
	"context"
	"math"
	"regexp"
	"sort"
	"time"

	systemddb "github.com/coreos/go-systemd/v22/dbus"
)

var providerUnitPattern = regexp.MustCompile(`^mdd-vowifi@line-[0-9a-f]{32}\.service$`)

var fixedUnits = []struct {
	name string
	role string
}{
	{"mdd-core.service", "core"},
	{"mdd-agent.service", "agent"},
	{"mdd-cellular-guard.service", "cellular_guard"},
	{"mdd-egress.service", "egress"},
	{"mdd-provider-apply.service", "provider_apply"},
}

type systemdConnection interface {
	ListUnitsByNamesContext(context.Context, []string) ([]systemddb.UnitStatus, error)
	ListUnitsByPatternsContext(context.Context, []string, []string) ([]systemddb.UnitStatus, error)
	GetUnitTypePropertiesContext(context.Context, string, string) (map[string]any, error)
	Close()
}

type systemdUnitSource struct {
	connect func(context.Context) (systemdConnection, error)
}

func newUnitSource() UnitSource {
	return systemdUnitSource{connect: func(ctx context.Context) (systemdConnection, error) {
		return systemddb.NewSystemConnectionContext(ctx)
	}}
}

func (source systemdUnitSource) CollectUnits(ctx context.Context) Section[SystemdInfo] {
	connection, err := source.connect(ctx)
	if err != nil {
		return errorSection[SystemdInfo]("systemd_bus_unavailable")
	}
	defer connection.Close()
	names := make([]string, len(fixedUnits))
	for index, unit := range fixedUnits {
		names[index] = unit.name
	}
	fixedStatus, err := connection.ListUnitsByNamesContext(ctx, names)
	if err != nil {
		return errorSection[SystemdInfo]("systemd_fixed_units_unavailable")
	}
	byName := make(map[string]systemddb.UnitStatus, len(fixedStatus))
	for _, status := range fixedStatus {
		byName[status.Name] = status
	}
	result := SystemdInfo{Fixed: []UnitStatus{}, Providers: []UnitStatus{}, ProvidersLoadedOnly: true, Errors: []string{}}
	for _, expected := range fixedUnits {
		status, found := byName[expected.name]
		if !found {
			result.Fixed = append(result.Fixed, UnitStatus{
				Name: expected.name, Role: expected.role, LoadState: "not-found",
				ActiveState: "inactive", SubState: "dead",
				Errors: []string{"systemd_fixed_unit_missing_from_response"},
			})
			result.Errors = append(result.Errors, "systemd_fixed_unit_missing_from_response")
			continue
		}
		current := systemdUnitStatus(status, expected.role)
		populateServiceProperties(ctx, connection, &current)
		if len(current.Errors) > 0 {
			result.Errors = append(result.Errors, current.Errors...)
		}
		result.Fixed = append(result.Fixed, current)
	}
	providers, err := connection.ListUnitsByPatternsContext(ctx, nil, []string{"mdd-vowifi@line-*.service"})
	if err != nil {
		result.Errors = append(result.Errors, "systemd_provider_units_unavailable")
	} else {
		for _, status := range providers {
			if !providerUnitPattern.MatchString(status.Name) {
				continue
			}
			current := systemdUnitStatus(status, "provider")
			populateServiceProperties(ctx, connection, &current)
			if len(current.Errors) > 0 {
				result.Errors = append(result.Errors, current.Errors...)
			}
			result.Providers = append(result.Providers, current)
		}
	}
	sort.Slice(result.Providers, func(left, right int) bool { return result.Providers[left].Name < result.Providers[right].Name })
	result.Errors = compactCodes(result.Errors)
	return availableSection(result)
}

func systemdUnitStatus(status systemddb.UnitStatus, role string) UnitStatus {
	return UnitStatus{
		Name: status.Name, Role: role, Description: status.Description,
		LoadState: status.LoadState, ActiveState: status.ActiveState, SubState: status.SubState,
		Errors: []string{},
	}
}

func populateServiceProperties(ctx context.Context, connection systemdConnection, target *UnitStatus) {
	if target.LoadState != "loaded" {
		return
	}
	properties, err := connection.GetUnitTypePropertiesContext(ctx, target.Name, "Service")
	if err != nil {
		target.Errors = append(target.Errors, "systemd_properties_unavailable")
		return
	}
	if value, ok := uint32Property(properties, "MainPID"); ok {
		target.MainPID = &value
	} else {
		target.Errors = append(target.Errors, "systemd_main_pid_type_invalid")
	}
	if value, ok := uint32Property(properties, "NRestarts"); ok {
		target.NRestarts = &value
	} else {
		target.Errors = append(target.Errors, "systemd_restarts_type_invalid")
	}
	if value, ok := stringProperty(properties, "Result"); ok {
		target.Result = value
	} else {
		target.Errors = append(target.Errors, "systemd_result_type_invalid")
	}
	if value, found := properties["ExecMainStartTimestamp"]; found {
		microseconds, ok := value.(uint64)
		if !ok || microseconds > math.MaxInt64 {
			target.Errors = append(target.Errors, "systemd_start_time_type_invalid")
		} else if microseconds != 0 {
			started := time.UnixMicro(int64(microseconds)).UTC()
			target.StartedAt = &started
		}
	} else {
		target.Errors = append(target.Errors, "systemd_start_time_unavailable")
	}
	target.Errors = compactCodes(target.Errors)
}

func uint32Property(properties map[string]any, name string) (uint32, bool) {
	value, found := properties[name]
	if !found {
		return 0, false
	}
	result, ok := value.(uint32)
	return result, ok
}

func stringProperty(properties map[string]any, name string) (string, bool) {
	value, found := properties[name]
	if !found {
		return "", false
	}
	result, ok := value.(string)
	return result, ok && safeText(result, 256)
}
