//go:build windows

package providerdeploy

import "os"

func owner(os.FileInfo) (int, int, bool) { return 0, 0, false }
