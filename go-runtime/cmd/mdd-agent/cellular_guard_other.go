//go:build !linux

package main

import "errors"

func runCellularGuardCommand([]string) error {
	return errors.New("cellular guard is available only on Linux")
}
