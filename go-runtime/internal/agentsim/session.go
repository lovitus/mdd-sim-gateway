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
}

type Manager struct {
	connector Connector
	pins      PINResolver
	mu        sync.RWMutex
	sessions  map[string]*session
	pinMu     sync.Mutex
	pinTried  map[string][sha256.Size]byte
}

type session struct {
	readerName string
	generation string
	cardID     string
	card       Card
	active     atomic.Bool
	operation  sync.Mutex
}

func NewManager(connector Connector, pins PINResolver) (*Manager, error) {
	if connector == nil {
		return nil, errors.New("SIM card connector is required")
	}
	return &Manager{
		connector: connector, pins: pins, sessions: make(map[string]*session),
		pinTried: make(map[string][sha256.Size]byte),
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
		readerName: reader.Name, generation: reader.SessionGeneration, card: card,
	}
	current.active.Store(true)
	// Identity discovery is best effort: an empty eUICC legitimately has no
	// active profile ICCID, but must remain visible as a live attachment.
	if err := card.BeginTransaction(); err != nil {
		_ = card.Close()
		return fmt.Errorf("begin identity transaction: %w", err)
	}
	cardID, identityErr := readICCID(ctx, card)
	endErr := card.EndTransaction()
	if identityErr == nil {
		current.cardID = cardID
	} else if !errors.Is(identityErr, errIdentityUnavailable) {
		_ = card.Close()
		return errors.Join(fmt.Errorf("read card identity: %w", identityErr), endErr)
	}
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

	<-ctx.Done()
	manager.mu.Lock()
	if manager.sessions[current.generation] == current {
		delete(manager.sessions, current.generation)
	}
	current.active.Store(false)
	manager.mu.Unlock()
	current.operation.Lock()
	defer current.operation.Unlock()
	return errors.Join(ctx.Err(), card.Close())
}

func (manager *Manager) Sessions() []SessionView {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	views := make([]SessionView, 0, len(manager.sessions))
	for _, current := range manager.sessions {
		views = append(views, SessionView{
			ReaderName: current.readerName, SessionGeneration: current.generation, CardID: current.cardID,
		})
	}
	sort.Slice(views, func(left, right int) bool {
		if views[left].ReaderName == views[right].ReaderName {
			return views[left].SessionGeneration < views[right].SessionGeneration
		}
		return views[left].ReaderName < views[right].ReaderName
	})
	return views
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
			previousHash, wasTried := manager.pinTried[current.cardID]
			alreadyTried := wasTried && previousHash == pinHash
			attempted, pinErr := verifyPIN(ctx, current.card, pin, !alreadyTried)
			if attempted {
				manager.pinTried[current.cardID] = pinHash
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
