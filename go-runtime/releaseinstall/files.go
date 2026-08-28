package releaseinstall

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

func ensureDirectory(path string, mode os.FileMode, uid, gid, parentUID, parentGID int) error {
	if err := validateDirectory(filepath.Dir(path), parentUID, parentGID, false, 0); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, mode); err != nil {
			return err
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode {
		return errors.New("release installation directory type or mode is invalid")
	}
	actualUID, actualGID, ok := owner(info)
	if !ok || actualUID != uid || actualGID != gid {
		return errors.New("release installation directory owner is invalid")
	}
	return nil
}

func validateDirectory(path string, uid, gid int, exactMode bool, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("release installation parent must be a real directory")
	}
	actualUID, actualGID, ok := owner(info)
	if !ok || actualUID != uid || actualGID != gid || info.Mode().Perm()&0o022 != 0 || (exactMode && info.Mode().Perm() != mode) {
		return errors.New("release installation parent permissions are invalid")
	}
	return nil
}

func currentTarget(link string) (string, error) {
	info, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", errors.New("release current path must be absent or a symbolic link")
	}
	target, err := os.Readlink(link)
	if err != nil || !filepath.IsAbs(target) {
		return "", errors.New("release current link target must be absolute")
	}
	return filepath.Clean(target), nil
}

func ensureLink(link, target string) error {
	info, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		return createLink(link, target)
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return errors.New("release stable path is not an expected symbolic link")
	}
	actual, err := os.Readlink(link)
	if err != nil || actual != target {
		return errors.New("release stable link points outside the managed layout")
	}
	return nil
}

func switchLink(link, target string) error {
	if !filepath.IsAbs(target) {
		return errors.New("release link target must be absolute")
	}
	parent := filepath.Dir(link)
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temporary := filepath.Join(parent, "."+filepath.Base(link)+"-"+hex.EncodeToString(suffix))
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(temporary)
		}
	}()
	if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return errors.New("release current path is not a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporary, link); err != nil {
		return err
	}
	complete = true
	return syncDirectory(parent)
}

func createLink(link, target string) error {
	if !filepath.IsAbs(target) {
		return errors.New("release stable link target must be absolute")
	}
	if err := os.Symlink(target, link); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(link))
}

func restoreLink(link, previous string) error {
	if previous != "" {
		return switchLink(link, previous)
	}
	info, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return errors.New("release current path is not a symbolic link")
	}
	if err := os.Remove(link); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(link))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
