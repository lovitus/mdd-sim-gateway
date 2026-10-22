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

func createSetupTemp(directory, original string) (*os.File, error) {
	// CreateTemp uses 0600 at creation, before another process can open it.
	return os.CreateTemp(directory, ".mdd-setup-*")
}
