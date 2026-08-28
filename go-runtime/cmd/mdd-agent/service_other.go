//go:build !windows

package main

import (
	"errors"
	"io"
)

func runOSService(string, string, config, io.Writer) error {
	return errors.New("OS service management is available only on Windows; run the Agent host or GUI directly on this platform")
}

func runOSServiceWithExecutable(string, string, string, config, io.Writer) error {
	return errors.New("OS service management is available only on Windows; run the Agent host or GUI directly on this platform")
}
