package releaseinstall

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/hostpreflight"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/releasebundle"
)

type Identity struct {
	RootUID, RootGID       int
	ServiceUID, ServiceGID int
}

func Activate(ctx context.Context, source string, layout Layout, identity Identity, reloader Reloader) (Receipt, error) {
	if reloader == nil || layout.Validate() != nil || identity.RootUID < 0 || identity.RootGID < 0 ||
		identity.ServiceUID < 1 || identity.ServiceGID < 1 {
		return Receipt{}, errors.New("invalid release installation input")
	}
	source = filepath.Clean(strings.TrimSpace(source))
	manifest, err := Preflight(source, layout)
	if err != nil {
		return Receipt{}, err
	}
	if err := prepareLayout(layout, identity); err != nil {
		return Receipt{}, err
	}
	lock, err := acquireLock(filepath.Join(layout.ReceiptDirectory, ".install.lock"), identity.RootUID)
	if err != nil {
		return Receipt{}, err
	}
	defer lock.Close()
	return activateLocked(ctx, source, manifest, layout, identity, reloader)
}

// Preflight verifies the immutable release and the host persistence boundary
// without creating an account, file, directory, symlink, or systemd state.
func Preflight(source string, layout Layout) (releasebundle.Manifest, error) {
	if layout.Validate() != nil {
		return releasebundle.Manifest{}, errors.New("invalid release installation layout")
	}
	source = filepath.Clean(strings.TrimSpace(source))
	manifest, err := releasebundle.LoadDirectory(source)
	if err != nil {
		return releasebundle.Manifest{}, err
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != manifest.Architecture {
		return releasebundle.Manifest{}, errors.New("release target does not match this Linux host")
	}
	if err := hostpreflight.CheckPersistentPath(layout.StateDirectory); err != nil {
		return releasebundle.Manifest{}, err
	}
	return manifest, nil
}

func activateLocked(ctx context.Context, source string, manifest releasebundle.Manifest, layout Layout, identity Identity, reloader Reloader) (Receipt, error) {
	target := filepath.Join(layout.ReleasesDirectory, manifest.ReleaseID)
	if err := stageRelease(source, target, manifest, identity.RootUID, identity.RootGID); err != nil {
		return Receipt{}, err
	}
	if err := ensureStableLinks(layout, manifest); err != nil {
		return Receipt{}, err
	}
	previous, err := currentTarget(layout.CurrentLink)
	if err != nil {
		return Receipt{}, err
	}
	journal, err := openJournal(layout.ReceiptDirectory, manifest.ReleaseID, previous, target)
	if err != nil {
		return Receipt{}, err
	}
	if previous != target {
		if err := switchLink(layout.CurrentLink, target); err != nil {
			return finish(journal, StateRolledBack, "switch_failed", err)
		}
	}
	if err := reload(ctx, reloader); err != nil {
		rollbackErr := restoreLink(layout.CurrentLink, previous)
		if rollbackErr == nil {
			rollbackErr = reload(ctx, reloader)
		}
		if rollbackErr != nil {
			return finish(journal, StateManualRecovery, "reload_rollback_failed", errors.Join(err, rollbackErr))
		}
		return finish(journal, StateRolledBack, "daemon_reload_failed", err)
	}
	return finish(journal, StateApplied, "installed", nil)
}

func Recover(ctx context.Context, layout Layout, rootUID int, reloader Reloader) (Receipt, error) {
	if reloader == nil || layout.Validate() != nil || rootUID < 0 {
		return Receipt{}, errors.New("invalid release recovery input")
	}
	lock, err := acquireLock(filepath.Join(layout.ReceiptDirectory, ".install.lock"), rootUID)
	if err != nil {
		return Receipt{}, err
	}
	defer lock.Close()
	return recoverLocked(ctx, layout, reloader)
}

func recoverLocked(ctx context.Context, layout Layout, reloader Reloader) (Receipt, error) {
	receipt, err := readCurrentReceipt(layout.ReceiptDirectory)
	if err != nil || (receipt.State != StateApplying && receipt.State != StateManualRecovery) {
		return Receipt{}, ErrIncompleteInstall
	}
	current, err := currentTarget(layout.CurrentLink)
	if err != nil || (current != receipt.CandidateTarget && current != receipt.PreviousTarget) {
		return receipt, errors.New("release link no longer matches incomplete receipt")
	}
	if err := restoreLink(layout.CurrentLink, receipt.PreviousTarget); err != nil {
		return receipt, err
	}
	if err := reload(ctx, reloader); err != nil {
		return receipt, err
	}
	instance := &journal{directory: layout.ReceiptDirectory, current: filepath.Join(layout.ReceiptDirectory, "current.json"), receipt: receipt}
	return finish(instance, StateRolledBack, "recovered_previous_release", nil)
}

func finish(instance *journal, state ReceiptState, code string, cause error) (Receipt, error) {
	finishErr := instance.finish(state, code)
	return instance.receipt, errors.Join(cause, finishErr)
}

func reload(ctx context.Context, reloader Reloader) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return reloader.DaemonReload()
	}
}

func prepareLayout(layout Layout, identity Identity) error {
	root := [2]int{identity.RootUID, identity.RootGID}
	service := [2]int{identity.ServiceUID, identity.ServiceGID}
	rootServiceGroup := [2]int{identity.RootUID, identity.ServiceGID}
	egressConfigDirectory := filepath.Join(filepath.Dir(layout.SystemState), "mdd-egress-config")
	for _, path := range []string{filepath.Dir(filepath.Dir(layout.ReleasesDirectory)), filepath.Dir(layout.UnitDirectory), filepath.Dir(layout.ConfigDirectory), filepath.Dir(layout.StateDirectory)} {
		if err := validateDirectory(path, root[0], root[1], false, 0); err != nil {
			return err
		}
	}
	ordered := []struct {
		path        string
		mode        os.FileMode
		owner       [2]int
		parentOwner [2]int
	}{
		{filepath.Dir(layout.ReleasesDirectory), 0o755, root, root},
		{layout.ReleasesDirectory, 0o755, root, root},
		{layout.LibexecDirectory, 0o755, root, root},
		// Configuration is installed by root and read by the service.  Keeping
		// the directory root-owned also lets an existing /etc/mdd namespace
		// coexist without granting the daemon permission to replace its config.
		{layout.ConfigDirectory, 0o755, root, root},
		{layout.StateDirectory, 0o700, service, root},
		{layout.ProviderState, 0o700, service, service},
		// The root apply helper publishes 0640 desired state here. The egress
		// executor can traverse and read it but cannot replace the document.
		{egressConfigDirectory, 0o750, rootServiceGroup, root},
		{layout.SystemState, 0o700, root, root},
		{layout.ReceiptDirectory, 0o700, root, root},
	}
	for _, item := range ordered {
		if err := ensureDirectory(item.path, item.mode, item.owner[0], item.owner[1], item.parentOwner[0], item.parentOwner[1]); err != nil {
			return err
		}
	}
	return validateDirectory(layout.UnitDirectory, root[0], root[1], false, 0)
}

func stageRelease(source, target string, manifest releasebundle.Manifest, uid, gid int) error {
	if existing, err := os.Lstat(target); err == nil {
		if !existing.IsDir() || existing.Mode()&os.ModeSymlink != 0 {
			return errors.New("installed release target is not a real directory")
		}
		loaded, err := releasebundle.LoadDirectory(target)
		if err != nil || !reflect.DeepEqual(loaded, manifest) {
			return errors.New("installed release ID has different contents")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(target), ".release-*")
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := os.Chmod(staging, 0o755); err != nil {
		return err
	}
	for _, artifact := range manifest.Artifacts {
		mode := os.FileMode(0o644)
		if artifact.Mode == "0755" {
			mode = 0o755
		}
		if err := copyOwned(filepath.Join(source, artifact.Name), filepath.Join(staging, artifact.Name), mode, uid, gid); err != nil {
			return err
		}
	}
	manifestPayload, err := os.ReadFile(filepath.Join(source, "manifest.json"))
	if err != nil {
		return err
	}
	if err := writeOwned(filepath.Join(staging, "manifest.json"), manifestPayload, 0o644, uid, gid); err != nil {
		return err
	}
	if err := os.Chown(staging, uid, gid); err != nil {
		return err
	}
	if _, err := releasebundle.LoadDirectory(staging); err != nil {
		return err
	}
	if err := syncDirectory(staging); err != nil {
		return err
	}
	if err := os.Rename(staging, target); err != nil {
		return err
	}
	complete = true
	return syncDirectory(filepath.Dir(target))
}

func ensureStableLinks(layout Layout, manifest releasebundle.Manifest) error {
	links := map[string]string{
		filepath.Join(layout.LibexecDirectory, "mdd-core"):         filepath.Join(layout.CurrentLink, "mdd-core"),
		filepath.Join(layout.LibexecDirectory, "mdd-vowifi"):       filepath.Join(layout.CurrentLink, "mdd-vowifi"),
		filepath.Join(layout.UnitDirectory, "mdd-core.service"):    filepath.Join(layout.CurrentLink, "mdd-core.service"),
		filepath.Join(layout.UnitDirectory, "mdd-vowifi@.service"): filepath.Join(layout.CurrentLink, "mdd-vowifi@.service"),
	}
	if _, found := manifest.Artifact(releasebundle.RoleApplyUnit); found {
		links[filepath.Join(layout.UnitDirectory, "mdd-provider-apply.service")] = filepath.Join(layout.CurrentLink, "mdd-provider-apply.service")
	}
	if _, found := manifest.Artifact(releasebundle.RoleEgressUnit); found {
		links[filepath.Join(layout.UnitDirectory, "mdd-egress.service")] = filepath.Join(layout.CurrentLink, "mdd-egress.service")
	}
	if _, found := manifest.Artifact(releasebundle.RoleAgent); found {
		links[filepath.Join(layout.LibexecDirectory, "mdd-agent")] = filepath.Join(layout.CurrentLink, "mdd-agent")
	}
	if _, found := manifest.Artifact(releasebundle.RoleAgentUnit); found {
		links[filepath.Join(layout.UnitDirectory, "mdd-agent.service")] = filepath.Join(layout.CurrentLink, "mdd-agent.service")
	}
	for link, target := range links {
		if err := ensureLink(link, target); err != nil {
			return err
		}
	}
	return nil
}

func copyOwned(source, destination string, mode os.FileMode, uid, gid int) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		_ = input.Close()
		return err
	}
	_, copyErr := io.Copy(output, input)
	ownerErr := os.Chown(destination, uid, gid)
	return errors.Join(copyErr, ownerErr, input.Close(), output.Sync(), output.Close())
}

func writeOwned(path string, payload []byte, mode os.FileMode, uid, gid int) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(payload)
	ownerErr := os.Chown(path, uid, gid)
	return errors.Join(writeErr, ownerErr, file.Sync(), file.Close())
}
