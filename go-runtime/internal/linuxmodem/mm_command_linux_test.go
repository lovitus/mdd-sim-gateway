//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type commandTestManager struct {
	testModemManager
	calls, submits int
}

func (m *commandTestManager) Command(_ context.Context, _ dbus.ObjectPath, _ string, _ time.Duration) ([]byte, error) {
	m.calls++
	return []byte("OK\r\n"), nil
}
func (m *commandTestManager) SendText(ctx context.Context, _ dbus.ObjectPath, _, _ string, check func(context.Context) error) ([]int, error) {
	if err := check(ctx); err != nil {
		return nil, err
	}
	m.submits++
	return []int{7}, nil
}

func TestMMCommandPortRechecksExactSIMWithoutOpeningTTY(t *testing.T) {
	snapshot := modemSnapshot{ObjectPath: "/org/freedesktop/ModemManager1/Modem/1", UID: "usb", EquipmentID: "equipment", ICCID: "card", SIMState: agentmodem.SIMReady}
	manager := &commandTestManager{testModemManager: testModemManager{inventory: []modemSnapshot{snapshot}}}
	port := &modemCommandPort{manager: manager, commands: manager, snapshot: snapshot}
	if _, err := port.Exchange(context.Background(), "AT+CLCC", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := port.SubmitSMSText(context.Background(), "+441234", "test"); err != nil {
		t.Fatal(err)
	}
	manager.inventory[0].ICCID = "replacement"
	if _, err := port.Exchange(context.Background(), "ATD123;", time.Second); !errors.Is(err, agentmodem.ErrOperationTargetReplaced) {
		t.Fatal("replaced SIM command accepted")
	}
	if _, err := port.SubmitSMSText(context.Background(), "+441234", "test"); !errors.Is(err, agentmodem.ErrOperationTargetReplaced) {
		t.Fatal("replaced SIM submitted SMS")
	}
	if manager.calls != 1 || manager.submits != 1 {
		t.Fatal("failed identity caused a command")
	}
	if _, err := port.Write([]byte("AT\r")); err == nil {
		t.Fatal("split serial write allowed")
	}
}

func TestDataOwnerOffersOnlyModemManagerCommandCandidate(t *testing.T) {
	manager := &commandTestManager{}
	prober, err := newProber(manager, false, "/helper", "/sys", func(agentat.Candidate) (agentat.Port, error) {
		t.Fatal("direct tty was opened while data active")
		return nil, errors.New("unexpected")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prober.at.Close()
	prober.devices["usb"] = &ownedDevice{snapshot: modemSnapshot{EquipmentID: "equipment", ATPorts: []string{"ttyUSB2"}}, usb: usbGeneration{PhysicalID: "physical", AttachmentID: "attachment"}}
	prober.data["equipment"] = &dataClaim{commandSnapshot: modemSnapshot{ObjectPath: "/org/freedesktop/ModemManager1/Modem/1"}}
	candidates, err := prober.enumerateAT()
	if err != nil || len(candidates) != 1 || candidates[0].Name != mmCommandPrefix+"/org/freedesktop/ModemManager1/Modem/1" {
		t.Fatalf("candidates %v %v", candidates, err)
	}
	prober.data["equipment"].cleanup = true
	candidates, err = prober.enumerateAT()
	if err != nil || len(candidates) != 0 {
		t.Fatal("cleanup allowed command ownership")
	}
}
