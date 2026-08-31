//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type testPortTuple struct {
	Name string
	Kind uint32
}

type testSignalTuple struct {
	Quality uint32
	Recent  bool
}

type testModemManager struct {
	inventory       []modemSnapshot
	inhibits        []bool
	releaseFailures int
}

func (manager *testModemManager) Inventory(context.Context) ([]modemSnapshot, error) {
	return append([]modemSnapshot(nil), manager.inventory...), nil
}

func (manager *testModemManager) Inhibit(_ context.Context, _ string, inhibit bool) error {
	manager.inhibits = append(manager.inhibits, inhibit)
	if !inhibit && manager.releaseFailures > 0 {
		manager.releaseFailures--
		return os.ErrPermission
	}
	return nil
}

func (*testModemManager) Connect(context.Context, dbus.ObjectPath, string) (dataBearer, error) {
	return dataBearer{}, errors.New("not configured")
}

func (*testModemManager) Disconnect(context.Context, dbus.ObjectPath) error { return nil }

func (*testModemManager) Close() error { return nil }

func TestParseManagedObjectsPreservesTypedModemFacts(t *testing.T) {
	modemPath := dbus.ObjectPath("/org/freedesktop/ModemManager1/Modem/0")
	simPath := dbus.ObjectPath("/org/freedesktop/ModemManager1/SIM/0")
	bearerPath := dbus.ObjectPath("/org/freedesktop/ModemManager1/Bearer/0")
	objects := managedObjects{
		modemPath: {
			mmModem: {
				"Device":              dbus.MakeVariant("/sys/devices/pci/usb/1-2"),
				"EquipmentIdentifier": dbus.MakeVariant("862547055201716"),
				"Manufacturer":        dbus.MakeVariant("Quectel"), "Model": dbus.MakeVariant("EC20"),
				"Revision":   dbus.MakeVariant("EC20CEHGR06A09M1G"),
				"Ports":      dbus.MakeVariant([]testPortTuple{{"wwan0", mmPortNet}, {"ttyUSB2", mmPortAT}, {"pcmC3D0p", mmPortAudio}}),
				"OwnNumbers": dbus.MakeVariant([]string{"+85212345678"}),
				"Sim":        dbus.MakeVariant(simPath), "UnlockRequired": dbus.MakeVariant(uint32(1)),
				"State": dbus.MakeVariant(int32(8)), "Bearers": dbus.MakeVariant([]dbus.ObjectPath{bearerPath}),
				"SignalQuality": dbus.MakeVariant(testSignalTuple{Quality: 73, Recent: true}),
			},
			mmModem3GPP: {
				"RegistrationState": dbus.MakeVariant(uint32(5)),
				"OperatorCode":      dbus.MakeVariant("45400"), "OperatorName": dbus.MakeVariant("CSL"),
			},
		},
		simPath: {mmSIM: {
			"SimIdentifier": dbus.MakeVariant("8985200000000000001"),
			"Imsi":          dbus.MakeVariant("454001234567890"),
		}},
		bearerPath: {mmBearer: {"Connected": dbus.MakeVariant(false)}},
	}
	facts, err := parseManagedObjects(objects)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts=%+v", facts)
	}
	fact := facts[0]
	if fact.UID != "/sys/devices/pci/usb/1-2" || fact.EquipmentID != "862547055201716" ||
		fact.SIMState != agentmodem.SIMReady || fact.ICCID != "8985200000000000001" ||
		fact.IMSI != "454001234567890" || fact.Registration != agentmodem.RegistrationRoaming ||
		fact.Connected || len(fact.ATPorts) != 1 || fact.ATPorts[0] != "ttyUSB2" ||
		len(fact.NetPorts) != 1 || fact.NetPorts[0] != "wwan0" ||
		len(fact.AudioPorts) != 1 || fact.SignalPercent == nil || *fact.SignalPercent != 73 {
		t.Fatalf("fact=%+v", fact)
	}
	objects[bearerPath][mmBearer]["Connected"] = dbus.MakeVariant(true)
	facts, err = parseManagedObjects(objects)
	if err != nil || len(facts) != 1 || !facts[0].Connected {
		t.Fatalf("connected facts=%+v err=%v", facts, err)
	}
}

func TestParseDataBearerRequiresConnectedStaticIPv4(t *testing.T) {
	path := dbus.ObjectPath("/org/freedesktop/ModemManager1/Bearer/7")
	properties := map[string]dbus.Variant{
		"Connected": dbus.MakeVariant(true),
		"Interface": dbus.MakeVariant("wwan0"),
		"Ip4Config": dbus.MakeVariant(map[string]dbus.Variant{
			"method": dbus.MakeVariant(uint32(2)), "address": dbus.MakeVariant("10.1.2.3"),
			"prefix": dbus.MakeVariant(uint32(30)), "gateway": dbus.MakeVariant("10.1.2.4"),
			"dns": dbus.MakeVariant([]string{"1.1.1.1", "8.8.8.8"}),
		}),
	}
	bearer, err := parseDataBearer(path, properties)
	if err != nil || bearer.ObjectPath != path || bearer.Interface != "wwan0" || bearer.Address != "10.1.2.3" ||
		bearer.Prefix != 30 || bearer.Gateway != "10.1.2.4" || len(bearer.DNS) != 2 {
		t.Fatalf("bearer=%+v err=%v", bearer, err)
	}
	config, _ := properties["Ip4Config"].Value().(map[string]dbus.Variant)
	config["method"] = dbus.MakeVariant(uint32(3))
	properties["Ip4Config"] = dbus.MakeVariant(config)
	if _, err := parseDataBearer(path, properties); err == nil {
		t.Fatal("DHCP bearer was accepted without a DHCP implementation")
	}
}

func TestResolveUSBGenerationAndExactALSACard(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "devices", "pci0000:00", "usb1", "1-2")
	interfacePath := filepath.Join(root, "devices", "pci0000:00", "usb1", "1-2", "1-2:1.2")
	ttyPath := filepath.Join(interfacePath, "ttyUSB2", "tty", "ttyUSB2")
	for _, path := range []string{physical, ttyPath, filepath.Join(root, "class", "tty", "ttyUSB2"), filepath.Join(root, "class", "sound", "card3")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]string{"idVendor": "2c7c\n", "idProduct": "0125\n", "busnum": "1\n", "devnum": "8\n"} {
		if err := os.WriteFile(filepath.Join(physical, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(ttyPath, filepath.Join(root, "class", "tty", "ttyUSB2", "device")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(interfacePath, filepath.Join(root, "class", "sound", "card3", "device")); err != nil {
		t.Fatal(err)
	}
	first, err := resolveUSBGeneration(root, []string{"ttyUSB2"})
	if err != nil || first.PhysicalID != physical || first.AttachmentID == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	cards, err := soundCardsForPhysical(root, first.PhysicalID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := cards[3]; !exists || len(cards) != 1 {
		t.Fatalf("cards=%v", cards)
	}
	if err := os.WriteFile(filepath.Join(physical, "devnum"), []byte("9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := resolveUSBGeneration(root, []string{"ttyUSB2"})
	if err != nil || second.AttachmentID == first.AttachmentID || second.Generation == first.Generation {
		t.Fatalf("second=%+v first=%+v err=%v", second, first, err)
	}
}

func TestUSBGenerationChangeReleasesStaleEquipmentIdentity(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "devices", "usb1", "1-2")
	interfacePath := filepath.Join(physical, "1-2:1.2")
	ttyPath := filepath.Join(interfacePath, "ttyUSB2", "tty", "ttyUSB2")
	for _, path := range []string{physical, ttyPath, filepath.Join(root, "class", "tty", "ttyUSB2")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]string{"idVendor": "2c7c\n", "idProduct": "0125\n", "busnum": "1\n", "devnum": "9\n"} {
		if err := os.WriteFile(filepath.Join(physical, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(ttyPath, filepath.Join(root, "class", "tty", "ttyUSB2", "device")); err != nil {
		t.Fatal(err)
	}
	at, err := agentat.NewManager(func() ([]agentat.Candidate, error) { return nil, nil }, func(agentat.Candidate) (agentat.Port, error) {
		t.Fatal("stale equipment identity attempted AT reacquisition")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := &testModemManager{}
	prober := &Prober{
		manager: manager, at: at, sysRoot: root, devices: map[string]*ownedDevice{
			"/sys/devices/usb1/1-2": {
				snapshot: modemSnapshot{UID: "/sys/devices/usb1/1-2", EquipmentID: "862547055201716", ATPorts: []string{"ttyUSB2"}},
				usb:      usbGeneration{PhysicalID: physical, Generation: physical + "@1:8", AttachmentID: "old-generation"},
			},
		},
	}
	facts, err := prober.probeLocked(context.Background(), false)
	if err != nil || len(facts) != 0 || len(prober.devices) != 0 {
		t.Fatalf("facts=%+v devices=%+v err=%v", facts, prober.devices, err)
	}
	if len(manager.inhibits) != 1 || manager.inhibits[0] {
		t.Fatalf("inhibit transitions=%v, want one release", manager.inhibits)
	}
}

func TestUSBGenerationReleaseRetriesWithoutRepublishingStaleIdentity(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "devices", "usb1", "1-2")
	ttyPath := filepath.Join(physical, "1-2:1.2", "ttyUSB2", "tty", "ttyUSB2")
	for _, path := range []string{physical, ttyPath, filepath.Join(root, "class", "tty", "ttyUSB2")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]string{"idVendor": "2c7c\n", "idProduct": "0125\n", "busnum": "1\n", "devnum": "9\n"} {
		if err := os.WriteFile(filepath.Join(physical, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(ttyPath, filepath.Join(root, "class", "tty", "ttyUSB2", "device")); err != nil {
		t.Fatal(err)
	}
	at, err := agentat.NewManager(func() ([]agentat.Candidate, error) { return nil, nil }, func(agentat.Candidate) (agentat.Port, error) {
		t.Fatal("release-pending identity attempted AT reacquisition")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := &testModemManager{releaseFailures: 1}
	prober := &Prober{
		manager: manager, at: at, sysRoot: root, devices: map[string]*ownedDevice{
			"uid": {
				snapshot: modemSnapshot{UID: "uid", EquipmentID: "862547055201716", ATPorts: []string{"ttyUSB2"}},
				usb:      usbGeneration{PhysicalID: physical, Generation: physical + "@1:8", AttachmentID: "old-generation"},
			},
		},
	}
	first, err := prober.probeLocked(context.Background(), false)
	if err != nil || len(first) != 1 || first[0].Condition != agentmodem.DeviceDegraded || !prober.devices["uid"].releasePending {
		t.Fatalf("first=%+v devices=%+v err=%v", first, prober.devices, err)
	}
	second, err := prober.probeLocked(context.Background(), false)
	if err != nil || len(second) != 0 || len(prober.devices) != 0 || len(manager.inhibits) != 2 {
		t.Fatalf("second=%+v devices=%+v inhibits=%v err=%v", second, prober.devices, manager.inhibits, err)
	}
}

func TestDecodeALSAHardwareID(t *testing.T) {
	id := "3a332c3000" // hex(":3,0\\x00")
	card, device, ok := decodeALSAHardwareID(id)
	if !ok || card != 3 || device != 0 {
		t.Fatalf("card=%d device=%d ok=%v", card, device, ok)
	}
}

func TestParseHelpersRejectAmbiguousOrMalformedFacts(t *testing.T) {
	if got := parseIMSI([]byte("AT+CIMI\r\n454001234567890\r\nOK\r\n")); got != "454001234567890" {
		t.Fatalf("IMSI=%q", got)
	}
	if got := parseMSISDNs([]byte("+CNUM: ,\"+85212345678\",145\r\n+CNUM: ,\"+85212345678\",145\r\n")); len(got) != 1 || got[0] != "+85212345678" {
		t.Fatalf("MSISDNs=%v", got)
	}
	if got := parseRegistration([]byte("+CEREG: 2,5\r\n")); got != agentmodem.RegistrationRoaming {
		t.Fatalf("registration=%q", got)
	}
	if id, name := parseOperator([]byte("+COPS: 0,2,\"23410\",7\r\n")); id != "23410" || name != "" {
		t.Fatalf("operator id=%q name=%q", id, name)
	}
	if value := parseSignal([]byte("+CSQ: 99,99\r\n")); value != nil {
		t.Fatalf("unknown signal=%v", *value)
	}
}
