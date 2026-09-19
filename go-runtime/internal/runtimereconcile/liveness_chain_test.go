package runtimereconcile

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const livenessTestToken = "liveness-simulator-loopback-token-32"

type livenessHTTPRuntime struct {
	client  *vowifiipc.Client
	fence   mediaauth.ProviderFence
	actions chan string
}

func (r *livenessHTTPRuntime) Observe(ctx context.Context, _ string) (vowifiipc.Snapshot, mediaauth.ProviderFence, error) {
	s, e := r.client.Status(ctx)
	return s, r.fence, e
}
func (r *livenessHTTPRuntime) Start(ctx context.Context, _ string, _ mediaauth.ProviderFence, q vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	s, e := r.client.Start(ctx, q)
	if e == nil {
		r.actions <- "start"
	}
	return s, e
}
func (r *livenessHTTPRuntime) Stop(ctx context.Context, _ string, _ mediaauth.ProviderFence, q vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error) {
	s, e := r.client.Stop(ctx, q)
	if e == nil {
		r.actions <- "stop"
	}
	return s, e
}

func TestLivenessRecoveryChainWithRealProviderProcess(t *testing.T) {
	binary := os.Getenv("MDD_LIVENESS_SIMULATOR")
	if binary == "" {
		t.Skip("set MDD_LIVENESS_SIMULATOR to the Provider service test binary; scripts/test-liveness-chain.sh runs both modules")
	}
	for _, fault := range []string{"drop", "ims", "counter_drop"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "-test.run=^TestLivenessSimulatorProcess$", "-test.timeout=25s")
			command.Env = append(os.Environ(), "MDD_LIVENESS_SIMULATOR_CHILD=1")
			if fault == "counter_drop" {
				command.Env = append(command.Env, "MDD_LIVENESS_INCIDENT_COUNTERS=1")
			}
			command.Stderr = os.Stderr
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = command.Start(); err != nil {
				t.Fatal(err)
			}
			scanner := bufio.NewScanner(output)
			url := ""
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "SIMULATOR_URL=") {
					url = strings.TrimPrefix(line, "SIMULATOR_URL=")
					break
				}
			}
			if url == "" {
				cancel()
				_ = command.Wait()
				t.Fatal("simulator did not publish loopback endpoint")
			}
			go func() {
				for scanner.Scan() {
				}
			}()
			send := func(fault string) map[string]int {
				t.Helper()
				request, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/simulate?fault="+fault, nil)
				request.Header.Set("Authorization", "Bearer "+livenessTestToken)
				res, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				var counters map[string]int
				if err = json.NewDecoder(res.Body).Decode(&counters); err != nil {
					t.Fatal(err)
				}
				return counters
			}
			defer func() {
				send("shutdown")
				if err := command.Wait(); err != nil {
					t.Errorf("simulator process: %v", err)
				}
			}()
			reconciler, catalog, fake, agents, _, clock := testReconciler(t, vowifiipc.RuntimeRunning, oneCard())
			client, err := vowifiipc.NewClient(url, livenessTestToken, &http.Client{Timeout: 2 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			real := &livenessHTTPRuntime{client: client, fence: fake.fence, actions: make(chan string, 8)}
			reconciler.runtime = real
			// Use the existing continuous-failure budget, but advance its injected clock.
			reconciler.continuousRetry = func() (recovery.ContinuousRetry, error) { return recovery.ContinuousRetry{Max: 3, Interval: 5}, nil }
			if _, err := client.Start(ctx, vowifiipc.LifecycleRequest{OperationID: "initial"}); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := catalog.SetRuntimeIntent("line-1", true); err != nil {
				t.Fatal(err)
			}
			injectedFault := fault
			if fault == "counter_drop" {
				injectedFault = "drop"
			}
			send(injectedFault)
			deadline := time.Now().Add(3 * time.Second)
			var degraded vowifiipc.Snapshot
			for time.Now().Before(deadline) {
				degraded, err = client.Status(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if degraded.RequiresIdleRecovery() {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !degraded.RequiresIdleRecovery() {
				t.Fatalf("fault never reached recovery: %+v", degraded)
			}
			health := degraded.Runtime.Health
			if health == nil {
				t.Fatal("health observation missing")
			}
			if injectedFault == "drop" && (!health.DPDDead || health.MissedDPDProbes != 3) {
				t.Fatalf("failure budget not exact: %+v", health)
			}
			if fault == "ims" && (health.DPDDead || health.IMSRegistered || health.IMSFailures == 0) {
				t.Fatalf("IMS failure conflated with tunnel death: %+v", health)
			}
			if fault == "ims" {
				if health.IMSExpiresAt == nil {
					t.Fatal("registration expiry missing")
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(max(time.Duration(0), time.Until(*health.IMSExpiresAt)) + 5*time.Millisecond):
				}
				afterExpiry, err := client.Status(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if h := afterExpiry.Runtime.Health; h == nil || h.IMSRegistered || h.LastDPDSuccessAt == nil || h.DPDDead {
					t.Fatalf("expired IMS with healthy DPD was misreported: %+v", h)
				}
			}
			if err = reconciler.reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case action := <-real.actions:
				t.Fatalf("recovery bypassed continuous budget: %s", action)
			default:
			}
			clock.Advance(16 * time.Second)
			agents.set(oneCard())
			for tries := 0; tries < 30; tries++ {
				if err = reconciler.reconcile(ctx); err != nil {
					t.Fatal(err)
				}
				if len(real.actions) > 0 {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			waitAction(t, real.actions, "stop")
			waitIdle(t, reconciler, "line-1")
			clock.Advance(10 * time.Second)
			agents.set(oneCard())
			if err = reconciler.reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			waitAction(t, real.actions, "start")
			waitIdle(t, reconciler, "line-1")
			current, err := client.Status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if current.Runtime.Condition != vowifiipc.RuntimeRunning || current.IMS.Condition != vowifiipc.LayerReady || current.Runtime.Health == nil || !current.Runtime.Health.IMSRegistered {
				t.Fatalf("line did not recover IMS: %+v", current)
			}
			if current.ProcessGeneration != degraded.ProcessGeneration {
				t.Fatal("recovery restarted Provider process")
			}
			if counts := send(""); counts["starts"] != 2 || counts["registers"] < 2 {
				t.Fatalf("unexpected lifecycle count: %v", counts)
			}
		})
	}
}
