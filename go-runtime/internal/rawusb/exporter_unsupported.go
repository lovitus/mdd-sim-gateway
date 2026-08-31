//go:build !linux && !windows

package rawusb

import (
	"context"
	"errors"
	"net"
)

type Device struct {
	StableID  string
	BusID     string
	VendorID  uint16
	ProductID uint16
	Serial    string
}

type Exporter struct{}

func NewExporter(context.Context, string) (*Exporter, error) {
	return nil, errors.New("raw USB passthrough is supported only on Windows and Linux")
}

func (exporter *Exporter) Device() Device { return Device{} }

func (exporter *Exporter) AcceptStream(context.Context, net.Conn) error {
	return errors.New("raw USB passthrough is supported only on Windows and Linux")
}

func (exporter *Exporter) ServeMultiplexed(context.Context, net.Conn) error {
	return errors.New("raw USB passthrough is supported only on Windows and Linux")
}

func (exporter *Exporter) Close() error { return nil }
