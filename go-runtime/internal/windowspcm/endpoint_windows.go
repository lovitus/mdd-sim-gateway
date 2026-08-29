//go:build windows

package windowspcm

import (
	"io"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowspnp"
	"go.bug.st/serial"
)

func Open(physicalID string) (io.ReadWriteCloser, error) {
	ports, err := windowspnp.Ports()
	if err != nil {
		return nil, err
	}
	endpoint, err := Select(ports, physicalID)
	if err != nil {
		return nil, err
	}
	port, err := serial.Open(endpoint.Name, &serial.Mode{
		BaudRate: 115200, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit,
		InitialStatusBits: &serial.ModemOutputBits{DTR: false, RTS: false},
	})
	if err != nil {
		return nil, err
	}
	if err := port.SetReadTimeout(100 * time.Millisecond); err != nil {
		_ = port.Close()
		return nil, err
	}
	return port, nil
}
