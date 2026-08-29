//go:build windows && (amd64 || arm64)

// Package windowsat connects the platform-neutral AT owner to Windows serial
// enumeration and exclusive COM handles.
package windowsat

import (
	"errors"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowspnp"
	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"
	"golang.org/x/sys/windows"
)

func NewManager(simAPDU bool) (*agentat.Manager, error) {
	return agentat.NewManagerWithSIMAPDU(enumerate, open, simAPDU)
}

func enumerate() ([]agentat.Candidate, error) {
	physical := map[string]windowspnp.Port{}
	pnpPorts, pnpErr := windowspnp.Ports()
	for _, port := range pnpPorts {
		physical[strings.ToUpper(port.Name)] = port
	}
	// No active USB-probe filter is supplied. Since v1.8.0 this reads SetupAPI
	// metadata only and does not issue USB descriptor requests to live modems.
	ports, detailedErr := enumerator.GetDetailedPortsList()
	result := make([]agentat.Candidate, 0, len(ports))
	seen := make(map[string]struct{}, len(ports))
	for _, port := range ports {
		if port == nil {
			continue
		}
		candidate := agentat.Candidate{Name: port.Name, Product: port.Product, USB: port.IsUSB}
		if fact, ok := physical[strings.ToUpper(port.Name)]; ok {
			candidate.Product, candidate.USB, candidate.PhysicalID = fact.Product, fact.USB, fact.PhysicalID
		}
		result = append(result, candidate)
		seen[port.Name] = struct{}{}
	}
	// Some vendor modem drivers publish a COM name in SERIALCOMM while their
	// devnode is missing from the detailed Ports-class walk. Merge the library's
	// passive registry list so those functions are still discoverable; detailed
	// metadata remains authoritative whenever both paths contain the port.
	plain, plainErr := serial.GetPortsList()
	for _, name := range plain {
		if _, exists := seen[name]; exists {
			continue
		}
		candidate := agentat.Candidate{Name: name}
		if fact, ok := physical[strings.ToUpper(name)]; ok {
			candidate.Product, candidate.USB, candidate.PhysicalID = fact.Product, fact.USB, fact.PhysicalID
		}
		result = append(result, candidate)
		seen[name] = struct{}{}
	}
	if detailedErr != nil && plainErr != nil {
		return nil, errors.Join(detailedErr, plainErr, pnpErr)
	}
	return result, nil
}

func open(candidate agentat.Candidate) (agentat.Port, error) {
	port, err := serial.Open(candidate.Name, &serial.Mode{
		BaudRate: 115200, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit,
		InitialStatusBits: &serial.ModemOutputBits{DTR: false, RTS: false},
	})
	if err != nil {
		return nil, serialOpenError{err: err, busy: portBusy(err)}
	}
	if err := port.SetReadTimeout(100 * time.Millisecond); err != nil {
		_ = port.Close()
		return nil, err
	}
	return port, nil
}

type serialOpenError struct {
	err  error
	busy bool
}

func (err serialOpenError) Error() string { return err.err.Error() }
func (err serialOpenError) Unwrap() error { return err.err }
func (err serialOpenError) Busy() bool    { return err.busy }

func portBusy(err error) bool {
	var value *serial.PortError
	return (errors.As(err, &value) && value.Code() == serial.PortBusy) ||
		errors.Is(err, windows.ERROR_BUSY) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
