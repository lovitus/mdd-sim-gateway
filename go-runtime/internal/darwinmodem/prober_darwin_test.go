//go:build darwin

package darwinmodem

import (
	"context"
	"errors"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellulario"
)

func TestConfirmSIMStatusRetriesOneTransientAbsentWithoutPublishingIt(t *testing.T) {
	want := agentat.SIMPINStatus{State: agentat.SIMPINNotRequired, CardID: "8985200000000000001"}
	calls := 0
	got, err := confirmSIMStatus(context.Background(), agentat.SIMPINStatus{}, errors.New("+CME ERROR: 10"),
		func() (agentat.SIMPINStatus, error) { calls++; return want, nil })
	if err != nil || got != want || calls != 1 {
		t.Fatalf("status=%+v calls=%d err=%v", got, calls, err)
	}
}

func TestConfirmSIMStatusPublishesConfirmedAbsence(t *testing.T) {
	absent := errors.New("read SIM PIN state: +CME ERROR: 10")
	calls := 0
	_, err := confirmSIMStatus(context.Background(), agentat.SIMPINStatus{}, absent,
		func() (agentat.SIMPINStatus, error) { calls++; return agentat.SIMPINStatus{}, absent })
	if !simAbsent(err) || calls != 2 {
		t.Fatalf("confirmed absence calls=%d err=%v", calls, err)
	}
}

func TestSIMIdentityFailureIsNeverReportedAsPhysicalAbsence(t *testing.T) {
	err := errors.New("read locked SIM identity: +CME ERROR: 10")
	if simAbsent(err) {
		t.Fatal("ICCID query failure was classified as a physical SIM absence")
	}
}

func TestModemPortLabelIsValidInAgentTopology(t *testing.T) {
	attachment := cellulario.Attachment{
		VID: 0x2c7c, PID: 0x0125, Bus: 1, Address: 4, Serial: "EC20-A",
	}
	topology := agentlink.TopologySnapshot{
		ReaderCondition: agentlink.ReaderStarting,
		Readers:         []agentlink.ReaderFact{},
		ModemCondition:  agentlink.ModemReady,
		Modems: []agentlink.ModemFact{{
			AttachmentID: attachment.ID(),
			Condition:    "ready",
			AT: agentlink.ModemATControlFact{
				State: "ready", Port: modemPortLabel(attachment), CallSignalling: true,
			},
			SIM: agentlink.ModemSIMFact{State: "ready", PINState: "not_required"},
			Network: agentlink.ModemNetworkFact{
				Registration: "roaming", SoftwareRadio: "on", HardwareRadio: "unknown",
				Data: "disconnected", DataGuard: "protected",
			},
		}},
	}
	if err := topology.Validate(); err != nil {
		t.Fatalf("modem topology: %v", err)
	}
}
