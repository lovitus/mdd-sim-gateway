package agentat

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
)

type pinStep struct{ command, response string }

type pinPort struct {
	mu        sync.Mutex
	steps     []pinStep
	current   string
	delivered bool
}

func (port *pinPort) Read(buffer []byte) (int, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	if len(port.steps) == 0 || port.steps[0].command != port.current {
		return 0, nil
	}
	if port.delivered {
		return 0, nil
	}
	port.delivered = true
	response := port.steps[0].response
	port.steps = port.steps[1:]
	return copy(buffer, response), nil
}

func (port *pinPort) Write(value []byte) (int, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	if len(value) == 0 {
		return 0, io.ErrShortWrite
	}
	port.current = string(value[:len(value)-1])
	port.delivered = false
	return len(value), nil
}

func (*pinPort) Close() error                 { return nil }
func (*pinPort) Drain() error                 { return nil }
func (port *pinPort) ResetInputBuffer() error { port.delivered = false; return nil }

func scriptedPINOwner(values ...string) *Owner {
	steps := make([]pinStep, 0, len(values)/2)
	for index := 0; index < len(values); index += 2 {
		steps = append(steps, pinStep{command: values[index], response: values[index+1]})
	}
	return &Owner{port: &pinPort{steps: steps}, equipmentID: "123456789012345"}
}

func TestParseSIMPINState(t *testing.T) {
	for input, want := range map[string]SIMPINState{
		"READY": SIMPINNotRequired, "SIM PIN": SIMPINRequired, "SIM PUK": SIMPINPUKRequired,
		"PH-NET PIN": SIMPINOtherLock, "": SIMPINUnknown,
	} {
		if got := parseSIMPINState(input); got != want {
			t.Fatalf("parseSIMPINState(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSIMPINStatusRequiresExactCounter(t *testing.T) {
	owner := scriptedPINOwner(
		"AT+CPIN?", "+CPIN: SIM PIN\r\nOK\r\n",
		"AT+QCCID", "+QCCID: 89010000000000000001\r\nOK\r\n",
		"AT+QPINC?", "+QPINC: \"SC\",3,10\r\n+QPINC: \"P2\",3,10\r\nOK\r\n",
	)
	status, err := owner.SIMPINStatus(context.Background())
	if err != nil || status.State != SIMPINRequired || status.CardID != "89010000000000000001" ||
		status.AttemptsRemaining == nil || *status.AttemptsRemaining != 3 {
		t.Fatalf("SIMPINStatus() = %+v, %v", status, err)
	}
}

func TestEnterSIMPINChecksBeforeSingleAttemptAndAfter(t *testing.T) {
	owner := scriptedPINOwner(
		"AT+CPIN?", "+CPIN: SIM PIN\r\nOK\r\n",
		"AT+QCCID", "+QCCID: 89010000000000000001\r\nOK\r\n",
		"AT+QPINC?", "+QPINC: \"SC\",3,10\r\nOK\r\n",
		`AT+CPIN="1234"`, "OK\r\n",
		"AT+CPIN?", "+CPIN: READY\r\nOK\r\n",
		"AT+QCCID", "+QCCID: 89010000000000000001\r\nOK\r\n",
	)
	result, err := owner.EnterSIMPIN(context.Background(), "89010000000000000001", "1234")
	if err != nil || !result.Attempted || result.Status.State != SIMPINNotRequired {
		t.Fatalf("EnterSIMPIN() = %+v, %v", result, err)
	}
}

func TestEnterSIMPINRefusesLastAttempt(t *testing.T) {
	owner := scriptedPINOwner(
		"AT+CPIN?", "+CPIN: SIM PIN\r\nOK\r\n",
		"AT+QCCID", "+QCCID: 89010000000000000001\r\nOK\r\n",
		"AT+QPINC?", "+QPINC: \"SC\",1,10\r\nOK\r\n",
	)
	result, err := owner.EnterSIMPIN(context.Background(), "89010000000000000001", "1234")
	if err == nil || result.Attempted || !strings.Contains(err.Error(), "too low") {
		t.Fatalf("EnterSIMPIN() = %+v, %v", result, err)
	}
}

func TestEnterSIMPINReadsCounterAgainAfterRejectedCommand(t *testing.T) {
	owner := scriptedPINOwner(
		"AT+CPIN?", "+CPIN: SIM PIN\r\nOK\r\n",
		"AT+QCCID", "+QCCID: 89010000000000000001\r\nOK\r\n",
		"AT+QPINC?", "+QPINC: \"SC\",3,10\r\nOK\r\n",
		`AT+CPIN="9999"`, "+CME ERROR: incorrect password\r\n",
		"AT+CPIN?", "+CPIN: SIM PIN\r\nOK\r\n",
		"AT+QCCID", "+QCCID: 89010000000000000001\r\nOK\r\n",
		"AT+QPINC?", "+QPINC: \"SC\",2,10\r\nOK\r\n",
	)
	result, err := owner.EnterSIMPIN(context.Background(), "89010000000000000001", "9999")
	if err == nil || !result.Attempted || result.Status.AttemptsRemaining == nil || *result.Status.AttemptsRemaining != 2 {
		t.Fatalf("EnterSIMPIN() = %+v, %v", result, err)
	}
}

func TestFullSIMPINStatusReadsReadyCounterWithoutMutation(t *testing.T) {
	owner := scriptedPINOwner(
		"AT+CPIN?", "+CPIN: READY\r\nOK\r\n",
		"AT+QCCID", "+QCCID: 89010000000000000001\r\nOK\r\n",
		"AT+QPINC?", "+QPINC: \"SC\",3,10\r\nOK\r\n",
	)
	status, err := owner.SIMPINStatusFull(context.Background())
	if err != nil || status.State != SIMPINNotRequired || status.AttemptsRemaining == nil || *status.AttemptsRemaining != 3 {
		t.Fatalf("SIMPINStatusFull() = %+v, %v", status, err)
	}
}
