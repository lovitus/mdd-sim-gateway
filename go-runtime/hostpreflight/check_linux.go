//go:build linux

package hostpreflight

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const ext4FilesystemMagic = 0xef53

// CheckPersistentPath checks the filesystem that contains path (or its
// nearest existing ancestor). It does not create files or directories.
func CheckPersistentPath(path string) error {
	existing, err := existingAncestor(path)
	if err != nil {
		return err
	}
	var filesystem unix.Statfs_t
	if err := unix.Statfs(existing, &filesystem); err != nil {
		return fmt.Errorf("inspect persistent filesystem: %w", err)
	}
	if uint64(filesystem.Type) != ext4FilesystemMagic {
		return nil
	}
	release, err := kernelRelease()
	if err != nil {
		return err
	}
	version, err := parseKernelVersion(release)
	if err != nil || !needsFastCommitFeatureCheck(version) {
		return err
	}
	enabled, err := mountedExt4FastCommit(existing)
	if err != nil {
		return fmt.Errorf("verify ext4 fast_commit safety: %w", err)
	}
	if enabled {
		return fmt.Errorf("ext4 fast_commit is unsafe for bbolt on Linux kernel %s", release)
	}
	return nil
}

func existingAncestor(path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return "", errors.New("persistent path must be absolute and scoped")
	}
	for {
		if _, err := os.Stat(path); err == nil {
			return filepath.EvalSymlinks(path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", errors.New("persistent path has no existing ancestor")
		}
		path = parent
	}
}

func kernelRelease() (string, error) {
	var name unix.Utsname
	if err := unix.Uname(&name); err != nil {
		return "", fmt.Errorf("read Linux kernel release: %w", err)
	}
	buffer := make([]byte, 0, len(name.Release))
	for _, character := range name.Release {
		if character == 0 {
			break
		}
		buffer = append(buffer, byte(character))
	}
	if len(buffer) == 0 {
		return "", errors.New("Linux kernel release is empty")
	}
	return string(buffer), nil
}

func mountedExt4FastCommit(path string) (bool, error) {
	var status unix.Stat_t
	if err := unix.Stat(path, &status); err != nil {
		return false, err
	}
	ueventPath := fmt.Sprintf("/sys/dev/block/%d:%d/uevent", unix.Major(uint64(status.Dev)), unix.Minor(uint64(status.Dev)))
	payload, err := os.ReadFile(ueventPath)
	if err != nil {
		return false, err
	}
	deviceName := ""
	for _, line := range strings.Split(string(payload), "\n") {
		if strings.HasPrefix(line, "DEVNAME=") {
			deviceName = strings.TrimSpace(strings.TrimPrefix(line, "DEVNAME="))
			break
		}
	}
	devicePath := filepath.Clean(filepath.Join("/dev", deviceName))
	if deviceName == "" || !strings.HasPrefix(devicePath, "/dev/") || strings.Contains(deviceName, "..") {
		return false, errors.New("mounted ext4 block device is invalid")
	}
	device, err := os.Open(devicePath)
	if err != nil {
		return false, err
	}
	defer device.Close()
	superblock := make([]byte, ext4SuperblockSize)
	read, err := device.ReadAt(superblock, 1024)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if read != len(superblock) {
		return false, io.ErrUnexpectedEOF
	}
	return ext4FastCommitEnabled(superblock)
}
