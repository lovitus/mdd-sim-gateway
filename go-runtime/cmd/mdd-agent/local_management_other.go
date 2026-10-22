//go:build !windows

package main

import (
	"errors"
	"io"
)

func runServiceAccess([]string, io.Writer) error {
	return errors.New("service-access is only available for Windows services")
}
