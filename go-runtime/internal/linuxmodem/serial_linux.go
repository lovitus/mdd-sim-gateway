//go:build linux

package linuxmodem

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"go.bug.st/serial"
)

type usbGeneration struct {
	PhysicalID   string
	Generation   string
	AttachmentID string
}

func resolveUSBGeneration(sysRoot string, ports []string) (usbGeneration, error) {
	sysRoot = filepath.Clean(sysRoot)
	if !filepath.IsAbs(sysRoot) || len(ports) == 0 {
		return usbGeneration{}, errors.New("invalid Linux modem sysfs discovery request")
	}
	var physical, bus, device string
	for _, name := range ports {
		name = strings.TrimSpace(name)
		if name == "" || filepath.Base(name) != name {
			return usbGeneration{}, errors.New("ModemManager returned an invalid AT port")
		}
		path, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "class", "tty", name, "device"))
		if err != nil {
			return usbGeneration{}, err
		}
		current, currentBus, currentDevice, err := findUSBPhysical(sysRoot, path)
		if err != nil {
			return usbGeneration{}, err
		}
		if physical != "" && physical != current {
			return usbGeneration{}, errors.New("ModemManager grouped AT ports from different USB devices")
		}
		physical, bus, device = current, currentBus, currentDevice
	}
	if physical == "" {
		return usbGeneration{}, errors.New("Linux modem has no exact USB parent")
	}
	generation := fmt.Sprintf("%s@%s:%s", physical, bus, device)
	digest := sha256.Sum256([]byte(generation))
	return usbGeneration{
		PhysicalID: physical, Generation: generation,
		AttachmentID: "linux-usb-" + hex.EncodeToString(digest[:12]),
	}, nil
}

func findUSBPhysical(sysRoot, start string) (path, bus, device string, err error) {
	sysRoot = filepath.Clean(sysRoot)
	current := filepath.Clean(start)
	for {
		relative, relErr := filepath.Rel(sysRoot, current)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", "", "", errors.New("Linux modem sysfs path escaped the configured root")
		}
		vendor, vendorErr := readSysfsToken(filepath.Join(current, "idVendor"), 4, 4, 16)
		product, productErr := readSysfsToken(filepath.Join(current, "idProduct"), 4, 4, 16)
		busValue, busErr := readSysfsToken(filepath.Join(current, "busnum"), 1, 3, 10)
		deviceValue, deviceErr := readSysfsToken(filepath.Join(current, "devnum"), 1, 3, 10)
		if errors.Join(vendorErr, productErr, busErr, deviceErr) == nil && vendor != "0000" && product != "0000" {
			return current, busValue, deviceValue, nil
		}
		if current == sysRoot {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", "", "", errors.New("Linux modem AT port has no USB device ancestor")
}

func readSysfsToken(path string, minimum, maximum, base int) (string, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(payload))
	if len(value) < minimum || len(value) > maximum {
		return "", errors.New("sysfs token has invalid width")
	}
	if _, err := strconv.ParseUint(value, base, 32); err != nil {
		return "", errors.New("sysfs token is not numeric")
	}
	return strings.ToLower(value), nil
}

func linuxATCandidates(snapshot modemSnapshot, physicalID string) []agentat.Candidate {
	result := make([]agentat.Candidate, 0, len(snapshot.ATPorts))
	for _, name := range snapshot.ATPorts {
		result = append(result, agentat.Candidate{
			Name: name, Product: strings.TrimSpace(snapshot.Manufacturer + " " + snapshot.Model),
			USB: true, PhysicalID: physicalID,
		})
	}
	return result
}

func openLinuxAT(candidate agentat.Candidate) (agentat.Port, error) {
	if candidate.Name == "" || filepath.Base(candidate.Name) != candidate.Name {
		return nil, errors.New("invalid Linux AT port name")
	}
	port, err := serial.Open(filepath.Join("/dev", candidate.Name), &serial.Mode{
		BaudRate: 115200, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit,
		InitialStatusBits: &serial.ModemOutputBits{DTR: false, RTS: false},
	})
	if err != nil {
		return nil, linuxSerialOpenError{err: err, busy: linuxPortBusy(err)}
	}
	if err := port.SetReadTimeout(100 * time.Millisecond); err != nil {
		_ = port.Close()
		return nil, err
	}
	return port, nil
}

type linuxSerialOpenError struct {
	err  error
	busy bool
}

func (err linuxSerialOpenError) Error() string { return err.err.Error() }
func (err linuxSerialOpenError) Unwrap() error { return err.err }
func (err linuxSerialOpenError) Busy() bool    { return err.busy }

func linuxPortBusy(err error) bool {
	var value *serial.PortError
	return errors.As(err, &value) && value.Code() == serial.PortBusy
}
