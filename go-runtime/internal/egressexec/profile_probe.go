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
	return ProbeProfileWithXray(ctx, binary, filepath.Join(filepath.Dir(binary), "xray"), stateRoot, profile)
}

func ProbeProfileWithXray(ctx context.Context, binary, xrayBinary, stateRoot string, profile egressconfig.Profile) (ProfileProbeResult, error) {
	return probeProfile(ctx, binary, xrayBinary, stateRoot, profile, systemProcessController{}, xrayProcessController{}, egressprobe.Probe)
}

func probeProfile(ctx context.Context, binary, xrayBinary, stateRoot string, profile egressconfig.Profile,
	controller, xray processController, probe func(context.Context, string) (egressprobe.Result, error)) (result ProfileProbeResult, resultErr error) {
	if ctx == nil || !filepath.IsAbs(binary) || !filepath.IsAbs(xrayBinary) || !filepath.IsAbs(stateRoot) ||
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
	runner := &executor{settings: Settings{StateDir: directory, SingBoxPath: binary, XrayPath: xrayBinary}, controller: controller, xrayController: xray}
	defer func() {
		if err := runner.stopPair(); err != nil {
			result = ProfileProbeResult{}
			resultErr = errors.Join(resultErr, errors.New("test proxy cleanup failed; private configuration retained"))
			return
		}
		if err := os.RemoveAll(directory); err != nil {
			resultErr = errors.Join(resultErr, errors.New("test proxy private configuration cleanup failed"))
		}
	}()
	if err := os.Chmod(directory, 0o700); err != nil {
		return result, err
	}
	port, err := availableLoopbackPort()
	if err != nil {
		return result, err
	}
	bridges := &xhttpBridges{allocatePort: availableLoopbackPort, reserved: map[int]bool{port: true}}
	outbounds, node, err := renderProfileWithBridges(profile, "profile_test", "profile-test", bridges)
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
	if err := controller.Check(ctx, binary, configPath); err != nil {
		return result, fmt.Errorf("validate test profile: %w", err)
	}
	xrayConfig, err := bridges.config()
	if err != nil {
		return result, err
	}
	if len(xrayConfig) != 0 {
		xrayPath := filepath.Join(directory, "xray.json")
		if err := atomicWrite(xrayPath, xrayConfig, 0o600); err != nil {
			return result, err
		}
		if err := xray.Check(ctx, xrayBinary, xrayPath); err != nil {
			return result, err
		}
		xrayChild, err := xray.Start(xrayBinary, xrayPath)
		if err != nil {
			return result, err
		}
		runner.xrayChild = xrayChild
		var ports []int
		for _, inbound := range bridges.inbounds {
			ports = append(ports, inbound["port"].(int))
		}
		if err := xray.WaitReady(ctx, ports, xrayChild, 5*time.Second); err != nil {
			return result, err
		}
	}
	child, err := controller.Start(binary, configPath)
	if err != nil {
		return result, fmt.Errorf("start test profile: %w", err)
	}
	runner.child = child
	if err := controller.WaitReady(ctx, []int{port}, child, 5*time.Second); err != nil {
		return result, err
	}
	observation, err := probe(ctx, fmt.Sprintf("socks5://127.0.0.1:%d", port))
	if err != nil {
		return result, err
	}
	return ProfileProbeResult{Node: node, LatencyMS: observation.LatencyMS, Target: observation.Target,
		AttemptedTargets: append([]string(nil), observation.AttemptedTargets...)}, nil
}

func availableLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return port, listener.Close()
}
