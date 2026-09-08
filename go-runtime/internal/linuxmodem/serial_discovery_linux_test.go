//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"os"
	"path/filepath"
	"testing"
)

func serialDiscoveryFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bus := filepath.Join(root, "bus", "usb", "devices")
	physical := filepath.Join(bus, "1-2")
	iface := filepath.Join(physical, "1-2:1.2")
	class := filepath.Join(root, "class", "tty", "ttyUSB2")
	for _, path := range []string{filepath.Join(iface, "ttyUSB2"), class} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]string{"idVendor": "2c7c", "idProduct": "0125", "busnum": "1", "devnum": "8", "serial": "serial value"} {
		if err := os.WriteFile(filepath.Join(physical, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(iface, filepath.Join(bus, "1-2:1.2")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(iface, filepath.Join(class, "device")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSerialDiscoveryUsesOriginalProfileAndInterfaceWithoutModemManager(t *testing.T) {
	root := serialDiscoveryFixture(t)
	profiles := []serialModemProfile{{VID: "2c7c", PID: "0125", Name: "Original modem"}}
	found, err := discoverSerialModems(root, profiles)
	if err != nil || len(found) != 1 {
		t.Fatal("profiled modem not discovered", err)
	}
	if found[0].ID != "2c7c-0125-serial-value" || found[0].TTY != "/dev/ttyUSB2" || found[0].USB.Generation == "" {
		t.Fatal("original discovery identity changed", found[0])
	}
	if found, err := discoverSerialModems(root, nil); err != nil || len(found) != 0 {
		t.Fatal("unprofiled USB device admitted", err)
	}
	wrong := uint8(3)
	profiles[0].ATInterface = &wrong
	if found, err := discoverSerialModems(root, profiles); err != nil || len(found) != 0 {
		t.Fatal("wrong USB interface admitted", err)
	}
}

func TestSerialInventoryRequiresProtectionAndReidentifiesNewUSBGeneration(t *testing.T) {
	root := serialDiscoveryFixture(t)
	profiles := []serialModemProfile{{VID: "2c7c", PID: "0125"}}
	devices, err := discoverSerialModems(root, profiles)
	if err != nil {
		t.Fatal(err)
	}
	device := devices[0]
	opened := 0
	protected := false
	inventory := &serialInventory{sysRoot: root, profiles: profiles, identities: map[string]string{device.USB.Generation + "\x00" + device.TTY: "862547055201716"},
		open: func(agentat.Candidate) (agentat.Port, error) { opened++; return nil, errors.New("fixture has no port") },
		verifyProtected: func(context.Context, string, []string) error {
			if !protected {
				return errors.New("not protected")
			}
			return nil
		}}
	if _, err := inventory.Inventory(context.Background()); err == nil || opened != 0 {
		t.Fatal("unprotected modem accessed")
	}
	protected = true
	facts, err := inventory.Inventory(context.Background())
	if err != nil || len(facts) != 1 || facts[0].EquipmentID != "862547055201716" || opened != 0 {
		t.Fatal("existing USB identity not reused", err)
	}
	if err := os.WriteFile(filepath.Join(device.USB.PhysicalID, "devnum"), []byte("9"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inventory.Inventory(context.Background()); err == nil || opened != 1 {
		t.Fatal("new USB insertion reused old identity")
	}
}
