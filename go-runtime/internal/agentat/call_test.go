package agentat

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type scriptedCallPort struct {
	mu        sync.Mutex
	responses map[string][][]byte
	commands  []string
}

func (port *scriptedCallPort) Exchange(_ context.Context, command string, _ time.Duration) ([]byte, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.commands = append(port.commands, command)
	queue := port.responses[command]
	if len(queue) == 0 {
		return nil, errors.New("unexpected AT command")
	}
	response := queue[0]
	port.responses[command] = queue[1:]
	return response, nil
}

func (*scriptedCallPort) Read([]byte) (int, error)        { return 0, io.EOF }
func (*scriptedCallPort) Write(value []byte) (int, error) { return len(value), nil }
func (*scriptedCallPort) Close() error                    { return nil }
func (*scriptedCallPort) Drain() error                    { return nil }
func (*scriptedCallPort) ResetInputBuffer() error         { return nil }

func TestParseCLCCSelectsAuthoritativeVoiceCall(t *testing.T) {
	observed := time.Unix(1700000000, 0)
	result, err := parseCLCC([]byte(
		"\r\n+CLCC: 1,1,0,1,0,\"\",128\r\n"+
			"+CLCC: 3,0,3,0,0,\"+15550100124\",145\r\n"+
			"+CLCC: 4,1,0,1,0,\"\",128\r\nOK\r\n"), observed)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "ringing_out" || result.Direction != "out" ||
		result.Number != "+15550100124" || result.NativeIndex != 3 || result.VoiceCalls != 1 ||
		result.IncomingCalls != 0 || !result.Authoritative || !result.ObservedAt.Equal(observed) {
		t.Fatalf("result=%+v", result)
	}
}

func TestIncomingAnswerAndRejectRequireOneExactNativeCall(t *testing.T) {
	ringing := []byte("+CLCC: 7,1,4,0,0,\"+85222333322\",145\r\nOK\r\n")
	active := []byte("+CLCC: 7,1,0,0,0,\"+85222333322\",145\r\nOK\r\n")
	answerPort := &scriptedCallPort{responses: map[string][][]byte{
		"AT+CLCC": {ringing, active}, "ATA": {[]byte("OK\r\n")},
	}}
	answer := &Owner{port: answerPort}
	result, err := answer.AnswerIncoming(context.Background(), IncomingCallFence{NativeIndex: 7, Number: "+85222333322"})
	if err != nil || result.State != "active" || result.NativeIndex != 7 || result.VoiceCalls != 1 {
		t.Fatalf("answer result=%+v err=%v", result, err)
	}
	if got := answerPort.commands; len(got) != 3 || got[0] != "AT+CLCC" || got[1] != "ATA" || got[2] != "AT+CLCC" {
		t.Fatalf("answer commands=%v", got)
	}

	stalePort := &scriptedCallPort{responses: map[string][][]byte{"AT+CLCC": {ringing}}}
	stale := &Owner{port: stalePort}
	if _, err := stale.AnswerIncoming(context.Background(), IncomingCallFence{NativeIndex: 6}); !errors.Is(err, ErrIncomingCallChanged) {
		t.Fatalf("stale answer err=%v", err)
	}
	if len(stalePort.commands) != 1 || stalePort.commands[0] != "AT+CLCC" {
		t.Fatalf("stale answer reached ATA: %v", stalePort.commands)
	}

	multi := []byte("+CLCC: 7,1,4,0,0,\"+85222333322\",145\r\n+CLCC: 8,0,0,0,0,\"+85211111111\",145\r\nOK\r\n")
	multiPort := &scriptedCallPort{responses: map[string][][]byte{"AT+CLCC": {multi}}}
	reject := &Owner{port: multiPort}
	if _, err := reject.RejectIncoming(context.Background(), IncomingCallFence{NativeIndex: 7}); !errors.Is(err, ErrIncomingCallChanged) {
		t.Fatalf("multi-call reject err=%v", err)
	}
	if len(multiPort.commands) != 1 {
		t.Fatalf("multi-call reject reached hangup: %v", multiPort.commands)
	}
}

func TestRejectIncomingConfirmsExactCallDisappeared(t *testing.T) {
	port := &scriptedCallPort{responses: map[string][][]byte{
		"AT+CLCC": {
			[]byte("+CLCC: 4,1,4,0,0,\"+44123\",145\r\nOK\r\n"),
			[]byte("OK\r\n"),
		},
		"AT+CHUP": {[]byte("OK\r\n")},
	}}
	owner := &Owner{port: port}
	result, err := owner.RejectIncoming(context.Background(), IncomingCallFence{NativeIndex: 4, Number: "+44123"})
	if err != nil || !result.TerminalConfirmed || result.Strategy != "incoming_chup" || result.State != "idle" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestParseCLCCEmptyIsFreshIdle(t *testing.T) {
	result, err := parseCLCC([]byte("\r\nOK\r\n"), time.Now())
	if err != nil || result.State != "idle" || !result.Authoritative {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSendDTMFRequiresActiveCallAndUsesOnlyTypedVTS(t *testing.T) {
	port := &fakePort{responses: map[string][]byte{
		"AT+CLCC":    []byte("+CLCC: 1,0,0,0,0,\"+852123\",145\r\nOK\r\n"),
		`AT+VTS="5"`: []byte("OK\r\n"),
	}}
	owner := &Owner{port: port}
	result, err := owner.SendDTMF(context.Background(), "5")
	if err != nil || result.State != "active" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := []string{"AT+CLCC", `AT+VTS="5"`, "AT+CLCC"}
	if len(port.commands) != len(want) {
		t.Fatalf("commands=%v", port.commands)
	}
	for index := range want {
		if port.commands[index] != want[index] {
			t.Fatalf("commands=%v", port.commands)
		}
	}
	before := len(port.commands)
	if _, err := owner.SendDTMF(context.Background(), "AT+CHUP"); err == nil || len(port.commands) != before {
		t.Fatalf("untyped signal reached modem: commands=%v err=%v", port.commands, err)
	}
}

func TestVerifiedHangupRequiresTwoFreshIdleSamples(t *testing.T) {
	statuses := [][]byte{
		[]byte("+CLCC: 1,0,0,0,0,\"22333322\",129\r\nOK\r\n"),
		[]byte("OK\r\n"),
		[]byte("OK\r\n"),
	}
	commands := []string{}
	exchange := func(_ context.Context, command string, _ time.Duration) ([]byte, error) {
		commands = append(commands, command)
		if command != "AT+CLCC" {
			return []byte("OK\r\n"), nil
		}
		response := statuses[0]
		statuses = statuses[1:]
		return response, nil
	}
	result, err := verifiedHangup(context.Background(), exchange, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !result.TerminalConfirmed || result.State != "idle" || result.Strategy != "chup" {
		t.Fatalf("result=%+v", result)
	}
	want := []string{"AT+CLCC", "AT+CHUP", "AT+CLCC", "AT+CLCC"}
	if len(commands) != len(want) {
		t.Fatalf("commands=%v", commands)
	}
	for index := range want {
		if commands[index] != want[index] {
			t.Fatalf("commands=%v", commands)
		}
	}
}

func TestVoicePCMCapabilityRequiresAdvertisedSerialModeZero(t *testing.T) {
	for _, test := range []struct {
		response string
		want     bool
	}{
		{response: "+QPCMV: (0,1),(0-2)\r\nOK\r\n", want: true},
		{response: "+QPCMV: (0,1),(1,2)\r\nOK\r\n", want: false},
		{response: "OK\r\n", want: false},
	} {
		if got := supportsVoicePCM([]byte(test.response)); got != test.want {
			t.Fatalf("supportsVoicePCM(%q)=%v want %v", test.response, got, test.want)
		}
	}
}

func TestEnableVoicePCMModeUsesOnlyDocumentedRoutes(t *testing.T) {
	port := &fakePort{responses: map[string][]byte{"AT+QPCMV=1,2": []byte("OK\r\n")}}
	owner := &Owner{port: port, capabilities: Capabilities{VoicePCM: true}}
	if err := owner.EnableVoicePCMMode(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if len(port.commands) != 1 || port.commands[0] != "AT+QPCMV=1,2" {
		t.Fatalf("commands=%v", port.commands)
	}
	if err := owner.EnableVoicePCMMode(context.Background(), 1); err == nil {
		t.Fatal("undocumented route was accepted")
	}
}
