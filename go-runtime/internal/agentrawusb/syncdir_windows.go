//go:build windows

package agentrawusb

// bbolt commits the file durably on Windows; directory handles do not expose
// the Unix directory fsync contract.
func syncRecoveryParent(string) error { return nil }
