//go:build windows

package main

import (
	"errors"
	"os"
)

func validateRootOwner(os.FileInfo) error {
	return errors.New("release installation is unsupported on Windows")
}
