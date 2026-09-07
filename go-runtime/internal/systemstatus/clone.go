package systemstatus

func cloneSnapshot(source Snapshot) Snapshot {
	result := source
	if source.SampledAt != nil {
		value := *source.SampledAt
		result.SampledAt = &value
	}
	result.Provenance = cloneProvenance(source.Provenance)
	result.Host = cloneSimpleSection(source.Host)
	result.CPU = cloneSimpleSection(source.CPU)
	result.Load = cloneSimpleSection(source.Load)
	result.Memory = cloneSimpleSection(source.Memory)
	result.Swap = cloneSimpleSection(source.Swap)
	result.Power = cloneSimpleSection(source.Power)
	result.DefaultRoute = cloneSimpleSection(source.DefaultRoute)
	result.Disk = cloneSimpleSection(source.Disk)
	result.Network = cloneNetworkSection(source.Network)
	result.Temperatures = cloneTemperatureSection(source.Temperatures)
	result.Systemd = cloneSystemdSection(source.Systemd)
	result.Alerts = cloneSlice(source.Alerts)
	result.Errors = cloneSlice(source.Errors)
	return result
}

func cloneProvenance(source Provenance) Provenance {
	result := source
	if source.VCSTime != nil {
		value := *source.VCSTime
		result.VCSTime = &value
	}
	if source.VCSModified != nil {
		value := *source.VCSModified
		result.VCSModified = &value
	}
	return result
}

func cloneSimpleSection[T any](source Section[T]) Section[T] {
	result := source
	if source.Value != nil {
		value := *source.Value
		result.Value = &value
	}
	return result
}

func cloneNetworkSection(source Section[NetworkInfo]) Section[NetworkInfo] {
	result := source
	if source.Value == nil {
		return result
	}
	value := NetworkInfo{
		Interfaces: cloneSlice(source.Value.Interfaces),
		Errors:     cloneSlice(source.Value.Errors),
	}
	for index, current := range source.Value.Interfaces {
		value.Interfaces[index].Addresses = cloneSlice(current.Addresses)
	}
	result.Value = &value
	return result
}

func cloneTemperatureSection(source Section[TemperatureInfo]) Section[TemperatureInfo] {
	result := source
	if source.Value != nil {
		value := TemperatureInfo{
			Sensors:        cloneSlice(source.Value.Sensors),
			InvalidSensors: source.Value.InvalidSensors,
			Errors:         cloneSlice(source.Value.Errors),
		}
		result.Value = &value
	}
	return result
}

func cloneSystemdSection(source Section[SystemdInfo]) Section[SystemdInfo] {
	result := source
	if source.Value == nil {
		return result
	}
	value := SystemdInfo{
		Fixed:               cloneSlice(source.Value.Fixed),
		Providers:           cloneSlice(source.Value.Providers),
		ProvidersLoadedOnly: source.Value.ProvidersLoadedOnly,
		Errors:              cloneSlice(source.Value.Errors),
	}
	for index := range value.Fixed {
		value.Fixed[index] = cloneUnit(value.Fixed[index])
	}
	for index := range value.Providers {
		value.Providers[index] = cloneUnit(value.Providers[index])
	}
	result.Value = &value
	return result
}

func cloneUnit(source UnitStatus) UnitStatus {
	result := source
	if source.MainPID != nil {
		value := *source.MainPID
		result.MainPID = &value
	}
	if source.NRestarts != nil {
		value := *source.NRestarts
		result.NRestarts = &value
	}
	if source.StartedAt != nil {
		value := *source.StartedAt
		result.StartedAt = &value
	}
	result.Errors = cloneSlice(source.Errors)
	return result
}

func cloneSlice[T any](source []T) []T {
	if source == nil {
		return nil
	}
	result := make([]T, len(source))
	copy(result, source)
	return result
}
