//go:build darwin || linux

package main

import (
	"errors"
	"os"
	"syscall"
)

func validateConfigOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("Agent private configuration must be owned by the current user")
	}
	return nil
}
