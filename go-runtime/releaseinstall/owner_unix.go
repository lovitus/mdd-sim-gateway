//go:build !windows

package releaseinstall

import (
	"os"
	"syscall"
)

func owner(info os.FileInfo) (int, int, bool) {
	status, ok := info.Sys().(*syscall.Stat_t)
	return int(status.Uid), int(status.Gid), ok
}
