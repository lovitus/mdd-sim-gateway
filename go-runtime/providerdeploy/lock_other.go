//go:build !linux

package providerdeploy

import "errors"

type ApplyLock struct{}

func AcquireLock(string) (*ApplyLock, error) {
	return nil, errors.New("provider apply locking is supported only on Linux")
}

func (*ApplyLock) Close() error { return nil }
