package agenthost

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsim"
)

type topologyState struct {
	mu         sync.RWMutex
	condition  agentreader.MonitorCondition
	detail     string
	readers    []agentreader.Reader
	failures   map[string]string
	observedAt time.Time
	now        func() time.Time
}

func (state *topologyState) observe(observation agentreader.Observation) {
	observedAt := state.currentTime()
	copy := make([]agentreader.Reader, len(observation.Readers))
	for index, reader := range observation.Readers {
		copy[index] = reader
		copy[index].ATR = append([]byte(nil), reader.ATR...)
	}
	state.mu.Lock()
	state.condition = observation.Condition
	state.detail = observation.Detail
	state.readers = copy
	current := make(map[string]struct{}, len(copy))
	for _, reader := range copy {
		if reader.SessionGeneration != "" {
			current[reader.SessionGeneration] = struct{}{}
		}
	}
	for generation := range state.failures {
		if _, exists := current[generation]; !exists {
			delete(state.failures, generation)
		}
	}
	state.observedAt = observedAt
	state.mu.Unlock()
}

func (state *topologyState) sessionFailed(reader agentreader.Reader, failure error) {
	if reader.SessionGeneration == "" || failure == nil {
		return
	}
	detail := strings.ToValidUTF8(failure.Error(), "?")
	if len(detail) > 1024 {
		detail = strings.ToValidUTF8(detail[:1024], "?")
	}
	state.mu.Lock()
	if state.failures == nil {
		state.failures = make(map[string]string)
	}
	state.failures[reader.SessionGeneration] = detail
	state.mu.Unlock()
}

func (state *topologyState) snapshot(sessions []agentsim.SessionView, staleAfter time.Duration) agentlink.TopologySnapshot {
	byGeneration := make(map[string]agentsim.SessionView, len(sessions))
	for _, session := range sessions {
		byGeneration[session.SessionGeneration] = session
	}
	state.mu.RLock()
	condition := state.condition
	detail := state.detail
	observedAt := state.observedAt
	readers := make([]agentreader.Reader, len(state.readers))
	failures := make(map[string]string, len(state.failures))
	for index, reader := range state.readers {
		readers[index] = reader
		readers[index].ATR = append([]byte(nil), reader.ATR...)
	}
	for generation, failure := range state.failures {
		failures[generation] = failure
	}
	state.mu.RUnlock()
	if condition == "" {
		condition = agentreader.MonitorStarting
	}
	if condition == agentreader.MonitorReady && staleAfter > 0 &&
		(observedAt.IsZero() || state.currentTime().Sub(observedAt) > staleAfter) {
		condition = agentreader.MonitorRecovering
		detail = "PC/SC observation is stale"
		readers = nil
	}

	topology := agentlink.TopologySnapshot{
		ReaderCondition: agentlink.ReaderCondition(condition), ReaderDetail: detail,
		Readers: make([]agentlink.ReaderFact, 0, len(readers)),
	}
	for _, reader := range readers {
		fact := agentlink.ReaderFact{ReaderName: reader.Name, IdentityState: agentlink.CardAbsent}
		if reader.CardPresent {
			fact.CardPresent = true
			fact.SessionGeneration = reader.SessionGeneration
			fact.IdentityState = agentlink.CardIdentityDiscovering
			fact.IdentityDetail = failures[reader.SessionGeneration]
			if len(reader.ATR) != 0 {
				digest := sha256.Sum256(reader.ATR)
				fact.ATRSHA256 = hex.EncodeToString(digest[:])
			}
			if session, found := byGeneration[reader.SessionGeneration]; found && session.ReaderName == reader.Name {
				fact.CardID = session.CardID
				fact.SIM = session.SIM
				fact.EUICC = session.EUICC
				fact.SecureElements = session.SecureElements
				fact.IdentityDetail = ""
				if session.CardID == "" && session.EUICC == nil && len(session.SecureElements) == 0 {
					fact.IdentityState = agentlink.CardIdentityUnavailable
				} else {
					fact.IdentityState = agentlink.CardIdentified
				}
			}
		}
		topology.Readers = append(topology.Readers, fact)
	}
	sort.Slice(topology.Readers, func(left, right int) bool {
		return topology.Readers[left].ReaderName < topology.Readers[right].ReaderName
	})
	return topology
}

func (state *topologyState) currentTime() time.Time {
	if state.now != nil {
		return state.now()
	}
	return time.Now()
}
