//go:build gui && !darwin && !windows

package main

import "errors"

func defaultLaunchCommand() string { return "gui" }

func runGUI(config, string) error {
	return errors.New("the MDD Agent GUI currently supports Windows and macOS")
}
