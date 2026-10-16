package egressexec

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
)

type Settings struct {
	DesiredPath string
	StatusPath  string
	StateDir    string
	SingBoxPath string
	XrayPath    string
	PortBase    int
	Poll        time.Duration
	CoreURL     string
	TokenPath   string
}

type managedProcess interface {
	Running() bool
	Signal(os.Signal) error
	Stop(time.Duration) error
}

type processController interface {
	Check(context.Context, string, string) error
	Start(string, string) (managedProcess, error)
	WaitReady(context.Context, []int, managedProcess, time.Duration) error
}

type systemProcessController struct{}

func (systemProcessController) Check(ctx context.Context, binary, config string) error {
	return checkConfig(ctx, binary, config)
}

func (systemProcessController) Start(binary, config string) (managedProcess, error) {
	return startProcess(binary, config)
}

func (systemProcessController) WaitReady(ctx context.Context, ports []int, child managedProcess, timeout time.Duration) error {
	return waitPorts(ctx, ports, child, timeout)
}

type executor struct {
	identity            string
	requestedAttempt    string
	settings            Settings
	controller          processController
	xrayController      processController
	now                 func() time.Time
	child               managedProcess
	consumerUnconfirmed bool
	xrayChild           managedProcess
	applied             string
	appliedConfig       []byte
	appliedResult       Rendered
	requested           string
	blocked             string
	failures            int
	nextAttempt         time.Time
	cellular            *cellularClient
	feedCache           *subscriptionCache
}

func Run(ctx context.Context, settings Settings) error {
	if ctx == nil {
		return errors.New("country exit context is required")
	}
	if err := settings.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(settings.StateDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(settings.StateDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(settings.StatusPath), 0o750); err != nil {
		return err
	}
	cellular, err := newCellularClient(settings.CoreURL, settings.TokenPath)
	if err != nil {
		return err
	}
	runner := &executor{identity: rand.Text(), settings: settings, controller: systemProcessController{}, xrayController: xrayProcessController{}, now: time.Now, cellular: cellular}
	leaseContext, stopRenewal := context.WithCancel(ctx)
	renewalDone := make(chan struct{})
	go func() { cellular.keepAlive(leaseContext); close(renewalDone) }()
	ticker := time.NewTicker(settings.Poll)
	defer ticker.Stop()
	defer func() {
		stopRenewal()
		<-renewalDone
		runner.publishFailure("country exit executor stopped", runner.requested, false)
		if runner.child != nil {
			_ = runner.child.Stop(5 * time.Second)
		}
		if runner.xrayChild != nil {
			_ = runner.xrayChild.Stop(5 * time.Second)
		}
		_ = os.Remove(filepath.Join(settings.StateDir, "xray.json"))
		_ = os.Remove(filepath.Join(settings.StateDir, ".xray-candidate.json"))
		runner.cellular.close()
		_ = os.Remove(filepath.Join(settings.StateDir, "sing-box.json"))
		_ = os.Remove(filepath.Join(settings.StateDir, ".sing-box-candidate.json"))
	}()
	runner.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			runner.reconcile(ctx)
		}
	}
}

func (runner *executor) reconcile(ctx context.Context) {
	document, err := egressdesired.Read(runner.settings.DesiredPath)
	if err != nil {
		runner.publishFailure("desired state unavailable", runner.requested, runner.runtimeReady())
		return
	}
	if document.Generation != runner.requested || document.AttemptID != runner.requestedAttempt {
		runner.requested, runner.blocked = document.Generation, ""
		runner.requestedAttempt = document.AttemptID
		runner.failures, runner.nextAttempt = 0, time.Time{}
	}
	if runner.consumerUnconfirmed {
		if runner.now().Before(runner.nextAttempt) {
			return
		}
		if runner.child != nil {
			if err := runner.child.Stop(5 * time.Second); err != nil && runner.child.Running() {
				runner.fail(document.Generation, "unconfirmed consumer cleanup pending", false)
				return
			}
		}
		runner.child, runner.consumerUnconfirmed = nil, false
	}
	runtimeProxy := document.Proxy
	var cellularErr error
	if runner.cellular != nil {
		runtimeProxy, cellularErr = runner.cellular.prepare(ctx, document.Proxy)
	}
	if cellularErr != nil {
		if stopErr := runner.stopPair(); stopErr == nil {
			_ = os.Remove(filepath.Join(runner.settings.StateDir, "sing-box.json"))
		}
		runner.fail(document.Generation, "prepare cellular exit: "+cellularErr.Error(), false)
		return
	}
	runtimeDocument := document
	runtimeDocument.Proxy = runtimeProxy
	feeds, feedErr := runner.subscriptions(ctx, document.Proxy)
	if feedErr != nil {
		runner.fail(document.Generation, feedErr.Error(), runner.runtimeReady())
		return
	}
	running := map[string]string{}
	for country, exit := range runner.appliedResult.Status.Exits {
		running[country] = exit.Node
	}
	result, renderErr := renderAtBase(runtimeDocument, runner.settings.PortBase, feeds, running)
	policy, _ := json.Marshal(document.Proxy)
	result.PolicyIdentity = fmt.Sprintf("%x", sha256.Sum256(policy))
	if renderErr != nil {
		runner.failWithCandidate(document.Generation, "render country exits: "+renderErr.Error(),
			runner.runtimeReady(), result.Status)
		return
	}
	if document.Generation == runner.blocked {
		if previous, err := os.ReadFile(runner.settings.StatusPath); err == nil {
			var status Status
			if json.Unmarshal(previous, &status) == nil && status.ExecutorID == runner.identity {
				runner.invalidateDependencies(&status, result)
				_ = writeStatus(runner.settings.StatusPath, status)
			}
		}
		return
	}
	if runner.applied != "" && !runner.runtimeReady() {
		runner.publishFailure("country exit process is not running", document.Generation, false)
	}
	if runner.runtimeReady() && bytes.Equal(result.Config, runner.appliedConfig) && bytes.Equal(result.XrayConfig, runner.appliedResult.XrayConfig) {
		runner.commit(document.Generation, result)
		return
	}
	if (runner.child == nil || !runner.child.Running()) && runner.xrayChild != nil && runner.xrayChild.Running() &&
		bytes.Equal(result.Config, runner.appliedConfig) && bytes.Equal(result.XrayConfig, runner.appliedResult.XrayConfig) {
		if !runner.now().Before(runner.nextAttempt) {
			runner.restoreSingBoxConsumer(ctx, document.Generation, result)
		}
		return
	}
	if runner.child != nil && runner.child.Running() && len(result.XrayConfig) > 0 &&
		(runner.xrayChild == nil || !runner.xrayChild.Running()) &&
		bytes.Equal(result.Config, runner.appliedConfig) && bytes.Equal(result.XrayConfig, runner.appliedResult.XrayConfig) {
		runner.restoreXrayBridge(ctx, document.Generation, result)
		return
	}
	// A changed feed OR temporary donor route needs the existing coordinated
	// apply. A lease renewal alone never authorizes interrupting healthy calls.
	if document.Generation == runner.applied && document.AttemptID == runner.appliedResult.Status.AttemptID &&
		(runner.child != nil && runner.child.Running() || runner.xrayChild != nil && runner.xrayChild.Running()) {
		status := cloneStatus(runner.appliedResult.Status)
		status.PendingApply, status.RuntimeConfirmed, status.Ready = true, false, false
		status.Error = "runtime route changed; coordinated apply required"
		runner.invalidateDependencies(&status, result)
		for country, exit := range status.Exits {
			if result.CountryIdentity[country] != runner.appliedResult.CountryIdentity[country] {
				exit.Ready = false
				exit.Error = "runtime route changed; coordinated apply required"
				exit.HostProxyHost, exit.ProxyPort = "", 0
				status.Exits[country] = exit
			}
		}
		_ = writeStatus(runner.settings.StatusPath, status)
		return
	}
	if runner.now().Before(runner.nextAttempt) {
		return
	}
	if !document.Proxy.Enabled || len(result.Ports) == 0 {
		if err = runner.stopPair(); err != nil {
			runner.fail(document.Generation, "stop exit processes: "+err.Error(), runner.runtimeReady())
			return
		}
		_ = os.Remove(filepath.Join(runner.settings.StateDir, "xray.json"))
		_ = os.Remove(filepath.Join(runner.settings.StateDir, "sing-box.json"))
		runner.commit(document.Generation, result)
		return
	}
	if len(result.XrayConfig) != 0 || len(runner.appliedResult.XrayConfig) != 0 {
		runner.activateXrayPair(ctx, document.Generation, result)
		return
	}

	currentPath := filepath.Join(runner.settings.StateDir, "sing-box.json")
	candidate := filepath.Join(runner.settings.StateDir, ".sing-box-candidate.json")
	if err = atomicWrite(candidate, result.Config, 0o600); err == nil {
		err = runner.controller.Check(ctx, runner.settings.SingBoxPath, candidate)
	}
	_ = os.Remove(candidate)
	if err != nil {
		runner.failWithCandidate(document.Generation, "sing-box configuration rejected: "+err.Error(),
			runner.runtimeReady(), result.Status)
		runner.confirmRejected(document.Generation, result)
		return
	}
	if err = atomicWrite(currentPath, result.Config, 0o600); err != nil {
		runner.failWithCandidate(document.Generation, "publish sing-box configuration: "+err.Error(),
			runner.runtimeReady(), result.Status)
		return
	}

	previousWasRunning := runner.runtimeReady()
	previousConfig := append([]byte(nil), runner.appliedConfig...)
	previousResult := runner.appliedResult
	previousGeneration := runner.applied
	if previousWasRunning {
		// SIGHUP plus old listening ports cannot prove the candidate loaded.
		// The apply owner already drained Providers; replace the exact child.
		err = runner.child.Stop(5 * time.Second)
		if err != nil && runner.child.Running() {
			runner.fail(document.Generation, "previous exit process could not stop", true)
			return
		}
		runner.child = nil
	}
	if err == nil {
		runner.child, err = runner.controller.Start(runner.settings.SingBoxPath, currentPath)
	}
	if err == nil {
		err = runner.controller.WaitReady(ctx, result.Ports, runner.child, 8*time.Second)
	}
	if err == nil {
		runner.appliedConfig = append(runner.appliedConfig[:0], result.Config...)
		runner.commit(document.Generation, result)
		return
	}

	activationErr := err
	if stopErr := runner.stopPair(); stopErr != nil {
		runner.failWithCandidate(document.Generation, "failed exit process could not stop", false, result.Status)
		return
	}
	rolledBack := false
	if previousWasRunning && previousGeneration != "" && len(previousConfig) != 0 && runner.rollbackAllowed(result) {
		if restoreErr := atomicWrite(currentPath, previousConfig, 0o600); restoreErr == nil {
			runner.child, restoreErr = runner.controller.Start(runner.settings.SingBoxPath, currentPath)
			if restoreErr == nil {
				restoreErr = runner.controller.WaitReady(ctx, previousResult.Ports, runner.child, 8*time.Second)
			}
			if restoreErr == nil {
				rolledBack = true
			} else if stopErr := runner.stopPair(); stopErr != nil {
				runner.failWithCandidate(document.Generation, "failed rollback process could not stop", false, result.Status)
				return
			}
		}
	}
	if rolledBack {
		runner.applied, runner.appliedConfig, runner.appliedResult = previousGeneration, previousConfig, previousResult
		runner.blocked = document.Generation
		runner.fail(document.Generation, "activate sing-box configuration: "+activationErr.Error()+"; previous generation restored", true)
		runner.confirmRollback(document.Generation)
		return
	}
	_ = os.Remove(currentPath)
	runner.confirmStopped(document.Generation, result, "activate sing-box configuration failed")
}

func (runner *executor) runtimeReady() bool {
	return !runner.consumerUnconfirmed && runner.child != nil && runner.child.Running() &&
		(len(runner.appliedResult.XrayConfig) == 0 || (runner.xrayChild != nil && runner.xrayChild.Running()))
}

func (runner *executor) commit(generation string, result Rendered) {
	result.Status.DesiredGeneration = generation
	result.Status.RequestedGeneration = generation
	result.Status.RuntimeConfirmed = true
	result.Status.RequestedAttempt = runner.requestedAttempt
	result.Status.Outcome = "applied"
	if !result.Status.Ready {
		result.Status.Outcome = "partial"
	}
	if len(result.Ports) == 0 && !result.Status.Ready {
		result.Status.Outcome = "unavailable"
	}
	if runner.identity == "" {
		runner.identity = rand.Text()
	}
	result.Status.ExecutorID = runner.identity
	result.Status.ConfigIdentity = fmt.Sprintf("%x", sha256.Sum256(append(append([]byte(nil), result.Config...), result.XrayConfig...)))
	runner.appliedConfig = append([]byte(nil), result.Config...)
	runner.applied, runner.appliedResult = generation, result
	runner.failures, runner.nextAttempt = 0, time.Time{}
	_ = writeStatus(runner.settings.StatusPath, result.Status)
}

func (runner *executor) fail(requested, detail string, runtimeStillReady bool) {
	runner.publishFailure(detail, requested, runtimeStillReady)
	runner.failures++
	runner.nextAttempt = runner.now().Add(retryDelay(runner.failures))
}

func (runner *executor) failWithCandidate(requested, detail string, runtimeStillReady bool, candidate Status) {
	if runtimeStillReady {
		runner.fail(requested, detail, true)
		return
	}
	status := unavailableStatus(candidate)
	status.SchemaVersion, status.RequestedGeneration, status.Error = 1, requested, detail
	_ = writeStatus(runner.settings.StatusPath, status)
	runner.failures++
	runner.nextAttempt = runner.now().Add(retryDelay(runner.failures))
}

func (runner *executor) publishFailure(detail, requested string, runtimeStillReady bool) {
	var status Status
	if runtimeStillReady && runner.applied != "" {
		status = cloneStatus(runner.appliedResult.Status)
	} else {
		status = unavailableStatus(runner.appliedResult.Status)
	}
	status.SchemaVersion = 1
	status.RequestedGeneration = requested
	status.RequestedAttempt = runner.requestedAttempt
	status.RuntimeConfirmed = false
	status.Error = detail
	_ = writeStatus(runner.settings.StatusPath, status)
}

func (runner *executor) rollbackAllowed(candidate Rendered) bool {
	previous := runner.appliedResult
	if previous.PolicyIdentity == "" || previous.PolicyIdentity != candidate.PolicyIdentity {
		return false
	}
	for country, exit := range previous.Status.Exits {
		if exit.Ready && exit.Mode == "cellular_sim" && previous.CountryIdentity[country] != candidate.CountryIdentity[country] {
			return false
		}
	}
	return true
}

func (runner *executor) confirmRollback(requested string) {
	status := cloneStatus(runner.appliedResult.Status)
	status.Outcome, status.RuntimeConfirmed = "rolled_back", true
	status.RequestedGeneration, status.RequestedAttempt = requested, runner.requestedAttempt
	status.Error = "new configuration failed; previous authorized configuration restored"
	_ = writeStatus(runner.settings.StatusPath, status)
}

// Retain healthy countries, but never refresh a dead process/temporary route
// into a fresh confirmation merely because the previous config file is intact.
func (runner *executor) invalidateDependencies(status *Status, candidate Rendered) {
	for country, exit := range status.Exits {
		if !exit.Ready {
			continue
		}
		if runner.child == nil || !runner.child.Running() ||
			runner.appliedResult.XrayCountries[country] && (runner.xrayChild == nil || !runner.xrayChild.Running()) ||
			exit.Mode == "cellular_sim" && runner.appliedResult.CountryIdentity[country] != candidate.CountryIdentity[country] {
			exit.Ready, exit.HostProxyHost, exit.ProxyPort = false, "", 0
			exit.Error = "previous runtime dependency is unavailable"
			status.Exits[country] = exit
			status.Ready, status.RuntimeConfirmed = false, false
			status.Error = exit.Error
		}
	}
}

func (runner *executor) confirmRejected(requested string, candidate Rendered) {
	if !runner.runtimeReady() || runner.appliedResult.Status.ConfigIdentity == "" {
		runner.confirmStopped(requested, candidate, "configuration rejected; prior runtime unavailable")
		return
	}
	status := cloneStatus(runner.appliedResult.Status)
	status.Outcome, status.RuntimeConfirmed = "not_applied", true
	status.RequestedGeneration, status.RequestedAttempt = requested, runner.requestedAttempt
	status.Error = "configuration rejected; running configuration unchanged"
	runner.invalidateDependencies(&status, candidate)
	if !status.RuntimeConfirmed {
		// This drained attempt cannot confirm revoked donor credentials. Finish
		// by proving the pair stopped, so the helper can release its exact leases.
		runner.confirmStopped(requested, candidate, "configuration rejected; prior dependency unavailable")
		return
	}
	runner.blocked = requested
	_ = writeStatus(runner.settings.StatusPath, status)
}

func (runner *executor) confirmStopped(generation string, result Rendered, detail string) {
	if err := runner.stopPair(); err != nil {
		runner.failWithCandidate(generation, detail, false, result.Status)
		return
	}
	result.Status = unavailableStatus(result.Status)
	result.Status.Error = detail
	result.Ports, result.XrayPorts, result.Config, result.XrayConfig = nil, nil, nil, nil
	runner.commit(generation, result)
	runner.blocked = generation
}

func unavailableStatus(previous Status) Status {
	status := cloneStatus(previous)
	status.Ready = false
	status.RuntimeConfirmed = false
	status.DesiredGeneration = ""
	for country, exit := range status.Exits {
		exit.Ready = false
		exit.HostProxyHost, exit.ProxyPort = "", 0
		status.Exits[country] = exit
	}
	if status.Exits == nil {
		status.Exits = map[string]ExitStatus{}
	}
	return status
}

func cloneStatus(source Status) Status {
	copy := source
	copy.Exits = make(map[string]ExitStatus, len(source.Exits))
	for country, exit := range source.Exits {
		exit.Candidates = append([]string(nil), exit.Candidates...)
		copy.Exits[country] = exit
	}
	return copy
}

func (settings *Settings) validate() error {
	if settings.XrayPath == "" {
		settings.XrayPath = "/usr/libexec/mdd/xray"
	}
	if !filepath.IsAbs(settings.XrayPath) || filepath.Clean(settings.XrayPath) == "/" {
		return errors.New("Xray path must be absolute and scoped")
	}
	settings.DesiredPath = filepath.Clean(strings.TrimSpace(settings.DesiredPath))
	settings.StatusPath = filepath.Clean(strings.TrimSpace(settings.StatusPath))
	settings.StateDir = filepath.Clean(strings.TrimSpace(settings.StateDir))
	settings.SingBoxPath = filepath.Clean(strings.TrimSpace(settings.SingBoxPath))
	settings.TokenPath = filepath.Clean(strings.TrimSpace(settings.TokenPath))
	for _, path := range []string{settings.DesiredPath, settings.StatusPath, settings.StateDir, settings.SingBoxPath, settings.TokenPath} {
		if !filepath.IsAbs(path) || path == string(filepath.Separator) {
			return errors.New("country exit paths must be absolute and scoped")
		}
	}
	if strings.TrimSpace(settings.CoreURL) == "" {
		return errors.New("country exit Core IPC URL is required")
	}
	if settings.PortBase == 0 {
		settings.PortBase = proxyPortBase
	}
	if settings.PortBase < 1024 || settings.PortBase+675 > 65535 {
		return errors.New("country exit proxy port base is invalid")
	}
	if settings.Poll == 0 {
		settings.Poll = 2 * time.Second
	}
	if settings.Poll < 100*time.Millisecond || settings.Poll > time.Minute {
		return errors.New("country exit polling interval is invalid")
	}
	return nil
}

func checkConfig(parent context.Context, binary, path string) error {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "check", "-c", path)
	payload, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(payload))
		if len(detail) > 512 {
			detail = detail[:512]
		}
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

type execProcess struct {
	command *exec.Cmd
	done    chan struct{}
}

func startProcess(binary, config string) (managedProcess, error) {
	command := exec.Command(binary, "run", "-c", config)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	child := &execProcess{command: command, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		close(child.done)
	}()
	return child, nil
}

func (child *execProcess) Running() bool {
	if child == nil || child.command == nil || child.command.Process == nil {
		return false
	}
	select {
	case <-child.done:
		return false
	default:
		return true
	}
}

func (child *execProcess) Signal(signal os.Signal) error {
	if !child.Running() {
		return errors.New("sing-box process is not running")
	}
	return child.command.Process.Signal(signal)
}

func (child *execProcess) Stop(timeout time.Duration) error {
	if child == nil || !child.Running() {
		return nil
	}
	if err := child.command.Process.Signal(syscall.SIGTERM); err != nil {
		if !child.Running() {
			return nil
		}
		return err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-child.done:
		return nil
	case <-timer.C:
		if err := child.command.Process.Kill(); err != nil {
			return err
		}
		<-child.done
		return errors.New("sing-box required forced termination")
	}
}

func waitPorts(ctx context.Context, ports []int, child managedProcess, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var stableSince time.Time
	for {
		if child == nil || !child.Running() {
			return errors.New("sing-box exited before listeners became ready")
		}
		ready := len(ports) > 0
		for _, port := range ports {
			connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), 100*time.Millisecond)
			if err != nil {
				ready = false
				break
			}
			_ = connection.Close()
		}
		if ready {
			if stableSince.IsZero() {
				stableSince = time.Now()
			} else if time.Since(stableSince) >= 750*time.Millisecond {
				return nil
			}
		} else {
			stableSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("loopback SOCKS listeners did not become ready")
		case <-ticker.C:
		}
	}
}

func writeStatus(path string, status Status) error {
	status.ObservedAt = time.Now().UTC()
	payload, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(payload, '\n'), 0o640)
}

func atomicWrite(path string, payload []byte, mode os.FileMode) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, payload) {
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm() == mode {
			return nil
		}
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".mdd-egress-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(name)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := errors.Join(temporary.Sync(), temporary.Close()); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	complete = true
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func retryDelay(failures int) time.Duration {
	if failures < 1 {
		return 0
	}
	shift := failures - 1
	if shift > 6 {
		shift = 6
	}
	return time.Second * time.Duration(1<<shift)
}
