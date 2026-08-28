package providerdeploy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
)

func CurrentTarget(linkPath string) (string, error) {
	linkPath = filepath.Clean(strings.TrimSpace(linkPath))
	if !filepath.IsAbs(linkPath) || linkPath == string(filepath.Separator) {
		return "", errors.New("provider current link must be absolute and scoped")
	}
	info, err := os.Lstat(linkPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", errors.New("provider current path must be absent or a symbolic link")
	}
	target, err := os.Readlink(linkPath)
	if err != nil || !filepath.IsAbs(target) {
		return "", errors.New("provider current link must have an absolute target")
	}
	target = filepath.Clean(target)
	if _, err := providerconfig.LoadDirectory(target); err != nil {
		return "", err
	}
	return target, nil
}

func SwitchLink(linkPath, target string) error {
	linkPath, target = filepath.Clean(strings.TrimSpace(linkPath)), filepath.Clean(strings.TrimSpace(target))
	if !filepath.IsAbs(linkPath) || !filepath.IsAbs(target) || linkPath == string(filepath.Separator) {
		return errors.New("provider link and target must be absolute and scoped")
	}
	if _, err := providerconfig.LoadDirectory(target); err != nil {
		return err
	}
	parent := filepath.Dir(linkPath)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("provider link parent must be a real directory")
	}
	if current, err := os.Lstat(linkPath); err == nil && current.Mode()&os.ModeSymlink == 0 {
		return errors.New("provider current path is not a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temporary := filepath.Join(parent, "."+filepath.Base(linkPath)+"-"+hex.EncodeToString(suffix))
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(temporary)
		}
	}()
	if err := os.Rename(temporary, linkPath); err != nil {
		return err
	}
	complete = true
	return syncDirectory(parent)
}

func RemoveLink(linkPath string) error {
	linkPath = filepath.Clean(strings.TrimSpace(linkPath))
	info, err := os.Lstat(linkPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return errors.New("provider current path is not a symbolic link")
	}
	if err := os.Remove(linkPath); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(linkPath))
}

func ValidateArtifacts(directory, binary string, manifest providerconfig.Manifest, expectedUID, expectedGID int) error {
	if runtime.GOOS == "windows" || expectedUID < 0 || expectedGID < 0 {
		return errors.New("provider deployment artifact validation requires a Unix owner")
	}
	if err := validateOwnedPath(directory, 0o700, true, expectedUID, expectedGID); err != nil {
		return err
	}
	if err := validateOwnedPath(filepath.Join(directory, "manifest.json"), 0o600, false, expectedUID, expectedGID); err != nil {
		return err
	}
	for _, entry := range manifest.Providers {
		if err := validateOwnedPath(filepath.Join(directory, entry.ConfigFile), 0o600, false, expectedUID, expectedGID); err != nil {
			return err
		}
	}
	binary = filepath.Clean(strings.TrimSpace(binary))
	if !filepath.IsAbs(binary) || binary == string(filepath.Separator) {
		return errors.New("provider binary must be absolute and scoped")
	}
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("provider binary must be a non-writable executable regular file")
	}
	binaryUID, _, ok := owner(info)
	if !ok || binaryUID != 0 {
		return errors.New("provider binary must be owned by root")
	}
	return nil
}

func validateOwnedPath(path string, mode os.FileMode, directory bool, uid, gid int) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode || info.IsDir() != directory {
		return errors.New("provider artifact type or permissions are invalid")
	}
	actualUID, actualGID, ok := owner(info)
	if !ok || actualUID != uid || actualGID != gid {
		return errors.New("provider artifact ownership is invalid")
	}
	return nil
}
