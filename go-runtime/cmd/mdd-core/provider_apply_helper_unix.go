//go:build !windows

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerdeploy"
)

type providerApplyService struct {
	settings       config
	uid            int
	gid            int
	desiredUID     int
	applying       atomic.Bool
	egressApplying atomic.Bool
}

func runProviderApplyHelper(arguments []string) error {
	flags := flag.NewFlagSet("provider-apply-helper", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "path to the running mdd-core configuration")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" || flags.NArg() != 0 {
		return errors.New("-config is required")
	}
	if err := requireProviderApplyPrivileges(); err != nil {
		return err
	}
	settings, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if !settings.ProviderApply.Enabled {
		return errors.New("provider apply is not enabled")
	}
	uid, gid, err := providerServiceIdentity(settings.ProviderApply.ProviderUser)
	if err != nil {
		return err
	}
	if err := validateProviderApplyRoots(settings, 0, gid); err != nil {
		return err
	}
	helperLock, err := providerdeploy.AcquireLock(filepath.Join(settings.ProviderApply.ReceiptPath, ".helper.lock"))
	if err != nil {
		return err
	}
	defer helperLock.Close()
	service := &providerApplyService{settings: settings, uid: uid, gid: gid}
	providerHandler, err := provideradmin.NewHandler(service)
	if err != nil {
		return err
	}
	egressHandler, err := egressconfig.NewApplyHandler(service)
	if err != nil {
		return err
	}
	credentialHandler, err := adminauth.NewCredentialPersistenceHandler(settings.AuthPath, uid, gid)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle(provideradmin.Path, providerHandler)
	mux.Handle(egressconfig.ApplyPath, egressHandler)
	mux.Handle(adminauth.CredentialPersistencePath, credentialHandler)
	handlerWithAuth, err := provideradmin.Authenticate(mux, settings.Local.Token)
	if err != nil {
		return err
	}
	listener, err := listenProviderApplySocket(settings.ProviderApply.SocketPath, gid)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(settings.ProviderApply.SocketPath)
	}()
	server := &http.Server{Handler: handlerWithAuth, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		result <- err
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		// An in-flight apply owns a durable rollback journal and may use its
		// full two-minute bound. Graceful service stop must not kill it halfway.
		shutdown, cancel := context.WithTimeout(context.Background(), 125*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func (service *providerApplyService) EgressStatus(ctx context.Context) (egressconfig.ApplyStatus, error) {
	configSnapshot, err := service.egressSnapshot(ctx)
	if err != nil {
		return egressconfig.ApplyStatus{}, egressFailure(http.StatusServiceUnavailable, "egress_config_snapshot_unavailable", err)
	}
	catalogSnapshot, err := service.catalogSnapshot(ctx)
	if err != nil {
		return egressconfig.ApplyStatus{}, egressFailure(http.StatusServiceUnavailable, "catalog_snapshot_unavailable", err)
	}
	applied, _, err := egressdesired.CurrentApplied(service.settings.ProviderApply.EgressDesiredPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return egressconfig.ApplyStatus{}, egressFailure(http.StatusConflict, "egress_desired_invalid", err)
	}
	runtimeGeneration, runtimeErr := egressdesired.StatusGeneration(service.settings.ProviderApply.EgressStatusPath)
	confirmed := runtimeErr == nil && runtimeGeneration == applied.Generation
	return egressconfig.ApplyStatus{
		SchemaVersion:  egressconfig.SchemaVersion,
		ConfigRevision: configSnapshot.Revision, CatalogRevision: catalogSnapshot.Revision,
		AppliedConfig: applied.ConfigRevision, AppliedCatalog: applied.CatalogRevision,
		DesiredGeneration: applied.Generation, RuntimeConfirmed: confirmed,
		Pending:  configSnapshot.Revision != applied.ConfigRevision || catalogSnapshot.Revision != applied.CatalogRevision || !confirmed,
		Applying: service.egressApplying.Load(),
	}, nil
}

func (service *providerApplyService) ApplyEgress(ctx context.Context, configRevision, catalogRevision uint64) (egressconfig.ApplyResult, error) {
	if !service.egressApplying.CompareAndSwap(false, true) {
		return egressconfig.ApplyResult{}, egressFailure(http.StatusConflict, "egress_apply_in_progress", nil)
	}
	defer service.egressApplying.Store(false)
	configSnapshot, err := service.egressSnapshot(ctx)
	if err != nil {
		return egressconfig.ApplyResult{}, egressFailure(http.StatusServiceUnavailable, "egress_config_snapshot_unavailable", err)
	}
	catalogSnapshot, err := service.catalogSnapshot(ctx)
	if err != nil {
		return egressconfig.ApplyResult{}, egressFailure(http.StatusServiceUnavailable, "catalog_snapshot_unavailable", err)
	}
	if configSnapshot.Revision != configRevision || catalogSnapshot.Revision != catalogRevision {
		return egressconfig.ApplyResult{}, egressFailure(http.StatusPreconditionFailed, "egress_apply_revision_changed", nil)
	}
	document, err := egressdesired.Render(configSnapshot, catalogSnapshot, time.Now())
	if err != nil {
		return egressconfig.ApplyResult{}, egressFailure(http.StatusConflict, "egress_desired_render_failed", err)
	}
	changed, err := egressdesired.PublishOwned(service.settings.ProviderApply.EgressDesiredPath, document,
		service.desiredUID, service.gid, 0o640)
	if err != nil {
		return egressconfig.ApplyResult{}, egressFailure(http.StatusInternalServerError, "egress_desired_publish_failed", err)
	}
	if err := egressdesired.WaitForRuntime(ctx, service.settings.ProviderApply.EgressStatusPath,
		document.Generation, 30*time.Second, 200*time.Millisecond); err != nil {
		code := "egress_runtime_confirmation_cancelled"
		if errors.Is(err, egressdesired.ErrRuntimeConfirmationTimeout) {
			code = "egress_runtime_confirmation_timeout"
		}
		return egressconfig.ApplyResult{}, egressFailure(http.StatusGatewayTimeout, code, err)
	}
	state := "unchanged"
	if changed {
		state = "applied"
	}
	return egressconfig.ApplyResult{
		SchemaVersion: egressconfig.SchemaVersion, ConfigRevision: configRevision,
		CatalogRevision: catalogRevision, Generation: document.Generation,
		State: state, Code: "runtime_confirmed",
	}, nil
}

func (service *providerApplyService) egressSnapshot(ctx context.Context) (egressconfig.Snapshot, error) {
	address, err := providerCoreAddress(service.settings.Local.Listen)
	if err != nil {
		return egressconfig.Snapshot{}, err
	}
	requestContext, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	return egressconfig.FetchSnapshot(requestContext, address+egressconfig.SnapshotIPCPath, service.settings.Local.Token,
		&http.Client{Transport: &http.Transport{Proxy: nil}})
}

func egressFailure(status int, code string, cause error) error {
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	return &egressconfig.ApplyError{Status: status, Code: code, Detail: detail, Cause: cause}
}

func (service *providerApplyService) Status(ctx context.Context) (provideradmin.Status, error) {
	snapshot, err := service.catalogSnapshot(ctx)
	if err != nil {
		return provideradmin.Status{}, providerFailure(http.StatusServiceUnavailable, "catalog_snapshot_unavailable", err)
	}
	status := provideradmin.Status{
		SchemaVersion: provideradmin.SchemaVersion, CatalogRevision: snapshot.Revision, Applying: service.applying.Load(),
	}
	target, err := providerdeploy.CurrentTarget(service.settings.ProviderApply.CurrentLink)
	if err != nil {
		return provideradmin.Status{}, providerFailure(http.StatusConflict, "provider_current_invalid", err)
	}
	if target != "" {
		manifest, err := providerconfig.LoadDirectory(target)
		if err != nil {
			return provideradmin.Status{}, providerFailure(http.StatusConflict, "provider_current_invalid", err)
		}
		status.AppliedRevision = manifest.CatalogRevision
	}
	status.Pending = status.CatalogRevision != status.AppliedRevision
	if receipt, err := providerdeploy.ReadCurrentReceipt(service.settings.ProviderApply.ReceiptPath); err == nil {
		status.LastApplyID = receipt.ApplyID
		status.LastState = string(receipt.State)
		status.LastCode = receipt.Code
	} else if !errors.Is(err, os.ErrNotExist) {
		return provideradmin.Status{}, providerFailure(http.StatusConflict, "provider_apply_receipt_invalid", err)
	}
	return status, nil
}

func (service *providerApplyService) Apply(ctx context.Context, revision uint64) (provideradmin.ApplyResult, error) {
	if !service.applying.CompareAndSwap(false, true) {
		return provideradmin.ApplyResult{}, providerFailure(http.StatusConflict, "provider_apply_in_progress", nil)
	}
	defer service.applying.Store(false)
	snapshot, err := service.catalogSnapshot(ctx)
	if err != nil {
		return provideradmin.ApplyResult{}, providerFailure(http.StatusServiceUnavailable, "catalog_snapshot_unavailable", err)
	}
	if snapshot.Revision != revision {
		return provideradmin.ApplyResult{}, providerFailure(http.StatusPreconditionFailed, "catalog_revision_changed", nil)
	}
	exits, err := egressstatus.Load(service.settings.ProviderApply.EgressStatusPath)
	if err != nil {
		return provideradmin.ApplyResult{}, providerFailure(http.StatusConflict, "egress_status_invalid", err)
	}
	candidate, err := service.candidatePath(revision)
	if err != nil {
		return provideradmin.ApplyResult{}, providerFailure(http.StatusInternalServerError, "candidate_identity_failed", err)
	}
	manifest, err := renderProviderDirectory(service.settings, snapshot, exits, candidate, service.settings.ProviderApply.StatePath)
	if err != nil {
		return provideradmin.ApplyResult{}, providerFailure(http.StatusConflict, "provider_render_failed", err)
	}
	if err := chownProviderCandidate(candidate, manifest, service.uid, service.gid); err != nil {
		return provideradmin.ApplyResult{}, providerFailure(http.StatusInternalServerError, "provider_candidate_ownership_failed", err)
	}
	applyContext, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	receipt, applyErr := executeProviderCandidate(applyContext, service.settings, candidate,
		service.settings.ProviderApply.CurrentLink, service.settings.ProviderApply.ReceiptPath,
		service.settings.ProviderApply.ProviderBinary, service.settings.ProviderApply.ProviderUser,
		service.settings.ProviderApply.SystemctlPath)
	result := applyResult(receipt, revision)
	if applyErr == nil {
		return result, nil
	}
	status, code := http.StatusInternalServerError, "provider_apply_failed"
	if errors.Is(applyErr, errProviderApplyBlocked) {
		status, code = http.StatusConflict, "provider_apply_blocked"
	} else if receipt.State == providerdeploy.StateRolledBack {
		status, code = http.StatusConflict, "provider_apply_rolled_back"
	} else if receipt.State == providerdeploy.StateAppliedResumeIncomplete || receipt.State == providerdeploy.StateManualRecovery {
		status, code = http.StatusConflict, "provider_apply_requires_recovery"
	}
	return result, providerFailure(status, code, applyErr)
}

func (service *providerApplyService) catalogSnapshot(ctx context.Context) (linecatalog.Snapshot, error) {
	address, err := providerCoreAddress(service.settings.Local.Listen)
	if err != nil {
		return linecatalog.Snapshot{}, err
	}
	requestContext, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	return linecatalog.FetchSnapshot(requestContext, address+linecatalog.SnapshotIPCPath, service.settings.Local.Token,
		&http.Client{Transport: &http.Transport{Proxy: nil}})
}

func (service *providerApplyService) candidatePath(revision uint64) (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	name := fmt.Sprintf("catalog-%020d-%s", revision, hex.EncodeToString(random))
	return filepath.Join(service.settings.ProviderApply.CandidateRoot, name), nil
}

func applyResult(receipt providerdeploy.Receipt, revision uint64) provideradmin.ApplyResult {
	result := provideradmin.ApplyResult{SchemaVersion: provideradmin.SchemaVersion, CatalogRevision: revision}
	if receipt.SchemaVersion == 0 {
		result.State = "failed_before_transaction"
		return result
	}
	result.ApplyID, result.State, result.Code = receipt.ApplyID, string(receipt.State), receipt.Code
	result.Added, result.Changed, result.Removed = len(receipt.Plan.Added), len(receipt.Plan.Changed), len(receipt.Plan.Removed)
	return result
}

func providerFailure(status int, code string, cause error) error {
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	return &provideradmin.Error{Status: status, Code: code, Detail: detail, Cause: cause}
}

func providerServiceIdentity(name string) (int, int, error) {
	account, err := user.Lookup(strings.TrimSpace(name))
	if err != nil {
		return 0, 0, errors.New("provider service account was not found")
	}
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	if uidErr != nil || gidErr != nil || uid < 1 || gid < 1 {
		return 0, 0, errors.New("provider service account has an invalid Unix identity")
	}
	return uid, gid, nil
}

func validateProviderApplyRoots(settings config, ownerUID, serviceGID int) error {
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{
		{settings.ProviderApply.CandidateRoot, 0o755},
		{settings.ProviderApply.ReceiptPath, 0o700},
	} {
		info, err := os.Lstat(item.path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != item.mode ||
			unixUID(info) != uint32(ownerUID) {
			return errors.New("provider apply root directory is invalid")
		}
	}
	desiredParent, err := os.Lstat(filepath.Dir(settings.ProviderApply.EgressDesiredPath))
	if err != nil || !desiredParent.IsDir() || desiredParent.Mode()&os.ModeSymlink != 0 ||
		desiredParent.Mode().Perm() != 0o750 || unixUID(desiredParent) != uint32(ownerUID) ||
		unixGID(desiredParent) != uint32(serviceGID) {
		return errors.New("country exit desired directory is invalid")
	}
	desiredInfo, err := os.Lstat(settings.ProviderApply.EgressDesiredPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !desiredInfo.Mode().IsRegular() || desiredInfo.Mode()&os.ModeSymlink != 0 ||
		desiredInfo.Mode().Perm() != 0o640 || unixUID(desiredInfo) != uint32(ownerUID) ||
		unixGID(desiredInfo) != uint32(serviceGID) {
		return errors.New("country exit desired file is invalid")
	}
	return nil
}

func chownProviderCandidate(directory string, manifest providerconfig.Manifest, uid, gid int) error {
	paths := []string{filepath.Join(directory, "manifest.json")}
	for _, entry := range manifest.Providers {
		paths = append(paths, filepath.Join(directory, entry.ConfigFile))
	}
	for _, path := range paths {
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
	}
	return os.Chown(directory, uid, gid)
}

func listenProviderApplySocket(path string, gid int) (net.Listener, error) {
	parentInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm() != 0o750 ||
		unixUID(parentInfo) != 0 || unixGID(parentInfo) != uint32(gid) {
		return nil, errors.New("provider apply socket parent is invalid")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 || unixUID(info) != 0 {
			return nil, errors.New("provider apply socket path is occupied")
		}
		connection, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil, errors.New("provider apply helper is already running")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chown(path, 0, gid); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return listener, nil
}

func unixUID(info os.FileInfo) uint32 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Uid
	}
	return ^uint32(0)
}

func unixGID(info os.FileInfo) uint32 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Gid
	}
	return ^uint32(0)
}
