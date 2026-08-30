//go:build windows

package main

import (
	"errors"
	"io"
)

func runBootstrapHost([]string, io.Reader, io.Writer) error {
	return errors.New("host bootstrap is supported only on Linux")
}
