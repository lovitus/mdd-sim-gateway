//go:build !linux

package hostpreflight

// CheckPersistentPath is a no-op outside Linux; the ext4/bbolt issue is
// Linux-specific.
func CheckPersistentPath(string) error { return nil }
