//go:build !linux

package releaseinstall

import "errors"

type installLock struct{}

func acquireLock(string, int) (*installLock, error) {
	return nil, errors.New("release installation is supported only on Linux")
}
func (*installLock) Close() error { return nil }
