//go:build linux

package releaseinstall

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type installLock struct{ file *os.File }

func acquireLock(path string, expectedUID int) (*installLock, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("release install lock could not be opened")
	}
	info, err := file.Stat()
	uid, _, ok := owner(info)
	if err != nil || !info.Mode().IsRegular() || !ok || uid != expectedUID {
		_ = file.Close()
		return nil, errors.New("release install lock owner or type is invalid")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.New("another release installation is running")
		}
		return nil, err
	}
	return &installLock{file: file}, nil
}

func (lock *installLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	return errors.Join(unix.Flock(int(lock.file.Fd()), unix.LOCK_UN), lock.file.Close())
}
