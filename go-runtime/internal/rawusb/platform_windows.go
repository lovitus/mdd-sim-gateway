//go:build windows

package rawusb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	usbip "github.com/sagernet/sing-usbip"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	usbipdVersion       = "5.3.0"
	usbipdVersionOutput = "5.3.0-54+Branch.master.Sha.aa3db8b82c4cb5071fd31bc54211606c70886912.aa3db8b82c4cb5071fd31bc54211606c70886912"
	usbipdServiceName   = "usbipd"
	usbipdMSIName       = "usbipd-win_5.3.0_x64.msi"
	usbipdMSISHA256     = "1c984914aec944de19b64eff232421439629699f8138e3ddc29301175bc6d938"
	usbipdMSISize       = 4501504
	usbipdReceiptPath   = `SOFTWARE\MDD\Dependencies\usbipd-win`
	usbipdReceiptValue  = "Receipt"
	usbipdReceiptSchema = 1
)

const (
	usbipdReceiptInstalling = "installing"
	usbipdReceiptOwned      = "owned"
)

var (
	errUSBIPDNotInstalled = errors.New("usbipd-win 5.3.0 is not installed")
	advapi32              = windows.NewLazySystemDLL("advapi32.dll")
	procRegFlushKey       = advapi32.NewProc("RegFlushKey")
)

type usbipdInstallReceipt struct {
	SchemaVersion int    `json:"schema_version"`
	State         string `json:"state"`
	Product       string `json:"product"`
	Version       string `json:"version"`
	Architecture  string `json:"architecture"`
	MSISHA256     string `json:"msi_sha256"`
}

type usbipdState struct {
	Devices []usbipdDevice `json:"Devices"`
}

type usbipdDevice struct {
	BusID           *string `json:"BusId"`
	ClientIPAddress *string `json:"ClientIPAddress"`
	Description     string  `json:"Description"`
	InstanceID      string  `json:"InstanceId"`
	IsForced        bool    `json:"IsForced"`
	PersistentID    *string `json:"PersistedGuid"`
	StubInstanceID  *string `json:"StubInstanceId"`
}

func newPlatformExporterForPhysicalID(ctx context.Context, physicalID string) (*Exporter, error) {
	physicalID = strings.TrimSpace(physicalID)
	if ctx == nil || physicalID == "" {
		return nil, errors.New("raw USB physical identity is empty")
	}
	client, err := newUSBIPDClient()
	if err != nil {
		return nil, err
	}
	state, err := client.state(ctx)
	if err != nil {
		return nil, err
	}
	current, err := exactUSBIPDInstance(state, physicalID)
	if err != nil {
		return nil, err
	}
	if current.PersistentID != nil || current.IsForced {
		if !current.IsForced || current.PersistentID == nil || current.BusID == nil || current.ClientIPAddress != nil {
			return nil, errors.New("persisted usbipd device is not forced, present, and idle")
		}
		device, deviceErr := deviceFromUSBIPD(*current)
		if deviceErr != nil {
			return nil, deviceErr
		}
		exporter, exportErr := client.exporter(ctx, *current, device)
		if exportErr != nil {
			return nil, &CapturedError{Device: device, Err: exportErr}
		}
		return exporter, nil
	}
	if current.ClientIPAddress != nil || current.BusID == nil {
		return nil, errors.New("usbipd device is already bound, attached, forced, or absent")
	}
	if err := client.run(ctx, "bind", "--busid", *current.BusID, "--force"); err != nil {
		return nil, fmt.Errorf("persistently bind exact USB device: %w", err)
	}
	state, err = client.state(ctx)
	if err != nil {
		return nil, err
	}
	current, err = exactUSBIPDInstance(state, physicalID)
	if err != nil {
		return nil, err
	}
	if !current.IsForced || current.PersistentID == nil || current.BusID == nil || current.ClientIPAddress != nil {
		return nil, errors.New("usbipd forced binding was not confirmed")
	}
	device, err := deviceFromUSBIPD(*current)
	if err != nil {
		return nil, err
	}
	exporter, err := client.exporter(ctx, *current, device)
	if err != nil {
		return nil, &CapturedError{Device: device, Err: err}
	}
	return exporter, nil
}

func newPlatformExporterFromDevice(ctx context.Context, expected Device) (*Exporter, error) {
	if ctx == nil || expected.Backend != windowsUSBIPDBackend || expected.InstanceID == "" ||
		expected.PersistentID == "" || expected.VendorID == 0 || expected.ProductID == 0 {
		return nil, errors.New("Windows raw USB recovery identity is incomplete")
	}
	client, err := newUSBIPDClient()
	if err != nil {
		return nil, err
	}
	state, err := client.state(ctx)
	if err != nil {
		return nil, err
	}
	current, err := exactUSBIPDPersistent(state, expected.InstanceID, expected.PersistentID)
	if err != nil {
		return nil, err
	}
	if !current.IsForced || current.BusID == nil || current.ClientIPAddress != nil {
		return nil, errors.New("persisted usbipd device is not forced, present, and idle")
	}
	actual, err := deviceFromUSBIPD(*current)
	if err != nil {
		return nil, err
	}
	actual.Serial = expected.Serial
	if !SamePersistentDevice(expected, actual) {
		return nil, errors.New("persisted usbipd device identity changed")
	}
	return client.exporter(ctx, *current, actual)
}

func newPlatformExporterFromPendingCapture(ctx context.Context, physicalID string) (*Exporter, error) {
	physicalID = strings.TrimSpace(physicalID)
	if ctx == nil || physicalID == "" {
		return nil, errors.New("Windows pending capture identity is incomplete")
	}
	client, err := newUSBIPDClient()
	if err != nil {
		return nil, err
	}
	state, err := client.state(ctx)
	if err != nil {
		return nil, err
	}
	current, err := exactUSBIPDInstance(state, physicalID)
	if err != nil {
		return nil, err
	}
	if current.PersistentID == nil && !current.IsForced && current.ClientIPAddress == nil && current.BusID != nil {
		return nil, ErrCaptureNotPresent
	}
	if current.PersistentID == nil || !current.IsForced || current.ClientIPAddress != nil || current.BusID == nil {
		return nil, errors.New("pending usbipd capture is neither safely absent nor forced")
	}
	device, err := deviceFromUSBIPD(*current)
	if err != nil {
		return nil, err
	}
	return client.exporter(ctx, *current, device)
}

func releasePlatformCapturedDevice(ctx context.Context, device Device) error {
	if ctx == nil || device.Backend != windowsUSBIPDBackend || device.InstanceID == "" || device.PersistentID == "" {
		return errors.New("Windows raw USB release identity is incomplete")
	}
	client, err := newUSBIPDClient()
	if err != nil {
		return err
	}
	state, err := client.state(ctx)
	if err != nil {
		return err
	}
	current, err := exactUSBIPDPersistent(state, device.InstanceID, device.PersistentID)
	if err != nil {
		return err
	}
	if current.ClientIPAddress != nil {
		return errors.New("usbipd device is still attached")
	}
	if err := client.run(ctx, "unbind", "--guid", device.PersistentID); err != nil {
		return fmt.Errorf("unbind exact persisted USB device: %w", err)
	}
	state, err = client.state(ctx)
	if err != nil {
		return err
	}
	for _, candidate := range state.Devices {
		if candidate.PersistentID != nil && strings.EqualFold(*candidate.PersistentID, device.PersistentID) {
			return errors.New("usbipd persisted binding remained after unbind")
		}
	}
	return nil
}

type usbipdClient struct{ executable string }

func newUSBIPDClient() (*usbipdClient, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\usbipd-win`, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, errUSBIPDNotInstalled
		}
		return nil, fmt.Errorf("read usbipd-win installation: %w", err)
	}
	defer key.Close()
	directory, _, err := key.GetStringValue("APPLICATIONFOLDER")
	if err != nil || !filepath.IsAbs(directory) {
		return nil, errors.New("usbipd installation directory is invalid")
	}
	executable := filepath.Clean(filepath.Join(directory, "usbipd.exe"))
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("usbipd executable is unavailable")
	}
	client := &usbipdClient{executable: executable}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := client.output(ctx, "--version")
	if err != nil || strings.TrimSpace(string(output)) != usbipdVersionOutput {
		return nil, errors.New("usbipd executable is not the pinned 5.3.0 release")
	}
	return client, nil
}

func (client *usbipdClient) output(ctx context.Context, arguments ...string) ([]byte, error) {
	if client == nil || client.executable == "" || ctx == nil {
		return nil, errors.New("usbipd command is unavailable")
	}
	command := exec.CommandContext(ctx, client.executable, arguments...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &limitedWriter{writer: &stdout, remaining: 1 << 20}
	command.Stderr = &limitedWriter{writer: &stderr, remaining: 1 << 20}
	if err := command.Run(); err != nil {
		return nil, errors.Join(err, errors.New(strings.TrimSpace(stderr.String())))
	}
	return stdout.Bytes(), nil
}

func (client *usbipdClient) run(ctx context.Context, arguments ...string) error {
	_, err := client.output(ctx, arguments...)
	return err
}

func (client *usbipdClient) state(ctx context.Context) (usbipdState, error) {
	output, err := client.output(ctx, "state")
	if err != nil {
		return usbipdState{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var state usbipdState
	if err := decoder.Decode(&state); err != nil {
		return usbipdState{}, fmt.Errorf("decode usbipd state: %w", err)
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || len(state.Devices) > 256 {
		return usbipdState{}, errors.New("usbipd state is not bounded")
	}
	return state, nil
}

func (client *usbipdClient) exporter(ctx context.Context, current usbipdDevice, device Device) (*Exporter, error) {
	if current.BusID == nil || current.PersistentID == nil {
		return nil, errors.New("usbipd device is not present and persisted")
	}
	if err := ensureUSBIPDServerDisabled(client.executable); err != nil {
		return nil, err
	}
	busNumber, address, err := parseUSBIPDBusID(*current.BusID)
	if err != nil {
		return nil, err
	}
	prepareContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := usbip.PrepareExternallyForcedWindowsDevice(
		prepareContext, current.InstanceID, busNumber, address,
	); err != nil {
		return nil, fmt.Errorf("prepare persistent forced USB device: %w", err)
	}
	transport, err := newPlatformExporter(ctx, Device{BusID: *current.BusID,
		VendorID: device.VendorID, ProductID: device.ProductID, Serial: device.Serial})
	if err != nil {
		return nil, err
	}
	actual := transport.Device()
	if actual.BusID != device.BusID || actual.VendorID != device.VendorID || actual.ProductID != device.ProductID ||
		device.Serial != "" && actual.Serial != device.Serial {
		_ = transport.closeTransport()
		return nil, errors.New("forced sing-usbip USB descriptor changed")
	}
	device.Serial = actual.Serial
	transport.device = device
	transport.preserve = transport.closeTransport
	transport.close = func() error {
		closeErr := transport.closeTransport()
		releaseContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return errors.Join(closeErr, releasePlatformCapturedDevice(releaseContext, device))
	}
	return transport, nil
}

func parseUSBIPDBusID(value string) (uint32, uint32, error) {
	busText, addressText, found := strings.Cut(value, "-")
	if !found || strings.Contains(addressText, "-") {
		return 0, 0, errors.New("usbipd device has an invalid BusID")
	}
	bus, busErr := strconv.ParseUint(busText, 10, 32)
	address, addressErr := strconv.ParseUint(addressText, 10, 32)
	if busErr != nil || addressErr != nil || bus == 0 || address == 0 ||
		strconv.FormatUint(bus, 10) != busText || strconv.FormatUint(address, 10) != addressText {
		return 0, 0, errors.New("usbipd device has an invalid BusID")
	}
	return uint32(bus), uint32(address), nil
}

func deviceFromUSBIPD(current usbipdDevice) (Device, error) {
	if current.BusID == nil || current.PersistentID == nil {
		return Device{}, errors.New("usbipd device is not present and persisted")
	}
	vendorID, productID, err := parseUSBHardwareID(current.InstanceID)
	if err != nil {
		return Device{}, err
	}
	return Device{StableID: strings.ToUpper(current.InstanceID), BusID: *current.BusID,
		VendorID: vendorID, ProductID: productID, Backend: windowsUSBIPDBackend,
		InstanceID: strings.ToUpper(current.InstanceID), PersistentID: strings.ToLower(*current.PersistentID)}, nil
}

func parseUSBHardwareID(instanceID string) (uint16, uint16, error) {
	upper := strings.ToUpper(instanceID)
	parse := func(marker string) (uint16, error) {
		index := strings.Index(upper, marker)
		if index < 0 || len(upper) < index+len(marker)+4 {
			return 0, errors.New("usbipd instance ID has no exact USB VID/PID")
		}
		value, err := strconv.ParseUint(upper[index+len(marker):index+len(marker)+4], 16, 16)
		return uint16(value), err
	}
	vendorID, err := parse("VID_")
	if err != nil {
		return 0, 0, err
	}
	productID, err := parse("PID_")
	if err != nil || vendorID == 0 || productID == 0 {
		return 0, 0, errors.New("usbipd instance ID has an invalid USB VID/PID")
	}
	return vendorID, productID, nil
}

func exactUSBIPDInstance(state usbipdState, instanceID string) (*usbipdDevice, error) {
	var result *usbipdDevice
	for index := range state.Devices {
		if strings.EqualFold(state.Devices[index].InstanceID, instanceID) {
			if result != nil {
				return nil, errors.New("usbipd instance identity is ambiguous")
			}
			result = &state.Devices[index]
		}
	}
	if result == nil {
		return nil, errors.New("exact USB instance is absent from usbipd")
	}
	return result, nil
}

func exactUSBIPDPersistent(state usbipdState, instanceID, persistentID string) (*usbipdDevice, error) {
	current, err := exactUSBIPDInstance(state, instanceID)
	if err != nil {
		return nil, err
	}
	if current.PersistentID == nil || !strings.EqualFold(*current.PersistentID, persistentID) {
		return nil, errors.New("usbipd persistent identity changed")
	}
	return current, nil
}

func ensureUSBIPDServerDisabled(executable string) error {
	receipt, exists, err := readUSBIPDInstallReceipt()
	if err != nil {
		return err
	}
	if !exists || receipt.State != usbipdReceiptOwned || validateUSBIPDInstallReceipt(receipt) != nil {
		return errors.New("usbipd-win installation is not owned by MDD")
	}
	return ensureUSBIPDServerStateDisabled(executable)
}

func ensureUSBIPDServerStateDisabled(executable string) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("open Windows service manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(usbipdServiceName)
	if err != nil {
		return errors.New("usbipd Windows service is unavailable")
	}
	defer service.Close()
	configuration, err := service.Config()
	if err != nil {
		return err
	}
	if err := validateUSBIPDServiceConfig(configuration, executable); err != nil {
		return err
	}
	status, err := service.Query()
	if err != nil {
		return err
	}
	if status.State != svc.Stopped {
		return errors.New("usbipd TCP service must remain stopped")
	}
	return nil
}

// BeginWindowsPersistentSource validates ownership before callers make any
// WFP or SCM change. A fresh installation writes one durable transaction debt;
// an existing usbipd-win installation without that receipt is never adopted.
func BeginWindowsPersistentSource(packageDirectory string) error {
	receipt, receiptExists, err := readUSBIPDInstallReceipt()
	if err != nil {
		return err
	}
	if receiptExists {
		if err := validateUSBIPDInstallReceipt(receipt); err != nil {
			return err
		}
	}
	client, clientErr := newUSBIPDClient()
	if clientErr == nil {
		if !receiptExists {
			return errors.New("refusing to modify an existing usbipd-win installation that is not owned by MDD")
		}
		if receipt.State == usbipdReceiptInstalling {
			return verifyUSBIPDMSI(filepath.Join(packageDirectory, usbipdMSIName))
		}
		if receipt.State != usbipdReceiptOwned {
			return errors.New("usbipd-win MDD ownership receipt has an invalid state")
		}
		_ = client
		return nil
	}
	if !errors.Is(clientErr, errUSBIPDNotInstalled) {
		return clientErr
	}
	if receiptExists {
		if receipt.State != usbipdReceiptInstalling {
			return errors.New("MDD-owned usbipd-win installation disappeared")
		}
		return verifyUSBIPDMSI(filepath.Join(packageDirectory, usbipdMSIName))
	}
	if !filepath.IsAbs(packageDirectory) {
		return errors.New("Windows Agent package directory must be absolute")
	}
	if err := rejectForeignVBoxServices(); err != nil {
		return err
	}
	if err := verifyUSBIPDMSI(filepath.Join(packageDirectory, usbipdMSIName)); err != nil {
		return err
	}
	return writeUSBIPDInstallReceipt(newUSBIPDInstallReceipt(usbipdReceiptInstalling))
}

// PrepareWindowsPersistentSource is an installation-time transaction. It
// leaves usbipd-win's signed CLI, registry and drivers installed, but disables
// its unauthenticated TCP daemon because MDD opens forced devices directly.
// Callers must install the persistent TCP 3240 WFP block before invoking it.
func PrepareWindowsPersistentSource(ctx context.Context, packageDirectory string) error {
	if ctx == nil {
		return errors.New("Windows raw USB installation context is unavailable")
	}
	if err := BeginWindowsPersistentSource(packageDirectory); err != nil {
		return err
	}
	receipt, exists, err := readUSBIPDInstallReceipt()
	if err != nil || !exists {
		return errors.Join(err, errors.New("usbipd-win MDD ownership receipt is unavailable"))
	}
	client, err := ensureUSBIPDInstalled(ctx, packageDirectory)
	if err != nil {
		return err
	}
	if receipt.State == usbipdReceiptInstalling {
		if err := validateInitialUSBIPDOwnership(ctx, client); err != nil {
			return err
		}
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("open Windows service manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(usbipdServiceName)
	if err != nil {
		return errors.New("usbipd Windows service is unavailable")
	}
	defer service.Close()
	configuration, err := service.Config()
	if err != nil {
		return err
	}
	if err := validateUSBIPDServiceIdentity(configuration, client.executable); err != nil {
		return err
	}
	if configuration.StartType != mgr.StartDisabled {
		configuration.StartType = mgr.StartDisabled
		if err := service.UpdateConfig(configuration); err != nil {
			return fmt.Errorf("disable usbipd TCP service: %w", err)
		}
	}
	status, err := service.Query()
	if err != nil {
		return err
	}
	if status.State != svc.Stopped {
		if _, err := service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return fmt.Errorf("stop usbipd TCP service: %w", err)
		}
		for status.State != svc.Stopped {
			select {
			case <-ctx.Done():
				return fmt.Errorf("stop usbipd TCP service: %w", ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
			status, err = service.Query()
			if err != nil {
				return err
			}
		}
	}
	if err := ensureUSBIPDServerStateDisabled(client.executable); err != nil {
		return err
	}
	if receipt.State == usbipdReceiptInstalling {
		return writeUSBIPDInstallReceipt(newUSBIPDInstallReceipt(usbipdReceiptOwned))
	}
	return nil
}

func ensureUSBIPDInstalled(ctx context.Context, packageDirectory string) (*usbipdClient, error) {
	client, err := newUSBIPDClient()
	if err == nil {
		return client, nil
	}
	if !errors.Is(err, errUSBIPDNotInstalled) {
		return nil, err
	}
	msiPath := filepath.Clean(filepath.Join(packageDirectory, usbipdMSIName))
	if err := verifyUSBIPDMSI(msiPath); err != nil {
		return nil, err
	}
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, fmt.Errorf("resolve Windows system directory: %w", err)
	}
	installer := filepath.Join(systemDirectory, "msiexec.exe")
	command := exec.CommandContext(ctx, installer, "/i", msiPath, "/qn", "/norestart")
	var stdout, stderr bytes.Buffer
	command.Stdout = &limitedWriter{writer: &stdout, remaining: 1 << 20}
	command.Stderr = &limitedWriter{writer: &stderr, remaining: 1 << 20}
	if err := command.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 3010 {
			return nil, errors.New("usbipd-win driver installation requires a Windows restart; restart and install the MDD Agent service again")
		}
		return nil, errors.Join(err, errors.New(strings.TrimSpace(stderr.String())))
	}
	client, err = newUSBIPDClient()
	if err != nil {
		return nil, fmt.Errorf("verify installed usbipd-win: %w", err)
	}
	return client, nil
}

func newUSBIPDInstallReceipt(state string) usbipdInstallReceipt {
	return usbipdInstallReceipt{
		SchemaVersion: usbipdReceiptSchema,
		State:         state,
		Product:       "usbipd-win",
		Version:       usbipdVersion,
		Architecture:  "x64",
		MSISHA256:     usbipdMSISHA256,
	}
}

func validateUSBIPDInstallReceipt(receipt usbipdInstallReceipt) error {
	if receipt.SchemaVersion != usbipdReceiptSchema ||
		(receipt.State != usbipdReceiptInstalling && receipt.State != usbipdReceiptOwned) ||
		receipt.Product != "usbipd-win" || receipt.Version != usbipdVersion ||
		receipt.Architecture != "x64" || receipt.MSISHA256 != usbipdMSISHA256 {
		return errors.New("usbipd-win MDD ownership receipt does not match the pinned dependency")
	}
	return nil
}

func readUSBIPDInstallReceipt() (usbipdInstallReceipt, bool, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, usbipdReceiptPath, registry.QUERY_VALUE)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return usbipdInstallReceipt{}, false, nil
	}
	if err != nil {
		return usbipdInstallReceipt{}, false, fmt.Errorf("read usbipd-win MDD ownership receipt: %w", err)
	}
	defer key.Close()
	payload, _, err := key.GetStringValue(usbipdReceiptValue)
	if err != nil || len(payload) == 0 || len(payload) > 4096 {
		return usbipdInstallReceipt{}, false, errors.New("usbipd-win MDD ownership receipt is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	var receipt usbipdInstallReceipt
	if decoder.Decode(&receipt) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return usbipdInstallReceipt{}, false, errors.New("usbipd-win MDD ownership receipt is invalid")
	}
	if err := validateUSBIPDInstallReceipt(receipt); err != nil {
		return usbipdInstallReceipt{}, false, err
	}
	return receipt, true, nil
}

func writeUSBIPDInstallReceipt(receipt usbipdInstallReceipt) error {
	if err := validateUSBIPDInstallReceipt(receipt); err != nil {
		return err
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, usbipdReceiptPath, registry.ALL_ACCESS)
	if err != nil {
		return fmt.Errorf("create usbipd-win MDD ownership receipt: %w", err)
	}
	defer key.Close()
	if err := key.SetStringValue(usbipdReceiptValue, string(payload)); err != nil {
		return fmt.Errorf("write usbipd-win MDD ownership receipt: %w", err)
	}
	result, _, _ := procRegFlushKey.Call(uintptr(key))
	if result != 0 {
		return fmt.Errorf("flush usbipd-win MDD ownership receipt: %w", windows.Errno(result))
	}
	return nil
}

func validateInitialUSBIPDOwnership(ctx context.Context, client *usbipdClient) error {
	state, err := client.state(ctx)
	if err != nil {
		return err
	}
	for _, device := range state.Devices {
		if device.PersistentID != nil || device.ClientIPAddress != nil || device.StubInstanceID != nil || device.IsForced {
			return errors.New("fresh usbipd-win installation unexpectedly owns a USB device")
		}
	}
	policy, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\usbipd-win\Policy`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return fmt.Errorf("read usbipd-win policy registry: %w", err)
	}
	defer policy.Close()
	names, err := policy.ReadSubKeyNames(1)
	if err != nil {
		return err
	}
	if len(names) != 0 {
		return errors.New("fresh usbipd-win installation unexpectedly contains a policy")
	}
	return nil
}

func rejectForeignVBoxServices() error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("open Windows service manager: %w", err)
	}
	defer manager.Disconnect()
	for _, name := range []string{usbipdServiceName, "VBoxUSBMon"} {
		service, openErr := manager.OpenService(name)
		if openErr == nil {
			service.Close()
			return fmt.Errorf("refusing to replace existing %s service without a verified usbipd-win installation", name)
		}
		if !errors.Is(openErr, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return fmt.Errorf("inspect existing %s service: %w", name, openErr)
		}
	}
	return nil
}

func verifyUSBIPDMSI(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != usbipdMSISize {
		return errors.New("pinned usbipd-win 5.3.0 x64 MSI is unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, io.LimitReader(file, usbipdMSISize+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return errors.Join(copyErr, closeErr)
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != usbipdMSISHA256 {
		return errors.New("pinned usbipd-win 5.3.0 x64 MSI hash mismatch")
	}
	return nil
}

func validateUSBIPDServiceConfig(configuration mgr.Config, executable string) error {
	if err := validateUSBIPDServiceIdentity(configuration, executable); err != nil {
		return err
	}
	if configuration.StartType != mgr.StartDisabled {
		return errors.New("usbipd disabled SCM contract changed")
	}
	return nil
}

func validateUSBIPDServiceIdentity(configuration mgr.Config, executable string) error {
	arguments, err := windows.DecomposeCommandLine(configuration.BinaryPathName)
	if err != nil || len(arguments) != 2 || arguments[1] != "server" {
		return errors.New("usbipd service command line changed")
	}
	configuredPath, err := filepath.Abs(arguments[0])
	if err != nil {
		return errors.New("usbipd service executable path is invalid")
	}
	expectedPath, err := filepath.Abs(executable)
	if err != nil || !strings.EqualFold(filepath.Clean(configuredPath), filepath.Clean(expectedPath)) {
		return errors.New("usbipd service executable ownership changed")
	}
	if configuration.ServiceType != windows.SERVICE_WIN32_OWN_PROCESS ||
		!strings.EqualFold(configuration.ServiceStartName, "LocalSystem") ||
		len(configuration.Dependencies) != 1 || !strings.EqualFold(configuration.Dependencies[0], "VBoxUsbMon") ||
		configuration.DisplayName != "USBIP Device Host" {
		return errors.New("usbipd disabled SCM contract changed")
	}
	return nil
}

type limitedWriter struct {
	writer    *bytes.Buffer
	remaining int
}

func (writer *limitedWriter) Write(payload []byte) (int, error) {
	original := len(payload)
	if writer.remaining <= 0 {
		return original, nil
	}
	if len(payload) > writer.remaining {
		payload = payload[:writer.remaining]
	}
	_, _ = writer.writer.Write(payload)
	writer.remaining -= len(payload)
	return original, nil
}
