//go:build windows

package rawusb

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestUSBIPDUserModeServerMustBeDisabled(t *testing.T) {
	executable := `C:\Program Files\usbipd-win\usbipd.exe`
	valid := mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartDisabled,
		BinaryPathName:   `"C:\Program Files\usbipd-win\usbipd.exe" server`,
		Dependencies:     []string{"VBoxUsbMon"},
		ServiceStartName: "LocalSystem",
		DisplayName:      "USBIP Device Host",
	}
	if err := validateUSBIPDServiceConfig(valid, executable); err != nil {
		t.Fatalf("valid disabled service: %v", err)
	}

	automatic := valid
	automatic.StartType = mgr.StartAutomatic
	if err := validateUSBIPDServiceConfig(automatic, executable); err == nil {
		t.Fatal("automatic usbipd TCP service was accepted")
	}

	wrongDependency := valid
	wrongDependency.Dependencies = []string{"Tcpip"}
	if err := validateUSBIPDServiceConfig(wrongDependency, executable); err == nil {
		t.Fatal("usbipd service without exact VBoxUSBMon dependency was accepted")
	}

	wrongCommand := valid
	wrongCommand.BinaryPathName = `"C:\Program Files\usbipd-win\usbipd.exe" state`
	if err := validateUSBIPDServiceConfig(wrongCommand, executable); err == nil {
		t.Fatal("unexpected usbipd service command was accepted")
	}
}

func TestVerifyUSBIPDMSIRejectsMissingOrChangedPackage(t *testing.T) {
	if err := verifyUSBIPDMSI(filepath.Join(t.TempDir(), usbipdMSIName)); err == nil {
		t.Fatal("missing usbipd-win MSI was accepted")
	}
	path := filepath.Join(t.TempDir(), usbipdMSIName)
	if err := os.WriteFile(path, []byte("not the pinned installer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyUSBIPDMSI(path); err == nil {
		t.Fatal("changed usbipd-win MSI was accepted")
	}
}

func TestVerifyUSBIPDMSIIntegration(t *testing.T) {
	path := os.Getenv("MDD_TEST_USBIPD_MSI")
	if path == "" {
		t.Skip("set MDD_TEST_USBIPD_MSI to verify the packaged installer")
	}
	if err := verifyUSBIPDMSI(path); err != nil {
		t.Fatal(err)
	}
}

func TestParseUSBIPDBusIDRequiresCanonicalPositivePair(t *testing.T) {
	bus, address, err := parseUSBIPDBusID("12-34")
	if err != nil || bus != 12 || address != 34 {
		t.Fatalf("bus=%d address=%d error=%v", bus, address, err)
	}
	for _, invalid := range []string{"", "1", "1-", "-1", "0-1", "1-0", "01-1", "1-01", "1-2-3", "1:a"} {
		if _, _, err := parseUSBIPDBusID(invalid); err == nil {
			t.Fatalf("invalid BusID %q was accepted", invalid)
		}
	}
}

func TestUSBIPDInstallReceiptIsExactAndVersioned(t *testing.T) {
	for _, state := range []string{usbipdReceiptInstalling, usbipdReceiptOwned} {
		receipt := newUSBIPDInstallReceipt(state)
		if err := validateUSBIPDInstallReceipt(receipt); err != nil {
			t.Fatalf("state %q: %v", state, err)
		}
	}
	invalid := newUSBIPDInstallReceipt(usbipdReceiptOwned)
	invalid.MSISHA256 = "changed"
	if err := validateUSBIPDInstallReceipt(invalid); err == nil {
		t.Fatal("changed MSI receipt was accepted")
	}
	invalid = newUSBIPDInstallReceipt(usbipdReceiptOwned)
	invalid.State = "adopted"
	if err := validateUSBIPDInstallReceipt(invalid); err == nil {
		t.Fatal("unknown ownership state was accepted")
	}
}
