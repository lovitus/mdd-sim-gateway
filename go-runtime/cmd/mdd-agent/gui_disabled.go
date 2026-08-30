//go:build !gui

package main

import "errors"

func defaultLaunchCommand() string { return "" }

func runGUI(config, string) error {
	return errors.New("this executable was built without GUI support")
}
