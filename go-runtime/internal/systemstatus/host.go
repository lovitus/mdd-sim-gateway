package systemstatus

import (
	"context"
	"math"
	"net/netip"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/sensors"
)

type HostMeasurements struct {
	Power        Section[PowerInfo]
	DefaultRoute Section[RouteInfo]
	Host         Section[HostInfo]
	CPU          Section[CPUInfo]
	Load         Section[LoadInfo]
	Memory       Section[MemoryInfo]
	Swap         Section[SwapInfo]
	Disk         Section[DiskInfo]
	Network      Section[NetworkInfo]
	Temperatures Section[TemperatureInfo]
}

type HostSource interface {
	CollectHost(context.Context, string) HostMeasurements
}

type gopsutilSource struct{}

func newHostSource() HostSource { return gopsutilSource{} }

func (gopsutilSource) CollectHost(ctx context.Context, dataPath string) HostMeasurements {
	return HostMeasurements{
		Power: collectPower(ctx), DefaultRoute: collectDefaultRoute(ctx),
		Host: collectHost(ctx), CPU: collectCPU(ctx), Load: collectLoad(ctx),
		Memory: collectMemory(ctx), Swap: collectSwap(ctx), Disk: collectDisk(ctx, dataPath),
		Network: collectNetwork(ctx), Temperatures: collectTemperatures(ctx),
	}
}

func collectHost(ctx context.Context) Section[HostInfo] {
	value, err := host.InfoWithContext(ctx)
	if err != nil || value == nil {
		return errorSection[HostInfo]("host_read_failed")
	}
	return availableSection(HostInfo{
		Platform: value.Platform, PlatformFamily: value.PlatformFamily,
		PlatformVersion: value.PlatformVersion, KernelVersion: value.KernelVersion,
		KernelArch: value.KernelArch, UptimeSeconds: value.Uptime,
	})
}

func collectCPU(ctx context.Context) Section[CPUInfo] {
	values, err := cpu.InfoWithContext(ctx)
	if err != nil {
		return errorSection[CPUInfo]("cpu_read_failed")
	}
	if len(values) == 0 {
		return unavailableSection[CPUInfo]("cpu_unavailable")
	}
	result := CPUInfo{LogicalCores: runtime.NumCPU()}
	for _, value := range values {
		if result.Model == "" && safeText(value.ModelName, 256) {
			result.Model = strings.TrimSpace(value.ModelName)
		}
		if finite(value.Mhz) && value.Mhz > result.MHz && value.Mhz < 100_000 {
			result.MHz = round(value.Mhz, 1)
		}
	}
	if result.LogicalCores < 1 {
		return errorSection[CPUInfo]("cpu_core_count_invalid")
	}
	return availableSection(result)
}

func collectLoad(ctx context.Context) Section[LoadInfo] {
	value, err := load.AvgWithContext(ctx)
	cores := runtime.NumCPU()
	if err != nil || value == nil {
		return errorSection[LoadInfo]("load_read_failed")
	}
	if cores < 1 || !finite(value.Load1) || !finite(value.Load5) || !finite(value.Load15) ||
		value.Load1 < 0 || value.Load5 < 0 || value.Load15 < 0 {
		return errorSection[LoadInfo]("load_value_invalid")
	}
	return availableSection(LoadInfo{
		OneMinute: round(value.Load1, 2), FiveMinutes: round(value.Load5, 2),
		FifteenMinutes: round(value.Load15, 2), OneMinutePerCore: round(value.Load1/float64(cores), 2),
	})
}

func collectMemory(ctx context.Context) Section[MemoryInfo] {
	value, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil || value == nil {
		return errorSection[MemoryInfo]("memory_read_failed")
	}
	if value.Total == 0 || value.Available > value.Total || value.Used > value.Total || !validPercent(value.UsedPercent) {
		return errorSection[MemoryInfo]("memory_value_invalid")
	}
	return availableSection(MemoryInfo{
		TotalBytes: value.Total, AvailableBytes: value.Available, UsedBytes: value.Used,
		UsedPercent: round(value.UsedPercent, 1),
	})
}

func collectSwap(ctx context.Context) Section[SwapInfo] {
	value, err := mem.SwapMemoryWithContext(ctx)
	if err != nil || value == nil {
		return errorSection[SwapInfo]("swap_read_failed")
	}
	if value.Used > value.Total || !validPercent(value.UsedPercent) {
		return errorSection[SwapInfo]("swap_value_invalid")
	}
	return availableSection(SwapInfo{
		TotalBytes: value.Total, UsedBytes: value.Used, UsedPercent: round(value.UsedPercent, 1),
		Sin: value.Sin, Sout: value.Sout,
	})
}

func collectDisk(ctx context.Context, dataPath string) Section[DiskInfo] {
	value, err := disk.UsageWithContext(ctx, dataPath)
	if err != nil || value == nil {
		return errorSection[DiskInfo]("disk_read_failed")
	}
	if value.Total == 0 || value.Free > value.Total || value.Used > value.Total || !validPercent(value.UsedPercent) {
		return errorSection[DiskInfo]("disk_value_invalid")
	}
	return availableSection(DiskInfo{
		TotalBytes: value.Total, FreeBytes: value.Free, UsedBytes: value.Used,
		UsedPercent: round(value.UsedPercent, 1),
	})
}

func collectNetwork(ctx context.Context) Section[NetworkInfo] {
	interfaces, err := gnet.InterfacesWithContext(ctx)
	if err != nil {
		return errorSection[NetworkInfo]("network_interfaces_read_failed")
	}
	counters, err := gnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return errorSection[NetworkInfo]("network_counters_read_failed")
	}
	byName := make(map[string]gnet.IOCountersStat, len(counters))
	for _, counter := range counters {
		byName[counter.Name] = counter
	}
	result := NetworkInfo{Interfaces: []NetworkInterface{}}
	for _, current := range interfaces {
		if !hasFlag(current.Flags, "up") || hasFlag(current.Flags, "loopback") ||
			!safeText(current.Name, 128) || current.MTU < 0 {
			continue
		}
		addresses := make([]string, 0, len(current.Addrs))
		for _, input := range current.Addrs {
			if address, ok := publicHostAddress(input.Addr); ok {
				addresses = append(addresses, address)
			}
		}
		sort.Strings(addresses)
		view := NetworkInterface{Name: current.Name, MTU: current.MTU, Addresses: addresses}
		if counter, found := byName[current.Name]; found {
			view.IOAvailable = true
			view.BytesSent, view.BytesRecv = counter.BytesSent, counter.BytesRecv
			view.PacketsSent, view.PacketsRecv = counter.PacketsSent, counter.PacketsRecv
			view.ErrorsIn, view.ErrorsOut = counter.Errin, counter.Errout
			view.DropsIn, view.DropsOut = counter.Dropin, counter.Dropout
		} else {
			view.IOCode = "network_counter_unavailable"
			result.Errors = append(result.Errors, "network_counter_unavailable")
		}
		result.Interfaces = append(result.Interfaces, view)
	}
	sort.Slice(result.Interfaces, func(left, right int) bool { return result.Interfaces[left].Name < result.Interfaces[right].Name })
	if len(result.Interfaces) == 0 {
		return unavailableSection[NetworkInfo]("network_interfaces_unavailable")
	}
	return availableSection(result)
}

func collectTemperatures(ctx context.Context) Section[TemperatureInfo] {
	values, err := sensors.TemperaturesWithContext(ctx)
	if err != nil {
		return errorSection[TemperatureInfo]("temperature_read_failed")
	}
	if len(values) == 0 {
		return unavailableSection[TemperatureInfo]("temperature_unavailable")
	}
	result := TemperatureInfo{Sensors: []Temperature{}, Errors: []string{}}
	for index, value := range values {
		if !validTemperature(value.Temperature) {
			result.InvalidSensors++
			continue
		}
		name := strings.TrimSpace(value.SensorKey)
		if !safeText(name, 128) {
			name = "sensor-" + strconv.Itoa(index+1)
		}
		result.Sensors = append(result.Sensors, Temperature{Sensor: name, Celsius: round(value.Temperature, 1)})
	}
	if len(result.Sensors) == 0 {
		return errorSection[TemperatureInfo]("temperature_values_invalid")
	}
	if result.InvalidSensors > 0 {
		result.Errors = []string{"temperature_value_invalid"}
	}
	sort.Slice(result.Sensors, func(left, right int) bool {
		if result.Sensors[left].Sensor != result.Sensors[right].Sensor {
			return result.Sensors[left].Sensor < result.Sensors[right].Sensor
		}
		return result.Sensors[left].Celsius < result.Sensors[right].Celsius
	})
	return availableSection(result)
}

func publicHostAddress(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if prefix, err := netip.ParsePrefix(input); err == nil {
		address := prefix.Addr()
		if address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified() {
			return "", false
		}
		return prefix.String(), true
	}
	address, err := netip.ParseAddr(input)
	if err != nil || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified() {
		return "", false
	}
	return address.String(), true
}

func hasFlag(flags []string, expected string) bool {
	for _, flag := range flags {
		if strings.EqualFold(strings.TrimSpace(flag), expected) {
			return true
		}
	}
	return false
}

func validPercent(value float64) bool { return finite(value) && value >= 0 && value <= 100 }

func validTemperature(value float64) bool { return finite(value) && value > 0 && value < 150 }

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func round(value float64, places int) float64 {
	factor := math.Pow10(places)
	return math.Round(value*factor) / factor
}

func safeText(value string, maximum int) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
