package agentat

import (
	"context"
	"testing"
)

func TestProfiledUSBIdentityUsesOnlyATAndCGSN(t *testing.T) {
	port := modemPort("862547055201716")
	got, err := IdentifyProfiledPort(context.Background(), Candidate{Name: "ttyUSB2", USB: true, PhysicalID: "/sys/devices/usb1/1-2"}, func(Candidate) (Port, error) { return port, nil })
	if err != nil || got != "862547055201716" || port.closed != 1 {
		t.Fatal("profile identity not read and closed", err)
	}
	if len(port.commands) != 2 || port.commands[0] != "AT" || port.commands[1] != "AT+CGSN" {
		t.Fatal("unexpected identification command", port.commands)
	}
	opened := false
	if _, err := IdentifyProfiledPort(context.Background(), Candidate{Name: "ttyS0"}, func(Candidate) (Port, error) { opened = true; return port, nil }); err == nil || opened {
		t.Fatal("unprofiled port opened")
	}
}
