//go:build !windows

package main

import (
	"errors"
	"os"
)

func requireProviderApplyPrivileges() error {
	if os.Geteuid() != 0 {
		return errors.New("provider apply requires root")
	}
	return nil
}
