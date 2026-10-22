//go:build !windows

package main

import (
	"golang.org/x/sys/unix"
	"os"
)

func protectSetupPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if info.IsDir() {
		mode = 0700
	}
	return os.Chmod(path, mode)
}

func lockSetupFile(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) }

func protectEditedConfig(path, original string) error { return protectSetupPath(path) }
