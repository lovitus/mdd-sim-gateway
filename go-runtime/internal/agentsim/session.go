// Package agentsim owns one high-level SIM authentication session per live
// PC/SC card attachment. It does not expose general APDU transport.
package agentsim

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
)

type Card interface {
	BeginTransaction() error
	EndTransaction() error
	Transmit([]byte) ([]byte, error)
	Close() error
}

type Connector interface {
	Connect(readerName string) (Card, error)
}

type PINResolver interface {
	PINForCard(context.Context, string) (string, error)
}

type PINResolverFunc func(context.Context, string) (string, error)

func (function PINResolverFunc) PINForCard(ctx context.Context, cardID string) (string, error) {
	return function(ctx, cardID)
}

type SessionView struct {
	ReaderName        string
	SessionGeneration string
	CardID            string
	EUICC             *agentlink.EUICCFact
	SecureElements    []agentlink.EUICCSlotFact
}

type Manager struct {
	connector       Connector
	pins            PINResolver
	mutateProfile   func(context.Context, Card, []byte, string, agentlink.EUICCProfileAction, string) error
	downloadProfile func(context.Context, Card, agentlink.EUICCDownloadRequest, []byte,
		func(agentlink.EUICCDownloadStage), func(*agentlink.EUICCDownloadMetadata)) error
	discoverProfiles    func(context.Context, Card, agentlink.EUICCDiscoveryRequest, []byte) (string, []agentlink.EUICCDiscoveryEntry, error)
	listNotifications   func(context.Context, Card, []byte) ([]agentlink.EUICCNotificationEntry, error)
	deliverNotification func(context.Context, Card, []byte, agentlink.EUICCNotificationEntry) (bool, bool, error)
	removeNotification  func(context.Context, Card, []byte, agentlink.EUICCNotificationEntry) (bool, error)
	downloadStore       *DownloadStore
	downloadTimeout     time.Duration
	downloadMu          sync.Mutex
	downloads           map[string]*downloadJob
	mu                  sync.RWMutex
	sessions            map[string]*session
	pinMu               sync.Mutex
	pinFailed           map[string][sha256.Size]byte
}

type session struct {
	readerName     string
	generation     string
	cardID         string
	secureElements []secureElement
	card           Card
	active         atomic.Bool
	operation      sync.Mutex
	refresh        chan struct{}
	refreshOnce    sync.Once
	ctx            context.Context
}

var errEUICCProfileChanged = errors.New("eUICC profile changed; reconnecting card session")

func NewManager(connector Connector, pins PINResolver) (*Manager, error) {
	return NewManagerWithDownloadStore(connector, pins, nil)
}

func NewManagerWithDownloadStore(connector Connector, pins PINResolver, store *DownloadStore) (*Manager, error) {
	if connector == nil {
		return nil, errors.New("SIM card connector is required")
	}
	if store != nil {
		if err := store.RecoverInterrupted(time.Now()); err != nil {
			return nil, fmt.Errorf("recover interrupted eUICC downloads: %w", err)
		}
	}
	return &Manager{
		connector: connector, pins: pins, mutateProfile: mutateEUICCProfile,
		downloadProfile: downloadEUICCProfile, downloadStore: store, downloadTimeout: 5 * time.Minute,
		discoverProfiles: func(ctx context.Context, card Card, request agentlink.EUICCDiscoveryRequest,
			aid []byte) (string, []agentlink.EUICCDiscoveryEntry, error) {
			return discoverEUICCProfiles(ctx, card, request, aid, nil)
		},
		listNotifications: listEUICCNotifications, deliverNotification: deliverEUICCNotification,
		removeNotification: removeEUICCNotification,
		downloads:          make(map[string]*downloadJob),
		sessions:           make(map[string]*session),
		pinFailed:          make(map[string][sha256.Size]byte),
	}, nil
}

// Run implements agentreader.SessionRunner. A reader name is used only to
// connect the current attachment; routing subsequently requires both the
// monitor generation and the discovered ICCID.
func (manager *Manager) Run(ctx context.Context, reader agentreader.Reader) error {
	if !reader.CardPresent || reader.Name == "" || reader.SessionGeneration == "" {
		return errors.New("invalid present-card session")
	}
	card, err := manager.connector.Connect(reader.Name)
	if err != nil {
		return fmt.Errorf("connect PC/SC card: %w", err)
	}
	current := &session{
		readerName: reader.Name, generation: reader.SessionGeneration, card: card, refresh: make(chan struct{}), ctx: ctx,
	}
	current.active.Store(true)
	// Identity discovery is best effort: an empty eUICC legitimately has no
	// active profile ICCID, but must remain visible as a live attachment.
	if err := card.BeginTransaction(); err != nil {
		_ = card.Close()
		return fmt.Errorf("begin identity transaction: %w", err)
	}
	cardID, identityErr := readICCID(ctx, card)
	if identityErr != nil && !errors.Is(identityErr, errIdentityUnavailable) {
		endErr := card.EndTransaction()
		_ = card.Close()
		return errors.Join(fmt.Errorf("read card identity: %w", identityErr), endErr)
	}
	secureElements, _ := inspectSecureElements(ctx, card)
	endErr := card.EndTransaction()
	if identityErr == nil {
		current.cardID = cardID
	}
	current.secureElements = secureElements
	if endErr != nil {
		_ = card.Close()
		return fmt.Errorf("end identity transaction: %w", endErr)
	}

	manager.mu.Lock()
	if _, exists := manager.sessions[current.generation]; exists {
		manager.mu.Unlock()
		_ = card.Close()
		return fmt.Errorf("duplicate SIM session generation %q", current.generation)
	}
	manager.sessions[current.generation] = current
	manager.mu.Unlock()

	runErr := ctx.Err()
	if runErr == nil {
		select {
		case <-ctx.Done():
			runErr = ctx.Err()
		case <-current.refresh:
			runErr = errEUICCProfileChanged
		}
	}
	manager.mu.Lock()
	if manager.sessions[current.generation] == current {
		delete(manager.sessions, current.generation)
	}
	current.active.Store(false)
	manager.mu.Unlock()
	current.operation.Lock()
	defer current.operation.Unlock()
	return errors.Join(runErr, card.Close())
}

func (current *session) requestRefresh() { current.refreshOnce.Do(func() { close(current.refresh) }) }

func (manager *Manager) Sessions() []SessionView {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	views := make([]SessionView, 0, len(manager.sessions))
	for _, current := range manager.sessions {
		slots := cloneSecureElements(current.secureElements)
		for index := range slots {
			euicc := slots[index].fact
			manager.downloadMu.Lock()
			var latestOperation string
			var latest agentlink.EUICCDownloadJob
			for operationID, job := range manager.downloads {
				if job.eid != euicc.EID {
					continue
				}
				status := job.snapshot()
				if latestOperation == "" || status.StartedAt.After(latest.StartedAt) {
					latestOperation, latest = operationID, status
				}
			}
			manager.downloadMu.Unlock()
			if latestOperation != "" {
				euicc.Download = &agentlink.EUICCDownloadFact{OperationID: latestOperation, Job: latest}
			}
		}
		view := SessionView{
			ReaderName: current.readerName, SessionGeneration: current.generation, CardID: current.cardID,
		}
		if len(slots) == 1 && slots[0].id == "" {
			view.EUICC = cloneEUICCFact(slots[0].fact)
		} else {
			view.SecureElements = make([]agentlink.EUICCSlotFact, len(slots))
			for index, slot := range slots {
				view.SecureElements[index] = agentlink.EUICCSlotFact{SlotID: slot.id, Label: slot.label,
					EUICC: *cloneEUICCFact(slot.fact)}
			}
		}
		views = append(views, view)
	}
	sort.Slice(views, func(left, right int) bool {
		if views[left].ReaderName == views[right].ReaderName {
			return views[left].SessionGeneration < views[right].SessionGeneration
		}
		return views[left].ReaderName < views[right].ReaderName
	})
	return views
}

func cloneSecureElements(source []secureElement) []secureElement {
	result := make([]secureElement, len(source))
	for index, slot := range source {
		result[index] = secureElement{id: slot.id, label: slot.label, aid: append([]byte(nil), slot.aid...),
			fact: cloneEUICCFact(slot.fact)}
	}
	return result
}

func findSecureElement(elements []secureElement, eid string) (*secureElement, bool) {
	var found *secureElement
	for index := range elements {
		if elements[index].fact == nil || elements[index].fact.EID != eid {
			continue
		}
		if found != nil {
			return nil, false
		}
		found = &elements[index]
	}
	return found, found != nil
}

func cloneEUICCFact(source *agentlink.EUICCFact) *agentlink.EUICCFact {
	if source == nil {
		return nil
	}
	profiles := make([]agentlink.EUICCProfileFact, len(source.Profiles))
	copy(profiles, source.Profiles)
	return &agentlink.EUICCFact{
		EID: source.EID, ProfilesAvailable: source.ProfilesAvailable, ProfileManagement: source.ProfileManagement,
		ProfileDownload: source.ProfileDownload, ProfileDiscovery: source.ProfileDiscovery,
		NotificationInventory: source.NotificationInventory, NotificationDelivery: source.NotificationDelivery,
		NotificationRemoval: source.NotificationRemoval,
		Download:            cloneEUICCDownloadFact(source.Download), Profiles: profiles,
	}
}

func cloneEUICCDownloadFact(source *agentlink.EUICCDownloadFact) *agentlink.EUICCDownloadFact {
	if source == nil {
		return nil
	}
	copy := *source
	copy.Job = cloneDownloadJob(source.Job)
	return &copy
}

func (manager *Manager) AuthenticateAKA(ctx context.Context, request agentlink.AKARequest) agentlink.AKAResponse {
	result := agentlink.AKAResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
	}
	if err := request.Validate(); err != nil {
		result.Failure = failure("rejected", "invalid_aka_request", false)
		return result
	}
	manager.mu.RLock()
	current := manager.sessions[request.SessionGeneration]
	manager.mu.RUnlock()
	if current == nil || !current.active.Load() {
		result.Failure = failure("not_ready", "card_session_replaced", true)
		return result
	}
	if current.cardID == "" {
		result.Failure = failure("not_ready", "card_identity_unavailable", false)
		return result
	}
	if current.cardID != request.CardID {
		result.Failure = failure("conflict", "card_identity_mismatch", false)
		return result
	}
	current.operation.Lock()
	defer current.operation.Unlock()
	if !current.active.Load() {
		result.Failure = failure("not_ready", "card_session_replaced", true)
		return result
	}
	if err := ctx.Err(); err != nil {
		result.Failure = failure("transport", "operation_canceled", true)
		return result
	}
	if err := current.card.BeginTransaction(); err != nil {
		result.Failure = failure("transport", "pcsc_transaction_failed", true)
		return result
	}
	result = manager.authenticateInTransaction(ctx, current, request, result)
	if err := current.card.EndTransaction(); err != nil {
		result.Body, result.SW1, result.SW2 = nil, 0, 0
		result.Failure = failure("transport", "pcsc_transaction_release_failed", true)
	}
	return result
}

func (manager *Manager) ExecuteEUICCProfile(ctx context.Context,
	request agentlink.EUICCProfileRequest) agentlink.EUICCProfileResponse {
	result := agentlink.EUICCProfileResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
		EID: request.EID, ICCID: request.ICCID, Action: request.Action,
	}
	if err := request.Validate(); err != nil {
		result.Failure = failure("rejected", "invalid_euicc_profile_request", false)
		return result
	}
	manager.mu.RLock()
	current := manager.sessions[request.SessionGeneration]
	manager.mu.RUnlock()
	if current == nil || !current.active.Load() {
		result.Failure = failure("not_ready", "card_session_replaced", true)
		return result
	}
	current.operation.Lock()
	defer current.operation.Unlock()
	if !current.active.Load() {
		result.Failure = failure("not_ready", "card_session_replaced", true)
		return result
	}
	if err := ctx.Err(); err != nil {
		result.Failure = failure("transport", "operation_canceled", true)
		return result
	}
	if err := current.card.BeginTransaction(); err != nil {
		result.Failure = failure("transport", "pcsc_transaction_failed", true)
		return result
	}
	target, unique := findSecureElement(current.secureElements, request.EID)
	if !unique {
		if !releaseEUICCTransaction(current, &result) {
			return result
		}
		result.Failure = failure("conflict", "euicc_identity_mismatch", false)
		return result
	}
	live, inspectErr := inspectEUICCWithAID(ctx, current.card, target.aid)
	if inspectErr != nil || live == nil || !live.ProfilesAvailable || !live.ProfileManagement {
		if !releaseEUICCTransaction(current, &result) {
			return result
		}
		result.Failure = failure("not_ready", "euicc_inventory_unavailable", true)
		return result
	}
	if live.EID != request.EID {
		if !releaseEUICCTransaction(current, &result) {
			return result
		}
		result.Failure = failure("conflict", "euicc_identity_mismatch", false)
		return result
	}
	profile, found := findEUICCProfile(live.Profiles, request.ICCID)
	if !found {
		if !releaseEUICCTransaction(current, &result) {
			return result
		}
		result.Failure = failure("conflict", "euicc_profile_not_found", false)
		return result
	}
	manager.mu.Lock()
	if manager.sessions[current.generation] == current && current.active.Load() {
		if stored, ok := findSecureElement(current.secureElements, request.EID); ok {
			stored.fact = cloneEUICCFact(live)
		}
	}
	manager.mu.Unlock()
	if request.Action == agentlink.EUICCProfileNickname {
		if profile.Nickname == request.Nickname {
			if !releaseEUICCTransaction(current, &result) {
				return result
			}
			result.Outcome, result.Nickname = agentlink.EUICCProfileAlreadyApplied, request.Nickname
			return result
		}
		if profile.Nickname != request.ExpectedNickname {
			if !releaseEUICCTransaction(current, &result) {
				return result
			}
			result.Failure = failure("conflict", "euicc_profile_nickname_changed", false)
			return result
		}
	} else {
		desired := agentlink.EUICCProfileEnabled
		if request.Action == agentlink.EUICCProfileDisable {
			desired = agentlink.EUICCProfileDisabled
		}
		if profile.State == desired {
			if !releaseEUICCTransaction(current, &result) {
				return result
			}
			result.Outcome, result.State = agentlink.EUICCProfileAlreadyApplied, desired
			return result
		}
		if profile.State != request.ExpectedState {
			if !releaseEUICCTransaction(current, &result) {
				return result
			}
			result.Failure = failure("conflict", "euicc_profile_state_changed", false)
			return result
		}
	}
	mutationErr := manager.mutateProfile(ctx, current.card, target.aid, request.ICCID, request.Action, request.Nickname)
	endErr := current.card.EndTransaction()
	if mutationErr == nil {
		current.requestRefresh()
		result.Outcome, result.Changed = agentlink.EUICCProfileRefreshPending, true
		return result
	}
	if endErr == nil {
		if classified := classifyEUICCProfileError(mutationErr); classified != nil {
			result.Failure = classified
			return result
		}
	}
	current.requestRefresh()
	result.Outcome = agentlink.EUICCProfileUncertain
	return result
}

func releaseEUICCTransaction(current *session, result *agentlink.EUICCProfileResponse) bool {
	if err := current.card.EndTransaction(); err != nil {
		current.requestRefresh()
		result.Failure = failure("transport", "pcsc_transaction_release_failed", true)
		return false
	}
	return true
}

func findEUICCProfile(profiles []agentlink.EUICCProfileFact, iccid string) (agentlink.EUICCProfileFact, bool) {
	for _, profile := range profiles {
		if profile.ICCID == iccid {
			return profile, true
		}
	}
	return agentlink.EUICCProfileFact{}, false
}

func (manager *Manager) authenticateInTransaction(ctx context.Context, current *session,
	request agentlink.AKARequest, result agentlink.AKAResponse) agentlink.AKAResponse {
	if err := selectApplication(ctx, current.card, request.Application); err != nil {
		if errors.Is(err, errApplicationUnavailable) || isAPDUStatus(err) {
			result.Failure = failure("not_ready", "sim_application_unavailable", false)
		} else {
			result.Failure = failure("transport", "sim_select_transport_failed", true)
		}
		return result
	}
	if manager.pins != nil {
		pin, err := manager.pins.PINForCard(ctx, current.cardID)
		if err != nil {
			result.Failure = failure("not_ready", "pin_configuration_unavailable", false)
			return result
		}
		if pin != "" {
			pinHash := sha256.Sum256([]byte(pin))
			manager.pinMu.Lock()
			previousHash, failedBefore := manager.pinFailed[current.cardID]
			blockedByFailure := failedBefore && previousHash == pinHash
			attempted, pinErr := verifyPIN(ctx, current.card, pin, !blockedByFailure)
			if attempted {
				if pinErr != nil {
					manager.pinFailed[current.cardID] = pinHash
				} else {
					delete(manager.pinFailed, current.cardID)
				}
			}
			manager.pinMu.Unlock()
			if pinErr != nil {
				result.Failure = failure("rejected", "pin_verification_failed", false)
				return result
			}
		}
	}
	response, err := authenticate(ctx, current.card, request.RAND, request.AUTN)
	if err != nil {
		result.Failure = failure("transport", "sim_auth_transport_failed", true)
		return result
	}
	result.Body, result.SW1, result.SW2 = response.body, response.sw1, response.sw2
	return result
}

func failure(kind, code string, retryable bool) *agentlink.RemoteError {
	return &agentlink.RemoteError{Kind: kind, Code: code, Retryable: retryable}
}
