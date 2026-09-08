package egressexec

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressprobe"
)

func TestXHTTPProfileProbeOwnsBothChildrenAndCleansAfterFailure(t *testing.T) {
	for _, stage := range []string{"success", "xray-check", "xray-wait", "sing-start", "probe"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			sing, xray := &fakeController{}, &fakeController{}
			switch stage {
			case "xray-check":
				xray.checkError = errors.New("fixture")
			case "xray-wait":
				xray.waitErrors = []error{errors.New("fixture")}
			case "sing-start":
				sing.startError = errors.New("fixture")
			}
			calls := 0
			profile := egressconfig.Profile{Name: "XHTTP", Type: "node", Value: "vless://00000000-0000-4000-8000-000000000001@192.0.2.10:443?security=reality&type=xhttp&pbk=fixture"}
			result, err := probeProfile(context.Background(), "/opt/proxy/sing-box", "/opt/other/xray", root, profile, sing, xray,
				func(_ context.Context, endpoint string) (egressprobe.Result, error) {
					calls++
					if !strings.HasPrefix(endpoint, "socks5://127.0.0.1:") || len(sing.processes) != 1 || len(xray.processes) != 1 ||
						!sing.processes[0].running || !xray.processes[0].running {
						t.Fatal("probe ran outside the live pair")
					}
					if stage == "probe" {
						return egressprobe.Result{}, errors.New("actual request failed")
					}
					return egressprobe.Result{LatencyMS: 12, Target: "fixture"}, nil
				})
			if (err == nil) != (stage == "success") {
				t.Fatalf("unexpected result: %v", err)
			}
			if stage == "success" && (result.Node != "XHTTP" || result.LatencyMS != 12) {
				t.Fatal("probe result lost")
			}
			if stage == "success" || stage == "probe" {
				if calls != 1 {
					t.Fatal("probe must run exactly once")
				}
			} else if calls != 0 {
				t.Fatal("probe ran after prerequisite failed")
			}
			for _, controller := range []*fakeController{sing, xray} {
				for _, child := range controller.processes {
					if child.running || child.stops != 1 {
						t.Fatal("test child leaked")
					}
				}
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatal("private test configuration retained", readErr)
			}
		})
	}
}
