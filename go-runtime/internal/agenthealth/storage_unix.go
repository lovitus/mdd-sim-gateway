//go:build darwin || linux

package agenthealth

import (
	"errors"

	"golang.org/x/sys/unix"
)

func platformDiskUsage(path string) (uint64, uint64, error) {
	var status unix.Statfs_t
	if err := unix.Statfs(path, &status); err != nil {
		return 0, 0, err
	}
	if status.Bsize <= 0 {
		return 0, 0, errors.New("filesystem block size is invalid")
	}
	blockSize := uint64(status.Bsize)
	if uint64(status.Blocks) > ^uint64(0)/blockSize || uint64(status.Bavail) > ^uint64(0)/blockSize {
		return 0, 0, errors.New("filesystem capacity overflows uint64")
	}
	return uint64(status.Blocks) * blockSize, uint64(status.Bavail) * blockSize, nil
}
