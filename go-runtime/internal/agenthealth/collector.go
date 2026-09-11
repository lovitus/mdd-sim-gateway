// Package agenthealth projects bounded, non-invasive Agent host facts. It is
// adapted from the retired MDD managed_runtime health snapshot and shares the
// existing Agent topology channel instead of creating a second WebSocket.
package agenthealth

import (
	"context"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/host"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/buildidentity"
)

const storageQuantum = uint64(512 << 20)

type diskUsageFunc func(string) (uint64, uint64, error)

type Config struct {
	StoragePath     string
	HostMode        string
	ModemEnabled    bool
	TokenConfigured bool
	Identity        buildidentity.Identity
	Platform        string
	Architecture    string
	diskUsage       diskUsageFunc
}

type Collector struct {
	config                                     Config
	hostname, osName, osVersion, kernelVersion string
}

func New(config Config) (*Collector, error) {
	if config.Platform == "" {
		config.Platform = runtime.GOOS
		if config.Platform == "darwin" {
			config.Platform = "macos"
		}
	}
	if config.Architecture == "" {
		config.Architecture = runtime.GOARCH
	}
	config.StoragePath = filepath.Clean(strings.TrimSpace(config.StoragePath))
	if !filepath.IsAbs(config.StoragePath) || config.HostMode != "service" && config.HostMode != "gui" && config.HostMode != "cli" {
		return nil, errors.New("invalid Agent health collector configuration")
	}
	if config.diskUsage == nil {
		config.diskUsage = platformDiskUsage
	}
	collector := &Collector{config: config}
	collector.hostname, _ = os.Hostname()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	collector.osName, _, collector.osVersion, _ = host.PlatformInformationWithContext(ctx)
	collector.kernelVersion, _ = host.KernelVersionWithContext(ctx)
	if err := collector.Snapshot().Validate(); err != nil {
		return nil, err
	}
	return collector, nil
}

func (collector *Collector) Snapshot() agentlink.AgentHostFact {
	config := collector.config
	manager, scope := config.HostMode, "user"
	if config.HostMode == "service" {
		scope = "machine"
		if config.Platform == "windows" {
			manager = "scm"
		} else {
			manager = "systemd"
		}
	}
	fact := agentlink.AgentHostFact{
		Hostname: collector.hostname, OSName: collector.osName, OSVersion: collector.osVersion,
		KernelVersion: collector.kernelVersion, Addresses: hostAddresses(),
		SchemaVersion: 1, Platform: config.Platform, Architecture: config.Architecture,
		BuildVersion: config.Identity.DisplayVersion(), HostMode: config.HostMode,
		Manager: manager, SessionScope: scope, ConfigState: "ok",
		TokenConfigured: config.TokenConfigured, ModemEnabled: config.ModemEnabled,
		Storage: agentlink.AgentStorageFact{State: "unknown", ErrorCode: "storage_unavailable"},
	}
	total, free, err := config.diskUsage(config.StoragePath)
	if err != nil || total == 0 || free > total {
		return fact
	}
	usedPercent := uint32(math.Round(float64(total-free) / float64(total) * 100))
	state := "ok"
	if usedPercent >= 95 {
		state = "critical"
	} else if usedPercent >= 85 {
		state = "warning"
	}
	fact.Storage = agentlink.AgentStorageFact{
		State: state, TotalBytes: total, FreeBytes: free / storageQuantum * storageQuantum,
		UsedPercent: usedPercent,
	}
	return fact
}

func hostAddresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.IsGlobalUnicast() {
				seen[ip.String()] = true
			}
		}
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Strings(values)
	if len(values) > 32 {
		values = values[:32]
	}
	return values
}
