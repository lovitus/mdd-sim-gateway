//go:build darwin

package darwinmodem

import (
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellulario"
)

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
