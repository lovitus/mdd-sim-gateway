//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linuxdataguard"
)

type acquisitionManager struct {
	testModemManager
	disconnected    []dbus.ObjectPath
	disconnectErr   error
	afterDisconnect func()
}

func (manager *acquisitionManager) Disconnect(_ context.Context, path dbus.ObjectPath) error {
	manager.disconnected = append(manager.disconnected, path)
	if manager.afterDisconnect != nil {
		manager.afterDisconnect()
	}
	return manager.disconnectErr
}

type acquisitionVoiceManager struct {
	*acquisitionManager
	idle       bool
	voiceErr   error
	afterVoice func(context.Context)
}

func (manager *acquisitionVoiceManager) VoiceIdle(ctx context.Context, _ dbus.ObjectPath) (bool, error) {
	if manager.afterVoice != nil {
		manager.afterVoice(ctx)
	}
	return manager.idle, manager.voiceErr
}

type acquisitionGuard struct {
	*linuxdataguard.Guard
	physical string
	err      error
}

func (guard *acquisitionGuard) VerifyProtected(_ context.Context, physical string, _ []string) error {
	if physical != guard.physical {
		return errors.New("unprotected physical device")
	}
	return guard.err
}

type acquisitionPort struct {
	closed   bool
	commands []string
	pin      string
}

func (*acquisitionPort) Read([]byte) (int, error)  { return 0, io.EOF }
func (*acquisitionPort) Write([]byte) (int, error) { return 0, errors.New("use exchange") }
func (*acquisitionPort) Drain() error              { return nil }
func (*acquisitionPort) ResetInputBuffer() error   { return nil }
func (port *acquisitionPort) Close() error         { port.closed = true; return nil }
func (port *acquisitionPort) Exchange(_ context.Context, command string, _ time.Duration) ([]byte, error) {
	if port.closed {
		return nil, errors.New("port closed")
	}
	port.commands = append(port.commands, command)
	var response string
	switch command {
	case "AT", "AT+CLCC", "AT+CMGF=?":
		response = "OK"
	case "AT+CGSN":
		response = "862547055201716\r\nOK"
	case "AT+CPIN?":
		response = "+CPIN: READY\r\nOK"
		if port.pin != "" {
			response = "+CPIN: " + port.pin + "\r\nOK"
		}
	case "AT+QPINC?":
		response = "+QPINC: \"SC\",3,10\r\nOK"
	case `AT+CPIN="1234"`:
		port.pin = ""
		response = "OK"
	case "AT+QCCID":
		response = "+QCCID: 8985200000000000001\r\nOK"
	default:
		return nil, errors.New("optional query unavailable")
	}
	return []byte("\r\n" + response + "\r\n"), nil
}

func acquisitionFixture(t *testing.T, manager modemManager, pin ...string) (*Prober, modemSnapshot, *[]*acquisitionPort) {
	t.Helper()
	root := serialDiscoveryFixture(t)
	ports := []*acquisitionPort{}
	prober, err := newProber(manager, false, "/fixture/audio", root, func(agentat.Candidate) (agentat.Port, error) {
		port := &acquisitionPort{}
		if len(pin) != 0 {
			port.pin = pin[0]
		}
		ports = append(ports, port)
		return port, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prober.guard = &acquisitionGuard{physical: filepath.Join(root, "bus", "usb", "devices", "1-2")}
	t.Cleanup(func() { _ = prober.Close() })
	snapshot := modemSnapshot{UID: "usb-a", EquipmentID: "862547055201716", ATPorts: []string{"ttyUSB2"},
		ObjectPath: "/org/freedesktop/ModemManager1/Modem/0", SIMState: agentmodem.SIMReady, ICCID: "8985200000000000001"}
	return prober, snapshot, &ports
}

func acquisitionProbe(t *testing.T, prober *Prober) []agentmodem.Fact {
	t.Helper()
	facts, err := prober.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestLinuxAcquisitionDuplicateCannotDisconnectOrInhibit(t *testing.T) {
	manager := &acquisitionVoiceManager{acquisitionManager: &acquisitionManager{}, idle: true}
	prober, snapshot, ports := acquisitionFixture(t, manager)
	snapshot.Connected = true
	snapshot.Bearers = []dbus.ObjectPath{"/org/freedesktop/ModemManager1/Bearer/1"}
	other := snapshot
	other.UID, other.ObjectPath, other.ATPorts = "usb-b", "/org/freedesktop/ModemManager1/Modem/1", []string{"ttyUSB3"}
	manager.inventory = []modemSnapshot{snapshot, other}
	facts := acquisitionProbe(t, prober)
	if len(manager.disconnected) != 0 || len(manager.inhibits) != 0 || len(*ports) != 0 || len(prober.devices) != 0 {
		t.Fatal("ambiguous equipment identity mutated a bearer or acquired AT ownership")
	}
	if len(facts) != 2 {
		t.Fatalf("facts=%+v", facts)
	}
	for _, fact := range facts {
		if fact.Condition == agentmodem.DeviceReady || !strings.Contains(fact.Detail, "same equipment identity") {
			t.Fatalf("duplicate not reported: %+v", fact)
		}
	}
	for _, present := range []bool{false, true} {
		t.Run("retained_present="+map[bool]string{false: "false", true: "true"}[present], func(t *testing.T) {
			manager := &acquisitionVoiceManager{acquisitionManager: &acquisitionManager{}, idle: true}
			prober, snapshot, ports := acquisitionFixture(t, manager)
			manager.inventory = []modemSnapshot{snapshot}
			ready := acquisitionProbe(t, prober)[0]
			if ready.Condition != agentmodem.DeviceReady || ready.SIM.SessionGeneration == "" {
				t.Fatalf("initial owner not qualified: %+v", ready)
			}
			before := len((*ports)[0].commands)
			other := snapshot
			other.UID, other.ObjectPath, other.ATPorts = "usb-b", "/org/freedesktop/ModemManager1/Modem/1", []string{"ttyUSB3"}
			manager.inventory = []modemSnapshot{other}
			if present {
				manager.inventory = append(manager.inventory, snapshot)
			}
			for attempt := 0; attempt < 2; attempt++ {
				facts := acquisitionProbe(t, prober)
				retained := false
				for _, fact := range facts {
					if fact.AttachmentID == ready.AttachmentID {
						retained = true
						if fact.Condition == agentmodem.DeviceReady || fact.AT.State == agentmodem.ATControlReady || fact.ContinuityEpoch != "" || fact.SIM.SessionGeneration != "" {
							t.Fatalf("duplicate preserved retained owner qualification: %+v", fact)
						}
					}
				}
				if !retained || len(manager.disconnected) != 0 || len(manager.inhibits) != 1 || len(*ports) != 1 || !(*ports)[0].closed || len((*ports)[0].commands) != before {
					t.Fatal("duplicate retirement lost the attachment, mutated MM, or issued AT commands")
				}
				manager.inventory = nil
			}
		})
	}
}

func TestLinuxAcquisitionCallStateFencesBothMutations(t *testing.T) {
	for _, connected := range []bool{false, true} {
		for _, state := range []string{"active_or_unknown", "error", "missing", "cancelled", "deadline"} {
			t.Run(state+"/connected="+map[bool]string{false: "false", true: "true"}[connected], func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				base := &acquisitionManager{}
				voice := &acquisitionVoiceManager{acquisitionManager: base}
				if state == "error" {
					voice.voiceErr = errors.New("call inventory read failed")
				}
				if state == "cancelled" {
					voice.idle, voice.afterVoice = true, func(context.Context) { cancel() }
				}
				if state == "deadline" {
					voice.idle, voice.afterVoice = true, func(ctx context.Context) { <-ctx.Done() }
				}
				var manager modemManager = voice
				if state == "missing" {
					manager = base
				}
				prober, snapshot, ports := acquisitionFixture(t, manager)
				snapshot.Connected, snapshot.Bearers = connected, []dbus.ObjectPath{"/org/freedesktop/ModemManager1/Bearer/1"}
				base.inventory = []modemSnapshot{snapshot}
				facts, _ := prober.Probe(ctx)
				if len(base.disconnected) != 0 || len(base.inhibits) != 0 || len(*ports) != 0 {
					t.Fatal("unproven idle state permitted disconnect or inhibition")
				}
				if ctx.Err() == nil && (len(facts) != 1 || facts[0].Condition == agentmodem.DeviceReady || facts[0].AT.State == agentmodem.ATControlReady) {
					t.Fatalf("unsafe ownership published: %+v", facts)
				}
			})
		}
	}
	t.Run("cancel between bearers", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		manager := &acquisitionVoiceManager{acquisitionManager: &acquisitionManager{afterDisconnect: cancel}, idle: true}
		prober, snapshot, ports := acquisitionFixture(t, manager)
		snapshot.Connected, snapshot.Bearers = true, []dbus.ObjectPath{"/bearer/1", "/bearer/2"}
		manager.inventory = []modemSnapshot{snapshot}
		_, _ = prober.Probe(ctx)
		if len(manager.disconnected) != 1 || len(manager.inhibits) != 0 || len(*ports) != 0 {
			t.Fatal("cancelled cleanup continued to another bearer")
		}
	})
	checkAcquisitionExistingOwners(t)
}

func TestLinuxAcquisitionConflictRetiresCachedOwnerUntilFreshHandoff(t *testing.T) {
	manager := &acquisitionVoiceManager{acquisitionManager: &acquisitionManager{}, idle: true}
	prober, snapshot, ports := acquisitionFixture(t, manager)
	manager.inventory = []modemSnapshot{snapshot}
	ready := acquisitionProbe(t, prober)
	if len(ready) != 1 || ready[0].Condition != agentmodem.DeviceReady || len(*ports) != 1 {
		t.Fatalf("initial ownership not ready: %+v", ready)
	}
	oldSession := ready[0].SIM.SessionGeneration
	manager.inventory = nil
	if facts := acquisitionProbe(t, prober); len(facts) != 1 || facts[0].Condition != agentmodem.DeviceReady || (*ports)[0].closed {
		t.Fatalf("normal inhibited inventory absence invalidated owner: %+v", facts)
	}
	snapshot.Connected = true
	snapshot.Bearers = []dbus.ObjectPath{"/org/freedesktop/ModemManager1/Bearer/1"}
	manager.inventory, manager.idle = []modemSnapshot{snapshot}, false
	before := len((*ports)[0].commands)
	for _, absent := range []bool{false, true} {
		if absent {
			manager.inventory = nil
		}
		facts := acquisitionProbe(t, prober)
		if len(facts) != 1 || facts[0].Condition != agentmodem.DeviceDegraded || facts[0].AT.State == agentmodem.ATControlReady ||
			facts[0].Network.Data == agentmodem.DataDisconnected || facts[0].ContinuityEpoch != "" || facts[0].SIM.SessionGeneration != "" {
			t.Fatalf("conflicting ownership republished cached ready/disconnected: %+v", facts)
		}
		if len(manager.disconnected) != 0 || len(manager.inhibits) != 1 || len(*ports) != 1 || !(*ports)[0].closed || len((*ports)[0].commands) != before {
			t.Fatal("conflict mutated MM while busy or retained/reopened stale AT owner")
		}
	}
	manager.idle, manager.inventory = true, []modemSnapshot{snapshot}
	for attempt := 0; attempt < 2; attempt++ {
		facts := acquisitionProbe(t, prober)
		if len(facts) != 1 || facts[0].Network.Data != agentmodem.DataConnected || facts[0].Condition == agentmodem.DeviceReady || len(manager.inhibits) != 1 {
			t.Fatalf("disconnect acknowledgement replaced actual readback: %+v", facts)
		}
	}
	if len(manager.disconnected) != 2 {
		t.Fatal("idle protected stale bearer was not reconciled")
	}
	snapshot.Connected = false
	manager.inventory = []modemSnapshot{snapshot}
	facts := acquisitionProbe(t, prober)
	if len(facts) != 1 || facts[0].Condition != agentmodem.DeviceReady || facts[0].Network.Data != agentmodem.DataDisconnected ||
		facts[0].SIM.SessionGeneration == "" || facts[0].SIM.SessionGeneration == oldSession || len(*ports) != 2 || len(manager.inhibits) != 2 {
		t.Fatalf("fresh inhibition did not recover exact new AT/SIM ownership: %+v", facts)
	}
}

func checkAcquisitionExistingOwners(t *testing.T) {
	t.Helper()
	t.Run("data claim", func(t *testing.T) {
		manager := &acquisitionVoiceManager{acquisitionManager: &acquisitionManager{}, idle: true}
		prober, snapshot, _ := acquisitionFixture(t, manager)
		manager.inventory = []modemSnapshot{snapshot}
		ready := acquisitionProbe(t, prober)[0]
		path := dbus.ObjectPath("/org/freedesktop/ModemManager1/Bearer/1")
		snapshot.Connected, snapshot.Bearers, snapshot.BearerStates = true, []dbus.ObjectPath{path}, map[dbus.ObjectPath]bool{path: true}
		claim := &dataClaim{uid: snapshot.UID, commandSnapshot: snapshot,
			target: agentdata.Target{EquipmentID: snapshot.EquipmentID, CardID: snapshot.ICCID, AttachmentID: ready.AttachmentID, SIMSessionGeneration: ready.SIM.SessionGeneration},
			bearer: dataBearer{ObjectPath: path}}
		prober.data[snapshot.EquipmentID] = claim
		t.Cleanup(func() { delete(prober.data, snapshot.EquipmentID) })
		manager.inventory, manager.idle = []modemSnapshot{snapshot}, false
		facts := acquisitionProbe(t, prober)
		if len(facts) != 1 || facts[0].Network.Data != agentmodem.DataConnected || prober.data[snapshot.EquipmentID] != claim || len(manager.disconnected) != 0 || len(manager.inhibits) != 1 {
			t.Fatalf("existing data claim replaced by acquisition cleanup: %+v", facts)
		}
	})
	t.Run("serial without MM voice", func(t *testing.T) {
		manager := &acquisitionManager{}
		prober, snapshot, _ := acquisitionFixture(t, manager)
		prober.serialOnly = true
		manager.inventory = []modemSnapshot{snapshot}
		facts := acquisitionProbe(t, prober)
		if len(facts) != 1 || facts[0].Condition != agentmodem.DeviceReady || len(manager.inhibits) != 1 {
			t.Fatalf("serial-only mode incorrectly required MM Voice: %+v", facts)
		}
	})
}

func TestLinuxAcquisitionKeepsInactivePINRecovery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]dbus.Variant, managedObjects)
		allow  bool
	}{
		{"PIN", func(map[string]dbus.Variant, managedObjects) {}, true},
		{"PUK", func(p map[string]dbus.Variant, _ managedObjects) { p["UnlockRequired"] = dbus.MakeVariant(uint32(4)) }, true},
		{"disabled", func(p map[string]dbus.Variant, _ managedObjects) {
			p["State"] = dbus.MakeVariant(int32(3))
			delete(p, "UnlockRequired")
		}, true},
		{"missing state", func(p map[string]dbus.Variant, _ managedObjects) { delete(p, "State") }, false},
		{"wrong state type", func(p map[string]dbus.Variant, _ managedObjects) { p["State"] = dbus.MakeVariant(uint32(2)) }, false},
		{"initializing", func(p map[string]dbus.Variant, _ managedObjects) { p["State"] = dbus.MakeVariant(int32(1)) }, false},
		{"enabled", func(p map[string]dbus.Variant, _ managedObjects) { p["State"] = dbus.MakeVariant(int32(6)) }, false},
		{"missing lock", func(p map[string]dbus.Variant, _ managedObjects) { delete(p, "UnlockRequired") }, false},
		{"wrong lock type", func(p map[string]dbus.Variant, _ managedObjects) { p["UnlockRequired"] = dbus.MakeVariant(int32(2)) }, false},
		{"unlocked", func(p map[string]dbus.Variant, _ managedObjects) { p["UnlockRequired"] = dbus.MakeVariant(uint32(1)) }, false},
		{"missing bearers", func(p map[string]dbus.Variant, _ managedObjects) { delete(p, "Bearers") }, false},
		{"wrong bearer type", func(p map[string]dbus.Variant, _ managedObjects) { p["Bearers"] = dbus.MakeVariant([]string{}) }, false},
		{"unknown bearer", func(p map[string]dbus.Variant, o managedObjects) {
			p["Bearers"] = dbus.MakeVariant([]dbus.ObjectPath{"/bearer/1"})
			o["/bearer/1"] = map[string]map[string]dbus.Variant{mmBearer: {}}
		}, false},
		{"connected bearer", func(p map[string]dbus.Variant, o managedObjects) {
			p["Bearers"] = dbus.MakeVariant([]dbus.ObjectPath{"/bearer/1"})
			o["/bearer/1"] = map[string]map[string]dbus.Variant{mmBearer: {"Connected": dbus.MakeVariant(true)}}
		}, false},
		{"Voice exists but unreadable", func(_ map[string]dbus.Variant, o managedObjects) {
			o["/org/freedesktop/ModemManager1/Modem/0"][mmModem+".Voice"] = map[string]dbus.Variant{}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &acquisitionVoiceManager{acquisitionManager: &acquisitionManager{}, voiceErr: errors.New("Voice query unavailable")}
			prober, snapshot, ports := acquisitionFixture(t, manager, "SIM PIN")
			p := map[string]dbus.Variant{
				"Device": dbus.MakeVariant(snapshot.UID), "EquipmentIdentifier": dbus.MakeVariant(snapshot.EquipmentID),
				"Ports": dbus.MakeVariant([]testPortTuple{{"ttyUSB2", mmPortAT}}), "State": dbus.MakeVariant(int32(2)),
				"UnlockRequired": dbus.MakeVariant(uint32(2)), "Bearers": dbus.MakeVariant([]dbus.ObjectPath{}), "Sim": dbus.MakeVariant(dbus.ObjectPath("/sim/1")),
			}
			o := managedObjects{snapshot.ObjectPath: {mmModem: p}, "/sim/1": {mmSIM: {"SimIdentifier": dbus.MakeVariant(snapshot.ICCID)}}}
			tc.change(p, o)
			inventory, err := parseManagedObjects(o)
			if err != nil || len(inventory) != 1 {
				t.Fatalf("inventory: %+v %v", inventory, err)
			}
			manager.inventory = inventory
			facts := acquisitionProbe(t, prober)
			if len(facts) != 1 {
				t.Fatalf("facts=%+v", facts)
			}
			if tc.allow {
				if facts[0].AT.State != agentmodem.ATControlReady || len(*ports) != 1 || len(manager.inhibits) != 1 {
					t.Fatalf("inactive Voice-less modem lost PIN/AT recovery: %+v", facts)
				}
				if tc.name == "PIN" {
					result, err := prober.EnterSIMPIN(context.Background(), agentmodem.SIMPINRequest{EquipmentID: snapshot.EquipmentID, AttachmentID: facts[0].AttachmentID, CardID: snapshot.ICCID, PIN: "1234"})
					if err != nil || !result.Attempted || !result.Ready {
						t.Fatalf("PIN recovery did not finish: %+v %v", result, err)
					}
				}
			} else if facts[0].AT.State == agentmodem.ATControlReady || len(*ports) != 0 || len(manager.inhibits) != 0 || len(manager.disconnected) != 0 {
				t.Fatalf("unknown/active Voice or bearer admitted exception: %+v", facts)
			}
		})
	}
}
