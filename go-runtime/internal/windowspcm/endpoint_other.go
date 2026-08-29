//go:build !windows

package windowspcm

import (
	"errors"
	"io"
)

func Open(string) (io.ReadWriteCloser, error) {
	return nil, errors.New("Windows serial voice PCM is unavailable")
}
