//go:build linux

package providerdeploy

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type ApplyLock struct{ file *os.File }

func AcquireLock(path string) (*ApplyLock, error) {
	path = filepath.Clean(path)
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm() != 0o700 {
		return nil, errors.New("provider apply lock directory must be a real 0700 directory")
	}
	uid, _, ok := owner(parent)
	if !ok || uid != 0 {
		return nil, errors.New("provider apply lock directory must be owned by root")
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("provider apply lock could not be opened")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("provider apply lock must be a regular file")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.New("another provider apply is running")
		}
		return nil, err
	}
	return &ApplyLock{file: file}, nil
}

func (lock *ApplyLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	return errors.Join(err, lock.file.Close())
}
