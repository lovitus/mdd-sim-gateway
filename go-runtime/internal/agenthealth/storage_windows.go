//go:build windows

package agenthealth

import "golang.org/x/sys/windows"

func platformDiskUsage(path string) (uint64, uint64, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(name, &available, &total, &free); err != nil {
		return 0, 0, err
	}
	return total, available, nil
}
