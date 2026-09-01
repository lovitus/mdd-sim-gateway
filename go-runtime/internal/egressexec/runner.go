package egressexec

import (
	"bytes"
	"context"
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
	settings      Settings
	controller    processController
	now           func() time.Time
	child         managedProcess
	applied       string
	appliedConfig []byte
	appliedResult Rendered
	requested     string
	blocked       string
	failures      int
	nextAttempt   time.Time
	cellular      *cellularClient
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
	runner := &executor{settings: settings, controller: systemProcessController{}, now: time.Now, cellular: cellular}
	ticker := time.NewTicker(settings.Poll)
	defer ticker.Stop()
	defer func() {
		if runner.child != nil {
			_ = runner.child.Stop(5 * time.Second)
		}
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
	if document.Generation != runner.requested {
		runner.requested, runner.blocked = document.Generation, ""
		runner.failures, runner.nextAttempt = 0, time.Time{}
	}
	if runner.now().Before(runner.nextAttempt) {
		return
	}
	runtimeProxy := document.Proxy
	var cellularErr error
	if runner.cellular != nil {
		runtimeProxy, cellularErr = runner.cellular.prepare(ctx, document.Proxy)
	}
	if cellularErr != nil {
		if runner.child != nil {
			_ = runner.child.Stop(5 * time.Second)
			runner.child = nil
		}
		_ = os.Remove(filepath.Join(runner.settings.StateDir, "sing-box.json"))
		runner.fail(document.Generation, "prepare cellular exit: "+cellularErr.Error(), false)
		return
	}
	runtimeDocument := document
	runtimeDocument.Proxy = runtimeProxy
	if document.Generation == runner.applied && (runner.runtimeReady() || !document.Proxy.Enabled) {
		status := cloneStatus(runner.appliedResult.Status)
		status.RequestedGeneration, status.Error = document.Generation, ""
		_ = writeStatus(runner.settings.StatusPath, status)
		return
	}
	if runner.applied != "" && !runner.runtimeReady() {
		runner.publishFailure("sing-box process is not running", document.Generation, false)
	}
	if document.Generation == runner.blocked && runner.runtimeReady() {
		return
	}
	result, renderErr := RenderAtBase(runtimeDocument, runner.settings.PortBase)
	if renderErr != nil {
		runner.failWithCandidate(document.Generation, "render country exits: "+renderErr.Error(),
			runner.runtimeReady(), result.Status)
		return
	}
	if !document.Proxy.Enabled {
		if runner.child != nil {
			err = runner.child.Stop(5 * time.Second)
		}
		if err != nil {
			runner.fail(document.Generation, "stop sing-box: "+err.Error(), runner.runtimeReady())
			return
		}
		runner.child = nil
		_ = os.Remove(filepath.Join(runner.settings.StateDir, "sing-box.json"))
		runner.commit(document.Generation, result)
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
		err = runner.child.Signal(syscall.SIGHUP)
	} else {
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
	if runner.child != nil {
		_ = runner.child.Stop(5 * time.Second)
		runner.child = nil
	}
	rolledBack := false
	if previousWasRunning && previousGeneration != "" && len(previousConfig) != 0 {
		if restoreErr := atomicWrite(currentPath, previousConfig, 0o600); restoreErr == nil {
			runner.child, restoreErr = runner.controller.Start(runner.settings.SingBoxPath, currentPath)
			if restoreErr == nil {
				restoreErr = runner.controller.WaitReady(ctx, previousResult.Ports, runner.child, 8*time.Second)
			}
			if restoreErr == nil {
				rolledBack = true
			} else if runner.child != nil {
				_ = runner.child.Stop(5 * time.Second)
				runner.child = nil
			}
		}
	}
	if rolledBack {
		runner.applied, runner.appliedConfig, runner.appliedResult = previousGeneration, previousConfig, previousResult
		runner.blocked = document.Generation
		runner.fail(document.Generation, "activate sing-box configuration: "+activationErr.Error()+"; previous generation restored", true)
		return
	}
	_ = os.Remove(currentPath)
	runner.failWithCandidate(document.Generation, "activate sing-box configuration: "+activationErr.Error(), false, result.Status)
}

func (runner *executor) runtimeReady() bool {
	return runner.child != nil && runner.child.Running()
}

func (runner *executor) commit(generation string, result Rendered) {
	result.Status.DesiredGeneration = generation
	result.Status.RequestedGeneration = generation
	result.Status.Ready = true
	result.Status.Error = ""
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
		status.Ready = true
	} else {
		status = unavailableStatus(runner.appliedResult.Status)
	}
	status.SchemaVersion = 1
	status.RequestedGeneration = requested
	status.Error = detail
	_ = writeStatus(runner.settings.StatusPath, status)
}

func unavailableStatus(previous Status) Status {
	status := cloneStatus(previous)
	status.Ready = false
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
