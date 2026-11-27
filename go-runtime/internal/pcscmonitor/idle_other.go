//go:build !windows

package pcscmonitor

func allowIdleNoReaders(error) bool {
	return false
}
