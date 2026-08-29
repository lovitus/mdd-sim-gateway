package agentat

import (
	"context"
	"testing"
	"time"
)

func TestParseCLCCSelectsAuthoritativeVoiceCall(t *testing.T) {
	observed := time.Unix(1700000000, 0)
	result, err := parseCLCC([]byte(
		"\r\n+CLCC: 1,1,0,1,0,\"\",128\r\n"+
			"+CLCC: 3,0,3,0,0,\"+85222333322\",145\r\n"+
			"+CLCC: 4,1,0,1,0,\"\",128\r\nOK\r\n"), observed)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "ringing_out" || result.Direction != "out" ||
		result.Number != "+85222333322" || !result.Authoritative || !result.ObservedAt.Equal(observed) {
		t.Fatalf("result=%+v", result)
	}
}

func TestParseCLCCEmptyIsFreshIdle(t *testing.T) {
	result, err := parseCLCC([]byte("\r\nOK\r\n"), time.Now())
	if err != nil || result.State != "idle" || !result.Authoritative {
		t.Fatalf("result=%+v err=%v", result, err)
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
