package agentat

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestManagerRecoversCallCapabilityWithoutReplacingOwner(t *testing.T) {
	const equipmentID = "862547055201716"
	port := modemPort(equipmentID)
	port.responses["AT+CLCC"] = []byte("\r\nERROR\r\n")
	now := time.Unix(1000, 0)
	opens := 0
	manager, err := NewManager(
		func() ([]Candidate, error) { return []Candidate{{Name: "COM16", Product: "USB Modem", USB: true}}, nil },
		func(Candidate) (Port, error) { opens++; return port, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.now = func() time.Time { return now }
	targets := []Target{{AttachmentID: "modem-a", EquipmentID: equipmentID}}
	first := manager.Reconcile(context.Background(), targets)["modem-a"]
	if first.State != "ready" || first.CallSignalling || !first.SMS {
		t.Fatalf("initial optional failure: %+v", first)
	}
	owner := manager.owners[equipmentID].owner
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = owner.Capabilities()
			}
		}
	}()
	defer func() { close(stop); <-done }()
	// A successful CLCC with data-mode entries proves command support, not a voice call.
	port.responses["AT+CLCC"] = []byte("\r\n+CLCC: 2,1,0,1,0,\"\",128\r\n+CLCC: 1,1,0,1,0,\"\",128\r\nOK\r\n")
	now = now.Add(10 * time.Second)
	after := manager.Reconcile(context.Background(), targets)["modem-a"]
	if !after.CallSignalling || after.State != "ready" || after.OwnerGeneration != first.OwnerGeneration || opens != 1 || port.closed != 0 {
		t.Fatalf("same-owner call capability did not recover: before=%+v after=%+v opens=%d closed=%d", first, after, opens, port.closed)
	}
	if after.SMS != first.SMS || after.SIMAPDU != first.SIMAPDU || owner.Capabilities().VoicePCM != true {
		t.Fatal("call recovery changed unrelated capabilities")
	}
	inventory, err := manager.CallInventory(context.Background(), equipmentID)
	if err != nil || !inventory.Authoritative || len(inventory.Records) != 0 {
		t.Fatalf("data entries became voice calls: %+v, %v", inventory, err)
	}
}

func TestCancelledCallCapabilityProbeCannotPublishSuccess(t *testing.T) {
	const equipmentID = "862547055201716"
	port := modemPort(equipmentID)
	port.responses["AT+CLCC"] = []byte("\r\nERROR\r\n")
	now := time.Unix(1000, 0)
	manager, err := NewManager(
		func() ([]Candidate, error) { return []Candidate{{Name: "COM16", USB: true}}, nil },
		func(Candidate) (Port, error) { return port, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.now = func() time.Time { return now }
	targets := []Target{{AttachmentID: "modem-a", EquipmentID: equipmentID}}
	first := manager.Reconcile(context.Background(), targets)["modem-a"]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port.responses["AT+CLCC"] = []byte("\r\nOK\r\n")
	port.beforeRead = func(command string) {
		if command == "AT+CLCC" {
			cancel()
		}
	}
	now = now.Add(10 * time.Second)
	after := manager.Reconcile(ctx, targets)["modem-a"]
	if after.CallSignalling || after.OwnerGeneration != first.OwnerGeneration || port.closed != 0 ||
		!strings.HasPrefix(after.Detail, "call_signalling_probe_cancelled; retry_at=") {
		t.Fatalf("cancelled proof was published or owner dropped: %+v closed=%d", after, port.closed)
	}
}

func TestManagerCallCapabilityRetryIsBoundedAndKeepsOwner(t *testing.T) {
	const equipmentID = "862547055201716"
	port := modemPort(equipmentID)
	port.responses["AT+CLCC"] = []byte("\r\nERROR\r\n")
	base := time.Unix(1000, 0)
	now := base
	manager, err := NewManager(
		func() ([]Candidate, error) { return []Candidate{{Name: "COM16", USB: true}}, nil },
		func(Candidate) (Port, error) { return port, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.now = func() time.Time { return now }
	targets := []Target{{AttachmentID: "modem-a", EquipmentID: equipmentID}}
	first := manager.Reconcile(context.Background(), targets)["modem-a"]
	for _, step := range []struct {
		seconds int
		probes  int
	}{
		{5, 1}, {10, 2}, {20, 2}, {30, 2}, {40, 3}, {90, 3},
		{100, 4}, {210, 4}, {220, 5}, {450, 5}, {460, 6},
		{750, 6}, {760, 7}, {1050, 7}, {1060, 8},
	} {
		now = base.Add(time.Duration(step.seconds) * time.Second)
		after := manager.Reconcile(context.Background(), targets)["modem-a"]
		probes := 0
		for _, command := range port.commands {
			if command == "AT+CLCC" {
				probes++
			}
		}
		if probes != step.probes || after.CallSignalling || after.State != "ready" || after.OwnerGeneration != first.OwnerGeneration || port.closed != 0 {
			t.Fatalf("at %ds: probes=%d want=%d before=%+v after=%+v closed=%d", step.seconds, probes, step.probes, first, after, port.closed)
		}
	}
	port.responses["AT+CLCC"] = []byte("\r\nOK\r\n")
	now = base.Add(1360 * time.Second)
	if after := manager.Reconcile(context.Background(), targets)["modem-a"]; !after.CallSignalling || after.OwnerGeneration != first.OwnerGeneration {
		t.Fatalf("capped backoff became a permanent negative cache: %+v", after)
	}
}
