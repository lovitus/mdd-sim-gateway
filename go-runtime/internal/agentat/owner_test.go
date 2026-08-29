package agentat

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type fakePort struct {
	mu        sync.Mutex
	responses map[string][]byte
	command   string
	delivered bool
	closed    int
}

func (port *fakePort) Read(buffer []byte) (int, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	if port.closed != 0 {
		return 0, io.ErrClosedPipe
	}
	if port.delivered {
		return 0, nil
	}
	value := port.responses[port.command]
	if value == nil {
		return 0, nil
	}
	port.delivered = true
	return copy(buffer, value), nil
}

func (port *fakePort) Write(value []byte) (int, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	if port.closed != 0 {
		return 0, io.ErrClosedPipe
	}
	port.command = string(value[:len(value)-1])
	port.delivered = false
	return len(value), nil
}

func (port *fakePort) Close() error {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.closed++
	return nil
}

func (port *fakePort) Drain() error { return nil }

func (port *fakePort) ResetInputBuffer() error {
	port.mu.Lock()
	port.delivered = false
	port.mu.Unlock()
	return nil
}

func modemPort(equipmentID string) *fakePort {
	return &fakePort{responses: map[string][]byte{
		"AT":        []byte("AT\r\r\nOK\r\n"),
		"AT+CGSN":   []byte("AT+CGSN\r\r\n" + equipmentID + "\r\nOK\r\n"),
		"AT+CLCC":   []byte("\r\nOK\r\n"),
		"AT+CMGF=?": []byte("\r\n+CMGF: (0,1)\r\nOK\r\n"),
	}}
}

type fakeBusyError struct{}

func (fakeBusyError) Error() string { return "busy" }
func (fakeBusyError) Busy() bool    { return true }

func TestDiscoverPrefersModemPortAndRetainsExclusiveOwner(t *testing.T) {
	const equipmentID = "862547055201716"
	opened := []string{}
	ports := map[string]*fakePort{"COM9": modemPort(equipmentID), "COM16": modemPort(equipmentID)}
	owner, err := Discover(context.Background(), equipmentID, []Candidate{
		{Name: "COM9", Product: "Quectel USB AT Port", USB: true},
		{Name: "COM16", Product: "Quectel USB Modem", USB: true},
		{Name: "COM8", Product: "Quectel NMEA Port", USB: true},
	}, func(candidate Candidate) (Port, error) {
		opened = append(opened, candidate.Name)
		return ports[candidate.Name], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if owner.Name() != "COM16" || len(opened) != 1 || opened[0] != "COM16" {
		t.Fatalf("owner=%s opened=%v", owner.Name(), opened)
	}
	if capabilities := owner.Capabilities(); !capabilities.CallSignalling || !capabilities.SMS || capabilities.SIMAPDU {
		t.Fatalf("capabilities=%+v", capabilities)
	}
	if ports["COM16"].closed != 0 {
		t.Fatal("matching port was not retained")
	}
	if err := owner.Close(); err != nil || ports["COM16"].closed != 1 {
		t.Fatalf("close err=%v count=%d", err, ports["COM16"].closed)
	}
}

func TestDiscoverClosesMismatchAndReportsBusy(t *testing.T) {
	mismatch := modemPort("111111111111111")
	_, err := Discover(context.Background(), "862547055201716", []Candidate{
		{Name: "COM4", Product: "USB Modem", USB: true},
		{Name: "COM5", Product: "USB AT Port", USB: true},
	}, func(candidate Candidate) (Port, error) {
		if candidate.Name == "COM5" {
			return nil, fakeBusyError{}
		}
		return mismatch, nil
	})
	var discovery DiscoveryError
	if !errors.As(err, &discovery) || !discovery.Busy || mismatch.closed != 1 {
		t.Fatalf("err=%v mismatch.closed=%d", err, mismatch.closed)
	}
}

func TestManagerRetainsHealthyOwnerAndClosesRemovedTarget(t *testing.T) {
	const equipmentID = "862547055201716"
	port := modemPort(equipmentID)
	opens := 0
	manager, err := NewManager(
		func() ([]Candidate, error) { return []Candidate{{Name: "COM16", Product: "USB Modem", USB: true}}, nil },
		func(Candidate) (Port, error) { opens++; return port, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	first := manager.Reconcile(context.Background(), []Target{{AttachmentID: "mbn-a", EquipmentID: equipmentID}})
	second := manager.Reconcile(context.Background(), []Target{{AttachmentID: "mbn-a", EquipmentID: equipmentID}})
	if first["mbn-a"].State != "ready" || second["mbn-a"].State != "ready" || opens != 1 || port.closed != 0 {
		t.Fatalf("first=%+v second=%+v opens=%d closed=%d", first, second, opens, port.closed)
	}
	manager.Reconcile(context.Background(), nil)
	if port.closed != 1 {
		t.Fatalf("removed target close count=%d", port.closed)
	}
}

func TestManagerDegradesEnumerationWithoutDroppingOwnedHandle(t *testing.T) {
	const equipmentID = "862547055201716"
	port := modemPort(equipmentID)
	fail := false
	manager, _ := NewManager(
		func() ([]Candidate, error) {
			if fail {
				return nil, errors.New("SetupAPI unavailable")
			}
			return []Candidate{{Name: "COM16", Product: "USB Modem", USB: true}}, nil
		},
		func(Candidate) (Port, error) { return port, nil },
	)
	manager.healthEvery = time.Hour
	manager.Reconcile(context.Background(), []Target{{AttachmentID: "mbn-a", EquipmentID: equipmentID}})
	fail = true
	result := manager.Reconcile(context.Background(), []Target{{AttachmentID: "mbn-a", EquipmentID: equipmentID}})
	if result["mbn-a"].State != "degraded" || result["mbn-a"].Detail == "" || port.closed != 0 {
		t.Fatalf("result=%+v closed=%d", result, port.closed)
	}
}

func TestManagerRejectsEveryDuplicateEquipmentAttachment(t *testing.T) {
	manager, _ := NewManager(
		func() ([]Candidate, error) { return nil, nil },
		func(Candidate) (Port, error) { t.Fatal("duplicate identity attempted discovery"); return nil, nil },
	)
	result := manager.Reconcile(context.Background(), []Target{
		{AttachmentID: "mbn-a", EquipmentID: "862547055201716"},
		{AttachmentID: "mbn-b", EquipmentID: "862547055201716"},
		{AttachmentID: "mbn-c", EquipmentID: "862547055201716"},
	})
	for _, attachmentID := range []string{"mbn-a", "mbn-b", "mbn-c"} {
		if result[attachmentID].State != "degraded" || result[attachmentID].Detail == "" {
			t.Fatalf("%s snapshot=%+v", attachmentID, result[attachmentID])
		}
	}
}

func TestExchangeRejectsCommandInjection(t *testing.T) {
	owner := &Owner{port: modemPort("862547055201716")}
	if _, err := owner.Exchange(context.Background(), "AT\rATD123;", time.Second); err == nil {
		t.Fatal("command injection was accepted")
	}
}

func TestDialAndAnswerExposeOnlyFixedValidatedCommands(t *testing.T) {
	port := modemPort("862547055201716")
	port.responses["ATD+448001076285;"] = []byte("\r\nOK\r\n")
	port.responses["ATA"] = []byte("\r\nOK\r\n")
	owner := &Owner{port: port}
	if result, err := owner.Dial(context.Background(), "+448001076285"); err != nil || result.State != "idle" {
		t.Fatalf("dial result=%+v err=%v", result, err)
	}
	if result, err := owner.Answer(context.Background()); err != nil || result.State != "idle" {
		t.Fatalf("answer result=%+v err=%v", result, err)
	}
	if _, err := owner.Dial(context.Background(), "123;ATH"); err == nil {
		t.Fatal("dial accepted command injection")
	}
}
