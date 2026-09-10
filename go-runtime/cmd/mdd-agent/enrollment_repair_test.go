package main

import (
	"io"
	"path/filepath"
	"testing"
)

func TestEnrollmentRepairRequiresExplicitScopedConfirmation(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"-equipment", "867530900000002", "-card", "8944100000000000001", "-first-seen", "2026-09-10T11:00:00Z"},
		{"-confirm", "-equipment", "867530900000002", "-card", "8944100000000000001", "-first-seen", "invalid"},
		{"-confirm", "-store", filepath.Join(t.TempDir(), "missing.db"), "-equipment", "867530900000002", "-card", "8944100000000000001", "-first-seen", "2026-09-10T11:00:00Z"},
	} {
		if err := runEnrollmentRepair(args, io.Discard); err == nil {
			t.Fatal("incomplete repair request accepted")
		}
	}
}
