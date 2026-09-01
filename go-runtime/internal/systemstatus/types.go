// Package systemstatus provides a cached, read-only view of verified release
// provenance, host resources and MDD systemd units. It owns no recovery or
// lifecycle authority.
package systemstatus

import "time"

const SchemaVersion = 1

const (
	SectionAvailable   = "available"
	SectionUnavailable = "unavailable"
	SectionError       = "error"
)

type Section[T any] struct {
	State string `json:"state"`
	Code  string `json:"code,omitempty"`
	Value *T     `json:"value,omitempty"`
}

type Snapshot struct {
	SchemaVersion   int                      `json:"schema_version"`
	SampledAt       *time.Time               `json:"sampled_at,omitempty"`
	IntervalSeconds int                      `json:"interval_seconds"`
	State           string                   `json:"state"`
	Code            string                   `json:"code"`
	Stale           bool                     `json:"stale"`
	Provenance      Provenance               `json:"provenance"`
	Host            Section[HostInfo]        `json:"host"`
	CPU             Section[CPUInfo]         `json:"cpu"`
	Load            Section[LoadInfo]        `json:"load"`
	Memory          Section[MemoryInfo]      `json:"memory"`
	Swap            Section[SwapInfo]        `json:"swap"`
	Disk            Section[DiskInfo]        `json:"disk"`
	Network         Section[NetworkInfo]     `json:"network"`
	Temperatures    Section[TemperatureInfo] `json:"temperatures"`
	Systemd         Section[SystemdInfo]     `json:"systemd"`
	Alerts          []Alert                  `json:"alerts"`
	Errors          []string                 `json:"errors"`
}

type Provenance struct {
	Kind           string     `json:"kind"`
	State          string     `json:"state"`
	Verified       bool       `json:"verified"`
	Code           string     `json:"code"`
	ReleaseID      string     `json:"release_id,omitempty"`
	SourceRevision string     `json:"source_revision,omitempty"`
	CoreSHA256     string     `json:"core_sha256,omitempty"`
	ModuleVersion  string     `json:"module_version,omitempty"`
	VCSRevision    string     `json:"vcs_revision,omitempty"`
	VCSTime        *time.Time `json:"vcs_time,omitempty"`
	VCSModified    *bool      `json:"vcs_modified,omitempty"`
}

type HostInfo struct {
	Platform        string `json:"platform,omitempty"`
	PlatformFamily  string `json:"platform_family,omitempty"`
	PlatformVersion string `json:"platform_version,omitempty"`
	KernelVersion   string `json:"kernel_version,omitempty"`
	KernelArch      string `json:"kernel_arch,omitempty"`
	UptimeSeconds   uint64 `json:"uptime_seconds"`
}

type CPUInfo struct {
	LogicalCores int     `json:"logical_cores"`
	Model        string  `json:"model,omitempty"`
	MHz          float64 `json:"mhz,omitempty"`
}

type LoadInfo struct {
	OneMinute        float64 `json:"one_minute"`
	FiveMinutes      float64 `json:"five_minutes"`
	FifteenMinutes   float64 `json:"fifteen_minutes"`
	OneMinutePerCore float64 `json:"one_minute_per_core"`
}

type MemoryInfo struct {
	TotalBytes     uint64  `json:"total_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	UsedPercent    float64 `json:"used_percent"`
}

type SwapInfo struct {
	TotalBytes  uint64  `json:"total_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`
	UsedPercent float64 `json:"used_percent"`
	Sin         uint64  `json:"bytes_in"`
	Sout        uint64  `json:"bytes_out"`
}

type DiskInfo struct {
	TotalBytes  uint64  `json:"total_bytes"`
	FreeBytes   uint64  `json:"free_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

type NetworkInfo struct {
	Interfaces []NetworkInterface `json:"interfaces"`
	Errors     []string           `json:"errors,omitempty"`
}

type NetworkInterface struct {
	Name        string   `json:"name"`
	MTU         int      `json:"mtu"`
	Addresses   []string `json:"addresses"`
	IOAvailable bool     `json:"io_available"`
	IOCode      string   `json:"io_code,omitempty"`
	BytesSent   uint64   `json:"bytes_sent"`
	BytesRecv   uint64   `json:"bytes_recv"`
	PacketsSent uint64   `json:"packets_sent"`
	PacketsRecv uint64   `json:"packets_recv"`
	ErrorsIn    uint64   `json:"errors_in"`
	ErrorsOut   uint64   `json:"errors_out"`
	DropsIn     uint64   `json:"drops_in"`
	DropsOut    uint64   `json:"drops_out"`
}

type TemperatureInfo struct {
	Sensors        []Temperature `json:"sensors"`
	InvalidSensors int           `json:"invalid_sensors"`
	Errors         []string      `json:"errors,omitempty"`
}

type Temperature struct {
	Sensor  string  `json:"sensor"`
	Celsius float64 `json:"celsius"`
}

type SystemdInfo struct {
	Fixed               []UnitStatus `json:"fixed"`
	Providers           []UnitStatus `json:"providers"`
	ProvidersLoadedOnly bool         `json:"providers_loaded_only"`
	Errors              []string     `json:"errors,omitempty"`
}

type UnitStatus struct {
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	Description string     `json:"description,omitempty"`
	LoadState   string     `json:"load_state"`
	ActiveState string     `json:"active_state"`
	SubState    string     `json:"sub_state"`
	MainPID     *uint32    `json:"main_pid,omitempty"`
	NRestarts   *uint32    `json:"nrestarts,omitempty"`
	Result      string     `json:"result,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	Errors      []string   `json:"errors,omitempty"`
}

type Alert struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Scope    string `json:"scope"`
}

func unavailableSnapshot(interval time.Duration) Snapshot {
	return Snapshot{
		SchemaVersion: SchemaVersion, IntervalSeconds: int(interval.Seconds()),
		State: "unavailable", Code: "status_unavailable", Stale: true,
		Provenance:   Provenance{Kind: "unknown", State: "pending", Code: "provenance_pending"},
		Host:         unavailableSection[HostInfo]("host_pending"),
		CPU:          unavailableSection[CPUInfo]("cpu_pending"),
		Load:         unavailableSection[LoadInfo]("load_pending"),
		Memory:       unavailableSection[MemoryInfo]("memory_pending"),
		Swap:         unavailableSection[SwapInfo]("swap_pending"),
		Disk:         unavailableSection[DiskInfo]("disk_pending"),
		Network:      unavailableSection[NetworkInfo]("network_pending"),
		Temperatures: unavailableSection[TemperatureInfo]("temperature_pending"),
		Systemd:      unavailableSection[SystemdInfo]("systemd_pending"),
		Alerts:       []Alert{}, Errors: []string{"status_unavailable"},
	}
}

func unavailableSection[T any](code string) Section[T] {
	return Section[T]{State: SectionUnavailable, Code: code}
}

func errorSection[T any](code string) Section[T] {
	return Section[T]{State: SectionError, Code: code}
}

func availableSection[T any](value T) Section[T] {
	return Section[T]{State: SectionAvailable, Value: &value}
}
