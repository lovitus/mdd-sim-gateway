// mdd-updater is a detached, root-owned release staging/install worker. It
// never runs inside mdd-core, so replacing Core cannot kill the worker.
package main

import (
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

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systemupdate"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerdeploy"
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
	status, err := store.Status()
	if err != nil {
		return err
	}
	if status.OperationID == request.OperationID && status.State == systemupdate.StateSucceeded {
		return nil
	}
	if status.OperationID == request.OperationID && status.State == systemupdate.StateUnknown {
		return errors.New("previous update outcome is unknown; explicit reconciliation is required")
	}
	status.State, status.Phase, status.UpdatedAt = systemupdate.StateRunning, "download", time.Now().UTC()
	if err := store.SetStatus(status); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	core, err := loadCoreMaintenance(filepath.Clean(*coreConfigPath))
	if err != nil {
		return markUpdaterFailure(store, status, "maintenance_preflight", "update_maintenance_preflight_failed", err)
	}
	if err := core.begin(ctx, request.OperationID); err != nil {
		return markUpdaterFailure(store, status, "maintenance_preflight", "update_maintenance_begin_failed", err)
	}
	maintenanceBegun := true
	defer func() {
		if maintenanceBegun {
			_ = core.resume(context.Background())
		}
	}()
	staged, err := systemupdate.FetchAndStage(ctx, request.Repository, request.Target, filepath.Clean(*destination), nil)
	if err != nil {
		status.State, status.Phase, status.ErrorCode, status.ErrorDetail, status.UpdatedAt = systemupdate.StateFailed, "download", "update_download_failed", err.Error(), time.Now().UTC()
		_ = store.SetStatus(status)
		return err
	}
	manager := providerdeploy.Systemctl{Path: filepath.Clean(*systemctlPath)}
	if err := manager.Validate(); err != nil {
		return err
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
	if _, err := releaseinstall.Activate(ctx, staged, releaseinstall.DefaultLayout(), releaseinstall.Identity{RootUID: 0, RootGID: 0, ServiceUID: uid, ServiceGID: gid}, updaterReloader{ctx: ctx, manager: manager}); err != nil {
		status.State, status.Phase, status.ErrorCode, status.ErrorDetail, status.UpdatedAt = systemupdate.StateUnknown, "install", "update_install_unknown", "release activation outcome is unknown", time.Now().UTC()
		_ = store.SetStatus(status)
		return err
	}
	if err := core.resume(ctx); err != nil {
		status.State, status.Phase, status.ErrorCode, status.ErrorDetail, status.UpdatedAt = systemupdate.StateUnknown, "maintenance_resume", "update_maintenance_resume_unknown", "provider resume outcome is unknown", time.Now().UTC()
		_ = store.SetStatus(status)
		return err
	}
	maintenanceBegun = false
	status.State, status.Phase, status.UpdatedAt = systemupdate.StateRunning, "restart", time.Now().UTC()
	if err := store.SetStatus(status); err != nil {
		return err
	}
	for _, unit := range []string{"mdd-core.service", "mdd-provider-apply.service", "mdd-egress.service"} {
		if err := manager.RestartFixed(ctx, unit); err != nil {
			status.State, status.Phase, status.ErrorCode, status.ErrorDetail, status.UpdatedAt = systemupdate.StateUnknown, "restart", "update_restart_unknown", "fixed service restart outcome is unknown", time.Now().UTC()
			_ = store.SetStatus(status)
			if _, rollbackErr := releaseinstall.RollbackLatest(context.Background(), releaseinstall.DefaultLayout(), 0, updaterReloader{ctx: context.Background(), manager: manager}); rollbackErr != nil {
				status.ErrorCode, status.ErrorDetail = "update_rollback_unknown", "restart failed and rollback outcome is unknown"
			} else {
				status.ErrorCode, status.ErrorDetail = "update_rolled_back", "restart failed; previous release restored"
			}
			status.UpdatedAt = time.Now().UTC()
			_ = store.SetStatus(status)
			return err
		}
	}
	if err := core.waitHealthy(ctx); err != nil {
		status.State, status.Phase, status.ErrorCode, status.ErrorDetail, status.UpdatedAt = systemupdate.StateUnknown, "health", "update_health_unknown", "Core health after restart is unconfirmed", time.Now().UTC()
		_ = store.SetStatus(status)
		if _, rollbackErr := releaseinstall.RollbackLatest(context.Background(), releaseinstall.DefaultLayout(), 0, updaterReloader{ctx: context.Background(), manager: manager}); rollbackErr != nil {
			status.ErrorCode, status.ErrorDetail = "update_rollback_unknown", "health failed and rollback outcome is unknown"
		} else {
			status.ErrorCode, status.ErrorDetail = "update_rolled_back", "health failed; previous release restored"
		}
		status.UpdatedAt = time.Now().UTC()
		_ = store.SetStatus(status)
		return err
	}
	status.State, status.Phase, status.ErrorCode, status.UpdatedAt = systemupdate.StateSucceeded, "restarted", "update_applied", time.Now().UTC()
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
	baseURL, token string
	request        providerapply.DrainRequest
	active         bool
}

func loadCoreMaintenance(path string) (coreMaintenance, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return coreMaintenance{}, err
	}
	var config struct {
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
	return coreMaintenance{baseURL: "http://" + config.Local.Listen, token: config.Local.Token}, nil
}
func (core *coreMaintenance) begin(ctx context.Context, operationID string) error {
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	snapshot, err := providerapply.Fetch(ctx, core.baseURL+providerapply.Path, core.token, client)
	if err != nil {
		return err
	}
	if len(snapshot.Lines) == 0 {
		return errors.New("no provider lines available for maintenance")
	}
	ids := make([]string, len(snapshot.Lines))
	for i, line := range snapshot.Lines {
		ids[i] = line.LineID
	}
	core.request = providerapply.DrainRequest{SchemaVersion: 1, CatalogRevision: snapshot.CatalogRevision, LeaseID: "update-drain-" + operationID, LineIDs: ids}
	result, err := providerapply.RequestMaintenance(ctx, core.baseURL, core.token, core.request, true, client)
	if err != nil {
		return err
	}
	if !result.Ready {
		return errors.New("provider maintenance drain not ready")
	}
	core.active = true
	return nil
}
func (core *coreMaintenance) resume(ctx context.Context) error {
	if !core.active {
		return nil
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	result, err := providerapply.RequestMaintenance(ctx, core.baseURL, core.token, core.request, false, client)
	if err != nil {
		return err
	}
	if !result.Ready {
		return errors.New("provider maintenance resume not ready")
	}
	core.active = false
	return nil
}

func (core *coreMaintenance) waitHealthy(ctx context.Context) error {
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, core.baseURL+"/healthz", nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("Core health did not recover after restart")
		case <-ticker.C:
		}
	}
}

type updaterReloader struct {
	ctx     context.Context
	manager providerdeploy.Systemctl
}

func (reloader updaterReloader) DaemonReload() error {
	return reloader.manager.DaemonReload(reloader.ctx)
}
