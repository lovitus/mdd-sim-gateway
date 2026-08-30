// Package operations contains the single catalog of facts required by each
// user operation. Display summaries are never inputs to this package.
package operations

import "github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"

const (
	CellularData = "cellular_data"
	CellularCall = "cellular_call"
	CellularSMS  = "cellular_sms"
	VoWiFiCall   = "vowifi_call"
	VoWiFiSMS    = "vowifi_sms"
)

var requirements = map[string][]state.Requirement{
	CellularData: required(state.LayerIntent, state.LayerAgentLink, state.LayerHardware,
		state.LayerCard, state.LayerCellularData),
	CellularCall: required(state.LayerIntent, state.LayerAgentLink, state.LayerHardware,
		state.LayerCard, state.LayerCellularVoice),
	CellularSMS: required(state.LayerIntent, state.LayerAgentLink, state.LayerHardware,
		state.LayerCard, state.LayerCellularSMS),
	VoWiFiCall: required(state.LayerIntent, state.LayerVoWiFiIntent, state.LayerEngineProcess, state.LayerCardRoute,
		state.LayerPIN, state.LayerTunnel, state.LayerIMS, state.LayerAdmission, state.LayerMedia),
	VoWiFiSMS: required(state.LayerIntent, state.LayerVoWiFiIntent, state.LayerEngineProcess, state.LayerCardRoute,
		state.LayerPIN, state.LayerTunnel, state.LayerIMS, state.LayerAdmission, state.LayerMessaging),
}

func EvaluateAll(view state.LineView) map[string]state.Readiness {
	result := make(map[string]state.Readiness, len(requirements))
	for name, layers := range requirements {
		result[name] = state.Evaluate(view, layers)
	}
	return result
}

func required(layers ...state.Layer) []state.Requirement {
	result := make([]state.Requirement, 0, len(layers))
	for _, layer := range layers {
		result = append(result, state.Requirement{Layer: layer})
	}
	return result
}
