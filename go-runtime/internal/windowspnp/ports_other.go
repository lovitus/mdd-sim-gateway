//go:build !windows

package windowspnp

import "errors"

func Ports() ([]Port, error) { return nil, errors.New("Windows PnP is unavailable") }
