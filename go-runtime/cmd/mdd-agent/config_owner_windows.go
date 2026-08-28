//go:build windows

package main

import "os"

func validateConfigOwner(os.FileInfo) error { return nil }
