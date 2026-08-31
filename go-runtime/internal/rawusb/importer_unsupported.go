//go:build !linux && !windows

package rawusb

import (
	"context"
	"errors"
	"net"
)

type StreamDial func(context.Context) (net.Conn, error)

type Importer struct{}

func NewImporter(context.Context, Device, StreamDial) (*Importer, error) {
	return nil, errors.New("raw USB passthrough is supported only on Windows and Linux")
}

func NewMultiplexedImporter(context.Context, Device, net.Conn) (*Importer, error) {
	return nil, errors.New("raw USB passthrough is supported only on Windows and Linux")
}

func (importer *Importer) Start() error {
	return errors.New("raw USB passthrough is supported only on Windows and Linux")
}

func (importer *Importer) Close() error { return nil }
