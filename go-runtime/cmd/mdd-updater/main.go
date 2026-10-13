// mdd-updater is a detached, root-owned release staging/install worker. It
// never runs inside mdd-core, so replacing Core cannot kill the worker.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systemupdate"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/updatenetwork"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerdeploy"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/releasebundle"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/releaseinstall"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mdd-updater:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("mdd-updater", flag.ContinueOnError)
	state := flags.String("state", "", "absolute update state path")
	destination := flags.String("destination", "", "absolute private staging directory")
	systemctlPath := flags.String("systemctl", "/bin/systemctl", "absolute systemctl executable")
	coreConfigPath := flags.String("core-config", "/etc/mdd/core.json", "absolute Core config containing loopback token")
	reconcile := flags.Bool("reconcile", false, "only reconcile the original maintenance operation; never install or restart")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || runtime.GOOS != "linux" || os.Geteuid() != 0 || strings.TrimSpace(*state) == "" || strings.TrimSpace(*destination) == "" || !filepath.IsAbs(filepath.Clean(*coreConfigPath)) {
		return errors.New("mdd-updater requires root Linux, -state and -destination")
	}
	store, err := systemupdate.Open(filepath.Clean(*state))
	if err != nil {
		return err
	}
	request, found, err := store.PendingRequest()
	if err != nil {
		return err
	}
	if !found {
		return errors.New("no pending update request")
	}
	serviceUser, err := user.Lookup("mdd")
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(serviceUser.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(serviceUser.Gid)
	if err != nil {
		return err
	}
	if err := store.SetWorkerStatusReaderGroup(gid); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	core, err := loadCoreMaintenance(filepath.Clean(*coreConfigPath))
	if err != nil {
		return err
	}
	view, err := core.maintenance(ctx, nil)
	if err != nil {
		return err
	}
	status, err := store.Status()
	if err != nil {
		return err
	}
	if status.OperationID == request.OperationID && status.State == systemupdate.StateSucceeded {
		return nil
	}
	if *reconcile {
		return reconcileUpdate(ctx, store, &core, status, request.OperationID, view)
	}
	if view.Record != nil && (view.Record.Phase != systemupdate.MaintenanceReleased || view.Record.OperationID == request.OperationID) {
		return errors.New("original maintenance exists; use explicit reconciliation, never repeat installation")
	}
	if status.OperationID == request.OperationID && status.State == systemupdate.StateUnknown {
		return errors.New("previous update outcome is unknown; explicit reconciliation is required")
	}
	if status.OperationID != request.OperationID || status.State != systemupdate.StateRequested {
		return errors.New("update worker requires the original fresh request; reconcile interrupted work")
	}
	status.State, status.Phase, status.UpdatedAt = systemupdate.StateRunning, "download", time.Now().UTC()
	if err := store.SetStatus(status); err != nil {
		return err
	}
	downloadClient, err := core.updateClient(ctx, request.Network)
	if err != nil {
		return markUpdaterFailure(store, status, "download", "update_network_changed", errors.New("verified update network route is unavailable"))
	}
	defer downloadClient.CloseIdleConnections()
	staged, err := systemupdate.FetchAndStage(ctx, request.Repository, request.Target, filepath.Clean(*destination), downloadClient)
	if err != nil {
		status.State, status.Phase, status.ErrorCode, status.ErrorDetail, status.UpdatedAt = systemupdate.StateFailed, "download", "update_download_failed", "release download or verification failed on the selected route", time.Now().UTC()
		_ = store.SetStatus(status)
		return errors.New(status.ErrorDetail)
	}
	manager := providerdeploy.Systemctl{Path: filepath.Clean(*systemctlPath)}
	if err := manager.Validate(); err != nil {
		return markUpdaterFailure(store, status, "preflight", "update_preflight_failed", err)
	}
	manifest, err := releaseinstall.Preflight(staged, releaseinstall.DefaultLayout())
	if err != nil {
		return markUpdaterFailure(store, status, "preflight", "update_preflight_failed", err)
	}
	target := systemupdate.UpdateTarget{RequestedTarget: request.Target, ReleaseID: manifest.ReleaseID, SourceRevision: manifest.SourceRevision, Contract: manifest.UpdateMaintenance}
	for _, artifact := range manifest.Artifacts {
		if artifact.Role == releasebundle.RoleCore {
			target.CoreSHA256 = artifact.SHA256
		}
	}
	if err := target.Validate(); err != nil {
		return markUpdaterFailure(store, status, "preflight", "update_maintenance_contract_unsupported", err)
	}
	view, err = core.maintenance(ctx, &systemupdate.MaintenanceCommand{Action: "begin", OperationID: request.OperationID, Target: target})
	if err != nil {
		if view.Instance != "" && !view.GuardInstalled && (view.Record == nil || view.Record.Phase == systemupdate.MaintenanceReleased) {
			return markUpdaterFailure(store, status, "maintenance", "update_maintenance_rejected", err)
		}
		return markUpdaterUnknown(store, status, "maintenance", "update_maintenance_unconfirmed", err)
	}
	if view.Record == nil || view.Record.Target != target || view.Record.Phase != systemupdate.MaintenanceReady || !view.GuardInstalled {
		return markUpdaterUnknown(store, status, "maintenance", "update_maintenance_unconfirmed", errors.New("update maintenance receipt mismatch"))
	}
	view, err = core.maintenance(ctx, &systemupdate.MaintenanceCommand{Action: "activate", OperationID: request.OperationID, Revision: view.Record.Revision})
	if err != nil {
		return markUpdaterUnknown(store, status, "install", "update_activation_permit_unknown", err)
	}
	activation := view.Record
	if activation == nil || activation.Target != target || activation.Phase != systemupdate.MaintenanceActivating {
		return markUpdaterUnknown(store, status, "install", "update_activation_permit_unknown", errors.New("activation receipt mismatch"))
	}
	if _, err := releaseinstall.Activate(ctx, staged, releaseinstall.DefaultLayout(), releaseinstall.Identity{RootUID: 0, RootGID: 0, ServiceUID: uid, ServiceGID: gid}, updaterReloader{ctx: ctx, manager: manager}); err != nil {
		status.State, status.Phase, status.ErrorCode, status.ErrorDetail, status.UpdatedAt = systemupdate.StateUnknown, "install", "update_install_unknown", "release activation outcome is unknown", time.Now().UTC()
		_ = store.SetStatus(status)
		return err
	}
	status.State, status.Phase, status.UpdatedAt = systemupdate.StateRunning, "restart", time.Now().UTC()
	if err := store.SetStatus(status); err != nil {
		return err
	}
	for _, unit := range systemupdate.FixedServices {
		if err := manager.RestartFixed(ctx, unit); err != nil {
			return markUpdaterUnknown(store, status, "restart", "update_restart_unknown", err)
		}
		if err := manager.VerifyFixedImage(ctx, unit, target.CoreSHA256); err != nil {
			return markUpdaterUnknown(store, status, "health", "update_fixed_service_unconfirmed", err)
		}
		view, err = core.waitRestored(ctx, *activation)
		if err != nil {
			return markUpdaterUnknown(store, status, "health", "update_guard_restore_unconfirmed", err)
		}
		view, err = core.maintenance(ctx, &systemupdate.MaintenanceCommand{Action: "restarted", OperationID: request.OperationID, Revision: view.Record.Revision, Unit: unit})
		if err != nil {
			return markUpdaterUnknown(store, status, "restart", "update_restart_receipt_unknown", err)
		}
	}
	view, err = core.maintenance(ctx, &systemupdate.MaintenanceCommand{Action: "finish", OperationID: request.OperationID, Revision: view.Record.Revision})
	if err != nil || view.Record == nil || view.Record.Phase != systemupdate.MaintenanceReleased || view.GuardInstalled {
		return markUpdaterUnknown(store, status, "release", "update_release_unconfirmed", errors.Join(err, errors.New("original maintenance release is unconfirmed")))
	}
	status.State, status.Phase, status.ErrorCode, status.UpdatedAt = systemupdate.StateSucceeded, "restarted", "update_applied", time.Now().UTC()
	status.ErrorDetail = ""
	if err := store.SetStatus(status); err != nil {
		return err
	}
	return nil
}

func markUpdaterFailure(store *systemupdate.Store, status systemupdate.Status, phase, code string, err error) error {
	status.State, status.Phase, status.ErrorCode, status.ErrorDetail, status.UpdatedAt = systemupdate.StateFailed, phase, code, err.Error(), time.Now().UTC()
	_ = store.SetStatus(status)
	return err
}

type coreMaintenance struct {
	egressStatusPath string
	baseURL, token   string
}

func loadCoreMaintenance(path string) (coreMaintenance, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return coreMaintenance{}, err
	}
	var config struct {
		ProviderApply struct {
			EgressStatusPath string `json:"egress_status_path"`
		} `json:"provider_apply"`
		Local struct {
			Listen string `json:"listen"`
			Token  string `json:"token"`
		} `json:"local"`
	}
	if json.Unmarshal(payload, &config) != nil {
		return coreMaintenance{}, errors.New("invalid Core config")
	}
	if !strings.HasPrefix(config.Local.Listen, "127.0.0.1:") && !strings.HasPrefix(config.Local.Listen, "localhost:") && !strings.HasPrefix(config.Local.Listen, "[::1]:") {
		return coreMaintenance{}, errors.New("Core maintenance endpoint is not loopback")
	}
	if len(config.Local.Token) < 32 {
		return coreMaintenance{}, errors.New("Core maintenance token unavailable")
	}
	return coreMaintenance{baseURL: "http://" + config.Local.Listen, token: config.Local.Token, egressStatusPath: config.ProviderApply.EgressStatusPath}, nil
}

func (core *coreMaintenance) updateClient(ctx context.Context, recorded *updatenetwork.Route) (*http.Client, error) {
	if recorded == nil {
		return (updatenetwork.Route{Mode: "direct"}).Client(0)
	}
	if err := recorded.ValidateIdentity(); err != nil {
		return nil, err
	}
	ipc := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer ipc.CloseIdleConnections()
	if err := updatenetwork.CheckPolicy(ctx, core.baseURL+updatenetwork.PolicyPath, core.token, recorded.PolicyRevision, ipc); err != nil {
		return nil, err
	}
	if recorded.Mode == "direct" {
		return recorded.Client(0)
	}
	config, err := egressconfig.FetchSnapshot(ctx, core.baseURL+egressconfig.SnapshotIPCPath, core.token, ipc)
	if err != nil {
		return nil, errors.New("update proxy configuration unavailable")
	}
	var actual egressstatus.Snapshot
	if recorded.Country != "" {
		actual, err = egressstatus.Load(core.egressStatusPath)
		if err != nil {
			return nil, errors.New("verified country exit unavailable")
		}
	}
	resolved, err := updatenetwork.ResolveRecorded(*recorded, config, actual)
	if err != nil {
		return nil, err
	}
	if err := updatenetwork.CheckPolicy(ctx, core.baseURL+updatenetwork.PolicyPath, core.token, recorded.PolicyRevision, ipc); err != nil {
		return nil, err
	}
	return resolved.Client(0)
}
func (core *coreMaintenance) maintenance(ctx context.Context, command *systemupdate.MaintenanceCommand) (systemupdate.MaintenanceView, error) {
	method := http.MethodGet
	var body io.Reader
	if command != nil {
		method = http.MethodPost
		payload, err := json.Marshal(command)
		if err != nil {
			return systemupdate.MaintenanceView{}, err
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, core.baseURL+systemupdate.MaintenancePath, body)
	if err != nil {
		return systemupdate.MaintenanceView{}, err
	}
	request.Header.Set("Authorization", "Bearer "+core.token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return systemupdate.MaintenanceView{}, err
	}
	defer response.Body.Close()
	var result struct {
		systemupdate.MaintenanceView
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return result.MaintenanceView, err
	}
	if result.Record != nil && result.Record.Validate() != nil {
		return result.MaintenanceView, errors.New("invalid update maintenance response")
	}
	if response.StatusCode != http.StatusOK {
		return result.MaintenanceView, fmt.Errorf("update maintenance rejected: %s", result.Error)
	}
	if command != nil && (result.Record == nil || result.Record.OperationID != command.OperationID) {
		return result.MaintenanceView, errors.New("update operation response mismatch")
	}
	return result.MaintenanceView, nil
}

func (core *coreMaintenance) waitRestored(ctx context.Context, expected systemupdate.Maintenance) (systemupdate.MaintenanceView, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	delay := time.Second
	for {
		view, err := core.maintenance(ctx, nil)
		if err == nil && view.Record != nil && view.Record.OperationID == expected.OperationID && view.Record.LeaseID == expected.LeaseID &&
			view.Record.Target == expected.Target && view.Record.Phase == systemupdate.MaintenanceActivating &&
			view.Record.Revision >= expected.Revision && view.Instance != expected.OriginalCore && view.CoreSHA256 == expected.Target.CoreSHA256 && view.GuardInstalled {
			return view, nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return view, errors.New("replacement Core maintenance identity did not recover")
		case <-timer.C:
		}
		if delay < 8*time.Second {
			delay *= 2
		}
	}
}

func markUpdaterUnknown(store *systemupdate.Store, status systemupdate.Status, phase, code string, cause error) error {
	status.State, status.Phase, status.ErrorCode, status.ErrorDetail, status.UpdatedAt = systemupdate.StateUnknown, phase, code, "original update requires explicit reconciliation", time.Now().UTC()
	return errors.Join(cause, store.SetStatus(status))
}

func reconcileUpdate(ctx context.Context, store *systemupdate.Store, core *coreMaintenance, status systemupdate.Status, operation string, view systemupdate.MaintenanceView) error {
	if view.Record == nil || view.Record.OperationID != operation {
		return errors.New("no matching original update maintenance")
	}
	switch view.Record.Phase {
	case systemupdate.MaintenancePreparing, systemupdate.MaintenanceReady, systemupdate.MaintenanceCleanup:
		var err error
		view, err = core.maintenance(ctx, &systemupdate.MaintenanceCommand{Action: "cleanup", OperationID: operation, Revision: view.Record.Revision})
		if err != nil {
			return err
		}
	case systemupdate.MaintenanceReleasing:
		var err error
		view, err = core.maintenance(ctx, &systemupdate.MaintenanceCommand{Action: "finish", OperationID: operation, Revision: view.Record.Revision})
		if err != nil {
			return err
		}
	case systemupdate.MaintenanceReleased:
		if view.GuardInstalled {
			var err error
			view, err = core.maintenance(ctx, &systemupdate.MaintenanceCommand{Action: "finish", OperationID: operation, Revision: view.Record.Revision})
			if err != nil {
				return err
			}
		}
	default:
		return errors.New("activation outcome unconfirmed; reconciliation will not install, restart or roll back")
	}
	if view.Record == nil || view.Record.Phase != systemupdate.MaintenanceReleased || view.GuardInstalled {
		return errors.New("maintenance release is unconfirmed")
	}
	status.OperationID, status.State, status.Phase, status.ErrorCode, status.ErrorDetail, status.UpdatedAt = operation, systemupdate.StateFailed, "cancelled", "update_cancelled_before_activation", "", time.Now().UTC()
	if view.Record.VerifiedCore != "" {
		status.State, status.Phase, status.ErrorCode = systemupdate.StateSucceeded, "restarted", "update_applied"
	}
	return store.SetStatus(status)
}

type updaterReloader struct {
	ctx     context.Context
	manager providerdeploy.Systemctl
}

func (reloader updaterReloader) DaemonReload() error {
	return reloader.manager.DaemonReload(reloader.ctx)
}
