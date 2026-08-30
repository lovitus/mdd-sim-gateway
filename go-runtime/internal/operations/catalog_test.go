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

func TestDisabledVoWiFiIntentDoesNotBlockCellularOperations(t *testing.T) {
	view := state.LineView{LineID: "line-1", Facts: []state.FactView{
		ready(state.LayerIntent),
		{Layer: state.LayerVoWiFiIntent, Condition: state.ConditionInactive, Fresh: true},
		ready(state.LayerAgentLink), ready(state.LayerHardware), ready(state.LayerCard),
		ready(state.LayerCellularData), ready(state.LayerCellularVoice), ready(state.LayerCellularSMS),
	}}
	result := EvaluateAll(view)
	if !result[CellularData].Ready || !result[CellularCall].Ready || !result[CellularSMS].Ready {
		t.Fatalf("VoWiFi intent blocked cellular operations: %+v", result)
	}
	if result[VoWiFiCall].Ready || result[VoWiFiSMS].Ready {
		t.Fatalf("disabled VoWiFi intent was ignored: %+v", result)
	}
}

func TestVoWiFiCallAndSMSHaveSeparateFinalCapability(t *testing.T) {
	view := state.LineView{LineID: "line-1", Facts: []state.FactView{
		ready(state.LayerIntent), ready(state.LayerVoWiFiIntent), ready(state.LayerEngineProcess), ready(state.LayerCardRoute),
		ready(state.LayerTunnel), ready(state.LayerIMS),
		ready(state.LayerAdmission), ready(state.LayerMessaging),
	}}
	result := EvaluateAll(view)
	if !result[VoWiFiSMS].Ready || result[VoWiFiCall].Ready {
		t.Fatalf("call and SMS readiness were collapsed: %+v", result)
	}
}

func TestVoWiFiPreflightDoesNotRequireSessionScopedMediaOrRedundantPIN(t *testing.T) {
	view := state.LineView{LineID: "line-1", Facts: []state.FactView{
		ready(state.LayerIntent), ready(state.LayerVoWiFiIntent), ready(state.LayerEngineProcess), ready(state.LayerCardRoute),
		ready(state.LayerTunnel), ready(state.LayerIMS), ready(state.LayerAdmission),
	}}
	result := EvaluateAll(view)
	if !result[VoWiFiCall].Ready {
		t.Fatalf("ready provider was blocked before a media session existed: %+v", result[VoWiFiCall])
	}
	if result[VoWiFiSMS].Ready {
		t.Fatalf("missing messaging capability was ignored: %+v", result[VoWiFiSMS])
	}
}

func ready(layer state.Layer) state.FactView {
	return state.FactView{Layer: layer, Condition: state.ConditionReady, Available: true, Fresh: true}
}
