//go:build !windows

package providermessages

import (
	"errors"
	"os"
)

func syncMessageParent(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
