package operations

import (
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
)

func TestCellularDataCannotSatisfyCallOrSMS(t *testing.T) {
	view := state.LineView{LineID: "line-1", Facts: []state.FactView{
		ready(state.LayerIntent), ready(state.LayerAgentLink), ready(state.LayerHardware),
		ready(state.LayerCard), ready(state.LayerCellularData),
	}}
	result := EvaluateAll(view)
	if !result[CellularData].Ready {
		t.Fatal("cellular data should be ready")
	}
	if result[CellularCall].Ready || result[CellularSMS].Ready {
		t.Fatalf("data fabricated another capability: %+v", result)
	}
}

func TestVoWiFiCallAndSMSHaveSeparateFinalCapability(t *testing.T) {
	view := state.LineView{LineID: "line-1", Facts: []state.FactView{
		ready(state.LayerIntent), ready(state.LayerEngineProcess), ready(state.LayerCardRoute),
		ready(state.LayerPIN), ready(state.LayerTunnel), ready(state.LayerIMS),
		ready(state.LayerAdmission), ready(state.LayerMessaging),
	}}
	result := EvaluateAll(view)
	if !result[VoWiFiSMS].Ready || result[VoWiFiCall].Ready {
		t.Fatalf("call and SMS readiness were collapsed: %+v", result)
	}
}

func ready(layer state.Layer) state.FactView {
	return state.FactView{Layer: layer, Condition: state.ConditionReady, Available: true, Fresh: true}
}
