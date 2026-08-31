package releaseinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/releasebundle"
)

type removeInspection struct {
	plan         RemovePlan
	current      string
	releases     []string
	stableTarget map[string]string
}

// PreflightRemove validates the complete managed software layout under the
// same install lock used by Activate and Remove. It never changes services or
// files.
func PreflightRemove(layout Layout, rootUID, rootGID int) (RemovePlan, error) {
	if layout.Validate() != nil || rootUID < 0 || rootGID < 0 {
		return RemovePlan{}, errors.New("invalid release removal input")
	}
	if err := validateDirectory(layout.ReceiptDirectory, rootUID, rootGID, true, 0o700); err != nil {
		return RemovePlan{}, err
	}
	lock, err := acquireLock(filepath.Join(layout.ReceiptDirectory, ".install.lock"), rootUID)
	if err != nil {
		return RemovePlan{}, err
	}
	defer lock.Close()
	inspection, err := inspectRemove(layout, rootUID, rootGID)
	return inspection.plan, err
}

// Remove detaches only the strictly verified release-owned software paths.
// Configuration, durable state, the service account and installation receipts
// are deliberately preserved. The caller-provided gate is checked while the
// install lock is held both before and after systemd reloads the detached unit
// paths.
func Remove(ctx context.Context, layout Layout, rootUID, rootGID int, gate RemovalGate, reloader Reloader) (RemovePlan, error) {
	if gate == nil || reloader == nil || layout.Validate() != nil || rootUID < 0 || rootGID < 0 {
		return RemovePlan{}, errors.New("invalid release removal input")
	}
	if err := validateDirectory(layout.ReceiptDirectory, rootUID, rootGID, true, 0o700); err != nil {
		return RemovePlan{}, err
	}
	lock, err := acquireLock(filepath.Join(layout.ReceiptDirectory, ".install.lock"), rootUID)
	if err != nil {
		return RemovePlan{}, err
	}
	defer lock.Close()
	return removeLocked(ctx, layout, rootUID, rootGID, gate, reloader)
}

func removeLocked(ctx context.Context, layout Layout, rootUID, rootGID int, gate RemovalGate, reloader Reloader) (RemovePlan, error) {
	inspection, err := inspectRemoveAfterDisable(layout, rootUID, rootGID)
	if err != nil {
		return RemovePlan{}, err
	}
	if err := gate.VerifyInactiveDisabled(ctx); err != nil {
		return inspection.plan, err
	}
	if err := detachAndReload(ctx, layout, inspection, gate, reloader); err != nil {
		return inspection.plan, err
	}
	if err := deleteVerifiedReleases(layout, inspection); err != nil {
		return inspection.plan, err
	}
	return inspection.plan, nil
}

func inspectRemove(layout Layout, rootUID, rootGID int) (removeInspection, error) {
	return inspectRemoveWithPolicy(layout, rootUID, rootGID, false)
}

// systemctl disable removes linked unit files as well as their enablement
// links. The shell entrypoint therefore performs a strict preflight first,
// disables every managed unit, and only then enters Remove. At that second
// inspection the known MDD unit links may be absent; release contents,
// libexec links, current, receipts and every other ownership boundary remain
// strict.
func inspectRemoveAfterDisable(layout Layout, rootUID, rootGID int) (removeInspection, error) {
	return inspectRemoveWithPolicy(layout, rootUID, rootGID, true)
}

func inspectRemoveWithPolicy(layout Layout, rootUID, rootGID int, allowDisabledUnitLinks bool) (removeInspection, error) {
	empty := removeInspection{}
	softwareRoot := filepath.Dir(layout.ReleasesDirectory)
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{
		{softwareRoot, 0o755},
		{layout.ReleasesDirectory, 0o755},
		{layout.LibexecDirectory, 0o755},
		{layout.ReceiptDirectory, 0o700},
	} {
		if err := validateDirectory(item.path, rootUID, rootGID, true, item.mode); err != nil {
			return empty, err
		}
	}
	if err := validateDirectory(layout.UnitDirectory, rootUID, rootGID, false, 0); err != nil {
		return empty, err
	}
	receiptPath := filepath.Join(layout.ReceiptDirectory, "current.json")
	if err := validateOwnedRegular(receiptPath, rootUID, rootGID, 0o600); err != nil {
		return empty, errors.New("release removal receipt is not strictly owned")
	}
	receipt, err := readCurrentReceipt(layout.ReceiptDirectory)
	if err != nil || (receipt.State != StateApplied && receipt.State != StateRolledBack) {
		return empty, ErrIncompleteInstall
	}
	current, err := currentTarget(layout.CurrentLink)
	if err != nil || !directReleaseChild(layout.ReleasesDirectory, current) {
		return empty, errors.New("release current target is not a managed release")
	}
	if err := validateOwnedSymlink(layout.CurrentLink, current, rootUID, rootGID); err != nil {
		return empty, err
	}
	if err := validateReceiptTarget(receipt, current, layout.ReleasesDirectory); err != nil {
		return empty, err
	}

	entries, err := os.ReadDir(layout.ReleasesDirectory)
	if err != nil || len(entries) == 0 {
		return empty, errors.New("release directory is empty or unreadable")
	}
	releases := make([]string, 0, len(entries))
	releaseIDs := make([]string, 0, len(entries))
	var currentManifest releasebundle.Manifest
	for _, entry := range entries {
		path := filepath.Join(layout.ReleasesDirectory, entry.Name())
		if entry.Name() == "." || entry.Name() == ".." || !directReleaseChild(layout.ReleasesDirectory, path) {
			return empty, errors.New("release directory contains an invalid entry")
		}
		manifest, loadErr := releasebundle.LoadDirectory(path)
		if loadErr != nil || manifest.ReleaseID != entry.Name() {
			return empty, errors.New("installed release is not a strict manifest directory")
		}
		if err := validateReleaseOwnership(path, manifest, rootUID, rootGID); err != nil {
			return empty, err
		}
		if path == current {
			currentManifest = manifest
		}
		releases = append(releases, path)
		releaseIDs = append(releaseIDs, manifest.ReleaseID)
	}
	if currentManifest.SchemaVersion == 0 {
		return empty, errors.New("current release is absent from the release directory")
	}
	sort.Strings(releases)
	sort.Strings(releaseIDs)
	stable := expectedStableLinks(layout, currentManifest)
	if err := validateStableNamespace(layout, stable, rootUID, rootGID, allowDisabledUnitLinks); err != nil {
		return empty, err
	}
	if err := validateSoftwareRoot(layout); err != nil {
		return empty, err
	}
	stablePaths := make([]string, 0, len(stable))
	for path := range stable {
		stablePaths = append(stablePaths, path)
	}
	sort.Strings(stablePaths)
	return removeInspection{
		plan: RemovePlan{
			SchemaVersion: 1, CurrentRelease: currentManifest.ReleaseID,
			ReleaseIDs: releaseIDs, StableLinks: stablePaths,
		},
		current: current, releases: releases, stableTarget: stable,
	}, nil
}

func validateReceiptTarget(receipt Receipt, current, releasesDirectory string) error {
	if !directReleaseChild(releasesDirectory, receipt.CandidateTarget) || filepath.Base(receipt.CandidateTarget) != receipt.ReleaseID {
		return errors.New("release receipt candidate is outside the managed layout")
	}
	switch receipt.State {
	case StateApplied:
		if receipt.CandidateTarget != current {
			return errors.New("applied release receipt does not match current")
		}
	case StateRolledBack:
		if !directReleaseChild(releasesDirectory, receipt.PreviousTarget) || receipt.PreviousTarget != current {
			return errors.New("rolled-back release receipt does not match current")
		}
	default:
		return ErrIncompleteInstall
	}
	return nil
}

func validateReleaseOwnership(path string, manifest releasebundle.Manifest, uid, gid int) error {
	if err := validateDirectory(path, uid, gid, true, 0o755); err != nil {
		return errors.New("installed release directory ownership is invalid")
	}
	if err := validateOwnedRegular(filepath.Join(path, "manifest.json"), uid, gid, 0o644); err != nil {
		return errors.New("installed release manifest ownership is invalid")
	}
	for _, artifact := range manifest.Artifacts {
		mode := os.FileMode(0o644)
		if artifact.Mode == "0755" {
			mode = 0o755
		}
		if err := validateOwnedRegular(filepath.Join(path, artifact.Name), uid, gid, mode); err != nil {
			return errors.New("installed release artifact ownership is invalid")
		}
	}
	return nil
}

func validateOwnedRegular(path string, uid, gid int, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode {
		return errors.New("managed path is not an expected regular file")
	}
	actualUID, actualGID, ok := owner(info)
	if !ok || actualUID != uid || actualGID != gid {
		return errors.New("managed path ownership is invalid")
	}
	return nil
}

func validateOwnedSymlink(path, target string, uid, gid int) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return errors.New("managed path is not an expected symbolic link")
	}
	actualUID, actualGID, ok := owner(info)
	if !ok || actualUID != uid || actualGID != gid {
		return errors.New("managed link ownership is invalid")
	}
	actual, err := os.Readlink(path)
	if err != nil || actual != target {
		return errors.New("managed link target is invalid")
	}
	return nil
}

func expectedStableLinks(layout Layout, manifest releasebundle.Manifest) map[string]string {
	links := map[string]string{
		filepath.Join(layout.LibexecDirectory, "mdd-core"):         filepath.Join(layout.CurrentLink, "mdd-core"),
		filepath.Join(layout.LibexecDirectory, "mdd-vowifi"):       filepath.Join(layout.CurrentLink, "mdd-vowifi"),
		filepath.Join(layout.UnitDirectory, "mdd-core.service"):    filepath.Join(layout.CurrentLink, "mdd-core.service"),
		filepath.Join(layout.UnitDirectory, "mdd-vowifi@.service"): filepath.Join(layout.CurrentLink, "mdd-vowifi@.service"),
	}
	for role, item := range map[string][2]string{
		releasebundle.RoleAgent:      {layout.LibexecDirectory, "mdd-agent"},
		releasebundle.RoleAgentAudio: {layout.LibexecDirectory, "mdd-call-audio-helper"},
		releasebundle.RoleAgentUnit:  {layout.UnitDirectory, "mdd-agent.service"},
		releasebundle.RoleGuardUnit:  {layout.UnitDirectory, "mdd-cellular-guard.service"},
		releasebundle.RoleApplyUnit:  {layout.UnitDirectory, "mdd-provider-apply.service"},
		releasebundle.RoleEgressUnit: {layout.UnitDirectory, "mdd-egress.service"},
	} {
		if artifact, found := manifest.Artifact(role); found {
			links[filepath.Join(item[0], item[1])] = filepath.Join(layout.CurrentLink, artifact.Name)
		}
	}
	return links
}

func validateStableNamespace(layout Layout, expected map[string]string, uid, gid int, allowDisabledUnitLinks bool) error {
	known := []string{
		filepath.Join(layout.LibexecDirectory, "mdd-core"),
		filepath.Join(layout.LibexecDirectory, "mdd-vowifi"),
		filepath.Join(layout.LibexecDirectory, "mdd-agent"),
		filepath.Join(layout.LibexecDirectory, "mdd-call-audio-helper"),
		filepath.Join(layout.UnitDirectory, "mdd-core.service"),
		filepath.Join(layout.UnitDirectory, "mdd-vowifi@.service"),
		filepath.Join(layout.UnitDirectory, "mdd-agent.service"),
		filepath.Join(layout.UnitDirectory, "mdd-cellular-guard.service"),
		filepath.Join(layout.UnitDirectory, "mdd-provider-apply.service"),
		filepath.Join(layout.UnitDirectory, "mdd-egress.service"),
	}
	for _, path := range known {
		target, wanted := expected[path]
		_, err := os.Lstat(path)
		if !wanted {
			if err == nil || !errors.Is(err, os.ErrNotExist) {
				return errors.New("unexpected managed stable path is present")
			}
			continue
		}
		if allowDisabledUnitLinks && filepath.Dir(path) == layout.UnitDirectory && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := validateOwnedSymlink(path, target, uid, gid); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(layout.LibexecDirectory)
	if err != nil || len(entries) != countInDirectory(expected, layout.LibexecDirectory) {
		return errors.New("libexec directory contains unmanaged entries")
	}
	for _, entry := range entries {
		if _, found := expected[filepath.Join(layout.LibexecDirectory, entry.Name())]; !found {
			return errors.New("libexec directory contains an unmanaged entry")
		}
	}
	return nil
}

func validateSoftwareRoot(layout Layout) error {
	root := filepath.Dir(layout.ReleasesDirectory)
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		return errors.New("release software root contains unmanaged entries")
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if path != layout.ReleasesDirectory && path != layout.CurrentLink {
			return errors.New("release software root contains an unmanaged entry")
		}
	}
	return nil
}

func countInDirectory(paths map[string]string, directory string) int {
	count := 0
	for path := range paths {
		if filepath.Dir(path) == directory {
			count++
		}
	}
	return count
}

func directReleaseChild(directory, path string) bool {
	return path != "" && filepath.Clean(path) == path && filepath.Dir(path) == directory && filepath.Base(path) != "."
}

func detachAndReload(ctx context.Context, layout Layout, inspection removeInspection, gate RemovalGate, reloader Reloader) error {
	return detachAndReloadWithRemove(ctx, layout, inspection, gate, reloader, os.Remove)
}

func detachAndReloadWithRemove(ctx context.Context, layout Layout, inspection removeInspection, gate RemovalGate, reloader Reloader, remove func(string) error) error {
	if remove == nil {
		return errors.New("release link remover is required")
	}
	if err := removeExpectedLinks(layout, inspection, remove); err != nil {
		return errors.Join(err, restoreExpectedLinks(layout, inspection))
	}
	if err := reloader.DaemonReload(); err != nil {
		restoreErr := restoreExpectedLinks(layout, inspection)
		reloadErr := reloader.DaemonReload()
		return errors.Join(err, restoreErr, reloadErr)
	}
	if err := gate.VerifyInactiveDisabled(ctx); err != nil {
		restoreErr := restoreExpectedLinks(layout, inspection)
		reloadErr := reloader.DaemonReload()
		return errors.Join(err, restoreErr, reloadErr)
	}
	return nil
}

func removeExpectedLinks(layout Layout, inspection removeInspection, remove func(string) error) error {
	paths := append([]string(nil), inspection.plan.StableLinks...)
	paths = append(paths, layout.CurrentLink)
	for _, path := range paths {
		if err := remove(path); err != nil {
			if filepath.Dir(path) == layout.UnitDirectory && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	}
	return nil
}

func restoreExpectedLinks(layout Layout, inspection removeInspection) error {
	var failures []error
	failures = appendIfError(failures, ensureRestoredLink(layout.CurrentLink, inspection.current))
	for _, path := range inspection.plan.StableLinks {
		failures = appendIfError(failures, ensureRestoredLink(path, inspection.stableTarget[path]))
	}
	return errors.Join(failures...)
}

func ensureRestoredLink(path, target string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return createLink(path, target)
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return errors.New("cannot restore managed link over an unexpected path")
	}
	actual, err := os.Readlink(path)
	if err != nil || actual != target {
		return errors.New("restored managed link target changed")
	}
	return nil
}

func appendIfError(values []error, err error) []error {
	if err != nil {
		return append(values, err)
	}
	return values
}

func deleteVerifiedReleases(layout Layout, inspection removeInspection) error {
	for _, path := range inspection.releases {
		if !directReleaseChild(layout.ReleasesDirectory, path) {
			return errors.New("refusing to remove release outside the managed directory")
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	if err := syncDirectory(layout.ReleasesDirectory); err != nil {
		return err
	}
	for _, path := range []string{layout.ReleasesDirectory, filepath.Dir(layout.ReleasesDirectory), layout.LibexecDirectory} {
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	}
	return nil
}
