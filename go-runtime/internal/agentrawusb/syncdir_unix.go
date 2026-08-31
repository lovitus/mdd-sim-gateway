//go:build !windows

package agentrawusb

import "os"

func syncRecoveryParent(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
