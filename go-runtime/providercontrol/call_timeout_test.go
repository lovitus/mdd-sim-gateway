package providercontrol

import (
	"errors"
	"testing"
	"time"
)

func TestCallTimeoutOnlyAppliesToOutboundStart(t *testing.T) {
	reads := 0
	handler := &Handler{callTimeout: func() (time.Duration, error) { reads++; return 35 * time.Second, nil }}
	for _, request := range []preparedOperation{{status: true}, {call: &callMutation{action: "end"}}, {message: true}} {
		got, err := handler.operationDuration(request)
		if err != nil || got != maximumOperationDuration {
			t.Fatalf("non-start duration %v %v", got, err)
		}
	}
	if reads != 0 {
		t.Fatal("hangup/status/SMS read outbound preferences")
	}
	got, err := handler.operationDuration(preparedOperation{call: &callMutation{action: "start"}})
	if err != nil || got != 35*time.Second || reads != 1 {
		t.Fatalf("start duration %v reads=%d err=%v", got, reads, err)
	}
	for _, value := range []time.Duration{0, 4 * time.Second, 181 * time.Second} {
		handler.callTimeout = func() (time.Duration, error) { return value, nil }
		if _, err := handler.operationDuration(preparedOperation{call: &callMutation{action: "start"}}); err == nil {
			t.Fatalf("accepted %v", value)
		}
	}
	handler.callTimeout = func() (time.Duration, error) { return 0, errors.New("store unavailable") }
	if _, err := handler.operationDuration(preparedOperation{call: &callMutation{action: "start"}}); err == nil {
		t.Fatal("ignored configuration failure")
	}
}
