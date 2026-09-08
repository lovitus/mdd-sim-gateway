package agentat

import (
	"context"
	"fmt"
	"testing"
)

func TestReadMNCLengthUsesReadOnlyUSIMChannelAndCloses(t *testing.T) {
	for _, length := range []int{2, 3, 15} {
		port := &fakePort{responses: map[string][]byte{
			`AT+CCHO="A0000000871002"`:        []byte("\r\n+CCHO: 2\r\nOK\r\n"),
			`AT+CGLA=2,16,"00A40004026FAD00"`: []byte("\r\n+CGLA: 4,\"9000\"\r\nOK\r\n"),
			`AT+CGLA=2,10,"00B0000004"`:       []byte(fmt.Sprintf("\r\n+CGLA: 12,\"000000%02X9000\"\r\nOK\r\n", length)),
			`AT+CCHC=2`:                       []byte("\r\nOK\r\n"),
		}}
		owner := &Owner{port: port, capabilities: Capabilities{SIMAPDU: true}}
		got, err := owner.ReadMNCLength(context.Background())
		if length == 15 {
			if err == nil || got != 0 {
				t.Fatal("unknown MNC length guessed")
			}
		} else if err != nil || got != length {
			t.Fatal("MNC length read failed", err)
		}
		if len(port.commands) != 4 || port.commands[3] != "AT+CCHC=2" {
			t.Fatalf("unexpected SIM commands: %v", port.commands)
		}
	}
}

func TestReadMNCLengthRequiresExplicitAPDUCapability(t *testing.T) {
	port := &fakePort{}
	owner := &Owner{port: port}
	if _, err := owner.ReadMNCLength(context.Background()); err == nil || len(port.commands) != 0 {
		t.Fatal("identity read bypassed APDU mode")
	}
}

func TestReadHomePLMNChecksCurrentCardAndUsesEFADLength(t *testing.T) {
	const equipment = "862547055201716"
	const card = "8944100000000000001"
	const imsi = "234100000000001"
	port := modemSIMPort(equipment)
	port.responses["AT+CPIN?"] = []byte("\r\n+CPIN: READY\r\nOK\r\n")
	port.responses["AT+QCCID"] = []byte("\r\n+QCCID: " + card + "\r\nOK\r\n")
	port.responses["AT+CIMI"] = []byte("\r\n" + imsi + "\r\nOK\r\n")
	port.responses[`AT+CGLA=2,16,"00A40004026FAD00"`] = []byte("\r\n+CGLA: 4,\"9000\"\r\nOK\r\n")
	port.responses[`AT+CGLA=2,10,"00B0000004"`] = []byte("\r\n+CGLA: 12,\"000000029000\"\r\nOK\r\n")
	owner := &Owner{port: port, capabilities: Capabilities{SIMAPDU: true, CallSignalling: true}}
	manager := &Manager{owners: map[string]*managedOwner{equipment: {owner: owner}}}
	mcc, mnc, err := manager.ReadHomePLMN(context.Background(), equipment, card, imsi)
	if err != nil || mcc != "234" || mnc != "10" {
		t.Fatal("exact SIM identity read failed", err)
	}
	port.commands = nil
	if _, _, err := manager.ReadHomePLMN(context.Background(), equipment, "8944100000000000002", imsi); err == nil {
		t.Fatal("different SIM accepted")
	}
	for _, command := range port.commands {
		if command == `AT+CCHO="A0000000871002"` {
			t.Fatal("opened SIM channel after identity mismatch")
		}
	}
}

func TestAPDULengthCorrectionReplacesExistingLe(t *testing.T) {
	port := &fakePort{responses: map[string][]byte{
		`AT+CGLA=2,10,"00B0000004"`: []byte("\r\n+CGLA: 4,\"6C08\"\r\nOK\r\n"),
		`AT+CGLA=2,10,"00B0000008"`: []byte("\r\n+CGLA: 20,\"00000002000000009000\"\r\nOK\r\n"),
	}}
	owner := &Owner{port: port}
	result, err := owner.transmitAPDULocked(context.Background(), 2, []byte{0, 0xB0, 0, 0, 4})
	if err != nil || result.SW1 != 0x90 || len(port.commands) != 2 || port.commands[1] != `AT+CGLA=2,10,"00B0000008"` {
		t.Fatal("Le correction appended instead of replacing", err)
	}
}
