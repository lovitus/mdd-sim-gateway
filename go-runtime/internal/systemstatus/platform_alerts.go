package systemstatus

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type PowerInfo struct {
	Flags uint64 `json:"flags"`
}
type RouteInfo struct {
	Primary string `json:"primary"`
}

// Directly carries over MDD sysinfo.throttling's fixed firmware operation.
// Non-Pi hosts are unsupported, not a fabricated all-clear reading.
func collectPower(ctx context.Context) Section[PowerInfo] {
	if runtime.GOOS != "linux" {
		return unavailableSection[PowerInfo]("power_platform_unsupported")
	}
	model, err := os.ReadFile("/proc/device-tree/model")
	if err != nil || !strings.Contains(string(model), "Raspberry Pi") {
		return unavailableSection[PowerInfo]("power_platform_unsupported")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	wire, err := exec.CommandContext(ctx, "vcgencmd", "get_throttled").Output()
	if err != nil {
		return errorSection[PowerInfo]("power_read_failed")
	}
	text := strings.TrimSpace(string(wire))
	if !strings.HasPrefix(text, "throttled=0x") {
		return errorSection[PowerInfo]("power_response_invalid")
	}
	flags, err := strconv.ParseUint(strings.TrimPrefix(text, "throttled=0x"), 16, 32)
	if err != nil {
		return errorSection[PowerInfo]("power_response_invalid")
	}
	return availableSection(PowerInfo{Flags: flags})
}

// Same fixed iproute2 read as MDD default_route_interfaces, with structured JSON.
func collectDefaultRoute(ctx context.Context) Section[RouteInfo] {
	if runtime.GOOS != "linux" {
		return unavailableSection[RouteInfo]("route_platform_unsupported")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	wire, err := exec.CommandContext(ctx, "ip", "-j", "-4", "route", "show", "default").Output()
	if err != nil {
		return errorSection[RouteInfo]("default_route_read_failed")
	}
	var routes []struct {
		Dev string `json:"dev"`
	}
	if len(wire) > 65536 || json.Unmarshal(wire, &routes) != nil {
		return errorSection[RouteInfo]("default_route_response_invalid")
	}
	if len(routes) == 0 || strings.TrimSpace(routes[0].Dev) == "" {
		return unavailableSection[RouteInfo]("default_route_unavailable")
	}
	return availableSection(RouteInfo{Primary: routes[0].Dev})
}

func platformAlerts(current Snapshot) []Alert {
	alerts := []Alert{}
	if current.Power.State == SectionAvailable && current.Power.Value != nil {
		flags := current.Power.Value.Flags
		if flags&1 != 0 {
			alerts = append(alerts, Alert{Severity: "critical", Code: "undervoltage_now", Scope: "host.power"})
		} else if flags&0x10000 != 0 {
			alerts = append(alerts, Alert{Severity: "warning", Code: "undervoltage_seen", Scope: "host.power"})
		}
		if flags&0xe != 0 {
			alerts = append(alerts, Alert{Severity: "warning", Code: "throttled_now", Scope: "host.power"})
		}
	}
	return alerts
}
