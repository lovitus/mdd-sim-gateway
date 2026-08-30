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
	recoveryStableWindow = time.Minute
	agentTopologyTTL     = 30 * time.Second
	intentPollInterval   = 250 * time.Millisecond
	coreProducerID       = "core-runtime-reconciler"
)

var coreLayers = []state.Layer{
	state.LayerIntent,
	state.LayerVoWiFiIntent,
	state.LayerCardRoute,
	state.LayerEngineProcess,
	state.LayerAdmission,
}

var agentLayers = []state.Layer{
	state.LayerAgentLink,
	state.LayerHardware,
	state.LayerCard,
	state.LayerPIN,
	state.LayerCellularData,
	state.LayerCellularVoice,
	state.LayerCellularSMS,
}

const agentProjectionProducerID = "core-agent-topology-projector"

type Catalog interface {
	Snapshot() (linecatalog.Snapshot, error)
	Get(string) (linecatalog.Line, error)
	RuntimeIntent(string) (enabled, found bool, revision uint64, err error)
	SetRuntimeIntent(string, bool) (lineEnabled, changed bool, revision uint64, err error)
}

type AgentFacts interface {
	Statuses() []agentlink.ConnectionStatus
}

type RuntimeControl interface {
	Observe(context.Context, string) (vowifiipc.Snapshot, mediaauth.ProviderFence, error)
	Start(context.Context, string, mediaauth.ProviderFence, vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error)
	Stop(context.Context, string, mediaauth.ProviderFence, vowifiipc.LifecycleRequest) (vowifiipc.OperationResult, error)
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
	action           string
	actionKey        string
	operationID      string
	inFlight         bool
	intentKnown      bool
	intentFound      bool
	intentValue      bool
	intentEpoch      uint64
	failures         uint32
	next             time.Time
	recovering       bool
	recoveryFailures uint32
	recoveryNext     time.Time
	recoveryEpisode  uint64
	healthySince     time.Time
}

type lineObservation struct {
	intentEnabled bool
	intentFound   bool
	intentEpoch   uint64
	cardMatches   int
	providerReady bool
	providerCode  string
	fence         mediaauth.ProviderFence
	recovering    bool
	status        vowifiipc.Snapshot
}

type actionPlan struct {
	lineID          string
	action          string
	catalogCardID   string
	lineEnabled     bool
	intentEnabled   bool
	intentFound     bool
	intentEpoch     uint64
	fence           mediaauth.ProviderFence
	recoveryEpisode uint64
	recovery        bool
}

func (plan actionPlan) key() string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%t\x00%t\x00%t\x00%d\x00%s\x00%s\x00%s\x00%s\x00%d\x00%t",
		plan.lineID, plan.action, plan.catalogCardID, plan.lineEnabled,
		plan.intentFound, plan.intentEnabled, plan.intentEpoch,
		plan.fence.LineID, plan.fence.ProviderID, plan.fence.Generation,
		plan.fence.CardID, plan.recoveryEpisode, plan.recovery)
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

// RequestIntent is the synchronous public lifecycle facade. It persists only
// operator intent, wakes the single reconciler executor, and returns success
// only after an exact Provider observation reaches the requested target.
func (reconciler *Reconciler) RequestIntent(ctx context.Context, lineID string, enabled bool, operationID string) (vowifiipc.OperationResult, error) {
	var result vowifiipc.OperationResult
	if ctx == nil || reconciler == nil || (vowifiipc.LifecycleRequest{OperationID: operationID}).Validate() != nil {
		return result, &vowifiipc.OperationError{Kind: vowifiipc.ErrorInvalid, Code: "invalid_request", Layer: "intent"}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	lineEnabled, _, _, commandEpoch, err := reconciler.setIntent(lineID, enabled)
	if err != nil {
		if errors.Is(err, linecatalog.ErrNotFound) {
			return result, &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotFound, Code: "line_not_found", Layer: "intent"}
		}
		return result, &vowifiipc.OperationError{Kind: vowifiipc.ErrorFailed, Code: "runtime_intent_persist_failed", Layer: "intent"}
	}
	reconciler.Wake()
	if enabled && !lineEnabled {
		return result, &vowifiipc.OperationError{
			Kind: vowifiipc.ErrorNotReady, Code: "line_disabled", Layer: "intent",
			Detail: "VoWiFi intent was saved; enable the line to allow runtime start",
		}
	}
	ticker := time.NewTicker(intentPollInterval)
	defer ticker.Stop()
	for {
		current, found, _, currentEpoch, intentErr := reconciler.readIntent(lineID)
		if intentErr != nil {
			if errors.Is(intentErr, linecatalog.ErrNotFound) {
				return result, &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotFound, Code: "line_not_found", Layer: "intent"}
			}
			return result, &vowifiipc.OperationError{Kind: vowifiipc.ErrorFailed, Code: "runtime_intent_read_failed", Layer: "intent"}
		}
		if !found || current != enabled || currentEpoch != commandEpoch {
			return result, &vowifiipc.OperationError{
				Kind: vowifiipc.ErrorConflict, Code: "runtime_intent_superseded", Layer: "intent",
				Detail: "a newer lifecycle request replaced this runtime intent",
			}
		}
		var currentLine linecatalog.Line
		if enabled {
			currentLine, err = reconciler.catalog.Get(lineID)
			if err != nil {
				if errors.Is(err, linecatalog.ErrNotFound) {
					return result, &vowifiipc.OperationError{Kind: vowifiipc.ErrorNotFound, Code: "line_not_found", Layer: "intent"}
				}
				return result, &vowifiipc.OperationError{Kind: vowifiipc.ErrorFailed, Code: "runtime_intent_read_failed", Layer: "intent"}
			}
			if !currentLine.Enabled {
				return result, &vowifiipc.OperationError{
					Kind: vowifiipc.ErrorNotReady, Code: "line_disabled", Layer: "intent",
					Detail: "VoWiFi intent remains saved for the next time this line is enabled",
				}
			}
		}
		status, fence, observeErr := reconciler.runtime.Observe(ctx, lineID)
		if observeErr == nil {
			reached := !enabled && status.Runtime.Condition == vowifiipc.RuntimeStopped
			if enabled && status.Runtime.Condition == vowifiipc.RuntimeRunning {
				reached = currentLine.CardID == fence.CardID &&
					matchingCards(reconciler.agents.Statuses(), currentLine.CardID, reconciler.now().UTC()) == 1
			}
			if reached {
				code := "stopped"
				if enabled {
					code = "started"
				}
				return vowifiipc.OperationResult{
					OperationID: operationID, Accepted: true, Code: code, Status: status,
				}, nil
			}
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-reconciler.ctx.Done():
			return result, context.Canceled
		case <-ticker.C:
		}
	}
}

// setIntent and readIntent serialize durable intent observation with a
// process-local per-line epoch. The catalog revision is global, so it cannot
// identify which public lifecycle request was superseded. The epoch changes
// only when this line's durable desired value (or presence) actually changes.
func (reconciler *Reconciler) setIntent(lineID string, enabled bool) (lineEnabled, changed bool, revision, epoch uint64, err error) {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	return reconciler.setIntentLocked(lineID, enabled)
}

func (reconciler *Reconciler) setIntentLocked(lineID string, enabled bool) (lineEnabled, changed bool, revision, epoch uint64, err error) {
	lineEnabled, changed, revision, err = reconciler.catalog.SetRuntimeIntent(lineID, enabled)
	if err != nil {
		return
	}
	line := reconciler.lineLocked(lineID)
	if !line.intentKnown {
		line.intentKnown = true
		line.intentEpoch = 1
	} else if changed || !line.intentFound || line.intentValue != enabled {
		line.intentEpoch++
	}
	line.intentFound = true
	line.intentValue = enabled
	epoch = line.intentEpoch
	return
}

func (reconciler *Reconciler) readIntent(lineID string) (enabled, found bool, revision, epoch uint64, err error) {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	enabled, found, revision, err = reconciler.catalog.RuntimeIntent(lineID)
	if err != nil {
		return
	}
	line := reconciler.lineLocked(lineID)
	if !line.intentKnown {
		line.intentKnown = true
		line.intentEpoch = 1
	} else if line.intentFound != found || (found && line.intentValue != enabled) {
		line.intentEpoch++
	}
	line.intentFound = found
	line.intentValue = enabled
	epoch = line.intentEpoch
	return
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
		status, fence, statusErr := reconciler.runtime.Observe(statusContext, line.ID)
		cancel()
		observation.status = status
		observation.fence = fence
		switch {
		case statusErr == nil:
			if fence.CardID == line.CardID {
				observation.providerReady = true
				observation.providerCode = "provider_reachable"
			} else {
				observation.providerCode = "provider_card_mismatch"
			}
		case errors.Is(statusErr, mediaauth.ErrProviderUnavailable):
			observation.providerCode = "provider_unavailable"
		default:
			observation.providerCode = "provider_status_failed"
			failures = append(failures, fmt.Errorf("line %s provider status: %w", line.ID, statusErr))
		}

		intent, found, _, intentEpoch, intentErr := reconciler.readIntent(line.ID)
		if intentErr != nil {
			failures = append(failures, fmt.Errorf("line %s runtime intent: %w", line.ID, intentErr))
			continue
		}
		observation.intentEnabled, observation.intentFound = intent, found
		observation.intentEpoch = intentEpoch
		adopted := false
		if !found && statusErr == nil && fence.CardID == line.CardID {
			intent = line.Enabled && adoptRunning(status.Runtime.Condition)
			_, _, _, intentEpoch, err = reconciler.setIntent(line.ID, intent)
			if err != nil {
				failures = append(failures, fmt.Errorf("line %s adopt runtime intent: %w", line.ID, err))
				continue
			}
			observation.intentEnabled, observation.intentFound = intent, true
			observation.intentEpoch = intentEpoch
			adopted = true
		}
		observation.recovering = reconciler.isRecovering(line.ID)
		if err := reconciler.publish(line, observation); err != nil {
			failures = append(failures, fmt.Errorf("line %s publish core facts: %w", line.ID, err))
		}
		if err := reconciler.publishAgentFacts(line, agents); err != nil {
			failures = append(failures, fmt.Errorf("line %s publish Agent facts: %w", line.ID, err))
		}
		// Adoption is deliberately side-effect free. A later observation may
		// converge the persisted intent after the current state is visible.
		if adopted || statusErr != nil {
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
	case !targetRunning && (observation.status.Runtime.Condition == vowifiipc.RuntimeRunning ||
		observation.status.Runtime.Condition == vowifiipc.RuntimeFailed):
		reconciler.clearRecovery(line.ID)
		reconciler.schedule(reconciler.actionPlan(line, observation, "stop", false))
	case !targetRunning && observation.status.Runtime.Condition == vowifiipc.RuntimeStopped:
		reconciler.clearRecovery(line.ID)
		reconciler.reset(line.ID)
	case targetRunning && observation.status.Runtime.Condition == vowifiipc.RuntimeFailed:
		if observation.status.ActiveCall == nil {
			reconciler.beginRecovery(line, observation)
		}
	case targetRunning && observation.status.Runtime.Condition == vowifiipc.RuntimeStopped:
		if reconciler.recoveryStartReady(line.ID) {
			reconciler.schedule(reconciler.actionPlan(line, observation, "start", false))
		}
	case targetRunning && observation.status.Runtime.Condition == vowifiipc.RuntimeRunning &&
		observation.status.Tunnel.Condition == vowifiipc.LayerDegraded:
		if observation.status.ActiveCall == nil {
			reconciler.beginRecovery(line, observation)
		}
	case targetRunning && observation.status.Runtime.Condition == vowifiipc.RuntimeRunning:
		reconciler.observeHealthy(line.ID)
		reconciler.reset(line.ID)
	}
}

func (reconciler *Reconciler) actionPlan(line linecatalog.Line, observation lineObservation, action string, recovery bool) actionPlan {
	reconciler.mu.Lock()
	episode := reconciler.lineLocked(line.ID).recoveryEpisode
	reconciler.mu.Unlock()
	return actionPlan{
		lineID: line.ID, action: action, catalogCardID: line.CardID, lineEnabled: line.Enabled,
		intentEnabled: observation.intentEnabled, intentFound: observation.intentFound,
		intentEpoch: observation.intentEpoch,
		fence:       observation.fence, recoveryEpisode: episode, recovery: recovery,
	}
}

// beginRecovery starts one Provider-owned cleanup cycle. It never terminates
// an active call, never restarts the Provider process, and keeps a successful
// cleanup in backoff before the matching start. Repeated terminal tunnel
// faults therefore cannot form a tight stop/start loop.
func (reconciler *Reconciler) beginRecovery(catalogLine linecatalog.Line, observation lineObservation) {
	now := reconciler.now().UTC()
	reconciler.mu.Lock()
	line := reconciler.lineLocked(catalogLine.ID)
	line.healthySince = time.Time{}
	if line.inFlight {
		reconciler.mu.Unlock()
		return
	}
	if !line.recovering {
		if now.Before(line.recoveryNext) {
			reconciler.mu.Unlock()
			return
		}
		line.recovering = true
		line.recoveryEpisode++
		line.recoveryFailures++
		line.recoveryNext = now.Add(reconciler.retryDelay(line.recoveryFailures, nil))
	}
	reconciler.mu.Unlock()
	reconciler.schedule(reconciler.actionPlan(catalogLine, observation, "stop", true))
}

func (reconciler *Reconciler) recoveryStartReady(lineID string) bool {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	line := reconciler.lineLocked(lineID)
	return !line.recovering || !reconciler.now().UTC().Before(line.recoveryNext)
}

func (reconciler *Reconciler) isRecovering(lineID string) bool {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	line := reconciler.lines[lineID]
	return line != nil && line.recovering
}

func (reconciler *Reconciler) observeHealthy(lineID string) {
	now := reconciler.now().UTC()
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	line := reconciler.lineLocked(lineID)
	// A newly observed healthy runtime cancels any cleanup episode whose stop
	// was fenced out or became unnecessary. Keep its backoff history until the
	// existing stable window proves the recovery durable.
	line.recovering = false
	if line.healthySince.IsZero() {
		line.healthySince = now
	}
	if !now.Before(line.healthySince.Add(recoveryStableWindow)) {
		line.recoveryFailures = 0
		line.recoveryNext = time.Time{}
	}
}

func (reconciler *Reconciler) clearRecovery(lineID string) {
	reconciler.mu.Lock()
	line := reconciler.lineLocked(lineID)
	line.recovering = false
	line.recoveryFailures = 0
	line.recoveryNext = time.Time{}
	line.healthySince = time.Time{}
	reconciler.mu.Unlock()
}

func (reconciler *Reconciler) lineLocked(lineID string) *lineState {
	line := reconciler.lines[lineID]
	if line == nil {
		line = &lineState{}
		reconciler.lines[lineID] = line
	}
	return line
}

func (reconciler *Reconciler) schedule(plan actionPlan) {
	now := reconciler.now().UTC()
	reconciler.mu.Lock()
	line := reconciler.lineLocked(plan.lineID)
	if line.inFlight {
		reconciler.mu.Unlock()
		return
	}
	key := plan.key()
	if line.actionKey != key {
		line.action, line.actionKey = plan.action, key
		line.operationID, line.failures, line.next = "", 0, time.Time{}
	}
	if now.Before(line.next) {
		reconciler.mu.Unlock()
		return
	}
	if line.operationID == "" {
		sequence := reconciler.counter.Add(1)
		fingerprint := sha256.Sum256([]byte(reconciler.generation))
		line.operationID = fmt.Sprintf("reconcile-%s-%s-%d", plan.action, hex.EncodeToString(fingerprint[:16]), sequence)
	}
	line.inFlight = true
	operationID := line.operationID
	reconciler.mu.Unlock()

	reconciler.wg.Add(1)
	go reconciler.execute(plan, operationID)
}

var errActionPlanChanged = errors.New("runtime reconcile action plan changed")

func (reconciler *Reconciler) execute(plan actionPlan, operationID string) {
	defer reconciler.wg.Done()
	ctx, cancel := context.WithTimeout(reconciler.ctx, reconciler.actionTimeout)
	request := vowifiipc.LifecycleRequest{OperationID: operationID, RequireIdle: plan.recovery}
	err := reconciler.validatePlan(ctx, plan)
	if err == nil {
		if plan.action == "start" {
			_, err = reconciler.runtime.Start(ctx, plan.lineID, plan.fence, request)
		} else {
			_, err = reconciler.runtime.Stop(ctx, plan.lineID, plan.fence, request)
		}
	}
	cancel()

	key := plan.key()
	reconciler.mu.Lock()
	line := reconciler.lines[plan.lineID]
	if line != nil && line.actionKey == key {
		line.inFlight = false
		switch {
		case err == nil:
			line.operationID, line.failures, line.next = "", 0, time.Time{}
			if plan.action == "start" {
				line.recovering = false
				line.healthySince = time.Time{}
			}
		case errors.Is(err, errActionPlanChanged), errors.Is(err, mediaauth.ErrProviderFenceConflict):
			line.action, line.actionKey, line.operationID = "", "", ""
			line.failures, line.next = 0, time.Time{}
		case reconciler.ctx.Err() == nil:
			line.failures++
			line.next = reconciler.now().UTC().Add(reconciler.retryDelay(line.failures, err))
			var response *vowifiipc.ResponseError
			if errors.As(err, &response) {
				// A typed Provider failure is a definitive outcome. The next
				// backed-off episode must use a fresh idempotency identity.
				line.operationID = ""
			}
			if plan.action == "start" && line.recovering {
				line.recoveryFailures++
				line.recoveryNext = reconciler.now().UTC().Add(reconciler.retryDelay(line.recoveryFailures, err))
			}
		}
	}
	reconciler.mu.Unlock()
	if err != nil && !errors.Is(err, errActionPlanChanged) && !errors.Is(err, mediaauth.ErrProviderFenceConflict) && reconciler.ctx.Err() == nil {
		reconciler.logf("runtime reconcile line %s %s failed: %v", plan.lineID, plan.action, err)
	}
	reconciler.Wake()
}

func (reconciler *Reconciler) validatePlan(ctx context.Context, plan actionPlan) error {
	line, err := reconciler.catalog.Get(plan.lineID)
	if err != nil {
		if errors.Is(err, linecatalog.ErrNotFound) {
			return errActionPlanChanged
		}
		return fmt.Errorf("re-read line before runtime action: %w", err)
	}
	intent, found, _, epoch, err := reconciler.readIntent(plan.lineID)
	if err != nil {
		return fmt.Errorf("re-read runtime intent before action: %w", err)
	}
	if line.CardID != plan.catalogCardID || line.Enabled != plan.lineEnabled ||
		found != plan.intentFound || intent != plan.intentEnabled || epoch != plan.intentEpoch {
		return errActionPlanChanged
	}
	cardMatches := matchingCards(reconciler.agents.Statuses(), line.CardID, reconciler.now().UTC())
	targetRunning := line.Enabled && found && intent && cardMatches == 1 && plan.fence.CardID == line.CardID
	if plan.action == "start" {
		if !targetRunning {
			return errActionPlanChanged
		}
	} else if plan.action != "stop" {
		return errActionPlanChanged
	} else if !plan.recovery {
		if targetRunning {
			return errActionPlanChanged
		}
	} else {
		if !targetRunning {
			return errActionPlanChanged
		}
		reconciler.mu.Lock()
		state := reconciler.lines[plan.lineID]
		recoveryCurrent := state != nil && state.recovering && state.recoveryEpisode == plan.recoveryEpisode
		reconciler.mu.Unlock()
		if !recoveryCurrent {
			return errActionPlanChanged
		}
	}

	// The observation that produced this plan may predate a concurrent action.
	// Re-read the exact fenced Provider before dispatch so a completed Start or
	// Stop cannot be repeated from a stale convergence observation.
	status, fence, observeErr := reconciler.runtime.Observe(ctx, plan.lineID)
	if observeErr != nil || fence != plan.fence {
		return errActionPlanChanged
	}
	if plan.action == "start" {
		if status.Runtime.Condition != vowifiipc.RuntimeStopped {
			return errActionPlanChanged
		}
		return nil
	}
	if !plan.recovery {
		if status.Runtime.Condition != vowifiipc.RuntimeRunning && status.Runtime.Condition != vowifiipc.RuntimeFailed {
			return errActionPlanChanged
		}
		return nil
	}
	if status.ActiveCall != nil {
		return errActionPlanChanged
	}
	failed := status.Runtime.Condition == vowifiipc.RuntimeFailed
	degraded := status.Runtime.Condition == vowifiipc.RuntimeRunning &&
		status.Tunnel.Condition == vowifiipc.LayerDegraded
	if !failed && !degraded {
		return errActionPlanChanged
	}
	return nil
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
		line.action, line.actionKey, line.operationID = "", "", ""
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
	publishedKey := string(events.RoleCore) + ":" + line.ID
	first := !reconciler.published[publishedKey]
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
	reconciler.published[publishedKey] = true
	reconciler.mu.Unlock()
	return nil
}

func (reconciler *Reconciler) publishAgentFacts(line linecatalog.Line, statuses []agentlink.ConnectionStatus) error {
	facts := agentFacts(line, statuses, reconciler.now().UTC())
	now := reconciler.now().UTC()
	reconciler.mu.Lock()
	reconciler.sequence[line.ID]++
	sequence := reconciler.sequence[line.ID]
	publishedKey := string(events.RoleAgent) + ":" + line.ID
	first := !reconciler.published[publishedKey]
	reconciler.mu.Unlock()

	generation := reconciler.generation + "-agent-projection"
	current := currentFacts(reconciler.replay.Projections(now), line.ID)
	changed := make([]events.Event, 0, len(facts))
	for _, fact := range facts {
		prior, found := current[fact.layer]
		if !first && found && prior.Source == string(events.RoleAgent) &&
			prior.ProducerID == agentProjectionProducerID && prior.Generation == generation &&
			prior.Condition == fact.condition && prior.Available == fact.available && prior.Code == fact.code {
			continue
		}
		changed = append(changed, events.Event{
			SchemaVersion: events.SchemaVersion,
			EventID:       fmt.Sprintf("agent-projection:%s:%s:%d:%s", generation, line.ID, sequence, fact.layer),
			LineID:        line.ID, ProducerRole: events.RoleAgent, ProducerID: agentProjectionProducerID,
			Layer: fact.layer, Condition: fact.condition, Available: fact.available, Code: fact.code,
			Generation: generation, Sequence: sequence, ObservedAt: now,
		})
	}
	checkpoint := events.ProducerCheckpoint{
		LineID: line.ID, ProducerRole: events.RoleAgent, ProducerID: agentProjectionProducerID,
		Generation: generation, Sequence: sequence, Layers: append([]state.Layer(nil), agentLayers...),
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
	reconciler.published[publishedKey] = true
	reconciler.mu.Unlock()
	return nil
}

func agentFacts(line linecatalog.Line, statuses []agentlink.ConnectionStatus, now time.Time) []desiredFact {
	facts := map[state.Layer]desiredFact{
		state.LayerAgentLink:     {layer: state.LayerAgentLink, condition: state.ConditionUnknown, code: "agent_target_not_found"},
		state.LayerHardware:      {layer: state.LayerHardware, condition: state.ConditionBlocked, code: "hardware_not_found"},
		state.LayerCard:          {layer: state.LayerCard, condition: state.ConditionBlocked, code: "card_not_present"},
		state.LayerPIN:           {layer: state.LayerPIN, condition: state.ConditionUnknown, code: "pin_state_unknown"},
		state.LayerCellularData:  {layer: state.LayerCellularData, condition: state.ConditionInactive, code: "cellular_data_unavailable"},
		state.LayerCellularVoice: {layer: state.LayerCellularVoice, condition: state.ConditionInactive, code: "cellular_voice_unavailable"},
		state.LayerCellularSMS:   {layer: state.LayerCellularSMS, condition: state.ConditionInactive, code: "cellular_sms_unavailable"},
	}
	type match struct{ modem *agentlink.ModemFact }
	matches := make([]match, 0, 2)
	for _, status := range statuses {
		if status.Topology == nil || status.LastReport.IsZero() || now.Sub(status.LastReport) > agentTopologyTTL {
			continue
		}
		if status.Topology.ReaderCondition == agentlink.ReaderReady {
			for _, reader := range status.Topology.Readers {
				if reader.CardPresent && reader.IdentityState == agentlink.CardIdentified &&
					reader.CardID == line.CardID && reader.SessionGeneration != "" {
					matches = append(matches, match{})
				}
			}
		}
		if status.Topology.ModemCondition == agentlink.ModemReady {
			for index := range status.Topology.Modems {
				modem := &status.Topology.Modems[index]
				if modem.SIM.State == "ready" && modem.SIM.ICCID == line.CardID && modem.SIM.SessionGeneration != "" &&
					(line.SIM.IMEI == "" || modem.EquipmentID == line.SIM.IMEI) {
					matches = append(matches, match{modem: modem})
				}
			}
		}
	}
	if len(matches) != 1 {
		if len(matches) > 1 {
			facts[state.LayerAgentLink] = desiredFact{layer: state.LayerAgentLink, condition: state.ConditionBlocked, code: "agent_target_ambiguous"}
			facts[state.LayerHardware] = desiredFact{layer: state.LayerHardware, condition: state.ConditionBlocked, code: "hardware_identity_ambiguous"}
			facts[state.LayerCard] = desiredFact{layer: state.LayerCard, condition: state.ConditionBlocked, code: "card_identity_ambiguous"}
		}
		return orderedAgentFacts(facts)
	}
	facts[state.LayerAgentLink] = desiredFact{layer: state.LayerAgentLink, condition: state.ConditionReady, available: true, code: "agent_connected"}
	facts[state.LayerHardware] = desiredFact{layer: state.LayerHardware, condition: state.ConditionReady, available: true, code: "hardware_ready"}
	facts[state.LayerCard] = desiredFact{layer: state.LayerCard, condition: state.ConditionReady, available: true, code: "card_present"}
	modem := matches[0].modem
	if modem == nil {
		return orderedAgentFacts(facts)
	}
	switch modem.SIM.PINState {
	case "not_required":
		facts[state.LayerPIN] = desiredFact{layer: state.LayerPIN, condition: state.ConditionReady, available: true, code: "pin_not_required"}
	case "pin_required", "puk_required", "other_lock":
		facts[state.LayerPIN] = desiredFact{layer: state.LayerPIN, condition: state.ConditionBlocked, code: modem.SIM.PINState}
	}
	if modem.Capabilities.CellularData && modem.Network.DataGuard == "protected" {
		facts[state.LayerCellularData] = desiredFact{layer: state.LayerCellularData, condition: state.ConditionReady, available: true, code: "cellular_data_guarded"}
	}
	if modem.AT.State == "ready" && modem.AT.CallSignalling {
		facts[state.LayerCellularVoice] = desiredFact{layer: state.LayerCellularVoice, condition: state.ConditionReady, available: true, code: "cellular_voice_ready"}
	}
	if modem.AT.State == "ready" && modem.AT.SMS {
		facts[state.LayerCellularSMS] = desiredFact{layer: state.LayerCellularSMS, condition: state.ConditionReady, available: true, code: "cellular_sms_ready"}
	}
	return orderedAgentFacts(facts)
}

func orderedAgentFacts(facts map[state.Layer]desiredFact) []desiredFact {
	result := make([]desiredFact, 0, len(agentLayers))
	for _, layer := range agentLayers {
		result = append(result, facts[layer])
	}
	return result
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
		if observation.recovering {
			admission.condition, admission.code = state.ConditionBackoff, "runtime_recovery_backoff"
		} else {
			admission.condition, admission.available, admission.code = state.ConditionReady, true, "runtime_admitted"
		}
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
