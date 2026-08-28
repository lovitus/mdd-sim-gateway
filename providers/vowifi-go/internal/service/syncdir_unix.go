//go:build !windows

// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"errors"
	"os"
)

func syncParentDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
