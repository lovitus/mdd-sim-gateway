package agenthost

import "github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"

// Keep the local diagnostic snapshot untouched. Only the wire view replaces
// an invalid reader payload with explicit unavailability, never stale success.
func reportTopology(source agentlink.TopologySnapshot) agentlink.TopologySnapshot {
	result := agentlink.NormalizeTopology(source)
	if result.ReaderCondition != agentlink.ReaderReady {
		return result
	}
	for index, reader := range result.Readers {
		probe := agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, Readers: []agentlink.ReaderFact{reader}}
		if err := probe.Validate(); err != nil {
			unavailable := agentlink.ReaderFact{
				ReaderName: reader.ReaderName, CardPresent: reader.CardPresent,
				SessionGeneration: reader.SessionGeneration,
				IdentityState:     agentlink.CardIdentityUnavailable,
				IdentityDetail:    "topology_invalid: " + err.Error(),
			}
			probe.Readers[0] = unavailable
			if probe.Validate() != nil {
				// Without a valid attachment fence, no per-card recovery claim is safe.
				result.ReaderCondition = agentlink.ReaderRecovering
				result.ReaderDetail = "topology_invalid: reader attachment identity is unavailable"
				result.Readers = nil
				return result
			}
			result.Readers[index] = unavailable
		}
	}
	return result
}
