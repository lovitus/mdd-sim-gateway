//go:build windows

package main

import "errors"

func requireProviderApplyPrivileges() error {
	return errors.New("provider apply is supported only on Linux")
}
