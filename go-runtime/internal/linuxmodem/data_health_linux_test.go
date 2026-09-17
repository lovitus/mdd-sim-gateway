//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func TestForcedCloseRecoveryRequiresExactModemManagerError(t *testing.T) {
	closed := dbus.Error{Name: "org.freedesktop.ModemManager1.Error.Core.Connected", Body: []interface{}{"Could not open serial device ttyUSB2: it has been forced close"}}
	for _, err := range []error{closed, &closed, fmt.Errorf("command: %w", closed)} {
		if !isForcedClosedPort(err) {
			t.Fatal("known forced-close rejected")
		}
	}
	for _, err := range []error{errors.New("has been forced close"), dbus.Error{Name: closed.Name, Body: []interface{}{"port is connected"}}, dbus.Error{Name: "org.freedesktop.DBus.Error.NoReply", Body: closed.Body}} {
		if isForcedClosedPort(err) {
			t.Fatal("unrelated error authorized ownership recovery")
		}
	}
}

type busyVoiceManager struct{ testModemManager }

func (*busyVoiceManager) VoiceIdle(context.Context, dbus.ObjectPath) (bool, error) { return false, nil }

func TestDataRecoveryCannotInhibitAModemWithAnActiveCall(t *testing.T) {
	target := agentdata.Target{EquipmentID: "equipment", AttachmentID: "attachment", CardID: "card"}
	claim := &dataClaim{target: target}
	manager := &busyVoiceManager{}
	prober := &Prober{manager: manager, data: map[string]*dataClaim{target.EquipmentID: claim}}
	if err := prober.stopDataLocked(target); err == nil || claim.cleanup || len(manager.inhibits) != 0 {
		t.Fatal("active call permitted destructive ownership recovery")
	}
}

func TestDataClaimFollowsExactLiveBearerInsteadOfCachedConnection(t *testing.T) {
	path := dbus.ObjectPath("/org/freedesktop/ModemManager1/Bearer/141")
	snapshot := modemSnapshot{ObjectPath: "/org/freedesktop/ModemManager1/Modem/91", UID: "usb", EquipmentID: "equipment", ICCID: "card", SIMState: agentmodem.SIMReady}
	claim := &dataClaim{uid: "usb", commandSnapshot: snapshot, target: agentdata.Target{EquipmentID: "equipment", CardID: "card"}, bearer: dataBearer{ObjectPath: path}, observedData: agentmodem.DataConnected}
	prober := &Prober{data: map[string]*dataClaim{"equipment": claim}}
	for _, tc := range []struct {
		name   string
		states map[dbus.ObjectPath]bool
		busy   bool
		want   agentmodem.DataState
	}{
		{"connected", map[dbus.ObjectPath]bool{path: true}, true, agentmodem.DataConnected},
		{"network disconnected", map[dbus.ObjectPath]bool{path: false}, false, agentmodem.DataDisconnected},
		{"removed bearer", map[dbus.ObjectPath]bool{}, false, agentmodem.DataDisconnected},
		{"transition", map[dbus.ObjectPath]bool{path: false}, true, agentmodem.DataUnknown},
		{"unobserved", nil, false, agentmodem.DataUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot.BearerStates, snapshot.Connected = tc.states, tc.busy
			prober.observeDataClaims([]modemSnapshot{snapshot})
			if claim.observedData != tc.want {
				t.Fatalf("state=%s want=%s", claim.observedData, tc.want)
			}
		})
	}
	snapshot.BearerStates = map[dbus.ObjectPath]bool{path: true}
	for _, change := range []func(*modemSnapshot){
		func(s *modemSnapshot) { s.ICCID = "replacement" },
		func(s *modemSnapshot) { s.ObjectPath = "/org/freedesktop/ModemManager1/Modem/92" },
		func(s *modemSnapshot) { s.EquipmentID = "other" },
	} {
		changed := snapshot
		change(&changed)
		prober.observeDataClaims([]modemSnapshot{changed})
		if claim.observedData != agentmodem.DataUnknown {
			t.Fatal("replacement inherited bearer health")
		}
	}
	prober.observeDataClaims([]modemSnapshot{snapshot, snapshot})
	if claim.observedData != agentmodem.DataUnknown {
		t.Fatal("ambiguous owner accepted")
	}
}

func TestDisconnectedDataCannotDialAndKeepsATFailureVisible(t *testing.T) {
	path := dbus.ObjectPath("/org/freedesktop/ModemManager1/Bearer/141")
	snapshot := modemSnapshot{ObjectPath: "/org/freedesktop/ModemManager1/Modem/91", UID: "usb", EquipmentID: "equipment", ICCID: "card", SIMState: agentmodem.SIMReady, BearerStates: map[dbus.ObjectPath]bool{path: false}}
	target := agentdata.Target{EquipmentID: "equipment", CardID: "card", AttachmentID: "attachment"}
	claim := &dataClaim{uid: "usb", commandSnapshot: snapshot, target: target, bearer: dataBearer{ObjectPath: path, Interface: "wwan0", Address: "10.0.0.2"}, observedData: agentmodem.DataConnected}
	manager := &testModemManager{inventory: []modemSnapshot{snapshot}}
	prober := &Prober{manager: manager, data: map[string]*dataClaim{"equipment": claim}, atSnapshot: map[string]agentat.Snapshot{"attachment": {State: "unavailable", Detail: "ttyUSB2 has been forced close"}}}
	if _, err := prober.DialData(context.Background(), target, "tcp", "127.0.0.1:9"); err == nil {
		t.Fatal("disconnected bearer admitted a data socket")
	}
	current := &ownedDevice{snapshot: snapshot, usb: usbGeneration{AttachmentID: "attachment"}, lastFact: agentmodem.Fact{EquipmentID: "equipment"}}
	fact := prober.dataFact(current, claim)
	if fact.Network.Data != agentmodem.DataDisconnected || fact.Network.Interface != "" || fact.Network.Address != "" || fact.Network.CountersAvailable {
		t.Fatalf("stale network fact: %+v", fact.Network)
	}
	if fact.AT.Detail != "ttyUSB2 has been forced close" || fact.AT.CallSignalling || fact.AT.SMS {
		t.Fatalf("AT fault hidden: %+v", fact.AT)
	}
}
