package agentsim

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
)

func TestReadReaderSIMIdentityUsesUSIMFilesWithoutPINAttempt(t *testing.T) {
	commands := make([][]byte, 0)
	card := readerIdentityCard(t, "234100000000001", 2, "+447785016005", false, &commands)
	fact := readReaderSIMIdentity(context.Background(), card)
	if fact.IdentityState != "ready" || fact.IMSI != "234100000000001" || fact.MCC != "234" ||
		fact.MNC != "10" || fact.SMSC != "+447785016005" || fact.ErrorCode != "" {
		t.Fatalf("identity=%+v", fact)
	}
	for _, command := range commands {
		if len(command) > 1 && (command[1] == 0x20 || command[1] == 0x24 || command[1] == 0x26 || command[1] == 0x28) {
			t.Fatalf("identity read sent PIN command %X", command)
		}
	}
}

func TestReadReaderSIMIdentityReportsPINRequiredWithoutGuessing(t *testing.T) {
	card := readerIdentityCard(t, "234100000000001", 2, "", true, nil)
	fact := readReaderSIMIdentity(context.Background(), card)
	if fact.IdentityState != "pin_required" || fact.ErrorCode != "reader_sim_pin_required" ||
		fact.IMSI != "" || fact.MCC != "" || fact.MNC != "" || fact.SMSC != "" {
		t.Fatalf("identity=%+v", fact)
	}
}

func TestReadReaderSIMIdentityKeepsUnknownMNCLengthPartial(t *testing.T) {
	card := readerIdentityCard(t, "310260123456789", 0, "", false, nil)
	fact := readReaderSIMIdentity(context.Background(), card)
	if fact.IdentityState != "partial" || fact.IMSI != "310260123456789" || fact.MCC != "310" ||
		fact.MNC != "" || fact.ErrorCode != "reader_sim_mnc_length_unavailable" {
		t.Fatalf("identity=%+v", fact)
	}
}

func TestSuccessfulPINVerifyRefreshesReaderSIMIdentity(t *testing.T) {
	const cardID = "8944000000000000001"
	card := readerIdentityCard(t, "208150123456789", 2, "", true, nil)
	manager, err := NewManager(fakeConnector{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	current := &session{readerName: "reader", generation: "session", cardID: cardID,
		card: card, ctx: context.Background()}
	current.active.Store(true)
	manager.sessions[current.generation] = current
	response := manager.ExecuteSIMPIN(context.Background(), agentlink.SIMPINRequest{
		OperationID: "verify-reader-pin", ProcessGeneration: "process", ReaderName: "reader",
		SIMSessionGeneration: "session", CardID: cardID, Action: agentlink.SIMPINVerify, PIN: "1721",
	})
	if response.Failure != nil || response.State != "verified" {
		t.Fatalf("verify response=%+v", response)
	}
	views := manager.Sessions()
	if len(views) != 1 || views[0].SIM == nil || views[0].SIM.IdentityState != "ready" ||
		views[0].SIM.IMSI != "208150123456789" || views[0].SIM.MNC != "15" {
		t.Fatalf("views=%+v", views)
	}
}

func TestReaderSessionPublishesReadOnlyUSIMIdentity(t *testing.T) {
	card := readerIdentityCard(t, "454120123456789", 2, "+85255555555", false, nil)
	manager, err := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "session"})
	}()
	waitForSession(t, manager, "session")
	views := manager.Sessions()
	if len(views) != 1 || views[0].SIM == nil || views[0].SIM.IdentityState != "ready" ||
		views[0].SIM.IMSI != "454120123456789" || views[0].SIM.MNC != "12" {
		t.Fatalf("views=%+v", views)
	}
	cancel()
	<-done
}

func TestDecodeIMSIAndAddressRejectInvalidBCD(t *testing.T) {
	if _, err := decodeIMSI([]byte{2, 0x2A, 0x43}); err == nil {
		t.Fatal("invalid IMSI BCD was accepted")
	}
	if _, err := decodeTONBCD([]byte{2, 0x91, 0xFA}); err == nil {
		t.Fatal("invalid address BCD was accepted")
	}
}

func TestDecodeIMSICoversVoHiveOddEvenAndFillerVectors(t *testing.T) {
	for name, test := range map[string]struct {
		raw  []byte
		want string
	}{
		"odd":  {[]byte{0x08, 0x39, 0x01, 0x62, 0x10, 0x32, 0x54, 0x76, 0x98}, "310260123456789"},
		"even": {[]byte{0x08, 0x01, 0x10, 0x10, 0x10, 0x32, 0x54, 0x76, 0xF8}, "00101012345678"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeIMSI(test.raw)
			if err != nil || got != test.want {
				t.Fatalf("decodeIMSI()=%q/%v want %q", got, err, test.want)
			}
		})
	}
	for name, raw := range map[string][]byte{
		"wrong type":      {0x01, 0x38},
		"length overflow": {0x09, 0x39, 0x01, 0x62, 0x10, 0x32, 0x54, 0x76, 0x98},
		"even filler":     {0x02, 0x31, 0x98},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := decodeIMSI(raw); err == nil {
				t.Fatalf("decodeIMSI()=%q without error", got)
			}
		})
	}
}

func TestSelectApplicationScansLateDirectoryRecordAndFallsBackP2(t *testing.T) {
	aid, _ := hex.DecodeString("A0000000871002FF49FF0189")
	lateRecord, p204, p200 := false, false, false
	card := &fakeCard{handler: func(command []byte) ([]byte, error) {
		switch {
		case len(command) >= 7 && command[1] == 0xA4 && command[2] == 0x00:
			return []byte{0x90, 0x00}, nil
		case len(command) == 5 && command[1] == 0xB2:
			if command[2] == 21 {
				lateRecord = true
				body := append([]byte{0x61, byte(len(aid) + 2), 0x4F, byte(len(aid))}, aid...)
				return append(body, 0x90, 0x00), nil
			}
			if command[2] > 21 {
				return []byte{0x6A, 0x83}, nil
			}
			return []byte{0x90, 0x00}, nil
		case len(command) > 5 && command[1] == 0xA4 && command[2] == 0x04:
			if command[3] == 0x04 {
				p204 = true
				return []byte{0x6A, 0x86}, nil
			}
			p200 = true
			return []byte{0x90, 0x00}, nil
		default:
			return []byte{0x6A, 0x82}, nil
		}
	}}
	if err := selectApplication(context.Background(), card, agentlink.AKAApplicationUSIM); err != nil {
		t.Fatal(err)
	}
	if !lateRecord || !p204 || !p200 {
		t.Fatalf("late=%v p2=04:%v p2=00:%v", lateRecord, p204, p200)
	}
}

func readerIdentityCard(t *testing.T, imsi string, mncLength int, smsc string, pinRequired bool,
	commands *[][]byte) *fakeCard {
	t.Helper()
	imsiData := encodeTestIMSI(t, imsi)
	selectedFile := uint16(0)
	locked := pinRequired
	return &fakeCard{handler: func(command []byte) ([]byte, error) {
		if commands != nil {
			*commands = append(*commands, append([]byte(nil), command...))
		}
		if len(command) >= 7 && command[1] == 0xA4 && command[2] == 0x00 {
			selectedFile = uint16(command[5])<<8 | uint16(command[6])
			if selectedFile == 0x6F42 {
				return append([]byte{0x62, 0x07, 0x82, 0x05, 0x42, 0x21, 0x00, 0x1C, 0x01}, 0x90, 0x00), nil
			}
			return []byte{0x90, 0x00}, nil
		}
		if len(command) >= 5 && command[1] == 0xA4 && command[2] == 0x04 {
			return []byte{0x90, 0x00}, nil
		}
		if bytes.Equal(command, []byte{0x00, 0xB2, 0x01, 0x04, 0x00}) {
			aid, _ := hex.DecodeString("A0000000871002")
			body := append([]byte{0x61, byte(len(aid) + 2), 0x4F, byte(len(aid))}, aid...)
			return append(body, 0x90, 0x00), nil
		}
		if len(command) == 5 && command[1] == 0xB2 && command[4] == 0x1C {
			record := bytes.Repeat([]byte{0xFF}, 28)
			if smsc != "" {
				field := encodeTestAddress(t, smsc)
				copy(record[13:25], field)
			}
			return append(record, 0x90, 0x00), nil
		}
		if len(command) == 5 && command[1] == 0xB2 {
			return []byte{0x6A, 0x83}, nil
		}
		if len(command) == 5 && command[1] == 0x20 {
			if locked {
				return []byte{0x63, 0xC3}, nil
			}
			return []byte{0x90, 0x00}, nil
		}
		if len(command) == 13 && command[1] == 0x20 {
			locked = false
			return []byte{0x90, 0x00}, nil
		}
		if len(command) == 5 && command[1] == 0xB0 {
			switch selectedFile {
			case 0x2FE2:
				return append(encodeICCID("8944000000000000001"), 0x90, 0x00), nil
			case 0x6F07:
				if locked {
					return []byte{0x69, 0x82}, nil
				}
				return append(imsiData, 0x90, 0x00), nil
			case 0x6FAD:
				return []byte{0x00, 0x00, 0x00, byte(mncLength), 0x90, 0x00}, nil
			}
		}
		return []byte{0x6A, 0x82}, nil
	}}
}

func encodeTestIMSI(t *testing.T, imsi string) []byte {
	t.Helper()
	digits := "9" + imsi
	if len(digits)%2 != 0 {
		digits += "F"
	}
	result := []byte{byte(len(digits) / 2)}
	for index := 0; index < len(digits); index += 2 {
		low, high := testNibble(t, digits[index]), testNibble(t, digits[index+1])
		result = append(result, low|high<<4)
	}
	return result
}

func encodeTestAddress(t *testing.T, number string) []byte {
	t.Helper()
	ton := byte(0x81)
	if len(number) > 0 && number[0] == '+' {
		ton, number = 0x91, number[1:]
	}
	if len(number)%2 != 0 {
		number += "F"
	}
	digits := make([]byte, 0, len(number)/2)
	for index := 0; index < len(number); index += 2 {
		digits = append(digits, testNibble(t, number[index])|testNibble(t, number[index+1])<<4)
	}
	return append([]byte{byte(len(digits) + 1), ton}, digits...)
}

func testNibble(t *testing.T, value byte) byte {
	t.Helper()
	if value == 'F' {
		return 0x0F
	}
	if value < '0' || value > '9' {
		t.Fatalf("invalid test digit %q", value)
	}
	return value - '0'
}
