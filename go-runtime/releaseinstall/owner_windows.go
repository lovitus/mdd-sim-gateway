//go:build windows

package releaseinstall

import "os"

func owner(os.FileInfo) (int, int, bool) { return 0, 0, false }
