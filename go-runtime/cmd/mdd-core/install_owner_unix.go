//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

func validateRootOwner(info os.FileInfo) error {
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || status.Uid != 0 {
		return errors.New("account tool must be owned by root")
	}
	return nil
}
