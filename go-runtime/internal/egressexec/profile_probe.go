package egressexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressprobe"
)

type ProfileProbeResult struct {
	Node             string   `json:"node"`
	LatencyMS        int      `json:"latency_ms"`
	Target           string   `json:"target"`
	AttemptedTargets []string `json:"attempted_targets"`
}

// ProbeProfile starts one isolated loopback-only sing-box child for a saved
// node/SOCKS profile, performs the same end-to-end UDP DNS probe used by live
// exits, and removes the child and private config before returning. It never
// publishes desired state or signals the production egress process.
func ProbeProfile(ctx context.Context, binary, stateRoot string, profile egressconfig.Profile) (ProfileProbeResult, error) {
	var result ProfileProbeResult
	if ctx == nil || !filepath.IsAbs(binary) || !filepath.IsAbs(stateRoot) ||
		(profile.Type != "node" && profile.Type != "socks5") {
		return result, errors.New("egress profile is not independently testable")
	}
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return result, err
	}
	directory, err := os.MkdirTemp(stateRoot, "profile-probe-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(directory)
	if err := os.Chmod(directory, 0o700); err != nil {
		return result, err
	}
	port, err := availableLoopbackPort()
	if err != nil {
		return result, err
	}
	outbounds, node, err := renderProfile(profile, "profile_test", "profile-test")
	if err != nil {
		return result, err
	}
	config := baseConfig(
		[]map[string]any{{"type": "socks", "tag": "profile-test-in", "listen": "127.0.0.1", "listen_port": port, "udp_timeout": "30s"}},
		outbounds,
		[]map[string]any{{"inbound": []string{"profile-test-in"}, "action": "route", "outbound": "profile-test"}},
	)
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return result, err
	}
	configPath := filepath.Join(directory, "sing-box.json")
	if err := atomicWrite(configPath, append(payload, '\n'), 0o600); err != nil {
		return result, err
	}
	controller := systemProcessController{}
	if err := controller.Check(ctx, binary, configPath); err != nil {
		return result, fmt.Errorf("validate test profile: %w", err)
	}
	child, err := controller.Start(binary, configPath)
	if err != nil {
		return result, fmt.Errorf("start test profile: %w", err)
	}
	defer child.Stop(3 * time.Second)
	if err := controller.WaitReady(ctx, []int{port}, child, 5*time.Second); err != nil {
		return result, err
	}
	probe, err := egressprobe.Probe(ctx, fmt.Sprintf("socks5://127.0.0.1:%d", port))
	if err != nil {
		return result, err
	}
	return ProfileProbeResult{Node: node, LatencyMS: probe.LatencyMS, Target: probe.Target,
		AttemptedTargets: append([]string(nil), probe.AttemptedTargets...)}, nil
}

func availableLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return port, listener.Close()
}
