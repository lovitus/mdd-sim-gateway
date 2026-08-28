//go:build !windows

package providerdeploy

import (
	"os"
	"syscall"
)

func owner(info os.FileInfo) (int, int, bool) {
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(status.Uid), int(status.Gid), true
}
