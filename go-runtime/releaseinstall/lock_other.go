//go:build !linux

package releaseinstall

import "errors"

type ExecutionLock struct{}

func AcquireExecutionLock(string, int) (*ExecutionLock, error) {
	return nil, errors.New("release installation is supported only on Linux")
}
func (*ExecutionLock) Close() error { return nil }
