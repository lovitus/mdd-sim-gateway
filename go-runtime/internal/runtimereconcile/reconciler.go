// Package runtimereconcile converges durable per-line VoWiFi intent with
// current Agent card facts and the current long-lived Provider process. It
// never supervises or restarts a process, container, modem, Agent, or network.
package runtimereconcile

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const (
	defaultInterval      = 10 * time.Second
	defaultActionTimeout = 120 * time.Second
	defaultBaseBackoff   = 5 * time.Second
	defaultMaxBackoff    = 10 * time.Minute
	agentTopologyTTL     = 30 * time.Second
	coreProducerID       = "core-runtime-reconciler"
)

var coreLayers = []state.Layer{
	state.LayerIntent,
	state.LayerVoWiFiIntent,
	state.LayerCardRoute,
	state.LayerEngineProcess,
	state.LayerAdmission,
}

type Catalog interface {
	Snapshot() (linecatalog.Snapshot, error)
	RuntimeIntent(string) (enabled, found bool, revision uint64, err error)
	SetRuntimeIntent(string, bool) (lineEnabled, changed bool, revision uint64, err error)
}

type AgentFacts interface {
	Statuses() []agentlink.ConnectionStatus
}

type RuntimeControl interface {
	Status(context.Context, string) (vowifiipc.Snapshot, error)
	Start(context.Context, string, vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error)
	Stop(context.Context, string, vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error)
}

type EventStore interface {
	AcceptSnapshot([]events.Event, events.ProducerCheckpoint) ([]events.Record, events.ProducerCheckpoint, error)
}

type Config struct {
	Context       context.Context
	Catalog       Catalog
	Agents        AgentFacts
	Runtime       RuntimeControl
	Store         EventStore
	Replay        *events.Replay
	Interval      time.Duration
	ActionTimeout time.Duration
	BaseBackoff   time.Duration
	MaxBackoff    time.Duration
	Now           func() time.Time
	Logf          func(string, ...any)
	Generation    string
}

type Reconciler struct {
	catalog Catalog
	agents  AgentFacts
	runtime RuntimeControl
	store   EventStore
	replay  *events.Replay

	ctx           context.Context
	cancel        context.CancelFunc
	interval      time.Duration
	actionTimeout time.Duration
	baseBackoff   time.Duration
	maxBackoff    time.Duration
	now           func() time.Time
	logf          func(string, ...any)
	generation    string
	wake          chan struct{}

	startOnce sync.Once
	closeOnce sync.Once
	wg        sync.WaitGroup
	counter   atomic.Uint64
	mu        sync.Mutex
	lines     map[string]*lineState
	sequence  map[string]uint64
	published map[string]bool
}

type lineState struct {
	action   string
	inFlight bool
	failures uint32
	next     time.Time
}

type lineObservation struct {
	intentEnabled bool
	intentFound   bool
	cardMatches   int
	providerReady bool
	providerCode  string
	status        vowifiipc.Snapshot
}

type desiredFact struct {
	layer     state.Layer
	condition state.Condition
	available bool
	code      string
}

func New(config Config) (*Reconciler, error) {
	if config.Context == nil || config.Catalog == nil || config.Agents == nil || config.Runtime == nil ||
		config.Store == nil || config.Replay == nil {
		return nil, errors.New("invalid runtime reconciler dependencies")
	}
	if config.Interval == 0 {
		config.Interval = defaultInterval
	}
	if config.ActionTimeout == 0 {
		config.ActionTimeout = defaultActionTimeout
	}
	if config.BaseBackoff == 0 {
		config.BaseBackoff = defaultBaseBackoff
	}
	if config.MaxBackoff == 0 {
		config.MaxBackoff = defaultMaxBackoff
	}
	if config.Interval < time.Second || config.ActionTimeout < time.Second ||
		config.BaseBackoff < time.Second || config.MaxBackoff < config.BaseBackoff {
		return nil, errors.New("invalid runtime reconciler timing")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Logf == nil {
		config.Logf = func(string, ...any) {}
	}
	if config.Generation == "" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, fmt.Errorf("create runtime reconciler generation: %w", err)
		}
		config.Generation = "core-" + hex.EncodeToString(random[:])
	}
	ctx, cancel := context.WithCancel(config.Context)
	return &Reconciler{
		catalog: config.Catalog, agents: config.Agents, runtime: config.Runtime,
		store: config.Store, replay: config.Replay, ctx: ctx, cancel: cancel,
		interval: config.Interval, actionTimeout: config.ActionTimeout,
		baseBackoff: config.BaseBackoff, maxBackoff: config.MaxBackoff,
		now: config.Now, logf: config.Logf, generation: config.Generation,
		wake: make(chan struct{}, 1), lines: make(map[string]*lineState),
		sequence: make(map[string]uint64), published: make(map[string]bool),
	}, nil
}

func (reconciler *Reconciler) Start() {
	reconciler.startOnce.Do(func() {
		reconciler.wg.Add(1)
		go reconciler.run()
	})
}

func (reconciler *Reconciler) Close() {
	if reconciler == nil {
		return
	}
	reconciler.closeOnce.Do(reconciler.cancel)
	reconciler.wg.Wait()
}

func (reconciler *Reconciler) Wake() {
	if reconciler == nil {
		return
	}
	select {
	case reconciler.wake <- struct{}{}:
	default:
	}
}

func (reconciler *Reconciler) run() {
	defer reconciler.wg.Done()
	ticker := time.NewTicker(reconciler.interval)
	defer ticker.Stop()
	for {
		if err := reconciler.reconcile(reconciler.ctx); err != nil && !errors.Is(err, context.Canceled) {
			reconciler.logf("runtime reconcile: %v", err)
		}
		select {
		case <-reconciler.ctx.Done():
			return
		case <-ticker.C:
		case <-reconciler.wake:
		}
	}
}

func (reconciler *Reconciler) reconcile(ctx context.Context) error {
	catalog, err := reconciler.catalog.Snapshot()
	if err != nil {
		return fmt.Errorf("read line catalog: %w", err)
	}
	agents := reconciler.agents.Statuses()
	var failures []error
	for _, line := range catalog.Lines {
		if err := ctx.Err(); err != nil {
			return err
		}
		observation := lineObservation{cardMatches: matchingCards(agents, line.CardID, reconciler.now().UTC())}
		statusContext, cancel := context.WithTimeout(ctx, min(reconciler.interval, 5*time.Second))
		status, statusErr := reconciler.runtime.Status(statusContext, line.ID)
		cancel()
		observation.status = status
		switch {
		case statusErr == nil:
			observation.providerReady = true
			observation.providerCode = "provider_reachable"
		case errors.Is(statusErr, mediaauth.ErrProviderUnavailable):
			observation.providerCode = "provider_unavailable"
		default:
			observation.providerCode = "provider_status_failed"
			failures = append(failures, fmt.Errorf("line %s provider status: %w", line.ID, statusErr))
		}

		intent, found, _, intentErr := reconciler.catalog.RuntimeIntent(line.ID)
		if intentErr != nil {
			failures = append(failures, fmt.Errorf("line %s runtime intent: %w", line.ID, intentErr))
			continue
		}
		observation.intentEnabled, observation.intentFound = intent, found
		adopted := false
		if !found && statusErr == nil {
			intent = line.Enabled && adoptRunning(status.Runtime.Condition)
			if _, _, _, err := reconciler.catalog.SetRuntimeIntent(line.ID, intent); err != nil {
				failures = append(failures, fmt.Errorf("line %s adopt runtime intent: %w", line.ID, err))
				continue
			}
			observation.intentEnabled, observation.intentFound = intent, true
			adopted = true
		}
		if err := reconciler.publish(line, observation); err != nil {
			failures = append(failures, fmt.Errorf("line %s publish core facts: %w", line.ID, err))
		}
		// Adoption is deliberately side-effect free. A later observation may
		// converge the persisted intent after the current state is visible.
		if adopted || statusErr != nil || !found && !observation.intentFound {
			continue
		}
		reconciler.plan(line, observation)
	}
	return errors.Join(failures...)
}

func adoptRunning(condition vowifiipc.RuntimeCondition) bool {
	switch condition {
	case vowifiipc.RuntimeRunning, vowifiipc.RuntimeStarting, vowifiipc.RuntimeFailed:
		return true
	default:
		return false
	}
}

func matchingCards(statuses []agentlink.ConnectionStatus, cardID string, now time.Time) int {
	matches := 0
	for _, status := range statuses {
		if status.Topology == nil || status.LastReport.IsZero() || now.Sub(status.LastReport) > agentTopologyTTL {
			continue
		}
		if status.Topology.ReaderCondition == agentlink.ReaderReady {
			for _, reader := range status.Topology.Readers {
				if reader.CardPresent && reader.IdentityState == agentlink.CardIdentified &&
					reader.CardID == cardID && reader.SessionGeneration != "" {
					matches++
				}
			}
		}
		if status.Topology.ModemCondition == agentlink.ModemReady {
			for _, modem := range status.Topology.Modems {
				if modem.SIM.State == "ready" && modem.SIM.ICCID == cardID && modem.SIM.SessionGeneration != "" &&
					modem.AT.State == "ready" && modem.AT.SIMAPDU {
					matches++
				}
			}
		}
	}
	return matches
}

func (reconciler *Reconciler) plan(line linecatalog.Line, observation lineObservation) {
	targetRunning := line.Enabled && observation.intentFound && observation.intentEnabled &&
		observation.cardMatches == 1 && observation.providerReady
	switch {
	case targetRunning && (observation.status.Runtime.Condition == vowifiipc.RuntimeStopped ||
		observation.status.Runtime.Condition == vowifiipc.RuntimeFailed):
		reconciler.schedule(line.ID, "start")
	case !targetRunning && (observation.status.Runtime.Condition == vowifiipc.RuntimeRunning ||
		observation.status.Runtime.Condition == vowifiipc.RuntimeFailed):
		reconciler.schedule(line.ID, "stop")
	case targetRunning && observation.status.Runtime.Condition == vowifiipc.RuntimeRunning:
		reconciler.reset(line.ID)
	case !targetRunning && observation.status.Runtime.Condition == vowifiipc.RuntimeStopped:
		reconciler.reset(line.ID)
	}
}

func (reconciler *Reconciler) schedule(lineID, action string) {
	now := reconciler.now().UTC()
	reconciler.mu.Lock()
	line := reconciler.lines[lineID]
	if line == nil {
		line = &lineState{}
		reconciler.lines[lineID] = line
	}
	if line.inFlight {
		reconciler.mu.Unlock()
		return
	}
	if line.action != action {
		line.action, line.failures, line.next = action, 0, time.Time{}
	}
	if now.Before(line.next) {
		reconciler.mu.Unlock()
		return
	}
	line.inFlight = true
	sequence := reconciler.counter.Add(1)
	reconciler.mu.Unlock()

	fingerprint := sha256.Sum256([]byte(reconciler.generation))
	operationID := fmt.Sprintf("reconcile-%s-%s-%d", action, hex.EncodeToString(fingerprint[:16]), sequence)
	reconciler.wg.Add(1)
	go reconciler.execute(lineID, action, operationID)
}

func (reconciler *Reconciler) execute(lineID, action, operationID string) {
	defer reconciler.wg.Done()
	ctx, cancel := context.WithTimeout(reconciler.ctx, reconciler.actionTimeout)
	request := vowifiipc.LifecycleRequest{OperationID: operationID}
	var err error
	if action == "start" {
		_, err = reconciler.runtime.Start(ctx, lineID, request)
	} else {
		_, err = reconciler.runtime.Stop(ctx, lineID, request)
	}
	cancel()

	reconciler.mu.Lock()
	line := reconciler.lines[lineID]
	if line != nil && line.action == action {
		line.inFlight = false
		if err == nil {
			line.failures, line.next = 0, time.Time{}
		} else if reconciler.ctx.Err() == nil {
			line.failures++
			line.next = reconciler.now().UTC().Add(reconciler.retryDelay(line.failures, err))
		}
	}
	reconciler.mu.Unlock()
	if err != nil && reconciler.ctx.Err() == nil {
		reconciler.logf("runtime reconcile line %s %s failed: %v", lineID, action, err)
	}
	reconciler.Wake()
}

func (reconciler *Reconciler) retryDelay(failures uint32, err error) time.Duration {
	var response *vowifiipc.ResponseError
	if errors.As(err, &response) && response.Failure.RetryAfter > 0 {
		return response.Failure.RetryAfter
	}
	shift := failures - 1
	if shift > 30 {
		shift = 30
	}
	delay := reconciler.baseBackoff * time.Duration(uint64(1)<<shift)
	if delay > reconciler.maxBackoff || delay < reconciler.baseBackoff {
		return reconciler.maxBackoff
	}
	return delay
}

func (reconciler *Reconciler) reset(lineID string) {
	reconciler.mu.Lock()
	if line := reconciler.lines[lineID]; line != nil && !line.inFlight {
		line.failures, line.next = 0, time.Time{}
	}
	reconciler.mu.Unlock()
}

func (reconciler *Reconciler) publish(line linecatalog.Line, observation lineObservation) error {
	facts := coreFacts(line, observation)
	now := reconciler.now().UTC()
	reconciler.mu.Lock()
	reconciler.sequence[line.ID]++
	sequence := reconciler.sequence[line.ID]
	first := !reconciler.published[line.ID]
	reconciler.mu.Unlock()

	current := currentFacts(reconciler.replay.Projections(now), line.ID)
	changed := make([]events.Event, 0, len(facts))
	for _, fact := range facts {
		prior, found := current[fact.layer]
		if !first && found && prior.Source == string(events.RoleCore) && prior.ProducerID == coreProducerID &&
			prior.Generation == reconciler.generation && prior.Condition == fact.condition &&
			prior.Available == fact.available && prior.Code == fact.code {
			continue
		}
		changed = append(changed, events.Event{
			SchemaVersion: events.SchemaVersion,
			EventID:       fmt.Sprintf("core-reconcile:%s:%s:%d:%s", reconciler.generation, line.ID, sequence, fact.layer),
			LineID:        line.ID, ProducerRole: events.RoleCore, ProducerID: coreProducerID,
			Layer: fact.layer, Condition: fact.condition, Available: fact.available, Code: fact.code,
			Generation: reconciler.generation, Sequence: sequence, ObservedAt: now,
		})
	}
	checkpoint := events.ProducerCheckpoint{
		LineID: line.ID, ProducerRole: events.RoleCore, ProducerID: coreProducerID,
		Generation: reconciler.generation, Sequence: sequence, Layers: append([]state.Layer(nil), coreLayers...),
		ObservedAt: now, ReceivedAt: now,
	}
	records, stored, err := reconciler.store.AcceptSnapshot(changed, checkpoint)
	if err != nil {
		return err
	}
	for _, record := range records {
		if _, err := reconciler.replay.Apply(record); err != nil {
			return err
		}
	}
	if err := reconciler.replay.Confirm(stored); err != nil {
		return err
	}
	reconciler.mu.Lock()
	reconciler.published[line.ID] = true
	reconciler.mu.Unlock()
	return nil
}

func coreFacts(line linecatalog.Line, observation lineObservation) []desiredFact {
	intent := desiredFact{layer: state.LayerIntent, condition: state.ConditionInactive, code: "line_disabled"}
	if line.Enabled {
		intent.condition, intent.available, intent.code = state.ConditionReady, true, "line_enabled"
	}
	vowifiIntent := desiredFact{layer: state.LayerVoWiFiIntent, condition: state.ConditionUnknown, code: "runtime_intent_uninitialized"}
	switch {
	case observation.intentFound && observation.intentEnabled:
		vowifiIntent.condition, vowifiIntent.available, vowifiIntent.code = state.ConditionReady, true, "vowifi_enabled"
	case observation.intentFound:
		vowifiIntent.condition, vowifiIntent.code = state.ConditionInactive, "vowifi_disabled"
	}
	card := desiredFact{layer: state.LayerCardRoute, condition: state.ConditionBlocked, code: "card_not_present"}
	if observation.cardMatches == 1 {
		card.condition, card.available, card.code = state.ConditionReady, true, "card_route_current"
	} else if observation.cardMatches > 1 {
		card.code = "card_identity_ambiguous"
	}
	process := desiredFact{layer: state.LayerEngineProcess, condition: state.ConditionUnknown, code: observation.providerCode}
	if observation.providerReady {
		process.condition, process.available = state.ConditionReady, true
	} else if observation.providerCode == "provider_status_failed" {
		process.condition = state.ConditionDegraded
	}
	admission := desiredFact{layer: state.LayerAdmission, condition: state.ConditionBlocked, code: "runtime_not_admitted"}
	if line.Enabled && observation.intentFound && observation.intentEnabled && observation.cardMatches == 1 && observation.providerReady {
		admission.condition, admission.available, admission.code = state.ConditionReady, true, "runtime_admitted"
	} else {
		switch {
		case !line.Enabled:
			admission.code = "line_disabled"
		case !observation.intentFound:
			admission.code = "runtime_intent_uninitialized"
		case !observation.intentEnabled:
			admission.code = "vowifi_disabled"
		case observation.cardMatches == 0:
			admission.code = "card_not_present"
		case observation.cardMatches > 1:
			admission.code = "card_identity_ambiguous"
		case !observation.providerReady:
			admission.code = observation.providerCode
		}
	}
	return []desiredFact{intent, vowifiIntent, card, process, admission}
}

func currentFacts(projections []events.LineProjection, lineID string) map[state.Layer]state.FactView {
	result := make(map[state.Layer]state.FactView)
	for _, projection := range projections {
		if projection.LineID != lineID {
			continue
		}
		for _, fact := range projection.Facts {
			result[fact.Layer] = fact
		}
		break
	}
	return result
}
