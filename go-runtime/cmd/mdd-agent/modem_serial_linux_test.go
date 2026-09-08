//go:build linux

package main

import "testing"

func TestSerialModemServiceOwnershipUsesActualStoppedState(t *testing.T) {
	for _, state := range []string{"LoadState=not-found\n", "LoadState=loaded\nActiveState=inactive\nUnitFileState=disabled\n", "LoadState=masked\nActiveState=inactive\nUnitFileState=masked\n"} {
		if err := validateSerialServiceState(state); err != nil {
			t.Fatal("stopped service rejected", err)
		}
	}
	for _, state := range []string{"", "LoadState=loaded\nActiveState=active\nUnitFileState=disabled\n", "LoadState=loaded\nActiveState=inactive\nUnitFileState=enabled\n", "ActiveState=activating\nUnitFileState=masked\n"} {
		if err := validateSerialServiceState(state); err == nil {
			t.Fatal("unconfirmed ownership accepted")
		}
	}
}
