//go:build windows

package main

import "errors"

func runProviderApplyHelper([]string) error {
	return errors.New("provider apply helper is supported only on Unix")
}
