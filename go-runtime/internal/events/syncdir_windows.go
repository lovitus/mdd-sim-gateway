//go:build windows

package events

// bbolt's file commit is durable on Windows; opening directory handles for an
// additional fsync requires platform-specific privileges and is not needed by
// the server deployment target.
func syncParentDirectory(string) error { return nil }
