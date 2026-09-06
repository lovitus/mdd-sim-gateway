//go:build !windows

package windowspnp

import "context"

func ResolveRestartTarget(context.Context, string) (string, error) { return "", ErrRestartUnavailable }
func RestartDevice(context.Context, string) error                  { return ErrRestartUnavailable }
