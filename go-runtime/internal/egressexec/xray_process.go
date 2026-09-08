package egressexec

// Adapted from ec620942 host/mdd_orchestrator.py:apply_xray. The Go executor
// coordinates Xray and sing-box as one checked generation, including rollback.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type xrayProcessController struct{}

func (xrayProcessController) Check(parent context.Context, binary, config string) error {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	// Xray diagnostics can contain the imported subscription credentials.
	if err := exec.CommandContext(ctx, binary, "run", "-test", "-config", config).Run(); err != nil {
		return errors.New("Xray executable unavailable or candidate configuration rejected")
	}
	return nil
}

func (xrayProcessController) Start(binary, config string) (managedProcess, error) {
	command := exec.Command(binary, "run", "-config", config)
	if err := command.Start(); err != nil {
		return nil, errors.New("Xray process could not start")
	}
	child := &execProcess{command: command, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		close(child.done)
	}()
	return child, nil
}

func (xrayProcessController) WaitReady(ctx context.Context, ports []int, child managedProcess, timeout time.Duration) error {
	if err := waitPorts(ctx, ports, child, timeout); err != nil {
		return errors.New("Xray loopback listeners unavailable")
	}
	return nil
}

func (runner *executor) stopPair() error {
	var failures []error
	// Stop consumers first, then the bridge. Do not lose a still-live handle.
	if runner.child != nil {
		if err := runner.child.Stop(5 * time.Second); err != nil && runner.child.Running() {
			failures = append(failures, errors.New("sing-box could not stop"))
		} else {
			runner.child = nil
		}
	}
	if runner.xrayChild != nil {
		if err := runner.xrayChild.Stop(5 * time.Second); err != nil && runner.xrayChild.Running() {
			failures = append(failures, errors.New("Xray could not stop"))
		} else {
			runner.xrayChild = nil
		}
	}
	return errors.Join(failures...)
}

// A failed bridge is not authority to interrupt healthy ordinary exits served
// by the still-running sing-box. Only recover the exact applied Xray config.
func (runner *executor) restoreXrayBridge(ctx context.Context, generation string, result Rendered) {
	if runner.xrayController == nil {
		runner.xrayController = xrayProcessController{}
	}
	path := filepath.Join(runner.settings.StateDir, "xray.json")
	err := atomicWrite(path, result.XrayConfig, 0o600)
	if err == nil {
		err = runner.xrayController.Check(ctx, runner.settings.XrayPath, path)
	}
	if err == nil {
		runner.xrayChild, err = runner.xrayController.Start(runner.settings.XrayPath, path)
	}
	if err == nil {
		err = runner.xrayController.WaitReady(ctx, result.XrayPorts, runner.xrayChild, 8*time.Second)
	}
	if err == nil && runner.runtimeReady() {
		runner.commit(generation, result)
		return
	}
	if runner.xrayChild != nil {
		if stopErr := runner.xrayChild.Stop(5 * time.Second); stopErr == nil || !runner.xrayChild.Running() {
			runner.xrayChild = nil
		}
	}
	runner.failWithCandidate(generation, "Xray bridge recovery failed", false, result.Status)
}

func (runner *executor) startPair(ctx context.Context, result Rendered) error {
	xrayPath := filepath.Join(runner.settings.StateDir, "xray.json")
	singPath := filepath.Join(runner.settings.StateDir, "sing-box.json")
	if err := atomicWrite(singPath, result.Config, 0o600); err != nil {
		return errors.New("publish sing-box configuration failed")
	}
	if len(result.XrayConfig) > 0 {
		if err := atomicWrite(xrayPath, result.XrayConfig, 0o600); err != nil {
			return errors.New("publish Xray configuration failed")
		}
		var err error
		runner.xrayChild, err = runner.xrayController.Start(runner.settings.XrayPath, xrayPath)
		if err != nil {
			return errors.New("start Xray failed")
		}
		if err := runner.xrayController.WaitReady(ctx, result.XrayPorts, runner.xrayChild, 8*time.Second); err != nil {
			return errors.New("Xray listeners not ready")
		}
	} else {
		_ = os.Remove(xrayPath)
	}
	var err error
	runner.child, err = runner.controller.Start(runner.settings.SingBoxPath, singPath)
	if err != nil {
		return errors.New("start sing-box failed")
	}
	if err := runner.controller.WaitReady(ctx, result.Ports, runner.child, 8*time.Second); err != nil {
		return errors.New("sing-box listeners not ready")
	}
	if !runner.child.Running() || (len(result.XrayConfig) > 0 && !runner.xrayChild.Running()) {
		return errors.New("exit process disappeared during activation")
	}
	return nil
}

func (runner *executor) activateXrayPair(ctx context.Context, generation string, result Rendered) {
	if runner.xrayController == nil {
		runner.xrayController = xrayProcessController{}
	}
	// Check both complete configurations before interrupting the current pair.
	for _, item := range []struct {
		name, binary string
		payload      []byte
		controller   processController
	}{
		{"sing-box", runner.settings.SingBoxPath, result.Config, runner.controller},
		{"xray", runner.settings.XrayPath, result.XrayConfig, runner.xrayController},
	} {
		if len(item.payload) == 0 {
			continue
		}
		path := filepath.Join(runner.settings.StateDir, "."+item.name+"-candidate.json")
		err := atomicWrite(path, item.payload, 0o600)
		if err == nil {
			err = item.controller.Check(ctx, item.binary, path)
		}
		_ = os.Remove(path)
		if err != nil {
			runner.failWithCandidate(generation, item.name+" candidate rejected", runner.runtimeReady(), result.Status)
			return
		}
	}
	previous, previousGeneration := runner.appliedResult, runner.applied
	wasReady := runner.runtimeReady()
	if err := runner.stopPair(); err != nil {
		runner.fail(generation, "exit process pair could not stop", runner.runtimeReady())
		return
	}
	if err := runner.startPair(ctx, result); err == nil {
		runner.appliedConfig = append([]byte(nil), result.Config...)
		runner.commit(generation, result)
		return
	}
	if err := runner.stopPair(); err != nil {
		runner.failWithCandidate(generation, "failed exit process pair could not stop", false, result.Status)
		return
	}
	if wasReady && previousGeneration != "" {
		if err := runner.startPair(ctx, previous); err == nil {
			runner.applied, runner.appliedResult = previousGeneration, previous
			runner.appliedConfig = append([]byte(nil), previous.Config...)
			runner.blocked = generation
			runner.fail(generation, "exit process activation failed; previous generation restored", true)
			return
		}
	}
	_ = runner.stopPair()
	_ = os.Remove(filepath.Join(runner.settings.StateDir, "sing-box.json"))
	_ = os.Remove(filepath.Join(runner.settings.StateDir, "xray.json"))
	runner.failWithCandidate(generation, "exit process activation and recovery failed", false, result.Status)
}
